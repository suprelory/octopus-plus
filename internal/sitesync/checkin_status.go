package sitesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/snowflake"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type checkinTriggerKey struct{}

func checkinWorkflowFingerprint(site *model.Site, account *model.SiteAccount) string {
	return site.CheckinWorkflowFingerprint(account)
}

func checkinWithStatus(ctx context.Context, site *model.Site, account *model.SiteAccount) (*model.SiteCheckinResult, string, error) {
	config := *site.CheckinStatus
	if err := config.Validate(); err != nil {
		return nil, "", err
	}
	if err := model.ValidateSiteCheckinCookie(account.Cookie); err != nil {
		return nil, "", err
	}
	workflow, err := beginStatusCheckin(ctx, site, account, &config)
	if err != nil {
		return nil, "", err
	}
	result := func(status model.SiteExecutionStatus, reason, message string) *model.SiteCheckinResult {
		return &model.SiteCheckinResult{LogID: workflow.LogID, Status: status, Reason: reason, Message: message, Workflow: workflow}
	}
	manual := isManualStatusCheckin(ctx)
	var stopped *model.SiteCheckinResult
	if workflow.Finished {
		stopped, err = replayStatusCheckin(ctx, site, account, workflow)
		if err != nil {
			return nil, "", err
		}
		if !manual || !workflow.Submitted || workflow.Status != model.SiteExecutionStatusFailed {
			return stopped, "", nil
		}
	} else {
		stopped = statusCheckinStopResult(site, account, workflow, time.Now())
		if stopped != nil && (!manual || !workflow.Submitted) {
			return stopped, "", nil
		}
	}
	deadline := time.Now().Add(time.Duration(config.TimeoutSeconds) * time.Second)
	// A manual recheck gets a bounded query window even after confirmation expired.
	// Submitted remains set, so this path can never send another check-in request.
	if stopped == nil && workflow.Deadline.Before(deadline) {
		deadline = workflow.Deadline
	}
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	queryDetail := ""
	query := func() (*configuredCheckinResponse, string, error) {
		querySite := *site
		querySite.CheckinHTTPMethod, querySite.CheckinHTTPPath = config.Method, strings.ReplaceAll(config.Path, "{{checkin_date}}", workflow.Date)
		querySite.CheckinHTTPBody = strings.ReplaceAll(config.Body, "{{checkin_date}}", workflow.Date)
		querySite.CheckinHTTPHeaders = config.Headers
		response, _, err := requestConfiguredCheckinHTTP(runCtx, &querySite, account)
		if err != nil {
			queryDetail = "状态查询请求失败或超时"
			return nil, checkinStateUnknown, err
		}
		queryDetail = "状态响应没有匹配到已签到或未签到条件"
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			queryDetail = fmt.Sprintf("状态接口返回 HTTP %d", response.StatusCode)
		}
		// Without a date-aware rule a 'today' endpoint cannot confirm yesterday.
		if workflow.Date != time.Now().In(resolveSiteCheckinWindow(site).location).Format("2006-01-02") && config.DatePath == "" && config.Code == "" {
			return response, checkinStateUnknown, nil
		}
		state, err := evaluateCheckinStatus(runCtx, &config, response, workflow.Date)
		if err != nil {
			queryDetail = "状态判定代码执行失败，请检查返回值和资源限制"
		}
		if state == checkinStateUnchecked {
			queryDetail = "状态接口暂未确认签到完成"
		}
		return response, state, err
	}
	confirm := func(response, submit *configuredCheckinResponse, resumed bool) *model.SiteCheckinResult {
		reason := model.SiteCheckinReasonAlreadyCheckedIn
		message := "今日已签到"
		if workflow.Submitted && workflow.BeforeUnchecked {
			reason, message = model.SiteCheckinReasonCheckedIn, "状态接口已确认签到成功"
		}
		r := result(model.SiteExecutionStatusSuccess, reason, message)
		r.CapabilityEvidence = model.SiteCheckinSupportSupported
		r.SkipBalanceReward = resumed
		if reason == model.SiteCheckinReasonCheckedIn {
			r.Reward = extractStatusCheckinReward(runCtx, site, submit, response)
		}
		return r
	}
	pending := func() *model.SiteCheckinResult {
		if stopped != nil {
			stopped.Message += "；本次仅复查状态，仍未确认签到成功"
			if queryDetail != "" {
				stopped.Message += "（" + queryDetail + "）"
			}
			return stopped
		}
		message := "签到结果待确认，后续只查询状态，不重复提交"
		if !workflow.Submitted {
			message = "签到前状态无法确认，暂未提交；后续重查状态"
		}
		if queryDetail != "" {
			message += "（" + queryDetail + "）"
		}
		return result(model.SiteExecutionStatusPending, "pending_confirmation", message)
	}
	resumed := workflow.Submitted
	var submit *configuredCheckinResponse
	if !workflow.Submitted {
		if !config.SkipPrecheck {
			response, state, _ := query()
			if state == checkinStateChecked {
				return confirm(response, nil, false), "", nil
			}
			if state != checkinStateUnchecked {
				return pending(), "", nil
			}
			workflow.BeforeUnchecked = true
		}
		if runCtx.Err() != nil {
			return pending(), "", nil
		}
		if trigger, _ := ctx.Value(checkinTriggerKey{}).(SiteBatchTrigger); trigger == SiteBatchTriggerScheduled && !checkinTimeWithinWindow(site, time.Now()) {
			queryDetail = "状态已确认为未签到，等待允许的签到时间窗口"
			return pending(), "", nil
		}
		// Persist before sending. Even a crash between this write and HTTP must
		// resume by querying, because the server cannot know whether POST arrived.
		if err := markStatusCheckinSubmitted(runCtx, account, workflow); err != nil {
			queryDetail = "未能取得签到提交权限，后续重新读取执行进度"
			return pending(), "", nil
		}
		submit, _, _ = requestConfiguredCheckinHTTP(runCtx, site, account)
		// Submission transport/HTTP errors do not prove that the operation failed.
		if !waitCheckinStatus(runCtx, time.Duration(config.InitialDelayMs)*time.Millisecond) {
			return pending(), "", nil
		}
	}
	for attempt := 0; attempt < config.MaxAttempts; attempt++ {
		if attempt > 0 && !waitCheckinStatus(runCtx, time.Duration(config.PollIntervalMs)*time.Millisecond) {
			break
		}
		response, state, _ := query()
		if state == checkinStateChecked {
			return confirm(response, submit, resumed), "", nil
		}
		if runCtx.Err() != nil {
			break
		}
	}
	return pending(), "", nil
}

