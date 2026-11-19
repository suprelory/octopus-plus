package checkin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin/router"
	"github.com/suprelory/octopus-plus/internal/checkin/rules"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func (r *checkinRun) platform(ctx context.Context, confirm bool) (*model.CheckinResult, error) {
	adapter := r.execution.Site.CheckinAdapter()
	if err := model.ValidateCheckinIdentity(r.execution.Site, r.execution.Account, r.execution.Credential); err != nil {
		return nil, err
	}
	r.captureBalance(ctx)
	runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if !confirm || r.execution.Trigger == model.CheckinTriggerScheduled {
		var deadlineCancel context.CancelFunc
		runCtx, deadlineCancel = context.WithDeadline(runCtx, r.execution.Workflow().Deadline)
		defer deadlineCancel()
	}
	if adapter == model.SiteCheckinAdapterAgentRouter {
		return r.agentRouter(runCtx)
	}
	if adapter == "" || confirm {
		return nil, op.ErrCheckinAdapter
	}
	if r.execution.Workflow().Submitted {
		// Another instance may still be receiving the one submission response.
		return r.engine.store.WorkflowResult(ctx, r.execution.Account.ID)
	}

	session, err := r.engine.sessions.Ensure(runCtx, r.execution.Account.CredentialID)
	if err != nil {
		return r.preparationFailure(ctx, err)
	}
	if session.Credential.ID != r.execution.Account.CredentialID || session.Credential.IdentityRevision != r.execution.Credential.IdentityRevision {
		return nil, op.ErrCheckinWorkflowChanged
	}
	if _, err := r.reload(runCtx); err != nil {
		return nil, err
	}
	p := r.platformClient(false)
	if adapter == model.SiteCheckinAdapterAnyRouter || (r.execution.Site.Platform == model.SitePlatformNewAPI && p.session.PlatformUserID == nil) {
		p, err = r.platformProfile(runCtx, p)
		if err != nil {
			if saveErr := r.savePlatformSession(ctx, p); saveErr != nil {
				return nil, saveErr
			}
			return r.preparationFailure(ctx, err)
		}
		if err := r.savePlatformSession(ctx, p); err != nil {
			return nil, err
		}
	}
	path := "/api/user/checkin"
	var body any
	if adapter == model.SiteCheckinAdapterAnyRouter && router.HasSession(p.cookies.Header(p.base+"/api/user/sign_in")) {
		path, body = "/api/user/sign_in", map[string]any{}
	}
	response, payload, err := r.submitPlatform(runCtx, p, path, body, false)
	if !r.execution.Workflow().Submitted {
		return r.submissionError(ctx, err)
	}
	outcome := platformOutcome(runCtx, r.execution.Site, response, payload, err)
	return r.finishPlatform(ctx, p, outcome)
}

func verificationOutcome(submitted bool) model.CheckinOutcome {
	message := "请前往签到站点完成人工验证后重试"
	if submitted {
		message = "站点要求人工验证，请前往站点核对本次签到结果"
	}
	return failedOutcome("verification_required", message)
}

func (r *checkinRun) preparationFailure(ctx context.Context, err error) (*model.CheckinResult, error) {
	if errors.Is(err, errVerificationRequired) {
		return r.finish(ctx, verificationOutcome(false))
	}
	if errors.Is(err, op.ErrCheckinWorkflowChanged) || errors.Is(err, op.ErrCheckinWorkflowConflict) || errors.Is(err, op.ErrCheckinUnavailable) {
		return nil, err
	}
	return r.finish(ctx, failedOutcome("authentication_unavailable", "无法确认登录身份，请检查账号凭据和平台用户 ID"))
}

func (r *checkinRun) readPlatform(ctx context.Context, p *platformHTTP, path string) (*rules.Response, map[string]any, error) {
	current, err := r.engine.store.CheckWorkflow(ctx, r.execution)
	if err != nil {
		return nil, nil, err
	}
	if current.Credential.IdentityRevision != p.original.IdentityRevision || sessionAdvanced(current.Credential, p.original) {
		return nil, nil, ErrSessionChanged
	}
	prepared, err := p.prepare(ctx, http.MethodGet, path, nil, false)
	if err != nil {
		return nil, nil, err
	}
	if path == "/api/user/self" && p.session.PlatformUserID == nil {
		setPlatformUser(prepared.request, profileIDHint(p.original.AccessToken))
	}
	return p.send(prepared)
}

