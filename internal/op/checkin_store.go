package op

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var (
	ErrCheckinCredentialConflict = errors.New("checkin credential version conflict")
	ErrCheckinCredentialInUse    = errors.New("checkin credential is still referenced")
	ErrCheckinRefreshLease       = errors.New("checkin credential refresh lease is unavailable")
	ErrCheckinSiteArchived       = errors.New("checkin site is archived")
	ErrCheckinSiteInUse          = errors.New("checkin site still has accounts")
	ErrCheckinWorkflowPending    = errors.New("checkin account has an unfinished submitted workflow")
	ErrCheckinSessionUncertain   = errors.New("checkin session exchange needs reconciliation")
	ErrCheckinSessionBackoff     = errors.New("checkin session exchange is cooling down")
)

// CheckinStore has an explicit connection so schema migration and tests can use
// the new tables without starting legacy caches, services or subscription jobs.
type CheckinStore struct{ conn *gorm.DB }

func NewCheckinStore(conn *gorm.DB) *CheckinStore {
	// GORM debug SQL can contain bound secret values during writes.
	return &CheckinStore{conn: conn.Session(&gorm.Session{Logger: logger.Discard})}
}

// Dependency checks must see the latest committed references after acquiring
// their owner locks. MySQL's default repeatable-read snapshot is unsuitable here.
func (s *CheckinStore) transaction(ctx context.Context, work func(*gorm.DB) error) error {
	conn := s.conn.WithContext(ctx)
	if conn.Dialector.Name() == "sqlite" {
		return conn.Transaction(work)
	}
	return conn.Transaction(work, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

func (s *CheckinStore) CreateSite(ctx context.Context, site model.CheckinSite) (*model.CheckinSite, error) {
	site.ID = 0
	site.Archived, site.ArchivedAt = false, nil
	site.VerificationFingerprint, site.VerificationStatus, site.VerifiedAt = "", model.SiteCheckinSupportUnknown, nil
	if err := site.Validate(); err != nil {
		return nil, checkinConfigurationError(err)
	}
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		if err := validateCheckinProxy(tx, site.CheckinNetwork); err != nil {
			return err
		}
		return tx.Create(&site).Error
	})
	if err != nil {
		return nil, s.checkinNameError(err)
	}
	return &site, nil
}

func (s *CheckinStore) GetSite(ctx context.Context, id int) (*model.CheckinSite, error) {
	var site model.CheckinSite
	if err := s.conn.WithContext(ctx).First(&site, id).Error; err != nil {
		return nil, err
	}
	return &site, nil
}

func (s *CheckinStore) CreateCredential(ctx context.Context, credential model.CheckinCredential) (*model.CheckinCredential, error) {
	err := s.transaction(ctx, func(tx *gorm.DB) error { return createCheckinCredential(tx, &credential) })
	if err != nil {
		return nil, err
	}
	return &credential, nil
}

func createCheckinCredential(tx *gorm.DB, credential *model.CheckinCredential) error {
	if err := prepareCheckinCredential(credential); err != nil {
		return err
	}
	if err := validateCheckinProxy(tx, credential.CheckinNetwork); err != nil {
		return err
	}
	return tx.Create(credential).Error
}

func prepareCheckinCredential(credential *model.CheckinCredential) error {
	credential.BalanceObservation = 0
	credential.ID = 0
	credential.CredentialVersion, credential.IdentityRevision = 0, 0
	credential.TokenRefreshLease, credential.TokenRefreshLeaseUntil, credential.TokenRefreshedAt = "", 0, 0
	credential.TokenRefreshStarted, credential.TokenRefreshRetryAt = false, 0
	return checkinConfigurationError(credential.Validate())
}

func (s *CheckinStore) GetCredential(ctx context.Context, id int) (*model.CheckinCredential, error) {
	var credential model.CheckinCredential
	if err := s.conn.WithContext(ctx).First(&credential, id).Error; err != nil {
		return nil, err
	}
	return &credential, nil
}

func (s *CheckinStore) ListCredentials(ctx context.Context) ([]model.CheckinCredential, error) {
	var credentials []model.CheckinCredential
	err := s.conn.WithContext(ctx).Order("id ASC").Find(&credentials).Error
	return credentials, err
}

// EditCredential accepts a complete internal candidate. API editors should use
// PatchCredential so omitted secrets are merged from storage inside the lock.
func (s *CheckinStore) EditCredential(ctx context.Context, candidate model.CheckinCredential, expectedVersion int64) (*model.CheckinCredential, error) {
	return s.editCredential(ctx, candidate.ID, expectedVersion, func(current *model.CheckinCredential) { *current = candidate })
}