func waitCheckinStatus(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func beginStatusCheckin(ctx context.Context, site *model.Site, account *model.SiteAccount, config *model.SiteCheckinStatusConfig) (*model.SiteCheckinPending, error) {
	now := time.Now().UTC()
	date := now.In(resolveSiteCheckinWindow(site).location).Format("2006-01-02")
	if previous := account.CheckinPending; previous != nil && isManualStatusCheckin(ctx) && !previous.Submitted && previous.Status != model.SiteExecutionStatusSuccess {
		if previous.Finished || statusCheckinStopResult(site, account, previous, now) != nil {
			if err := resetUnsubmittedStatusCheckin(ctx, site, account); err != nil {
				return nil, err
			}
		}
	}
	if previous := account.CheckinPending; previous != nil && (!previous.Finished || previous.Date == date) {
		recheckingFailure := isManualStatusCheckin(ctx) && previous.Submitted && previous.Finished && previous.Status == model.SiteExecutionStatusFailed
		if !previous.Finished || recheckingFailure {
			// Backups may omit history. Retain the reservation when recreating a log
			// so an unfinished or failed submission can be confirmed without resending.
			entry := model.SiteCheckinLog{ID: previous.LogID, SiteID: site.ID, AccountID: account.ID, SiteName: site.Name, AccountName: account.Name, Platform: site.Platform,
				Source: "import", Status: model.SiteExecutionStatusPending, Reason: "pending_confirmation", Message: "继续确认签到状态", StartedAt: previous.StartedAt, FinishedAt: now}
			if recheckingFailure {
				original, err := replayStatusCheckin(ctx, site, account, previous)
				if err != nil {
					return nil, err
				}
				entry.Status, entry.Reason, entry.Message = original.Status, original.Reason, original.Message
			}
			if err := db.GetDB().WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&entry).Error; err != nil {
				return nil, err
			}
		}
		return previous, nil
	}
	workflow := &model.SiteCheckinPending{LogID: snowflake.GenerateID(), Date: date, StartedAt: now,
		Deadline: now.Add(time.Duration(config.ConfirmTimeoutMinutes) * time.Minute), Fingerprint: checkinWorkflowFingerprint(site, account)}
	trigger, _ := ctx.Value(checkinTriggerKey{}).(SiteBatchTrigger)
	if trigger == "" {
		trigger = SiteBatchTriggerManual
	}
	entry := model.SiteCheckinLog{ID: workflow.LogID, SiteID: site.ID, AccountID: account.ID, SiteName: site.Name, AccountName: account.Name, Platform: site.Platform,
		Source: string(trigger), Status: model.SiteExecutionStatusPending, Reason: "pending_confirmation", Message: "正在检查签到状态", StartedAt: now, FinishedAt: now, BatchJobID: siteBatchTaskID(ctx)}
	var resumeAt *time.Time
	if account.AutoCheckin {
		next := now.Add(10 * time.Minute)
		resumeAt = &next
	}
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&model.SiteAccount{}).Where("id = ?", account.ID)
		if account.CheckinPending == nil {
			query = query.Where("checkin_pending IS NULL OR checkin_pending = 'null' OR checkin_pending = ''")
		} else {
			old, _ := json.Marshal(account.CheckinPending)
			query = query.Where("checkin_pending = ?", string(old))
		}
		updated := query.Select("checkin_pending", "last_checkin_status", "last_checkin_at", "last_checkin_message", "next_auto_checkin_at").Updates(&model.SiteAccount{
			CheckinPending: workflow, LastCheckinStatus: model.SiteExecutionStatusPending, LastCheckinAt: &now, LastCheckinMessage: "正在检查签到状态", NextAutoCheckinAt: resumeAt})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return fmt.Errorf("checkin state changed; retry to query its status")
		}
		return tx.Create(&entry).Error
	})
	if err != nil {
		return nil, err
	}
	account.CheckinPending = workflow
	return workflow, nil
}

