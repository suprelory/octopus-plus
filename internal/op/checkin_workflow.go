package op

import (
	"context"
	"errors"
	"math"
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
	Site       model.CheckinSite       `json:"-"`
	Account    model.CheckinAccount    `json:"-"`
	Credential model.CheckinCredential `json:"-"`
	Trigger    model.CheckinTrigger    `json:"-"`
}

func (e CheckinExecution) Workflow() *model.SiteCheckinPending { return e.Account.CheckinPending }

func loadCheckinExecution(tx *gorm.DB, id int, trigger model.CheckinTrigger) (*CheckinExecution, error) {
	site, account, err := lockCheckinAccount(tx, id)
	if err != nil {
		return nil, err
	}
	if site.Archived || !site.Enabled || !account.Enabled || site.CheckinMode == model.SiteCheckinModeDisabled {
		return nil, ErrCheckinUnavailable
	}
	if trigger == model.CheckinTriggerScheduled && !account.AutoCheckin {
		return nil, ErrCheckinNotDue
	}
	var credential model.CheckinCredential
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&credential, account.CredentialID).Error; err != nil {
		return nil, err
	}
	return &CheckinExecution{Site: site, Account: account, Credential: credential, Trigger: trigger}, nil
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

// BeginWorkflow resumes an unfinished reservation before considering a new day.
// Confirm never creates a workflow. A submitted reservation cannot be cleared by
// changes to configuration, identity, clocks, or an omitted history backup.
func (s *CheckinStore) BeginWorkflow(ctx context.Context, id int, trigger model.CheckinTrigger, confirm bool) (*CheckinExecution, error) {
	if trigger != model.CheckinTriggerManual && trigger != model.CheckinTriggerScheduled && trigger != model.CheckinTriggerBatch {
		return nil, errors.New("invalid checkin trigger")
	}
	var execution *CheckinExecution
	var err error
	// Concurrent instances can generate the same time-based log ID. A failed log
	// insert rolls back the reservation; retry allocation without any HTTP call.
	for attempt := 0; attempt < 3; attempt++ {
		allocationConflict := false
		err = s.transaction(ctx, func(tx *gorm.DB) error {
			var err error
			execution, err = loadCheckinExecution(tx, id, trigger)
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
			if confirm && (previous == nil || !previous.Submitted) {
				return ErrCheckinNotSubmitted
			}
			fingerprint := e.Site.WorkflowFingerprint(e.Account, e.Credential)
			resume := previous != nil && (confirm || !previous.Finished || previous.Date == date)
			if resume && !previous.Submitted && previous.Status != model.SiteExecutionStatusSuccess && trigger == model.CheckinTriggerManual &&
				(previous.Finished || previous.Fingerprint != fingerprint || !now.Before(previous.Deadline) || previous.Date != date) {
				// Only an unsubmitted attempt may be replaced by a manual retry.
				if err := ensureCheckinWorkflowLog(tx, e); err != nil {
					return err
				}
				if err := tx.Model(&model.CheckinLog{}).Where("id = ? AND account_id = ?", previous.LogID, id).
					Updates(map[string]any{"status": model.SiteExecutionStatusFailed, "reason": "precheck_restarted", "message": "未提交的前置检查已重新开始", "finished_at": now}).Error; err != nil {
					return err
				}
				resume = false
			}
			if resume {
				return ensureCheckinWorkflowLog(tx, e)
			}
			if !e.Site.CheckinHTTPEnabled {
				return ErrCheckinAdapter
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
			e.Account.CheckinPending = &model.SiteCheckinPending{LogID: logID, Date: date, Fingerprint: fingerprint,
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
	return model.CheckinLog{ID: w.LogID, SiteID: e.Site.ID, AccountID: e.Account.ID, SiteName: e.Site.Name,
		AccountName: e.Account.Name, Platform: e.Site.Platform, Source: string(e.Trigger), Status: status,
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
		current, err = loadCheckinExecution(tx, expected.Account.ID, expected.Trigger)
		if err != nil {
			return err
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
		current, err = loadCheckinExecution(tx, expected.Account.ID, expected.Trigger)
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
		current, err := loadCheckinExecution(tx, expected.Account.ID, expected.Trigger)
		if err != nil {
			return err
		}
		if err := checkinWorkflowMatches(current, expected); err != nil {
			return err
		}
		w := current.Workflow()
		if w.Finished && !(w.Submitted && w.Status == model.SiteExecutionStatusFailed && outcome.Status == model.SiteExecutionStatusSuccess) {
			return ErrCheckinWorkflowConflict
		}
		if !w.Submitted && outcome.Status == model.SiteExecutionStatusSuccess && outcome.Reason != model.SiteCheckinReasonAlreadyCheckedIn {
			return ErrCheckinWorkflowConflict
		}
		if err := ensureCheckinWorkflowLog(tx, current); err != nil {
			return err
		}
		now := time.Now().UTC()
		priorStatus := w.Status
		w.Status, w.Reason, w.Finished = outcome.Status, outcome.Reason, outcome.Status != model.SiteExecutionStatusPending
		if err := writeCheckinWorkflow(tx, current); err != nil {
			return err
		}
		entry := checkinWorkflowLog(current)
		entry.Status, entry.Reason, entry.Message, entry.Reward = outcome.Status, outcome.Reason, outcome.Message, outcome.Reward
		entry.FinishedAt, entry.DurationMs = now, max(int64(0), now.Sub(w.StartedAt).Milliseconds())
		if err := tx.Model(&model.CheckinLog{}).Where("id = ? AND account_id = ? AND site_id = ?", w.LogID, current.Account.ID, current.Site.ID).
			Select("status", "reason", "message", "reward", "finished_at", "duration_ms").Updates(&entry).Error; err != nil {
			return err
		}
		updates := map[string]any{"last_checkin_status": outcome.Status, "last_checkin_message": outcome.Message, "last_checkin_at": now}
		if outcome.Status == model.SiteExecutionStatusSuccess {
			updates["last_checkin_success_at"], updates["checkin_failure_count"] = now, 0
			if err := tx.Model(&model.CheckinSite{}).Where("id = ?", current.Site.ID).Updates(map[string]any{
				"verification_status": model.SiteCheckinSupportSupported, "verification_fingerprint": current.Site.ConfigFingerprint(), "verified_at": now}).Error; err != nil {
				return err
			}
		} else if outcome.Status == model.SiteExecutionStatusFailed && priorStatus != model.SiteExecutionStatusFailed {
			updates["checkin_failure_count"] = gorm.Expr("checkin_failure_count + 1")
		}
		if err := tx.Model(&model.CheckinAccount{}).Where("id = ?", current.Account.ID).Updates(updates).Error; err != nil {
			return err
		}
		result = checkinResult(current.Account, entry)
		return nil
	})
	return result, err
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
