package op

import (
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/snowflake"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrCheckinUnavailable      = errors.New("checkin site or account is disabled or archived")
	ErrCheckinAdapter          = errors.New("checkin execution adapter is not available")
	ErrCheckinWorkflowConflict = errors.New("checkin workflow changed; reload its result")
	ErrCheckinWorkflowChanged  = errors.New("checkin configuration or identity changed")
	ErrCheckinNotSubmitted     = errors.New("no submitted checkin workflow to confirm")
	ErrCheckinNotDue           = errors.New("checkin submission is outside its schedule")
	ErrCheckinWorkflowExpired  = errors.New("checkin workflow is outside its submission date or deadline")
)

// CheckinExecution is an internal snapshot. Its workflow version authorizes one
// transition only; refreshed tokens do not change the identity fingerprint.
type CheckinExecution struct {
	Site              model.CheckinSite       `json:"-"`
	Account           model.CheckinAccount    `json:"-"`
	Credential        model.CheckinCredential `json:"-"`
	CheckinInvocation `json:"-"`
}

type CheckinInvocation struct {
	Trigger          model.CheckinTrigger
	Confirm          bool
	BatchJobID       int64
	ScheduledVersion *int64
}

func (e CheckinExecution) Workflow() *model.SiteCheckinPending { return e.Account.CheckinPending }

func loadCheckinExecution(tx *gorm.DB, id int, request CheckinInvocation) (*CheckinExecution, error) {
	var batch *model.CheckinBatchJob
	if request.BatchJobID != 0 {
		var err error
		batch, err = runningCheckinBatch(tx, request.BatchJobID)
		if err != nil {
			return nil, err
		}
	}
	site, account, err := lockCheckinAccount(tx, id)
	if err != nil {
		return nil, err
	}
	if site.Archived || !site.Enabled || !account.Enabled || site.CheckinMode == model.SiteCheckinModeDisabled {
		return nil, ErrCheckinUnavailable
	}
	if (request.Trigger == model.CheckinTriggerScheduled || batch != nil) && !account.AutoCheckin {
		return nil, ErrCheckinNotDue
	}
	if batch != nil && len(batch.SiteIDs) != 0 && !slices.Contains(batch.SiteIDs, site.ID) {
		return nil, model.ErrCheckinBatchSelection
	}
	var credential model.CheckinCredential
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&credential, account.CredentialID).Error; err != nil {
		return nil, err
	}
	return &CheckinExecution{Site: site, Account: account, Credential: credential, CheckinInvocation: request}, nil
}

func checkinCalendarDate(site model.CheckinSite, now time.Time) (string, error) {
	location, err := time.LoadLocation(site.CheckinTimezone)
	if err != nil {
		return "", errors.New("invalid checkin timezone")
	}
	return now.In(location).Format("2006-01-02"), nil
}

func checkinScheduleAllows(e *CheckinExecution, now time.Time) bool {
	if e.Trigger != model.CheckinTriggerScheduled {
		return true
	}
	if !e.Account.AutoCheckin || e.Account.NextAutoCheckinAt == nil || now.Before(*e.Account.NextAutoCheckinAt) {
		return false
	}
	location, err := time.LoadLocation(e.Site.CheckinTimezone)
	if err != nil {
		return false
	}
	clock := now.In(location).Format("15:04")
	return clock >= e.Site.CheckinWindowStart && clock <= e.Site.CheckinWindowEnd
}

func checkinExecutionAllowed(e *CheckinExecution) error {
	capability := e.Site.ResolveCheckinCapability()
	if !capability.CanVerify {
		return ErrCheckinAdapter
	}
	if e.Trigger != model.CheckinTriggerManual && !capability.Enabled {
		return ErrCheckinUnavailable
	}
	return nil
}

// BeginWorkflow resumes an unfinished reservation before considering a new day.
// Confirm never creates a workflow. A submitted reservation cannot be cleared by
// changes to configuration, identity, clocks, or an omitted history backup.
func (s *CheckinStore) BeginWorkflow(ctx context.Context, id int, trigger model.CheckinTrigger, confirm bool) (*CheckinExecution, error) {
	return s.BeginExecution(ctx, id, CheckinInvocation{Trigger: trigger, Confirm: confirm})
}