func markStatusCheckinSubmitted(ctx context.Context, account *model.SiteAccount, workflow *model.SiteCheckinPending) error {
	// Read the persisted precheck state; BeforeUnchecked was only set in memory.
	var current model.SiteAccount
	if err := db.GetDB().WithContext(ctx).Select("id", "checkin_pending").First(&current, account.ID).Error; err != nil {
		return err
	}
	if current.CheckinPending == nil || current.CheckinPending.LogID != workflow.LogID || current.CheckinPending.Submitted || current.CheckinPending.Finished {
		return fmt.Errorf("checkin submission already reserved; retry to query its status")
	}
	old, _ := json.Marshal(current.CheckinPending)
	workflow.Submitted = true
	updated := db.GetDB().WithContext(ctx).Model(&model.SiteAccount{}).Where("id = ? AND checkin_pending = ?", account.ID, string(old)).
		Select("checkin_pending").Updates(&model.SiteAccount{CheckinPending: workflow})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return fmt.Errorf("checkin submission already reserved; retry to query its status")
	}
	return nil
}

func persistStatusWorkflow(tx *gorm.DB, account *model.SiteAccount, result *model.SiteCheckinResult, entry *model.SiteCheckinLog) (bool, error) {
	var current model.SiteAccount
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "checkin_pending").First(&current, account.ID).Error; err != nil {
		return false, err
	}
	workflow := result.Workflow
	recovering := workflow.Finished && workflow.Submitted && workflow.Status == model.SiteExecutionStatusFailed && result.Status == model.SiteExecutionStatusSuccess
	if current.CheckinPending == nil || current.CheckinPending.LogID != workflow.LogID ||
		(current.CheckinPending.Finished && !(recovering && current.CheckinPending.Submitted && current.CheckinPending.Status == model.SiteExecutionStatusFailed)) ||
		(current.CheckinPending.Submitted && !workflow.Submitted) {
		return false, nil
	}
	saved, err := updateStatusCheckinLog(tx, entry, workflow)
	if err != nil || !saved {
		return saved, err
	}
	workflow.Finished = result.Status != model.SiteExecutionStatusPending
	workflow.Status, workflow.Reason = result.Status, result.Reason
	if err := tx.Model(&model.SiteAccount{}).Where("id = ?", account.ID).Select("checkin_pending").Updates(&model.SiteAccount{CheckinPending: workflow}).Error; err != nil {
		return false, err
	}
	account.CheckinPending = workflow
	return true, nil
}

func updateStatusCheckinLog(tx *gorm.DB, entry *model.SiteCheckinLog, workflow *model.SiteCheckinPending) (bool, error) {
	previousStatus := model.SiteExecutionStatusPending
	if workflow.Finished && workflow.Submitted && workflow.Status == model.SiteExecutionStatusFailed && entry.Status == model.SiteExecutionStatusSuccess {
		previousStatus = model.SiteExecutionStatusFailed
	}
	updated := tx.Model(&model.SiteCheckinLog{}).Where("id = ? AND account_id = ? AND site_id = ? AND status = ?", entry.ID, entry.AccountID, entry.SiteID, previousStatus).
		Updates(map[string]any{"status": entry.Status, "reason": entry.Reason, "message": entry.Message, "reward": entry.Reward, "duration_ms": entry.DurationMs, "finished_at": entry.FinishedAt})
	return updated.RowsAffected > 0, updated.Error
}