func (r *checkinRun) platformProfile(ctx context.Context, p *platformHTTP) (*platformHTTP, error) {
	for {
		response, payload, err := r.readPlatform(ctx, p, "/api/user/self")
		if err != nil {
			return p, err
		}
		switch response.StatusCode {
		case http.StatusUnauthorized:
			if r.recoveryUsed || !p.original.SupportsSessionLogin() {
				return p, ErrSessionExpired
			}
			r.recoveryUsed = true
			if _, err := r.engine.sessions.RecoverRead(ctx, Session{Credential: p.original}); err != nil {
				return p, err
			}
			if _, err := r.reload(ctx); err != nil {
				return p, err
			}
			p = r.platformClient(false)
			continue
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			// Deployments with a check-in endpoint but no self endpoint can use
			// their configured identity directly.
			return p, nil
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 || payload["success"] != true {
			return p, ErrSessionUnavailable
		}
		if id := platformUserID(payload); id != nil {
			p.session.PlatformUserID = id
		}
		return p, nil
	}
}

// Finish constructing the request before claiming the send. The transaction
// checks the same session snapshot that populated its headers and body.
func (r *checkinRun) submitPlatform(ctx context.Context, p *platformHTTP, path string, body any, login bool) (*rules.Response, map[string]any, error) {
	prepared, err := p.prepare(ctx, http.MethodPost, path, body, login)
	if err != nil {
		return nil, nil, err
	}
	expected := *r.execution
	expected.Credential = p.original
	reserved, err := r.engine.store.ReserveWorkflowSubmission(ctx, &expected, false)
	if err != nil {
		prepared.cleanup()
		return nil, nil, err
	}
	r.execution = reserved
	return p.send(prepared)
}

func (r *checkinRun) savePlatformSession(ctx context.Context, p *platformHTTP) error {
	s := p.snapshot()
	before := p.original
	if s.AccessToken == before.AccessToken && s.Cookie == before.Cookie && s.TokenExpiresAt == before.TokenExpiresAt &&
		reflect.DeepEqual(s.SessionCookies, before.SessionCookies) && reflect.DeepEqual(s.PlatformUserID, before.PlatformUserID) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	expected := *r.execution
	expected.Credential = before
	for attempt := 0; attempt < 3; attempt++ {
		saved, err := r.engine.store.SaveWorkflowSession(ctx, &expected, s)
		if err == nil {
			r.execution.Credential = *saved
			p.original = *saved
			return nil
		}
		if errors.Is(err, op.ErrCheckinCredentialConflict) || errors.Is(err, op.ErrCheckinWorkflowChanged) || errors.Is(err, op.ErrCheckinWorkflowConflict) {
			return err
		}
		if attempt < 2 && !waitSession(ctx, time.Duration(1<<attempt)*100*time.Millisecond) {
			break
		}
	}
	return ErrSessionSave
}

func (r *checkinRun) finishPlatform(ctx context.Context, p *platformHTTP, outcome model.CheckinOutcome) (*model.CheckinResult, error) {
	if err := r.savePlatformSession(ctx, p); err != nil {
		outcome.Message += "。会话更新未保存，后续读取可能需要重新认证"
	}
	return r.finish(ctx, outcome)
}

func platformOutcome(ctx context.Context, site model.CheckinSite, response *rules.Response, payload map[string]any, err error) model.CheckinOutcome {
	unknown := failedOutcome(model.SiteCheckinReasonUnconfirmed, "签到接口未确认本次提交结果")
	if errors.Is(err, errVerificationRequired) {
		return verificationOutcome(true)
	}
	if err != nil || response == nil {
		return unknown
	}
	status := response.StatusCode
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		// AnyRouter exposes different endpoints for Cookie and Bearer identities.
		// One absent route cannot disable check-ins for other accounts on the site.
		if site.Platform == model.SitePlatformAnyRouter {
			return failedOutcome("checkin_route_unavailable", "该身份对应的签到接口不可用")
		}
		return failedOutcome(model.SiteCheckinReasonUnsupported, "该站点未提供内置签到接口")
	}
	successHTTP := status >= 200 && status < 300
	if (successHTTP || status == http.StatusBadRequest || status == http.StatusConflict) && rules.AlreadyCheckedIn(platformMessage(payload)) {
		return model.CheckinOutcome{Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonAlreadyCheckedIn, Message: "签到接口确认今日已签到"}
	}
	if !successHTTP {
		return unknown
	}
	acknowledged := payload["success"] == true
	if value, exists := payload["success"]; exists && value != true {
		return unknown
	}
	if site.Platform == model.SitePlatformAnyRouter {
		for key, want := range map[string]int64{"ret": 1, "code": 0} {
			if value, exists := payload[key]; exists {
				number, ok := value.(json.Number)
				value, err := number.Float64()
				if !ok || err != nil || value != float64(want) {
					return unknown
				}
				acknowledged = true
			}
		}
	}
	if !acknowledged {
		return unknown
	}
	return model.CheckinOutcome{Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonCheckedIn, Message: "签到接口已确认成功",
		Reward: numericReward(rules.Reward(ctx, site.CheckinRewardExtractor, "submit", response, nil))}
}
