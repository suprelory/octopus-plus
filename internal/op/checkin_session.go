package op

import (
	"context"
	"crypto/rand"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CheckinSessionClaim struct {
	Before model.CheckinCredential `json:"-"`
	Lease  string                  `json:"-"`
}

type CheckinTokenPair struct {
	AccessToken    string `json:"-"`
	RefreshToken   string `json:"-"`
	TokenExpiresAt int64  `json:"-"`
}

// ClaimRefresh coordinates token rotation on a shared credential, independent
// of the number of check-in or balance accounts referencing it.
func (s *CheckinStore) ClaimRefresh(ctx context.Context, id int, expectedVersion int64, ttl time.Duration) (*CheckinSessionClaim, error) {
	return s.claimSession(ctx, id, expectedVersion, ttl, false)
}

func (s *CheckinStore) ClaimLogin(ctx context.Context, id int, expectedVersion int64, ttl time.Duration) (*CheckinSessionClaim, error) {
	return s.claimSession(ctx, id, expectedVersion, ttl, true)
}

func (s *CheckinStore) claimSession(ctx context.Context, id int, expectedVersion int64, ttl time.Duration, login bool) (*CheckinSessionClaim, error) {
	if ttl <= 0 || ttl > 5*time.Minute {
		return nil, errors.New("invalid refresh lease duration")
	}
	var claim CheckinSessionClaim
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&claim.Before, id).Error; err != nil {
			return err
		}
		if claim.Before.CredentialVersion != expectedVersion {
			return ErrCheckinCredentialConflict
		}
		if login {
			if !claim.Before.SupportsSessionLogin() {
				return errors.New("credential does not support automatic login")
			}
		} else if claim.Before.Type != model.CheckinCredentialAccessToken || claim.Before.Platform != model.SitePlatformSub2API || strings.TrimSpace(claim.Before.RefreshToken) == "" {
			return errors.New("credential does not support token refresh")
		}
		now := time.Now()
		if claim.Before.TokenRefreshStarted {
			return ErrCheckinSessionUncertain
		}
		if claim.Before.TokenRefreshRetryAt > now.UnixMilli() {
			return ErrCheckinSessionBackoff
		}
		if claim.Before.TokenRefreshLeaseUntil > now.UnixMilli() {
			return ErrCheckinRefreshLease
		}
		claim.Lease = rand.Text()
		result := tx.Model(&model.CheckinCredential{}).
			Where("id = ? AND credential_version = ? AND token_refresh_lease_until <= ? AND token_refresh_started = ? AND token_refresh_retry_at <= ?", id, expectedVersion, now.UnixMilli(), false, now.UnixMilli()).
			Updates(map[string]any{"token_refresh_lease": claim.Lease, "token_refresh_lease_until": now.Add(ttl).UnixMilli()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCheckinRefreshLease
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &claim, nil
}

// CommitRefresh atomically saves both rotated tokens. A settings edit made
// during the exchange is preserved; a replacement identity is never overwritten.
// The execution layer owns the bounded, cancellation-independent save retries.
func (s *CheckinStore) CommitRefresh(ctx context.Context, claim CheckinSessionClaim, pair CheckinTokenPair) (*model.CheckinCredential, error) {
	pair.AccessToken = model.NormalizeSiteAccessToken(pair.AccessToken)
	pair.RefreshToken = strings.TrimSpace(pair.RefreshToken)
	if claim.Lease == "" || pair.AccessToken == "" || pair.RefreshToken == "" || claim.Before.Type != model.CheckinCredentialAccessToken || claim.Before.Platform != model.SitePlatformSub2API {
		return nil, errors.New("refresh claim and complete token pair are required")
	}
	var result model.CheckinCredential
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var current model.CheckinCredential
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, claim.Before.ID).Error; err != nil {
			return err
		}
		// A previous save may have committed despite a lost acknowledgement.
		// Re-observing the exact rotated pair is safe and does not write again.
		committed := claim.Before
		committed.AccessToken, committed.RefreshToken = pair.AccessToken, pair.RefreshToken
		if sameCheckinSession(committed, current) && current.CredentialVersion > claim.Before.CredentialVersion && current.TokenRefreshedAt > claim.Before.TokenRefreshedAt {
			result = current
			return nil
		}
		if !sameCheckinSession(claim.Before, current) {
			return ErrCheckinCredentialConflict
		}
		if current.TokenRefreshLease != claim.Lease {
			return ErrCheckinRefreshLease
		}
		result = current
		result.AccessToken, result.RefreshToken, result.TokenExpiresAt = pair.AccessToken, pair.RefreshToken, pair.TokenExpiresAt
		result.TokenRefreshedAt = max(time.Now().UnixMilli(), current.TokenRefreshedAt+1)
		result.CredentialVersion++
		result.TokenRefreshLease, result.TokenRefreshLeaseUntil = "", 0
		result.TokenRefreshStarted, result.TokenRefreshRetryAt = false, 0
		if err := result.Validate(); err != nil {
			return err
		}
		written := tx.Model(&model.CheckinCredential{}).
			Where("id = ? AND credential_version = ? AND token_refresh_lease = ?", current.ID, current.CredentialVersion, claim.Lease).
			Updates(map[string]any{"access_token": result.AccessToken, "refresh_token": result.RefreshToken,
				"token_expires_at": result.TokenExpiresAt, "token_refreshed_at": result.TokenRefreshedAt,
				"credential_version": result.CredentialVersion, "token_refresh_lease": "", "token_refresh_lease_until": 0,
				"token_refresh_started": false, "token_refresh_retry_at": 0})
		if written.Error != nil {
			return written.Error
		}
		if written.RowsAffected != 1 {
			return ErrCheckinCredentialConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *CheckinStore) ReleaseRefresh(ctx context.Context, claim CheckinSessionClaim) error {
	if claim.Lease == "" {
		return errors.New("refresh claim is required")
	}
	return s.conn.WithContext(ctx).Model(&model.CheckinCredential{}).
		Where("id = ? AND token_refresh_lease = ? AND token_refresh_started = ?", claim.Before.ID, claim.Lease, false).
		Updates(map[string]any{"token_refresh_lease": "", "token_refresh_lease_until": 0}).Error
}

// BeginSessionExchange persists intent before sending a one-time refresh token.
// Expiry permits takeover only before dispatch; an unknown outcome requires the
// original owner to save its result or a manual replacement of the credentials.
func (s *CheckinStore) BeginSessionExchange(ctx context.Context, claim CheckinSessionClaim) error {
	if claim.Lease == "" {
		return ErrCheckinRefreshLease
	}
	result := s.conn.WithContext(ctx).Model(&model.CheckinCredential{}).
		Where("id = ? AND token_refresh_lease = ? AND token_refresh_started = ? AND token_refresh_lease_until > ?", claim.Before.ID, claim.Lease, false, time.Now().UnixMilli()).
		Updates(map[string]any{"token_refresh_started": true})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCheckinRefreshLease
	}
	return nil
}

// RetrySessionExchange is only for an explicit, non-consuming upstream failure
// (or an error before dispatch). Never use it for timeouts or malformed replies.
func (s *CheckinStore) RetrySessionExchange(ctx context.Context, claim CheckinSessionClaim, retryAt time.Time) error {
	if claim.Lease == "" || retryAt.Before(time.Now()) {
		return errors.New("session retry requires an owner and a future retry time")
	}
	return s.conn.WithContext(ctx).Model(&model.CheckinCredential{}).
		Where("id = ? AND token_refresh_lease = ?", claim.Before.ID, claim.Lease).
		Updates(map[string]any{"token_refresh_started": false, "token_refresh_lease": "", "token_refresh_lease_until": 0, "token_refresh_retry_at": retryAt.UnixMilli()}).Error
}

type CheckinLoginSession struct {
	AccessToken    string `json:"-"`
	Cookie         string `json:"-"`
	PlatformUserID *int   `json:"-"`
	TokenExpiresAt int64  `json:"-"`
}

func (s *CheckinStore) CommitLogin(ctx context.Context, claim CheckinSessionClaim, session CheckinLoginSession) (*model.CheckinCredential, error) {
	session.AccessToken = model.NormalizeSiteAccessToken(session.AccessToken)
	if claim.Lease == "" || !claim.Before.SupportsSessionLogin() || (session.AccessToken == "" && session.Cookie == "") {
		return nil, errors.New("login claim and session are required")
	}
	if session.Cookie != "" {
		if err := model.ValidateSiteCheckinCookie(session.Cookie); err != nil {
			return nil, errors.New("invalid login cookie")
		}
	}
	var result model.CheckinCredential
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var current model.CheckinCredential
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, claim.Before.ID).Error; err != nil {
			return err
		}
		committed := claim.Before
		committed.AccessToken, committed.Cookie = session.AccessToken, session.Cookie
		if sameCheckinSession(committed, current) && current.CredentialVersion > claim.Before.CredentialVersion && current.TokenRefreshedAt > claim.Before.TokenRefreshedAt {
			result = current
			return nil
		}
		if !sameCheckinSession(claim.Before, current) {
			return ErrCheckinCredentialConflict
		}
		if current.TokenRefreshLease != claim.Lease {
			return ErrCheckinRefreshLease
		}
		result = current
		result.AccessToken, result.Cookie, result.TokenExpiresAt = session.AccessToken, session.Cookie, session.TokenExpiresAt
		if session.PlatformUserID != nil && reflect.DeepEqual(current.PlatformUserID, claim.Before.PlatformUserID) {
			result.PlatformUserID = session.PlatformUserID
		}
		result.TokenRefreshedAt = max(time.Now().UnixMilli(), current.TokenRefreshedAt+1)
		result.CredentialVersion++
		result.TokenRefreshLease, result.TokenRefreshLeaseUntil = "", 0
		result.TokenRefreshStarted, result.TokenRefreshRetryAt = false, 0
		if err := result.Validate(); err != nil {
			return err
		}
		written := tx.Model(&model.CheckinCredential{}).
			Where("id = ? AND credential_version = ? AND token_refresh_lease = ?", current.ID, current.CredentialVersion, claim.Lease).
			Updates(map[string]any{"access_token": result.AccessToken, "cookie": result.Cookie, "platform_user_id": result.PlatformUserID,
				"token_expires_at": result.TokenExpiresAt, "token_refreshed_at": result.TokenRefreshedAt, "credential_version": result.CredentialVersion,
				"token_refresh_lease": "", "token_refresh_lease_until": 0, "token_refresh_started": false, "token_refresh_retry_at": 0})
		if written.Error != nil {
			return written.Error
		}
		if written.RowsAffected != 1 {
			return ErrCheckinCredentialConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
