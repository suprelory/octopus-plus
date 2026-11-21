package checkin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin/finance"
	"github.com/suprelory/octopus-plus/internal/checkin/rules"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

type WorkflowStore interface {
	BeginExecution(context.Context, int, op.CheckinInvocation) (*op.CheckinExecution, error)
	CheckWorkflow(context.Context, *op.CheckinExecution) (*op.CheckinExecution, error)
	ReserveWorkflowSubmission(context.Context, *op.CheckinExecution, bool) (*op.CheckinExecution, error)
	SaveWorkflowSession(context.Context, *op.CheckinExecution, op.CheckinLoginSession) (*model.CheckinCredential, error)
	FinishWorkflow(context.Context, *op.CheckinExecution, model.CheckinOutcome) (*model.CheckinResult, error)
	WorkflowResult(context.Context, int) (*model.CheckinResult, error)
}

type SessionProvider interface {
	Ensure(context.Context, int) (Session, error)
	RecoverRead(context.Context, Session) (Session, error)
}

var ErrCheckinAlreadyRunning = errors.New("checkin is already running for this account")

// Engine executes configured HTTP and built-in check-ins. Availability and reservations
// are rechecked by the store; no production scheduler or public API starts it yet.
type Engine struct {
	store         WorkflowStore
	sessions      SessionProvider
	proxies       ProxyResolver
	now           func() time.Time
	mu            sync.Mutex
	running       map[int]bool
	balances      *Balances
	notifications *Notifications
	workers       *workLifecycle
}

func NewEngine(store WorkflowStore, sessions SessionProvider, proxies ProxyResolver) *Engine {
	e := &Engine{store: store, sessions: sessions, proxies: proxies, now: time.Now, running: make(map[int]bool), workers: newWorkLifecycle()}
	if store, ok := store.(BalanceStore); ok {
		e.balances = NewBalances(store, sessions, proxies)
	}
	return e
}

// Configure before sharing the engine with workers. NewServices wires delivery;
// tests can inject local delivery without global settings.
func (e *Engine) WithNotifications(notifications *Notifications) *Engine {
	e.notifications = notifications
	return e
}

func (e *Engine) Execute(ctx context.Context, accountID int, trigger model.CheckinTrigger) (*model.CheckinResult, error) {
	return e.run(ctx, accountID, op.CheckinInvocation{Trigger: trigger})
}

// Confirm never creates a workflow or sends another submission, including after
// a failed/expired confirmation or when status configuration has been removed.
func (e *Engine) Confirm(ctx context.Context, accountID int) (*model.CheckinResult, error) {
	return e.run(ctx, accountID, op.CheckinInvocation{Trigger: model.CheckinTriggerManual, Confirm: true})
}

type checkinRun struct {
	engine        *Engine
	execution     *op.CheckinExecution
	recoveryUsed  bool
	beforeBalance *BalanceObservation
}

