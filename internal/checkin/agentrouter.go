package checkin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

// Login is the submission. A run that finds an existing reservation can
// only read the account's log for the original calendar day.
func (r *checkinRun) agentRouter(ctx context.Context) (*model.CheckinResult, error) {
	w := r.execution.Workflow()
	newLogin := !w.Submitted
	directEvidence := false
	if newLogin {
		p := r.platformClient(true)
		credential := p.original
		response, payload, err := r.submitPlatform(ctx, p, "/api/user/login",
			map[string]string{"username": credential.Username, "password": credential.Password}, true)
		if !r.execution.Workflow().Submitted {
			return r.submissionError(ctx, err)
		}
		if errors.Is(err, errVerificationRequired) {
			return r.finish(ctx, verificationOutcome(true))
		}
		if response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
			return r.finish(ctx, failedOutcome("authentication_rejected", "AgentRouter 拒绝了本次登录"))
		}
		if response != nil && response.StatusCode >= 200 && response.StatusCode < 300 && payload["success"] == true {
			data, _ := payload["data"].(map[string]any)
			location, locErr := time.LoadLocation(r.execution.Site.CheckinTimezone)
			directEvidence = data["checked_in"] == true && locErr == nil && r.engine.now().In(location).Format(time.DateOnly) == w.Date
			p.session.AccessToken = platformLoginToken(payload)
			p.session.TokenExpiresAt = 0
			if id := platformUserID(payload); id != nil {
				p.session.PlatformUserID = id
			}
			if err := r.savePlatformSession(ctx, p); err != nil {
				if directEvidence {
					outcome := agentConfirmed(false)
					outcome.Message += "。会话更新未保存，后续读取可能需要重新认证"
					return r.finish(ctx, outcome)
				}
				return r.pending(ctx, "pending_confirmation", "登录请求已提交，等待会话恢复后核对签到日志")
			}
		}
	}
	p := r.platformClient(false)
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stamp, err := r.agentLog(readCtx, p)
	if !stamp.IsZero() || directEvidence {
		already := !stamp.IsZero() && stamp.Unix() < w.StartedAt.Unix()
		return r.finishPlatform(ctx, p, agentConfirmed(already))
	}
	saveErr := r.savePlatformSession(ctx, p)
	if !newLogin && (w.Finished || r.engine.now().Before(w.Deadline)) {
		// A concurrent confirmation must not finish a login still in flight.
		result, readErr := r.engine.store.WorkflowResult(ctx, r.execution.Account.ID)
		if readErr != nil {
			return nil, readErr
		}
		return result, errors.Join(saveErr, err)
	}
	if saveErr != nil {
		return r.pending(ctx, "pending_confirmation", "等待确认签到日志，最新会话未能保存")
	}
	if errors.Is(err, errVerificationRequired) {
		return r.pending(ctx, "verification_required", "请前往站点完成验证后再次确认签到日志")
	}
	return r.pending(ctx, "pending_confirmation", "登录请求已提交，等待当日签到日志确认")
}

func agentConfirmed(already bool) model.CheckinOutcome {
	reason := model.SiteCheckinReasonCheckedIn
	if already {
		reason = model.SiteCheckinReasonAlreadyCheckedIn
	}
	return model.CheckinOutcome{Status: model.SiteExecutionStatusSuccess, Reason: reason, Message: "AgentRouter 已确认当日签到"}
}

func (r *checkinRun) agentLog(ctx context.Context, p *platformHTTP) (time.Time, error) {
	const path = "/api/log/self/"
	if p.session.PlatformUserID == nil {
		if p.session.AccessToken == "" && p.cookies.Header(p.base+"/api/user/self") == "" {
			// A concurrent login may not have returned a session yet.
			return time.Time{}, nil
		}
		refreshed, err := r.platformProfile(ctx, p)
		if err != nil {
			return time.Time{}, err
		}
		if refreshed.session.PlatformUserID == nil {
			return time.Time{}, ErrSessionUnavailable
		}
	}
	location, err := time.LoadLocation(r.execution.Site.CheckinTimezone)
	if err != nil {
		return time.Time{}, err
	}
	start, err := time.ParseInLocation(time.DateOnly, r.execution.Workflow().Date, location)
	if err != nil {
		return time.Time{}, err
	}
	end := min(start.AddDate(0, 0, 1).Unix()-1, r.engine.now().Unix())
	target := fmt.Sprintf("%s?p=1&page_size=20&type=4&start_timestamp=%d&end_timestamp=%d", path, start.Unix(), end)
	response, payload, err := r.readPlatform(ctx, p, target)
	if err != nil {
		return time.Time{}, err
	}
	if response.StatusCode != http.StatusOK || payload["success"] != true {
		return time.Time{}, ErrCheckinRequest
	}
	data, _ := payload["data"].(map[string]any)
	items, ok := data["items"].([]any)
	if !ok {
		items, ok = payload["items"].([]any)
	}
	if !ok {
		items, _ = payload["data"].([]any)
	}
	var newest time.Time
	for _, value := range items {
		entry, _ := value.(map[string]any)
		kind, kindOK := integer(entry["type"])
		stamp, stampOK := integer(entry["created_at"])
		if !kindOK || kind != 4 || !stampOK || stamp < start.Unix() || stamp > end {
			continue
		}
		content := strings.ToLower(stringValue(entry["content"]))
		if strings.Contains(content, "签到成功") || strings.Contains(content, "daily check-in success") || strings.Contains(content, "daily checkin success") {
			at := time.Unix(stamp, 0)
			if at.After(newest) {
				newest = at
			}
		}
	}
	return newest, nil
}