func (s *CheckinStore) BeginExecution(ctx context.Context, id int, request CheckinInvocation) (*CheckinExecution, error) {
	trigger, confirm := request.Trigger, request.Confirm
	if trigger != model.CheckinTriggerManual && trigger != model.CheckinTriggerScheduled && trigger != model.CheckinTriggerBatch {
		return nil, errors.New("invalid checkin trigger")
	}
	if request.BatchJobID != 0 && trigger != model.CheckinTriggerBatch {
		return nil, model.ErrCheckinBatchSelection
	}
	var execution *CheckinExecution
	var err error
	// Concurrent instances can generate the same time-based log ID. A failed log
	// insert rolls back the reservation; retry allocation without any HTTP call.
	for attempt := 0; attempt < 3; attempt++ {
		allocationConflict := false
		err = s.transaction(ctx, func(tx *gorm.DB) error {
			var err error
			execution, err = loadCheckinExecution(tx, id, request)
			if err != nil {
				return err
			}
			e := execution
			now := time.Now().UTC()
			date, err := checkinCalendarDate(e.Site, now)
			if err != nil {
				return err
			}
			previous := e.Workflow()
			if request.ScheduledVersion != nil {
				if e.Account.CheckinWorkflowVersion != *request.ScheduledVersion || e.Account.NextAutoCheckinAt == nil || now.Before(*e.Account.NextAutoCheckinAt) {
					return ErrCheckinNotDue
				}
				if err := checkinExecutionAllowed(e); err != nil {
					return err
				}
			}
			if confirm && (previous == nil || !previous.Submitted) {
				return ErrCheckinNotSubmitted
			}
			fingerprint := e.Site.WorkflowFingerprint(e.Account, e.Credential)
			resume := previous != nil && (confirm || !previous.Finished || previous.Date == date)
			scheduledRetry := request.ScheduledVersion != nil && retryUnsubmitted(previous) && checkinScheduleAllows(e, now)
			manualRetry := previous != nil && trigger == model.CheckinTriggerManual && (previous.Finished || previous.Fingerprint != fingerprint || !now.Before(previous.Deadline) || previous.Date != date)
			if resume && !previous.Submitted && previous.Status != model.SiteExecutionStatusSuccess && (manualRetry || scheduledRetry) {
				// Preserve the historical log when replacing an unsubmitted attempt.
				if err := ensureCheckinWorkflowLog(tx, e); err != nil {
					return err
				}
				if !previous.Finished {
					if err := tx.Model(&model.CheckinLog{}).Where("id = ? AND account_id = ?", previous.LogID, id).
						Updates(map[string]any{"status": model.SiteExecutionStatusFailed, "reason": "precheck_restarted", "message": "未提交的前置检查已重新开始", "finished_at": now}).Error; err != nil {
						return err
					}
				}
				resume = false
			}
			if resume {
				if request.ScheduledVersion != nil && !previous.Finished &&
					(previous.Fingerprint != fingerprint || !now.Before(previous.Deadline) || !previous.Submitted && previous.Date != date) {
					// Close the original log even when the identity no longer matches.
					// Nothing in this transition grants permission to send a request.
					reason := "confirmation_expired"
					if previous.Fingerprint != fingerprint {
						reason = "configuration_changed"
					}
					_, err := saveCheckinOutcome(tx, e, model.CheckinOutcome{Status: model.SiteExecutionStatusFailed, Reason: reason, Message: "原签到任务已结束，请核对原日期和配置"}, now)
					return err
				}
				if request.ScheduledVersion != nil && !previous.Finished && !previous.Submitted && !checkinScheduleAllows(e, now) {
					return ErrCheckinNotDue
				}
				return ensureCheckinWorkflowLog(tx, e)
			}
			if err := checkinExecutionAllowed(e); err != nil {
				return err
			}
			if err := e.Site.Validate(); err != nil {
				return err
			}
			if err := model.ValidateCheckinIdentity(e.Site, e.Account, e.Credential); err != nil {
				return err
			}
			if !checkinScheduleAllows(e, now) {
				return ErrCheckinNotDue
			}
			var maxID int64
			if err := tx.Model(&model.CheckinLog{}).Select("COALESCE(MAX(id), 0)").Scan(&maxID).Error; err != nil {
				return err
			}
			logID, err := snowflake.GenerateIDAfter(maxID)
			if err != nil {
				return err
			}
			window := time.Hour
			if e.Site.CheckinStatus != nil {
				window = time.Duration(e.Site.CheckinStatus.ConfirmTimeoutMinutes) * time.Minute
			}
			e.Account.CheckinPending = &model.SiteCheckinPending{LogID: logID, Date: date, Fingerprint: fingerprint, Source: trigger, BatchJobID: request.BatchJobID,
				StartedAt: now, Deadline: now.Add(window), Status: model.SiteExecutionStatusPending, Reason: "pending_confirmation"}
			entry := checkinWorkflowLog(e)
			if err := tx.Create(&entry).Error; err != nil {
				allocationConflict = errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(strings.ToLower(err.Error()), "unique constraint")
				return err
			}
			return writeCheckinWorkflow(tx, e)
		})
		if err == nil || !allocationConflict {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	return execution, nil
}

func checkinWorkflowLog(e *CheckinExecution) model.CheckinLog {
	w := e.Workflow()
	status, reason := w.Status, w.Reason
	if status == "" {
		status = model.SiteExecutionStatusPending
	}
	if reason == "" {
		reason = "pending_confirmation"
	}
	source := w.Source
	if source == "" {
		source = e.Trigger
	}
	return model.CheckinLog{ID: w.LogID, SiteID: e.Site.ID, AccountID: e.Account.ID, SiteName: e.Site.Name,
		AccountName: e.Account.Name, Platform: e.Site.Platform, Source: string(source), BatchJobID: w.BatchJobID, Status: status,
		Reason: reason, Message: "继续检查签到状态", StartedAt: w.StartedAt, FinishedAt: time.Now().UTC()}
}

func ensureCheckinWorkflowLog(tx *gorm.DB, e *CheckinExecution) error {
	w := e.Workflow()
	if w == nil || w.LogID <= 0 {
		return ErrCheckinWorkflowConflict
	}
	var entry model.CheckinLog
	err := tx.First(&entry, w.LogID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		entry = checkinWorkflowLog(e)
		return tx.Create(&entry).Error
	}
	if err != nil {
		return err
	}
	if entry.SiteID != e.Site.ID || entry.AccountID != e.Account.ID {
		return ErrCheckinWorkflowConflict
	}
	return nil
}

func writeCheckinWorkflow(tx *gorm.DB, e *CheckinExecution) error {
	version := e.Account.CheckinWorkflowVersion
	e.Account.CheckinWorkflowVersion++
	result := tx.Model(&model.CheckinAccount{}).Where("id = ? AND checkin_workflow_version = ?", e.Account.ID, version).
		Select("checkin_pending", "checkin_workflow_version").Updates(&e.Account)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCheckinWorkflowConflict
	}
	return nil
}

func checkinWorkflowMatches(current, expected *CheckinExecution) error {
	if current.Workflow() == nil || expected.Workflow() == nil || current.Workflow().LogID != expected.Workflow().LogID ||
		current.Account.CheckinWorkflowVersion != expected.Account.CheckinWorkflowVersion {
		return ErrCheckinWorkflowConflict
	}
	if current.Workflow().Fingerprint != current.Site.WorkflowFingerprint(current.Account, current.Credential) ||
		current.Workflow().Fingerprint != expected.Workflow().Fingerprint {
		return ErrCheckinWorkflowChanged
	}
	return nil
}

// CheckWorkflow rereads availability and identity before each network operation.
// It never replaces the stored fingerprint with the editor's latest settings.
func (s *CheckinStore) CheckWorkflow(ctx context.Context, expected *CheckinExecution) (*CheckinExecution, error) {
	var current *CheckinExecution
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var err error
		current, err = loadCheckinExecution(tx, expected.Account.ID, expected.CheckinInvocation)
		if err != nil {
			return err
		}
		if current.Trigger != model.CheckinTriggerManual {
			if err := checkinExecutionAllowed(current); err != nil {
				return err
			}
		}
		return checkinWorkflowMatches(current, expected)
	})
	if err != nil {
		return nil, err
	}
	return current, nil
}

