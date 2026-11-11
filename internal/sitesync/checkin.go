package sitesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/utils/snowflake"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// All in-process entry points share this guard. The map contains only active
// accounts and the mutex is never held during database or upstream calls.
var accountCheckins = struct {
	sync.Mutex
	running map[int]struct{}
}{running: make(map[int]struct{})}

func acquireAccountCheckin(accountID int) bool {
	accountCheckins.Lock()
	defer accountCheckins.Unlock()
	if _, running := accountCheckins.running[accountID]; running {
		return false
	}
	accountCheckins.running[accountID] = struct{}{}
	return true
}

func releaseAccountCheckin(accountID int) {
	accountCheckins.Lock()
	delete(accountCheckins.running, accountID)
	accountCheckins.Unlock()
}

func CheckinAccount(ctx context.Context, accountID int) (*model.SiteCheckinResult, error) {
	started := time.Now()
	result, err := runAccountCheckin(ctx, accountID, SiteBatchTriggerManual, true)
	status := model.SiteExecutionStatusFailed
	reason, message := string(siteBatchReason(err)), sanitizeSiteStatusMessage(err)
	if result != nil {
		// Prefer the persisted outcome, which has already had account credentials redacted.
		status, reason, message = result.Status, result.Reason, sanitizeSiteStatusText(result.Message)
	}
	fields := []any{
		"trigger", string(SiteBatchTriggerManual),
		"account_id", accountID,
		"status", string(status),
		"reason", reason,
		"message", message,
		"duration", time.Since(started).String(),
	}
	if result != nil {
		fields = append(fields, "site_id", result.SiteID, "log_id", result.LogID)
		if result.Reward != "" {
			fields = append(fields, "reward", sanitizeSiteStatusText(result.Reward))
		}
	}
	if err != nil || status == model.SiteExecutionStatusFailed {
		log.Warnw("sitesync.checkin.manual", fields...)
	} else {
		log.Infow("sitesync.checkin.manual", fields...)
	}
	return result, err
}

func checkinAccountWithTrigger(ctx context.Context, accountID int, trigger SiteBatchTrigger) (*model.SiteCheckinResult, error) {
	return runAccountCheckin(ctx, accountID, trigger, false)
}