func (s *CheckinStore) PatchCredential(ctx context.Context, id int, patch model.CheckinCredentialPatch) (*model.CheckinCredential, error) {
	if !patch.ExpectedVersion.Present || patch.ExpectedVersion.Value < 0 {
		return nil, checkinConfigurationError(errors.New("expected credential version is required"))
	}
	return s.editCredential(ctx, id, patch.ExpectedVersion.Value, patch.Apply)
}

func (s *CheckinStore) editCredential(ctx context.Context, id int, expectedVersion int64, apply func(*model.CheckinCredential)) (*model.CheckinCredential, error) {
	var result model.CheckinCredential
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var current model.CheckinCredential
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, id).Error; err != nil {
			return err
		}
		if expectedVersion < 0 || current.CredentialVersion != expectedVersion {
			return ErrCheckinCredentialConflict
		}
		candidate := current
		apply(&candidate)
		candidate.ID = current.ID
		candidate.CredentialVersion = current.CredentialVersion
		candidate.IdentityRevision = current.IdentityRevision
		candidate.TokenRefreshedAt = current.TokenRefreshedAt
		candidate.TokenRefreshLease, candidate.TokenRefreshLeaseUntil = current.TokenRefreshLease, current.TokenRefreshLeaseUntil
		candidate.TokenRefreshStarted, candidate.TokenRefreshRetryAt = current.TokenRefreshStarted, current.TokenRefreshRetryAt
		candidate.SessionCookies = current.SessionCookies
		if !sameCheckinSession(candidate, current) {
			candidate.SessionCookies = nil
			if candidate.Type != model.CheckinCredentialCookie && len(current.SessionCookies) > 0 && candidate.Cookie == current.Cookie {
				candidate.Cookie = ""
			}
		}
		if err := candidate.Validate(); err != nil {
			return checkinConfigurationError(err)
		}
		if candidate.Type == model.CheckinCredentialPassword && (candidate.Type != current.Type || candidate.Platform != current.Platform || candidate.AuthBaseURL != current.AuthBaseURL || candidate.Username != current.Username || candidate.Password != current.Password) {
			// A password/principal change must not keep a session issued to the old
			// login. Explicitly supplied replacement session values are retained.
			if candidate.AccessToken == current.AccessToken {
				candidate.AccessToken = ""
			}
			if candidate.Cookie == current.Cookie {
				candidate.Cookie = ""
			}
			if candidate.RefreshToken == current.RefreshToken {
				candidate.RefreshToken = ""
			}
			if reflect.DeepEqual(candidate.PlatformUserID, current.PlatformUserID) {
				candidate.PlatformUserID = nil
			}
			candidate.TokenExpiresAt = 0
		}
		if err := validateCheckinProxy(tx, candidate.CheckinNetwork); err != nil {
			return err
		}
		if identityChanged(current, candidate) {
			candidate.IdentityRevision++
		}
		// Setting edits must not release the lease of an already-consumed refresh
		// token. A genuinely different login identity can retire the old lease.
		if !sameCheckinSession(current, candidate) {
			candidate.TokenRefreshLease, candidate.TokenRefreshLeaseUntil, candidate.TokenRefreshedAt = "", 0, 0
			candidate.TokenRefreshStarted, candidate.TokenRefreshRetryAt = false, 0
		}
		if err := validateCredentialReferences(tx, candidate); err != nil {
			return err
		}
		candidate.BalanceObservation = current.BalanceObservation
		candidate.CredentialVersion++
		written := tx.Model(&model.CheckinCredential{}).Where("id = ? AND credential_version = ?", current.ID, expectedVersion).
			Select("*").Omit("id").Updates(&candidate)
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

func identityChanged(a, b model.CheckinCredential) bool {
	a.BalanceObservation, b.BalanceObservation = 0, 0
	a.Name, b.Name = "", ""
	a.TokenExpiresAt, b.TokenExpiresAt = 0, 0
	a.TokenRefreshedAt, b.TokenRefreshedAt = 0, 0
	a.CredentialVersion, b.CredentialVersion = 0, 0
	a.IdentityRevision, b.IdentityRevision = 0, 0
	a.TokenRefreshLease, b.TokenRefreshLease = "", ""
	a.TokenRefreshLeaseUntil, b.TokenRefreshLeaseUntil = 0, 0
	a.TokenRefreshStarted, b.TokenRefreshStarted = false, false
	a.TokenRefreshRetryAt, b.TokenRefreshRetryAt = 0, 0
	return !reflect.DeepEqual(a, b)
}