// ReserveWorkflowSubmission is the only path that sets Submitted. The caller
// sends HTTP only after an acknowledged commit. Losing an acknowledgement can
// therefore postpone a check-in, but can never authorize a duplicate submission.
func (s *CheckinStore) ReserveWorkflowSubmission(ctx context.Context, expected *CheckinExecution, beforeUnchecked bool) (*CheckinExecution, error) {
	var current *CheckinExecution
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var err error
		current, err = loadCheckinExecution(tx, expected.Account.ID, expected.CheckinInvocation)
		if err != nil {
			return err
		}
		if err := checkinWorkflowMatches(current, expected); err != nil {
			return err
		}
		w := current.Workflow()
		if w.Submitted || w.Finished {
			return ErrCheckinWorkflowConflict
		}
		// A built-in adapter has already prepared its one mutation using this
		// exact session. An intervening login or ID edit invalidates the send.
		if !current.Site.CheckinHTTPEnabled && !sameWorkflowSession(current.Credential, expected.Credential) {
			return ErrCheckinCredentialConflict
		}
		now := time.Now()
		date, err := checkinCalendarDate(current.Site, now)
		if err != nil {
			return err
		}
		if date != w.Date || !now.Before(w.Deadline) {
			return ErrCheckinWorkflowExpired
		}
		if !checkinScheduleAllows(current, now) {
			return ErrCheckinNotDue
		}
		if err := checkinExecutionAllowed(current); err != nil {
			return err
		}
		w.Submitted, w.BeforeUnchecked = true, beforeUnchecked
		return writeCheckinWorkflow(tx, current)
	})
	if err != nil {
		return nil, err
	}
	return current, nil
}