func runAccountCheckin(ctx context.Context, accountID int, trigger SiteBatchTrigger, allowVerify bool) (*model.SiteCheckinResult, error) {
	ctx = context.WithValue(ctx, checkinTriggerKey{}, trigger)
	started := time.Now()
	acquired := acquireAccountCheckin(accountID)
	if acquired {
		defer releaseAccountCheckin(accountID)
	}
	// Load after acquiring the guard, so a preceding run cannot leave us with a
	// stale success timestamp, failure count or next execution time.
	siteRecord, account, err := loadSiteAccount(ctx, accountID)
	if err != nil {
		return nil, sanitizeSiteError(err)
	}
	if !siteRecord.IsCheckinOnly() {
		return nil, newCheckinSiteRequiredError()
	}
	if !acquired {
		return persistCheckinOutcome(ctx, siteRecord, account, trigger, started, &model.SiteCheckinResult{
			Status: model.SiteExecutionStatusSkipped, Reason: model.SiteCheckinReasonAlreadyRunning,
			Message: "checkin is already running",
		}, "", false)
	}
	// Recheck availability for every entry point, including direct manual API
	// calls and accounts disabled after a batch selected its work.
	unavailableReason, unavailableMessage := "", ""
	switch {
	case siteRecord.Archived:
		unavailableReason, unavailableMessage = model.SiteCheckinReasonSiteArchived, "site is archived"
	case !siteRecord.Enabled:
		unavailableReason, unavailableMessage = model.SiteCheckinReasonSiteDisabled, "site is disabled"
	case !account.Enabled:
		unavailableReason, unavailableMessage = model.SiteCheckinReasonAccountDisabled, "account is disabled"
	}
	if unavailableReason != "" {
		return persistCheckinOutcome(ctx, siteRecord, account, trigger, started, &model.SiteCheckinResult{
			Status: model.SiteExecutionStatusSkipped, Reason: unavailableReason, Message: unavailableMessage,
		}, "", false)
	}
	if result := checkinCapabilitySkip(siteRecord, allowVerify); result != nil {
		return persistCheckinOutcome(ctx, siteRecord, account, trigger, started, result, "", false)
	}
	if pending := account.CheckinPending; pending != nil && (!siteRecord.CheckinHTTPEnabled || siteRecord.CheckinStatus == nil) &&
		(!pending.Finished || pending.Date == time.Now().In(resolveSiteCheckinWindow(siteRecord).location).Format("2006-01-02")) {
		result := &model.SiteCheckinResult{LogID: pending.LogID, Workflow: pending, Replayed: pending.Finished,
			Status: model.SiteExecutionStatusFailed, Reason: "checkin_config_changed", Message: "状态查询配置已停用，本次不再提交；请核对上游结果"}
		return persistCheckinOutcome(ctx, siteRecord, account, trigger, started, result, "", true)
	}
	if trigger == SiteBatchTriggerScheduled {
		// A manual run or an account edit may have completed after the batch
		// selected its work. Recheck eligibility while holding the account guard.
		nextAt, scheduleErr := ensureAccountCheckinSchedule(ctx, siteRecord, account, time.Now())
		if scheduleErr != nil {
			return nil, sanitizeSiteError(scheduleErr)
		}
		if nextAt == nil || time.Now().Before(*nextAt) {
			return persistCheckinOutcome(ctx, siteRecord, account, trigger, started, &model.SiteCheckinResult{
				Status: model.SiteExecutionStatusSkipped, Reason: string(SiteBatchReasonScheduledLater),
				Message: "automatic checkin is not due",
			}, "", false)
		}
	}

	executionAccount := account
	var runErr error
	if !siteRecord.CheckinHTTPEnabled && (siteRecord.LinkedSiteID != nil || account.LinkedAccountID != nil || account.CredentialType == model.SiteCredentialTypeLinkedAccount) {
		executionAccount, runErr = op.SiteCheckinLinkedAccount(siteRecord, account, ctx)
		if runErr == nil {
			// Keep the subscription account ID for credential/user-ID updates,
			// while using the check-in account's independently selected proxy.
			executionAccount.ProxyMode, executionAccount.ProxyConfigID = account.ProxyMode, account.ProxyConfigID
		}
	}
	var result *model.SiteCheckinResult
	var accessToken string
	var balanceBefore siteBalanceFetchResult
	var externalBalance *linkedCheckinBalance
	if siteRecord.CheckinHTTPEnabled && account.LinkedAccountID != nil &&
		(siteRecord.CheckinStatus == nil || account.CheckinPending == nil || (!account.CheckinPending.Submitted && !account.CheckinPending.Finished) ||
			(account.CheckinPending.Finished && account.CheckinPending.Date != time.Now().In(resolveSiteCheckinWindow(siteRecord).location).Format("2006-01-02"))) {
		externalBalance = prepareLinkedCheckinBalance(ctx, siteRecord, account)
	}
	if runErr == nil {
		originalToken := executionAccount.AccessToken
		result, accessToken, runErr = checkinAccountState(ctx, siteRecord, executionAccount, func(token string) error {
			balanceBefore = fetchAccountBalanceBeforeCheckin(ctx, siteRecord, executionAccount, token)
			// A challenge blocks this site's requests, including the check-in.
			// Preserve the batch's existing stop/backoff behavior after one hit.
			if IsCloudflareProtectionError(balanceBefore.err) {
				return balanceBefore.err
			}
			return nil
		})
		if executionAccount != account && accessToken != "" && accessToken != originalToken {
			persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			saveErr := db.GetDB().WithContext(persistCtx).Model(&model.SiteAccount{}).
				Where("id = ? AND access_token = ?", executionAccount.ID, originalToken).
				Update("access_token", accessToken).Error
			cancel()
			if runErr == nil && saveErr != nil {
				runErr = fmt.Errorf("save linked account token: %w", saveErr)
			}
		}
	}
	if runErr != nil {
		result = &model.SiteCheckinResult{
			Status:  model.SiteExecutionStatusFailed,
			Reason:  string(siteBatchReason(sanitizeSiteError(runErr))),
			Message: apperror.Message(runErr),
		}
	} else if result == nil {
		runErr = fmt.Errorf("checkin returned no result")
		result = &model.SiteCheckinResult{Status: model.SiteExecutionStatusFailed, Reason: string(SiteBatchReasonInternalError), Message: runErr.Error()}
	} else if result.Status == model.SiteExecutionStatusFailed {
		runErr = newSiteBusinessError(result.Message)
		if result.Reason == "" {
			result.Reason = string(siteBatchReason(runErr))
		}
	} else if result.Status == model.SiteExecutionStatusSkipped && result.Reason == "" {
		result.Reason = model.SiteCheckinReasonUnsupported
	}
	if executionAccount != nil && executionAccount != account {
		result.Message = sanitizeCheckinText(result.Message, executionAccount, accessToken, siteRecord)
		result.Reward = sanitizeCheckinText(result.Reward, executionAccount, accessToken, siteRecord)
	}
	result, err = persistCheckinOutcome(ctx, siteRecord, account, trigger, started, result, accessToken, true)
	if err != nil {
		return nil, sanitizeSiteError(err)
	}
	var balance siteBalanceFetchResult
	if result.Status == model.SiteExecutionStatusSuccess && !result.Replayed {
		if siteRecord.CheckinHTTPEnabled && account.LinkedAccountID != nil && externalBalance == nil {
			externalBalance = resolveLinkedCheckinBalance(ctx, siteRecord, account, false)
		}
		if !siteRecord.CheckinHTTPEnabled {
			balanceSite := *siteRecord
			balanceSite.ID = executionAccount.SiteID
			balance = refreshAccountBalanceAfterCheckin(ctx, &balanceSite, executionAccount, accessToken)
			if executionAccount != account {
				mirrorCheckinBalance(ctx, siteRecord, account, balance)
			}
		} else if externalBalance != nil {
			balanceBefore = externalBalance.before
			balance = refreshAccountBalanceAfterCheckin(ctx, externalBalance.site, externalBalance.account, externalBalance.token)
			mirrorCheckinBalance(ctx, siteRecord, account, balance)
		}
		fillCheckinRewardFromBalance(ctx, result, balanceBefore, balance)
	}
	if !result.Replayed {
		checkinNotifications.notify(siteRecord, account, result, balance, trigger)
	}
	if runErr != nil {
		safeErr := sanitizeSiteError(runErr)
		params := apperror.Params(safeErr)
		for key, value := range params {
			if text, ok := value.(string); ok {
				text = sanitizeCheckinText(text, account, accessToken, siteRecord)
				if executionAccount != nil {
					text = sanitizeCheckinText(text, executionAccount, accessToken, siteRecord)
				}
				params[key] = text
			}
		}
		return result, apperror.Wrap(apperror.Code(safeErr), result.Message, safeErr).
			WithStatus(apperror.Status(safeErr)).WithParams(params)
	}
	return result, nil
}

