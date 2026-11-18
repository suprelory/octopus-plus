package op

import (
	"context"
	"reflect"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func sameWorkflowSession(a, b model.CheckinCredential) bool {
	return a.IdentityRevision == b.IdentityRevision && sameCheckinSession(a, b) && a.TokenRefreshedAt == b.TokenRefreshedAt &&
		a.TokenExpiresAt == b.TokenExpiresAt && reflect.DeepEqual(a.PlatformUserID, b.PlatformUserID)
}

// SaveWorkflowSession saves cookies/user IDs learned by read-only probes or by
// the reserved AgentRouter login. It cannot authorize login or another submit.
// Unlike a manual edit it preserves identity_revision, so confirmation remains valid.
func (s *CheckinStore) SaveWorkflowSession(ctx context.Context, expected *CheckinExecution, session CheckinLoginSession) (*model.CheckinCredential, error) {
	var result model.CheckinCredential
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		// Lock order matches configuration and submission transactions. Saving an
		// already-issued session is still useful after an account is disabled.
		site, account, err := lockCheckinAccount(tx, expected.Account.ID)
		if err != nil {
			return err
		}
		if account.CredentialID != expected.Credential.ID {
			return ErrCheckinCredentialConflict
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&result, account.CredentialID).Error; err != nil {
			return err
		}
		if result.IdentityRevision != expected.Credential.IdentityRevision {
			return ErrCheckinCredentialConflict
		}
		if site.CheckinHTTPEnabled || site.CheckinAdapter() == "" {
			return ErrCheckinAdapter
		}
		if expected.Workflow() == nil || account.CheckinPending == nil || account.CheckinPending.LogID != expected.Workflow().LogID {
			return ErrCheckinWorkflowConflict
		}
		if account.CheckinPending.Fingerprint != site.WorkflowFingerprint(account, result) ||
			account.CheckinPending.Fingerprint != expected.Workflow().Fingerprint {
			return ErrCheckinWorkflowChanged
		}
		if site.Platform == model.SitePlatformAgentRouter && !account.CheckinPending.Submitted {
			return ErrCheckinNotSubmitted
		}
		if site.Platform != model.SitePlatformAgentRouter && session.AccessToken != expected.Credential.AccessToken {
			return ErrCheckinCredentialConflict
		}
		candidate := result
		candidate.AccessToken, candidate.Cookie, candidate.SessionCookies = session.AccessToken, session.Cookie, session.SessionCookies
		if result.TokenExpiresAt == expected.Credential.TokenExpiresAt {
			candidate.TokenExpiresAt = session.TokenExpiresAt
		}
		if session.PlatformUserID != nil {
			candidate.PlatformUserID = session.PlatformUserID
		}
		if sameWorkflowSession(candidate, result) {
			return nil
		}
		if !sameWorkflowSession(result, expected.Credential) || result.TokenRefreshStarted || result.TokenRefreshLeaseUntil > time.Now().UnixMilli() {
			return ErrCheckinCredentialConflict
		}
		if err := candidate.Validate(); err != nil {
			return err
		}
		candidate.CredentialVersion++
		candidate.TokenRefreshedAt = max(time.Now().UnixMilli(), result.TokenRefreshedAt+1)
		written := tx.Model(&model.CheckinCredential{}).Where("id = ? AND credential_version = ?", result.ID, result.CredentialVersion).
			Select("access_token", "cookie", "session_cookies", "platform_user_id", "token_expires_at", "token_refreshed_at", "credential_version").Updates(&candidate)
		if written.Error != nil {
			return written.Error
		}
		if written.RowsAffected != 1 {
			return ErrCheckinCredentialConflict
		}
		result = candidate
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