func (e *Engine) run(ctx context.Context, id int, request op.CheckinInvocation) (*model.CheckinResult, error) {
	ctx, release, err := e.workers.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	if e.running[id] {
		e.mu.Unlock()
		return nil, ErrCheckinAlreadyRunning
	}
	e.running[id] = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.running, id); e.mu.Unlock() }()
	execution, err := e.store.BeginExecution(ctx, id, request)
	if err != nil {
		return nil, err
	}
	w := execution.Workflow()
	confirm := request.Confirm
	if w.Finished && (!confirm || request.Trigger == model.CheckinTriggerScheduled || w.Status == model.SiteExecutionStatusSuccess) {
		return e.store.WorkflowResult(ctx, id)
	}
	run := &checkinRun{engine: e, execution: execution}
	if _, err := run.reload(ctx); err != nil {
		return nil, err
	}
	if err := execution.Site.Validate(); err != nil {
		return nil, err
	}
	enforceDeadline := !confirm || request.Trigger == model.CheckinTriggerScheduled
	if enforceDeadline && !e.now().Before(w.Deadline) {
		return run.finish(ctx, failedOutcome("confirmation_expired", "签到确认期限已到，已提交请求不会重发"))
	}
	if !execution.Site.CheckinHTTPEnabled {
		return run.platform(ctx, confirm)
	}
	if confirm && execution.Site.CheckinStatus == nil {
		return nil, op.ErrCheckinAdapter
	}
	if w.Submitted && execution.Site.CheckinStatus == nil {
		// A concurrent worker may still be waiting for its submission response.
		// Only the owner or an expired deadline can settle this workflow.
		return e.store.WorkflowResult(ctx, id)
	}
	run.captureBalance(ctx)
	timeout := 30 * time.Second
	if execution.Site.CheckinStatus != nil {
		timeout = time.Duration(execution.Site.CheckinStatus.TimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if enforceDeadline {
		var deadlineCancel context.CancelFunc
		runCtx, deadlineCancel = context.WithDeadline(runCtx, w.Deadline)
		defer deadlineCancel()
	}
	session, err := e.sessions.Ensure(runCtx, execution.Account.CredentialID)
	if err != nil {
		if w.Submitted {
			return run.pending(ctx, "authentication_unavailable", "登录会话暂不可用；保留提交标记，等待状态确认")
		}
		return run.finish(ctx, failedOutcome("authentication_unavailable", "登录会话不可用，本次未提交签到"))
	}
	if session.Credential.ID != execution.Account.CredentialID || session.Credential.IdentityRevision != execution.Credential.IdentityRevision {
		return nil, op.ErrCheckinWorkflowChanged
	}
	if _, err := run.reload(runCtx); err != nil {
		return nil, err
	}
	if execution.Site.CheckinStatus == nil {
		response, err := run.submit(runCtx, false)
		if err != nil {
			if run.execution.Workflow().Submitted {
				return run.finish(ctx, failedOutcome("unconfirmed_checkin", "签到请求结果未确认，本次不会重复提交"))
			}
			return run.submissionError(ctx, err)
		}
		return run.finish(ctx, interpretSubmission(runCtx, run.execution.Site, response))
	}
	config := execution.Site.CheckinStatus
	resumed := w.Submitted
	var submitted *rules.Response
	if !w.Submitted {
		beforeUnchecked := false
		if !config.SkipPrecheck {
			_, state, err := run.query(runCtx)
			if err != nil || state == rules.Unknown {
				return run.pending(ctx, "precheck_pending", "签到前状态未确认，本次未提交；等待再次检查")
			}
			if state == rules.Checked {
				return run.finish(ctx, model.CheckinOutcome{Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonAlreadyCheckedIn, Message: "状态接口确认已签到"})
			}
			beforeUnchecked = true
		}
		if err := runCtx.Err(); err != nil {
			return run.pending(ctx, "precheck_pending", "签到前检查已中断，本次未提交")
		}
		submitted, err = run.submit(runCtx, beforeUnchecked)
		if err != nil && !run.execution.Workflow().Submitted {
			return run.submissionError(ctx, err)
		}
		// Submission errors (including 401) do not prove non-delivery. Queries
		// below may recover authentication; the submission is never repeated.
		if !waitSession(runCtx, time.Duration(config.InitialDelayMs)*time.Millisecond) {
			return run.pending(ctx, "pending_confirmation", "签到已提交，结果待确认；后续只查询状态")
		}
	}
	for attempt := 0; attempt < config.MaxAttempts; attempt++ {
		if attempt > 0 && !waitSession(runCtx, time.Duration(config.PollIntervalMs)*time.Millisecond) {
			break
		}
		response, state, err := run.query(runCtx)
		if err == nil && state == rules.Checked {
			outcome := model.CheckinOutcome{Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonAlreadyCheckedIn, Message: "状态接口确认已签到"}
			if run.execution.Workflow().BeforeUnchecked {
				outcome.Reason, outcome.Message = model.SiteCheckinReasonCheckedIn, "状态接口已确认本次签到成功"
				// A resumed run has no original submission response or balance snapshot.
				// A status reward remains valid when it is explicitly selected.
				if !resumed || config.RewardSource == "status" {
					outcome.Reward = numericReward(rules.Reward(runCtx, execution.Site.CheckinRewardExtractor, config.RewardSource, submitted, response))
				}
			}
			return run.finish(ctx, outcome)
		}
		if errors.Is(err, op.ErrCheckinWorkflowChanged) || errors.Is(err, op.ErrCheckinWorkflowConflict) || errors.Is(err, op.ErrCheckinUnavailable) {
			return nil, err
		}
		if runCtx.Err() != nil {
			break
		}
	}
	return run.pending(ctx, "pending_confirmation", "签到结果待确认，后续只查询状态，不重复提交")
}

func (r *checkinRun) reload(ctx context.Context) (*op.CheckinExecution, error) {
	current, err := r.engine.store.CheckWorkflow(ctx, r.execution)
	if err != nil {
		return nil, err
	}
	r.execution = current
	return current, nil
}

// A request that failed to build before reservation can be retried with backoff.
// A store error can mean the reservation committed with a lost acknowledgment.
func (r *checkinRun) submissionError(ctx context.Context, err error) (*model.CheckinResult, error) {
	if r.execution.Trigger == model.CheckinTriggerScheduled && errors.Is(err, ErrCheckinRequest) {
		return r.finish(ctx, failedOutcome("preparation_failed", "签到请求准备失败，稍后重试"))
	}
	return nil, err
}

func (r *checkinRun) submit(ctx context.Context, beforeUnchecked bool) (*rules.Response, error) {
	if _, err := r.reload(ctx); err != nil {
		return nil, err
	}
	// Validate HTTP construction and proxy availability before reserving a send.
	prepared, err := prepareCheckinRequest(ctx, r.engine.proxies, r.execution, false)
	if err != nil {
		return nil, err
	}
	prepared.cleanup()
	reserved, err := r.engine.store.ReserveWorkflowSubmission(ctx, r.execution, beforeUnchecked)
	if err != nil {
		return nil, err
	}
	r.execution = reserved
	prepared, err = prepareCheckinRequest(ctx, r.engine.proxies, reserved, false)
	if err != nil {
		return nil, err
	}
	return prepared.send()
}

func (r *checkinRun) query(ctx context.Context) (*rules.Response, string, error) {
	current, err := r.reload(ctx)
	if err != nil {
		return nil, rules.Unknown, err
	}
	config := current.Site.CheckinStatus
	if config == nil {
		return nil, rules.Unknown, op.ErrCheckinAdapter
	}
	location, err := time.LoadLocation(current.Site.CheckinTimezone)
	if err != nil {
		return nil, rules.Unknown, err
	}
	needsDateEvidence := func() bool {
		return current.Workflow().Date != r.engine.now().In(location).Format("2006-01-02") && config.DatePath == "" && config.Code == ""
	}
	if needsDateEvidence() {
		return nil, rules.Unknown, nil
	}
	request := func() (*rules.Response, error) {
		prepared, err := prepareCheckinRequest(ctx, r.engine.proxies, r.execution, true)
		if err != nil {
			return nil, err
		}
		return prepared.send()
	}
	response, err := request()
	if err == nil && !r.recoveryUsed && recoverableCheckinQuery(response, current.Credential) {
		r.recoveryUsed = true
		if _, err := r.engine.sessions.RecoverRead(ctx, Session{Credential: current.Credential}); err != nil {
			return nil, rules.Unknown, err
		}
		if _, err := r.reload(ctx); err != nil {
			return nil, rules.Unknown, err
		}
		response, err = request()
	}
	if err != nil {
		return nil, rules.Unknown, err
	}
	// A today-only endpoint may roll over while the request is in flight. Its
	// response can no longer establish the status of the workflow's original day.
	if needsDateEvidence() {
		return response, rules.Unknown, nil
	}
	state, err := rules.Evaluate(ctx, config, response, current.Workflow().Date)
	return response, state, err
}

func (r *checkinRun) finish(ctx context.Context, outcome model.CheckinOutcome) (*model.CheckinResult, error) {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	result, err := r.engine.store.FinishWorkflow(persistCtx, r.execution, outcome)
	if errors.Is(err, op.ErrCheckinWorkflowConflict) {
		// The winner owns all log/reward writes. Losing confirmations only replay.
		return r.engine.store.WorkflowResult(persistCtx, r.execution.Account.ID)
	}
	if err == nil && result != nil && result.Status != model.SiteExecutionStatusPending && !result.Replayed {
		r.complete(ctx, result)
	}
	return result, err
}

func (r *checkinRun) captureBalance(ctx context.Context) {
	if r.engine.balances == nil || r.execution.Workflow().Submitted || r.execution.Site.Platform == model.SitePlatformAgentRouter {
		return
	}
	value, err := r.engine.balances.Query(ctx, r.execution.Account.ID, false)
	if err == nil {
		r.beforeBalance = value
	}
}

func (r *checkinRun) complete(ctx context.Context, result *model.CheckinResult) {
	var balance *float64
	if result.Status == model.SiteExecutionStatusSuccess && r.engine.balances != nil {
		after, err := r.engine.balances.Query(context.WithoutCancel(ctx), result.AccountID, true)
		if err == nil {
			balance = &after.Value
			if r.beforeBalance != nil && result.Reason == model.SiteCheckinReasonCheckedIn && result.Reward == "" && r.execution.Site.Platform != model.SitePlatformAgentRouter {
				reward := finance.Reward(r.beforeBalance.Balance, after.Balance)
				saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				saved, saveErr := r.engine.balances.store.FillBalanceReward(saveCtx, result, r.beforeBalance.Snapshot, after.Snapshot, reward)
				cancel()
				if saveErr == nil && saved {
					result.Reward = reward
				}
			}
		}
	}
	if store, ok := r.engine.store.(interface {
		ReadyCompletion(context.Context, *model.CheckinResult, *float64) error
	}); ok {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := store.ReadyCompletion(ctx, result, balance); err != nil {
			log.Warnf("check-in completion %d will recover on the next scan", result.LogID)
		}
	}
	if r.engine.notifications != nil {
		r.engine.notifications.Wake()
	}
}

func (r *checkinRun) pending(ctx context.Context, reason, message string) (*model.CheckinResult, error) {
	if r.execution.Workflow().Finished {
		return r.engine.store.WorkflowResult(ctx, r.execution.Account.ID)
	}
	outcome := model.CheckinOutcome{Status: model.SiteExecutionStatusPending, Reason: reason, Message: message}
	if !r.engine.now().Before(r.execution.Workflow().Deadline) {
		outcome = failedOutcome("confirmation_expired", "签到确认期限已到，已提交请求不会重发")
	}
	return r.finish(ctx, outcome)
}

func failedOutcome(reason, message string) model.CheckinOutcome {
	return model.CheckinOutcome{Status: model.SiteExecutionStatusFailed, Reason: reason, Message: message}
}

func interpretSubmission(ctx context.Context, site model.CheckinSite, response *rules.Response) model.CheckinOutcome {
	unknown := failedOutcome("unconfirmed_checkin", "签到响应未确认成功，本次不会重复提交")
	if response == nil || strings.EqualFold(response.Header.Get("Cf-Mitigated"), "challenge") {
		return unknown
	}
	var body map[string]any
	if json.Unmarshal(response.Body, &body) != nil || body == nil {
		return unknown
	}
	message, _ := body["message"].(string)
	if rules.AlreadyCheckedIn(message) && (response.StatusCode >= 200 && response.StatusCode < 300 || response.StatusCode == 400 || response.StatusCode == 409) {
		return model.CheckinOutcome{Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonAlreadyCheckedIn, Message: "签到接口确认今日已签到"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || body["success"] != true {
		return unknown
	}
	return model.CheckinOutcome{Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonCheckedIn,
		Message: "签到响应已确认成功", Reward: numericReward(rules.Reward(ctx, site.CheckinRewardExtractor, "submit", response, nil))}
}

func numericReward(value string) string {
	number, err := strconv.ParseFloat(value, 64)
	if len(value) > 128 || err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return ""
	}
	return strconv.FormatFloat(number, 'f', -1, 64)
}