func validateCheckinOutcome(outcome model.CheckinOutcome) error {
	if outcome.Status != model.SiteExecutionStatusSuccess && outcome.Status != model.SiteExecutionStatusFailed && outcome.Status != model.SiteExecutionStatusPending {
		return errors.New("invalid checkin outcome")
	}
	if len(outcome.Reason) > 64 || len(outcome.Message) > 2048 {
		return errors.New("checkin outcome exceeds its limit")
	}
	if outcome.Reward != "" {
		reward, err := strconv.ParseFloat(outcome.Reward, 64)
		if len(outcome.Reward) > 128 || err != nil || math.IsNaN(reward) || math.IsInf(reward, 0) || reward < 0 || outcome.Status != model.SiteExecutionStatusSuccess || outcome.Reason != model.SiteCheckinReasonCheckedIn {
			return errors.New("invalid checkin reward")
		}
	}
	return nil
}

// FinishWorkflow updates one log and its account atomically. Versioned writes
// make simultaneous confirmations, retries and reward recording idempotent.
func (s *CheckinStore) FinishWorkflow(ctx context.Context, expected *CheckinExecution, outcome model.CheckinOutcome) (*model.CheckinResult, error) {
	if err := validateCheckinOutcome(outcome); err != nil {
		return nil, err
	}
	var result *model.CheckinResult
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		// A canceled batch still records the response to an authorized submission.
		current, err := loadCheckinExecution(tx, expected.Account.ID, CheckinInvocation{Trigger: expected.Trigger})
		if err != nil {
			return err
		}
		if err := checkinWorkflowMatches(current, expected); err != nil {
			return err
		}
		result, err = saveCheckinOutcome(tx, current, outcome, time.Now().UTC())
		return err
	})
	return result, err
}