func checkinCapabilitySkip(siteRecord *model.Site, allowVerify bool) *model.SiteCheckinResult {
	capability := siteRecord.ResolveCheckinCapability()
	reason, message := "", ""
	switch {
	case siteRecord.CheckinMode == model.SiteCheckinModeDisabled:
		reason, message = model.SiteCheckinReasonDisabled, "checkin is disabled for this site"
	case !capability.CanVerify:
		reason, message = model.SiteCheckinReasonNotConfigured, "configure a custom HTTP checkin endpoint for this site"
	case !allowVerify && !capability.Enabled:
		if capability.Support == model.SiteCheckinSupportUnsupported {
			reason, message = model.SiteCheckinReasonUnsupported, "this site's checkin endpoint was verified as unavailable; manually check in to verify again"
		} else {
			reason, message = model.SiteCheckinReasonDefaultDisabled, "checkin is disabled by default; enable it for this site or manually check in to verify"
		}
	}
	if reason == "" {
		return nil
	}
	return &model.SiteCheckinResult{Status: model.SiteExecutionStatusSkipped, Reason: reason, Message: message}
}

func newSuccessfulCheckinResult(message, reward string) *model.SiteCheckinResult {
	reason := model.SiteCheckinReasonCheckedIn
	if isAlreadyCheckedInMessage(message) {
		reason = model.SiteCheckinReasonAlreadyCheckedIn
		reward = ""
	}
	return &model.SiteCheckinResult{
		Status: model.SiteExecutionStatusSuccess, Reason: reason,
		Message: firstNonEmptyString(message, "checkin success"), Reward: reward,
		CapabilityEvidence: model.SiteCheckinSupportSupported,
	}
}