func isManualStatusCheckin(ctx context.Context) bool {
	trigger, _ := ctx.Value(checkinTriggerKey{}).(SiteBatchTrigger)
	return trigger == "" || trigger == SiteBatchTriggerManual
}

func statusCheckinStopResult(site *model.Site, account *model.SiteAccount, workflow *model.SiteCheckinPending, now time.Time) *model.SiteCheckinResult {
	reason, message := "", ""
	switch {
	case workflow.Fingerprint != checkinWorkflowFingerprint(site, account):
		reason, message = "checkin_config_changed", "待确认期间配置或凭据已改变；已提交的请求不会重复发送，可手动复查状态"
		if !workflow.Submitted {
			message = "签到前配置或凭据已改变，本次未提交；可手动重新检查"
		}
	case !now.Before(workflow.Deadline):
		reason, message = model.SiteCheckinReasonUnconfirmed, "签到确认期限已到，结果仍未确认；本次不会重新提交，可手动复查状态"
		if !workflow.Submitted {
			message = "签到前状态确认已超时，本次未提交；可手动重新检查"
		}
	case !workflow.Submitted && workflow.Date != now.In(resolveSiteCheckinWindow(site).location).Format("2006-01-02"):
		reason, message = model.SiteCheckinReasonUnconfirmed, "前置检查已跨日，本次未提交；下次执行将检查今日状态"
	default:
		return nil
	}
	return &model.SiteCheckinResult{LogID: workflow.LogID, Workflow: workflow, Status: model.SiteExecutionStatusFailed, Reason: reason, Message: message}
}

func replayStatusCheckin(ctx context.Context, site *model.Site, account *model.SiteAccount, workflow *model.SiteCheckinPending) (*model.SiteCheckinResult, error) {
	result := &model.SiteCheckinResult{LogID: workflow.LogID, Workflow: workflow, Status: workflow.Status, Reason: workflow.Reason, Replayed: true}
	if workflow.Status == model.SiteExecutionStatusSuccess {
		result.Reason, result.Message = model.SiteCheckinReasonAlreadyCheckedIn, "今日已签到"
		return result, nil
	}
	// Read the original sanitized message rather than hiding it behind a generic
	// replay message. Older backups can contain a workflow without its history.
	var entry model.SiteCheckinLog
	err := db.GetDB().WithContext(ctx).Select("message").Where("id = ? AND site_id = ? AND account_id = ?", workflow.LogID, site.ID, account.ID).First(&entry).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	result.Message = entry.Message
	if result.Message == "" && account.LastCheckinStatus == workflow.Status {
		result.Message = account.LastCheckinMessage
	}
	if result.Message == "" {
		switch workflow.Reason {
		case "checkin_config_changed":
			result.Message = "此前签到因配置或凭据改变而终止，请检查状态查询配置"
		case model.SiteCheckinReasonUnconfirmed:
			result.Message = "此前签到未能在确认期限内确认结果"
		default:
			result.Message = "此前签到未完成，请查看签到记录中的失败原因"
		}
	}
	return result, nil
}

func resetUnsubmittedStatusCheckin(ctx context.Context, site *model.Site, account *model.SiteAccount) error {
	previous := account.CheckinPending
	if previous == nil || previous.Submitted || previous.Status == model.SiteExecutionStatusSuccess {
		return fmt.Errorf("only an unsubmitted checkin can be restarted")
	}
	now := time.Now().UTC()
	outcome := statusCheckinStopResult(site, account, previous, now)
	if outcome == nil {
		outcome = &model.SiteCheckinResult{Status: model.SiteExecutionStatusFailed, Reason: model.SiteCheckinReasonUnconfirmed, Message: "签到前检查未完成，本次未提交；已重新开始检查"}
	}
	old, err := json.Marshal(previous)
	if err != nil {
		return err
	}
	err = db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Compare the whole reservation so a concurrent sender cannot be cleared.
		updated := tx.Model(&model.SiteAccount{}).Where("id = ? AND checkin_pending = ?", account.ID, string(old)).Update("checkin_pending", nil)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return fmt.Errorf("checkin state changed; retry to query its status")
		}
		entry := &model.SiteCheckinLog{ID: previous.LogID, SiteID: site.ID, AccountID: account.ID,
			Status: outcome.Status, Reason: outcome.Reason, Message: outcome.Message, FinishedAt: now, DurationMs: now.Sub(previous.StartedAt).Milliseconds()}
		_, err := updateStatusCheckinLog(tx, entry, previous)
		return err
	})
	if err == nil {
		account.CheckinPending = nil
	}
	return err
}