func saveCheckinOutcome(tx *gorm.DB, current *CheckinExecution, outcome model.CheckinOutcome, now time.Time) (*model.CheckinResult, error) {
	w := current.Workflow()
	if w.Finished && !(w.Submitted && w.Status == model.SiteExecutionStatusFailed && outcome.Status == model.SiteExecutionStatusSuccess) {
		return nil, ErrCheckinWorkflowConflict
	}
	if !w.Submitted && outcome.Status == model.SiteExecutionStatusSuccess && outcome.Reason != model.SiteCheckinReasonAlreadyCheckedIn {
		return nil, ErrCheckinWorkflowConflict
	}
	if err := ensureCheckinWorkflowLog(tx, current); err != nil {
		return nil, err
	}
	priorStatus := w.Status
	w.Status, w.Reason, w.Finished = outcome.Status, outcome.Reason, outcome.Status != model.SiteExecutionStatusPending
	if err := writeCheckinWorkflow(tx, current); err != nil {
		return nil, err
	}
	entry := checkinWorkflowLog(current)
	entry.Status, entry.Reason, entry.Message, entry.Reward = outcome.Status, outcome.Reason, outcome.Message, outcome.Reward
	entry.FinishedAt, entry.DurationMs = now, max(int64(0), now.Sub(w.StartedAt).Milliseconds())
	if err := tx.Model(&model.CheckinLog{}).Where("id = ? AND account_id = ? AND site_id = ?", w.LogID, current.Account.ID, current.Site.ID).
		Select("status", "reason", "message", "reward", "finished_at", "duration_ms").Updates(&entry).Error; err != nil {
		return nil, err
	}
	updates := map[string]any{"last_checkin_status": outcome.Status, "last_checkin_message": outcome.Message, "last_checkin_at": now}
	current.Account.LastCheckinAt = &now
	var evidence model.SiteCheckinSupport
	if outcome.Status == model.SiteExecutionStatusSuccess {
		current.Account.LastCheckinSuccessAt, current.Account.CheckinFailureCount = &now, 0
		updates["last_checkin_success_at"], updates["checkin_failure_count"] = now, 0
		evidence = model.SiteCheckinSupportSupported
	} else if outcome.Status == model.SiteExecutionStatusFailed && priorStatus != model.SiteExecutionStatusFailed {
		current.Account.CheckinFailureCount++
		updates["checkin_failure_count"] = current.Account.CheckinFailureCount
		if outcome.Reason == model.SiteCheckinReasonUnsupported {
			evidence = model.SiteCheckinSupportUnsupported
		}
	}
	if evidence != "" {
		current.Site.VerifiedAt, current.Site.VerificationStatus, current.Site.VerificationFingerprint = &now, evidence, current.Site.ConfigFingerprint()
		if err := tx.Model(&current.Site).Select("verified_at", "verification_status", "verification_fingerprint").Updates(&current.Site).Error; err != nil {
			return nil, err
		}
	}
	next, err := nextCheckinAt(current.Site, current.Account, now, false)
	if err != nil {
		return nil, err
	}
	updates["next_auto_checkin_at"] = next
	if err := tx.Model(&model.CheckinAccount{}).Where("id = ?", current.Account.ID).Updates(updates).Error; err != nil {
		return nil, err
	}
	return checkinResult(current.Account, entry), nil
}

func checkinResult(account model.CheckinAccount, entry model.CheckinLog) *model.CheckinResult {
	return &model.CheckinResult{CheckinOutcome: model.CheckinOutcome{Status: entry.Status, Reason: entry.Reason, Message: entry.Message, Reward: entry.Reward},
		LogID: entry.ID, SiteID: entry.SiteID, AccountID: entry.AccountID, Date: account.CheckinPending.Date, Submitted: account.CheckinPending.Submitted}
}

func (s *CheckinStore) WorkflowResult(ctx context.Context, id int) (*model.CheckinResult, error) {
	var result *model.CheckinResult
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var account model.CheckinAccount
		if err := tx.First(&account, id).Error; err != nil {
			return err
		}
		if account.CheckinPending == nil {
			return ErrCheckinNotSubmitted
		}
		var entry model.CheckinLog
		if err := tx.Where("id = ? AND site_id = ? AND account_id = ?", account.CheckinPending.LogID, account.SiteID, id).First(&entry).Error; err != nil {
			return err
		}
		result = checkinResult(account, entry)
		result.Replayed = true
		return nil
	})
	return result, err
}
