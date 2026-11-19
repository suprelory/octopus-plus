package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/checkin"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"gorm.io/gorm"
)

func checkinError(c *gin.Context, err error) {
	for _, mapping := range []struct {
		target        error
		status        int
		code, message string
	}{
		{gorm.ErrRecordNotFound, 404, "checkin.not_found", "check-in resource or referenced configuration was not found"},
		{op.ErrCheckinConfiguration, 400, "checkin.invalid_configuration", "check-in configuration is invalid or incompatible with its references"},
		{model.ErrCheckinBatchSelection, 400, "checkin.invalid_batch_selection", "select all sites or a nonempty valid site selection"},
		{op.ErrCheckinNameConflict, 409, "checkin.name_conflict", "a check-in resource with this name already exists"},
		{op.ErrCheckinCredentialConflict, 409, "checkin.credential_conflict", "credential changed; reload it before saving"},
		{op.ErrCheckinCredentialInUse, 409, "checkin.credential_in_use", "credential is still referenced by a check-in or balance account"},
		{op.ErrCheckinSiteInUse, 409, "checkin.site_in_use", "remove the site's accounts before deleting the site"},
		{op.ErrCheckinSiteArchived, 409, "checkin.site_archived", "restore the site before adding accounts"},
		{op.ErrCheckinWorkflowPending, 409, "checkin.workflow_pending", "the account has an unfinished submitted check-in"},
		{op.ErrCheckinWorkflowConflict, 409, "checkin.workflow_conflict", "check-in state changed; reload its result"},
		{op.ErrCheckinWorkflowChanged, 409, "checkin.configuration_changed", "check-in configuration or identity changed"},
		{op.ErrCheckinWorkflowExpired, 409, "checkin.workflow_expired", "check-in submission date or deadline has expired"},
		{op.ErrCheckinNotSubmitted, 409, "checkin.not_submitted", "there is no submitted check-in to confirm"},
		{op.ErrCheckinNotDue, 409, "checkin.not_due", "the account is not eligible for this scheduled operation"},
		{op.ErrCheckinUnavailable, 409, "checkin.unavailable", "the site or account is disabled or archived"},
		{op.ErrCheckinAdapter, 409, "checkin.adapter_unavailable", "the configured check-in or confirmation adapter is unavailable"},
		{op.ErrCheckinProxyDisabled, 409, "checkin.proxy_disabled", "the configured proxy is disabled"},
		{checkin.ErrVerificationRequired, 409, "checkin.verification_required", "complete verification at the check-in site before continuing"},
		{checkin.ErrCheckinAlreadyRunning, 409, "checkin.already_running", "a check-in is already running for this account"},
		{op.ErrCheckinBatchActive, 409, "checkin.batch_active", "a check-in batch with a different selection is active"},
		{op.ErrCheckinBatchStopped, 409, "checkin.batch_stopped", "the check-in batch has stopped"},
		{op.ErrCheckinBalanceDisabled, 409, "checkin.balance_disabled", "balance queries are disabled for this account"},
		{op.ErrCheckinBalanceChanged, 409, "checkin.balance_changed", "balance configuration or observation changed; refresh the account"},
		{op.ErrCheckinNotificationState, 409, "checkin.notification_state", "notification is not eligible for retry or its state changed"},
		{op.ErrCheckinRefreshLease, 409, "checkin.session_busy", "a credential session operation is in progress"},
		{checkin.ErrSessionChanged, 409, "checkin.credential_conflict", "credential identity changed during the operation"},
		{checkin.ErrSessionExpired, 409, "checkin.session_expired", "the login session expired; update the credential"},
		{checkin.ErrUnsupportedSession, 409, "checkin.session_unsupported", "the credential does not support this session operation"},
		{checkin.ErrLoginRequiresCheckin, 409, "checkin.login_requires_checkin", "AgentRouter login requires an explicit check-in"},
		{checkin.ErrSessionUncertain, 409, "checkin.session_uncertain", "session exchange result is unknown; reconcile the credential"},
		{op.ErrCheckinSessionUncertain, 409, "checkin.session_uncertain", "session exchange result is unknown; reconcile the credential"},
		{checkin.ErrSessionBackoff, 503, "checkin.session_backoff", "the login session is cooling down"},
		{op.ErrCheckinSessionBackoff, 503, "checkin.session_backoff", "the login session is cooling down"},
		{checkin.ErrSessionSave, 503, "checkin.session_save_failed", "the issued login session could not be saved"},
		{checkin.ErrSessionUnavailable, 503, "checkin.session_unavailable", "the login session is unavailable"},
		{checkin.ErrBalanceResponse, 502, "checkin.balance_unavailable", "the upstream balance response is unavailable or invalid"},
		{checkin.ErrCheckinRequest, 502, "checkin.request_failed", "the upstream request could not be completed"},
		{context.DeadlineExceeded, 504, "checkin.timeout", "the operation timed out; reload its state before retrying"},
		{context.Canceled, 408, "checkin.canceled", "the request was canceled; reload its state before retrying"},
	} {
		if errors.Is(err, mapping.target) {
			resp.ErrorWithCode(c, mapping.status, mapping.code, mapping.message)
			return
		}
	}
	// A driver or transport can include bound secrets in its error. Classification
	// is the logging boundary; neither the response nor access log gets that text.
	resp.UseSafeErrorLogDetails(c, "checkin.internal_error", "check-in operation failed")
	resp.ErrorWithCode(c, http.StatusInternalServerError, "checkin.internal_error", "check-in operation failed")
}