func newUnsupportedCheckinResult(message string) *model.SiteCheckinResult {
	return &model.SiteCheckinResult{
		Status: model.SiteExecutionStatusSkipped, Reason: model.SiteCheckinReasonUnsupported,
		Message:            firstNonEmptyString(message, "checkin endpoint is unavailable on this site"),
		CapabilityEvidence: model.SiteCheckinSupportUnsupported,
	}
}

func checkinRewardString(value any) string {
	if number, ok := value.(float64); ok {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	return jsonString(value)
}

func persistCheckinOutcome(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, trigger SiteBatchTrigger, started time.Time, result *model.SiteCheckinResult, accessToken string, updateAccount bool) (*model.SiteCheckinResult, error) {
	finished := time.Now()
	result.SiteID, result.AccountID = siteRecord.ID, account.ID
	if result.Replayed {
		capability := siteRecord.ResolveCheckinCapability()
		result.Capability = &capability
		return result, nil
	}
	result.Message = sanitizeCheckinText(result.Message, account, accessToken, siteRecord)
	result.Reward = sanitizeCheckinText(result.Reward, account, accessToken, siteRecord)
	if result.Status != model.SiteExecutionStatusSuccess || result.Reason == model.SiteCheckinReasonAlreadyCheckedIn {
		result.Reward = ""
	}
	entry := model.SiteCheckinLog{
		ID: snowflake.GenerateID(), SiteID: siteRecord.ID, AccountID: account.ID,
		SiteName: siteRecord.Name, AccountName: account.Name, Platform: siteRecord.Platform,
		Source: string(trigger), Status: result.Status, Reason: result.Reason,
		Message: result.Message, Reward: result.Reward, DurationMs: finished.Sub(started).Milliseconds(),
		StartedAt: started.UTC(), FinishedAt: finished.UTC(), BatchJobID: siteBatchTaskID(ctx),
	}
	if result.Workflow != nil {
		entry.ID, entry.StartedAt = result.Workflow.LogID, result.Workflow.StartedAt
		entry.DurationMs = finished.Sub(entry.StartedAt).Milliseconds()
	}
	// A client disconnect or a batch deadline must not discard an upstream
	// outcome that has already happened. Bound this final database write alone.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := db.GetDB().WithContext(persistCtx).Transaction(func(tx *gorm.DB) error {
		// A request may finish after the URL or policy was edited. Keep the log,
		// but only attach its evidence to the configuration that was executed.
		var current model.Site
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, siteRecord.ID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			// Deleting a site during a request must not discard its actual outcome.
			if result.Workflow != nil {
				saved, err := updatePendingCheckinLog(tx, &entry)
				if err == nil && !saved {
					result.Replayed = true
					result.Reward = ""
				}
				return err
			}
			return tx.Create(&entry).Error
		} else if err != nil {
			return err
		}
		if updateAccount && current.CheckinConfigFingerprint() == siteRecord.CheckinConfigFingerprint() &&
			(result.CapabilityEvidence == model.SiteCheckinSupportSupported || result.CapabilityEvidence == model.SiteCheckinSupportUnsupported) {
			current.CheckinVerificationStatus = result.CapabilityEvidence
			current.CheckinVerificationFingerprint = current.CheckinConfigFingerprint()
			verifiedAt := finished.UTC()
			current.CheckinVerifiedAt = &verifiedAt
			if err := tx.Model(&current).Updates(map[string]any{
				"checkin_verification_status":      current.CheckinVerificationStatus,
				"checkin_verification_fingerprint": current.CheckinVerificationFingerprint,
				"checkin_verified_at":              current.CheckinVerifiedAt,
			}).Error; err != nil {
				return err
			}
		}
		capability := current.ResolveCheckinCapability()
		result.Capability = &capability
		if !capability.Enabled {
			if err := tx.Model(&model.SiteAccount{}).Where("site_id = ? AND next_auto_checkin_at IS NOT NULL", current.ID).
				Update("next_auto_checkin_at", nil).Error; err != nil {
				return err
			}
		}
		if result.Workflow != nil {
			saved, err := persistStatusWorkflow(tx, account, result, &entry)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// Account deletion preserves history. Save the in-flight outcome
				// without requiring or recreating the deleted workflow reservation.
				saved, err = updatePendingCheckinLog(tx, &entry)
				updateAccount = false
			}
			if err != nil {
				return err
			}
			if !saved {
				result.Replayed = true
				result.Reward = ""
				return nil
			}
		} else if err := tx.Create(&entry).Error; err != nil {
			return err
		}
		if !updateAccount {
			return nil
		}
		return updateAccountCheckinState(tx, &current, account, result, accessToken, finished)
	})
	if err != nil {
		return nil, fmt.Errorf("persist checkin outcome: %w", err)
	}
	result.LogID = entry.ID
	return result, nil
}