func sameCheckinSession(a, b model.CheckinCredential) bool {
	return a.ID == b.ID && a.Type == b.Type && a.Platform == b.Platform && a.AuthBaseURL == b.AuthBaseURL &&
		a.Username == b.Username && a.Password == b.Password && a.AccessToken == b.AccessToken && a.RefreshToken == b.RefreshToken && a.APIKey == b.APIKey && a.Cookie == b.Cookie && reflect.DeepEqual(a.SessionCookies, b.SessionCookies)
}

type CheckinCredentialReferences struct {
	PrimaryAccountIDs []int `json:"primary_account_ids"`
	BalanceAccountIDs []int `json:"balance_account_ids"`
}

func (s *CheckinStore) CredentialReferences(ctx context.Context, id int) (CheckinCredentialReferences, error) {
	return checkinCredentialReferences(s.conn.WithContext(ctx), id)
}

func checkinCredentialReferences(tx *gorm.DB, id int) (CheckinCredentialReferences, error) {
	refs := CheckinCredentialReferences{PrimaryAccountIDs: []int{}, BalanceAccountIDs: []int{}}
	var accounts []model.CheckinAccount
	// Balance configuration is JSON. Use portable decoding rather than a
	// different JSON predicate for each of the three supported SQL dialects.
	if err := tx.Select("id", "credential_id", "balance_config").Order("id ASC").Find(&accounts).Error; err != nil {
		return refs, err
	}
	for _, account := range accounts {
		if account.CredentialID == id {
			refs.PrimaryAccountIDs = append(refs.PrimaryAccountIDs, account.ID)
		}
		if account.BalanceConfig.Mode == model.CheckinBalanceCustom && account.BalanceConfig.CredentialID != nil && *account.BalanceConfig.CredentialID == id {
			refs.BalanceAccountIDs = append(refs.BalanceAccountIDs, account.ID)
		}
	}
	return refs, nil
}

func (s *CheckinStore) DeleteCredential(ctx context.Context, id int) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var credential model.CheckinCredential
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&credential, id).Error; err != nil {
			return err
		}
		refs, err := checkinCredentialReferences(tx, id)
		if err != nil {
			return err
		}
		if len(refs.PrimaryAccountIDs)+len(refs.BalanceAccountIDs) > 0 {
			return ErrCheckinCredentialInUse
		}
		if credential.TokenRefreshStarted || (credential.TokenRefreshLease != "" && credential.TokenRefreshLeaseUntil > time.Now().UnixMilli()) {
			return ErrCheckinRefreshLease
		}
		return tx.Delete(&credential).Error
	})
}

func (s *CheckinStore) CreateAccount(ctx context.Context, account model.CheckinAccount, inline *model.CheckinCredential) (*model.CheckinAccount, error) {
	if inline != nil && account.CredentialID != 0 {
		return nil, checkinConfigurationError(errors.New("provide a credential or credential ID, not both"))
	}
	account.ID = 0
	account.CheckinWorkflowVersion = 0
	account.CheckinPending, account.NextAutoCheckinAt, account.LastCheckinAt, account.LastCheckinSuccessAt = nil, nil, nil, nil
	account.LastCheckinStatus, account.LastCheckinMessage, account.CheckinFailureCount = model.SiteExecutionStatusIdle, "", 0
	account.Balance, account.BalanceUsed, account.TodayIncome = 0, 0, 0
	account.BalanceVersion, account.BalanceConfigVersion, account.BalanceUpdatedAt = 0, 0, nil
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var site model.CheckinSite
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&site, account.SiteID).Error; err != nil {
			return err
		}
		if site.Archived {
			return ErrCheckinSiteArchived
		}
		if inline != nil {
			credential := *inline
			if err := prepareCheckinCredential(&credential); err != nil {
				return err
			}
			// Defer proxy locks until all referenced credentials are locked below.
			// A missing/deleted proxy then rolls back the entire inline creation.
			if err := tx.Create(&credential).Error; err != nil {
				return err
			}
			account.CredentialID = credential.ID
		}
		if err := account.Validate(); err != nil {
			return checkinConfigurationError(err)
		}
		if err := validateCheckinAccounts(tx, site, []model.CheckinAccount{account}); err != nil {
			return err
		}
		return tx.Create(&account).Error
	})
	if err != nil {
		return nil, s.checkinNameError(err)
	}
	return &account, nil
}

