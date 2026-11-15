package op

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CheckinRefreshClaim struct {
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
func (s *CheckinStore) ClaimRefresh(ctx context.Context, id int, expectedVersion int64, ttl time.Duration) (*CheckinRefreshClaim, error) {
	if ttl <= 0 || ttl > 5*time.Minute {
		return nil, errors.New("invalid refresh lease duration")
	}
	var claim CheckinRefreshClaim
	err := s.conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&claim.Before, id).Error; err != nil {
			return err
		}
		if claim.Before.CredentialVersion != expectedVersion {
			return ErrCheckinCredentialConflict
		}
		if claim.Before.Type != model.CheckinCredentialAccessToken || claim.Before.Platform != model.SitePlatformSub2API || strings.TrimSpace(claim.Before.RefreshToken) == "" {
			return errors.New("credential does not support token refresh")
		}
		now := time.Now()
		if claim.Before.TokenRefreshLeaseUntil > now.UnixMilli() {
			return ErrCheckinRefreshLease
		}
		claim.Lease = rand.Text()
		result := tx.Model(&model.CheckinCredential{}).
			Where("id = ? AND credential_version = ? AND token_refresh_lease_until <= ?", id, expectedVersion, now.UnixMilli()).
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
func (s *CheckinStore) CommitRefresh(ctx context.Context, claim CheckinRefreshClaim, pair CheckinTokenPair) (*model.CheckinCredential, error) {
	pair.AccessToken = model.NormalizeSiteAccessToken(pair.AccessToken)
	pair.RefreshToken = strings.TrimSpace(pair.RefreshToken)
	if claim.Lease == "" || pair.AccessToken == "" || pair.RefreshToken == "" {
		return nil, errors.New("refresh claim and complete token pair are required")
	}
	var result model.CheckinCredential
	err := s.conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.CheckinCredential
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, claim.Before.ID).Error; err != nil {
			return err
		}
		// A previous save may have committed despite a lost acknowledgement.
		// Re-observing the exact rotated pair is safe and does not write again.
		committed := claim.Before
		committed.AccessToken, committed.RefreshToken = pair.AccessToken, pair.RefreshToken
		if sameCheckinSession(committed, current) && current.CredentialVersion > claim.Before.CredentialVersion {
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
		result.TokenRefreshedAt = time.Now().UnixMilli()
		result.CredentialVersion++
		result.TokenRefreshLease, result.TokenRefreshLeaseUntil = "", 0
		if err := result.Validate(); err != nil {
			return err
		}
		written := tx.Model(&model.CheckinCredential{}).
			Where("id = ? AND credential_version = ? AND token_refresh_lease = ?", current.ID, current.CredentialVersion, claim.Lease).
			Updates(map[string]any{"access_token": result.AccessToken, "refresh_token": result.RefreshToken,
				"token_expires_at": result.TokenExpiresAt, "token_refreshed_at": result.TokenRefreshedAt,
				"credential_version": result.CredentialVersion, "token_refresh_lease": "", "token_refresh_lease_until": 0})
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

func (s *CheckinStore) ReleaseRefresh(ctx context.Context, claim CheckinRefreshClaim) error {
	if claim.Lease == "" {
		return errors.New("refresh claim is required")
	}
	return s.conn.WithContext(ctx).Model(&model.CheckinCredential{}).
		Where("id = ? AND token_refresh_lease = ?", claim.Before.ID, claim.Lease).
		Updates(map[string]any{"token_refresh_lease": "", "token_refresh_lease_until": 0}).Error
}