func sanitizeCheckinText(value string, account *model.SiteAccount, resolvedToken string, sites ...*model.Site) string {
	secrets := []string{account.Username, account.Password, account.AccessToken, account.APIKey, account.RefreshToken, account.Cookie, resolvedToken}
	for _, site := range sites {
		if site == nil {
			continue
		}
		for _, header := range append(append([]model.CustomHeader(nil), site.CustomHeader...), site.CheckinHTTPHeaders...) {
			if isSensitiveCheckinField(header.HeaderKey) {
				secrets = append(secrets, header.HeaderValue, expandCheckinHTTPTemplate(header.HeaderValue, account, resolvedToken))
			}
		}
		if site.CheckinStatus != nil {
			for _, header := range site.CheckinStatus.Headers {
				if isSensitiveCheckinField(header.HeaderKey) {
					secrets = append(secrets, header.HeaderValue, expandCheckinHTTPTemplate(header.HeaderValue, account, resolvedToken))
				}
			}
			var statusBody any
			if json.Unmarshal([]byte(expandCheckinHTTPBodyTemplate(site.CheckinStatus.Body, account, resolvedToken)), &statusBody) == nil {
				secrets = appendSensitiveCheckinJSONValues(secrets, statusBody, false)
			}
		}
		var requestBody any
		if json.Unmarshal([]byte(expandCheckinHTTPBodyTemplate(site.CheckinHTTPBody, account, resolvedToken)), &requestBody) == nil {
			secrets = appendSensitiveCheckinJSONValues(secrets, requestBody, false)
		}
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
		if looksLikeCookieToken(secret) {
			for _, part := range strings.Split(secret, ";") {
				_, cookieValue, ok := strings.Cut(part, "=")
				if cookieValue = strings.TrimSpace(cookieValue); ok && cookieValue != "" {
					value = strings.ReplaceAll(value, cookieValue, "[REDACTED]")
				}
			}
		}
	}
	return sanitizeSiteStatusText(value)
}

func isSensitiveCheckinField(name string) bool {
	lowered := strings.ToLower(strings.TrimSpace(name))
	for _, marker := range []string{"authorization", "cookie", "token", "secret", "password", "api-key", "api_key", "apikey", "credential"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

func appendSensitiveCheckinJSONValues(secrets []string, value any, sensitive bool) []string {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			secrets = appendSensitiveCheckinJSONValues(secrets, child, sensitive || isSensitiveCheckinField(key))
		}
	case []any:
		for _, child := range typed {
			secrets = appendSensitiveCheckinJSONValues(secrets, child, sensitive)
		}
	case string:
		if sensitive {
			secrets = append(secrets, typed)
		}
	}
	return secrets
}