func (s *CheckinStore) GetAccount(ctx context.Context, id int) (*model.CheckinAccount, error) {
	var account model.CheckinAccount
	if err := s.conn.WithContext(ctx).First(&account, id).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

// All configuration writes use site -> account -> sorted credentials -> sorted
// proxies. Credential edits only read dependents, so they never lock backwards.
func validateCheckinAccounts(tx *gorm.DB, site model.CheckinSite, accounts []model.CheckinAccount) error {
	var ids []int
	networks := []model.CheckinNetwork{site.CheckinNetwork}
	for _, account := range accounts {
		ids = append(ids, account.CredentialID)
		networks = append(networks, account.CheckinNetwork)
		if account.BalanceConfig.Mode == model.CheckinBalanceCustom {
			if account.BalanceConfig.CredentialID == nil {
				return checkinConfigurationError(errors.New("balance credential ID is required"))
			}
			ids = append(ids, *account.BalanceConfig.CredentialID)
			networks = append(networks, account.BalanceConfig.CheckinNetwork)
		}
	}
	slices.Sort(ids)
	credentials := make(map[int]model.CheckinCredential)
	for _, id := range slices.Compact(ids) {
		var credential model.CheckinCredential
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&credential, id).Error; err != nil {
			return err
		}
		credentials[id] = credential
		networks = append(networks, credential.CheckinNetwork)
	}
	for _, account := range accounts {
		if err := validateCheckinAccountIdentity(site, account, credentials); err != nil {
			return err
		}
	}
	return validateCheckinProxies(tx, networks...)
}

func validateCheckinAccountIdentity(site model.CheckinSite, account model.CheckinAccount, credentials map[int]model.CheckinCredential) error {
	if err := model.ValidateCheckinIdentity(site, account, credentials[account.CredentialID]); err != nil {
		return checkinConfigurationError(err)
	}
	if account.BalanceConfig.Mode == model.CheckinBalanceCustom {
		if err := validateCheckinBalanceCredential(account.BalanceConfig, credentials[*account.BalanceConfig.CredentialID]); err != nil {
			return err
		}
	} else if account.BalanceConfig.Mode == model.CheckinBalancePrimary {
		credential := credentials[account.CredentialID]
		if err := validateCheckinBalanceCredential(model.CheckinBalanceConfig{Platform: credential.Platform}, credential); err != nil {
			return err
		}
	}
	return nil
}

func validateCredentialReferences(tx *gorm.DB, candidate model.CheckinCredential) error {
	var accounts []model.CheckinAccount
	if err := tx.Find(&accounts).Error; err != nil {
		return err
	}
	for _, account := range accounts {
		if account.CredentialID == candidate.ID {
			var site model.CheckinSite
			if err := tx.First(&site, account.SiteID).Error; err != nil {
				return err
			}
			if err := model.ValidateCheckinIdentity(site, account, candidate); err != nil {
				return checkinConfigurationError(err)
			}
			if account.BalanceConfig.Mode == model.CheckinBalancePrimary {
				if err := validateCheckinBalanceCredential(model.CheckinBalanceConfig{Platform: candidate.Platform}, candidate); err != nil {
					return err
				}
			}
		}
		if account.BalanceConfig.Mode == model.CheckinBalanceCustom && account.BalanceConfig.CredentialID != nil && *account.BalanceConfig.CredentialID == candidate.ID {
			if err := validateCheckinBalanceCredential(account.BalanceConfig, candidate); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCheckinBalanceCredential(config model.CheckinBalanceConfig, credential model.CheckinCredential) error {
	return checkinConfigurationError(model.ValidateCheckinBalanceCredential(config, credential))
}

func validateCheckinProxy(tx *gorm.DB, network model.CheckinNetwork) error {
	return validateCheckinProxies(tx, network)
}

func validateCheckinProxies(tx *gorm.DB, networks ...model.CheckinNetwork) error {
	var ids []int
	for _, network := range networks {
		if network.ProxyMode != model.ProxyUsageModePool {
			continue
		}
		if network.ProxyConfigID == nil || *network.ProxyConfigID <= 0 {
			return checkinConfigurationError(errors.New("checkin proxy ID is required"))
		}
		ids = append(ids, *network.ProxyConfigID)
	}
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		var proxy model.ProxyConfiguration
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&proxy, id).Error; err != nil {
			return fmt.Errorf("load checkin proxy: %w", err)
		}
		if !proxy.Enabled {
			return ErrCheckinProxyDisabled
		}
	}
	return nil
}
