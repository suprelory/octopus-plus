package op

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin/finance"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrCheckinBalanceDisabled = errors.New("balance query is disabled")
	ErrCheckinBalanceChanged  = errors.New("balance configuration or observation changed")
)

type CheckinBalanceSnapshot struct {
	Account     model.CheckinAccount       `json:"-"`
	Credential  model.CheckinCredential    `json:"-"`
	Config      model.CheckinBalanceConfig `json:"-"`
	Fingerprint string                     `json:"-"`
	Exclusive   bool                       `json:"-"`
}

func loadCheckinBalance(tx *gorm.DB, id int) (*CheckinBalanceSnapshot, error) {
	site, account, err := lockCheckinAccount(tx, id)
	if err != nil {
		return nil, err
	}
	if site.Archived || !site.Enabled || !account.Enabled {
		return nil, ErrCheckinUnavailable
	}
	config := account.BalanceConfig
	credentialID := account.CredentialID
	switch config.Mode {
	case model.CheckinBalancePrimary:
	case model.CheckinBalanceCustom:
		if config.CredentialID == nil {
			return nil, ErrCheckinBalanceDisabled
		}
		credentialID = *config.CredentialID
	default:
		return nil, ErrCheckinBalanceDisabled
	}
	var credential model.CheckinCredential
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&credential, credentialID).Error; err != nil {
		return nil, err
	}
	if config.Mode == model.CheckinBalancePrimary {
		config.Platform, config.BaseURL, config.Headers, config.CheckinNetwork = credential.Platform, credential.AuthBaseURL, credential.AuthHeaders, credential.CheckinNetwork
	}
	if err := validateCheckinBalanceCredential(config, credential); err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal([]any{account.BalanceConfigVersion, config, credential.ID, credential.IdentityRevision})
	digest := sha256.Sum256(encoded)
	return &CheckinBalanceSnapshot{Account: account, Credential: credential, Config: config, Fingerprint: hex.EncodeToString(digest[:])}, nil
}

// Reserve an observation before network I/O. Later-started queries supersede
// earlier ones, including queries whose upstream response is slower.
func (s *CheckinStore) BeginBalance(ctx context.Context, id int) (*CheckinBalanceSnapshot, error) {
	var snapshot *CheckinBalanceSnapshot
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var err error
		snapshot, err = loadCheckinBalance(tx, id)
		if err != nil {
			return err
		}
		snapshot.Account.BalanceVersion++
		snapshot.Credential.BalanceObservation++
		if err := tx.Model(&model.CheckinAccount{}).Where("id = ?", id).Update("balance_version", snapshot.Account.BalanceVersion).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.CheckinCredential{}).Where("id = ?", snapshot.Credential.ID).Update("balance_observation", snapshot.Credential.BalanceObservation).Error; err != nil {
			return err
		}
		refs, err := checkinCredentialReferences(tx, snapshot.Credential.ID)
		if err != nil {
			return err
		}
		owners := map[int]bool{}
		for _, ids := range [][]int{refs.PrimaryAccountIDs, refs.BalanceAccountIDs} {
			for _, id := range ids {
				owners[id] = true
			}
		}
		snapshot.Exclusive = len(owners) == 1
		return nil
	})
	return snapshot, err
}

func checkinBalanceMatches(current, expected *CheckinBalanceSnapshot) error {
	if current.Fingerprint != expected.Fingerprint || current.Account.BalanceVersion != expected.Account.BalanceVersion {
		return ErrCheckinBalanceChanged
	}
	return nil
}

// Rejected reads may revoke cookies. Persist their scoped deletion markers even
// when no valid balance was returned, under the same identity/session checks.
func (s *CheckinStore) SaveBalanceSession(ctx context.Context, expected *CheckinBalanceSnapshot, session CheckinLoginSession) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		current, err := loadCheckinBalance(tx, expected.Account.ID)
		if err != nil {
			return err
		}
		if err := checkinBalanceMatches(current, expected); err != nil {
			return err
		}
		_, err = saveObservedCheckinSession(tx, current.Credential, expected.Credential, session, false)
		return err
	})
}

func (s *CheckinStore) SaveBalance(ctx context.Context, expected *CheckinBalanceSnapshot, value finance.Balance, session CheckinLoginSession) error {
	if !finance.Finite(value.Value) || value.UsedKnown && !finance.Finite(value.Used) || value.IncomeKnown && !finance.Finite(value.Income) {
		return errors.New("invalid balance")
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		current, err := loadCheckinBalance(tx, expected.Account.ID)
		if err != nil {
			return err
		}
		if err := checkinBalanceMatches(current, expected); err != nil {
			return err
		}
		if _, err := saveObservedCheckinSession(tx, current.Credential, expected.Credential, session, false); err != nil {
			return err
		}
		updates := map[string]any{"balance": value.Value, "balance_updated_at": time.Now().UTC()}
		if value.UsedKnown {
			updates["balance_used"] = value.Used
		}
		if value.IncomeKnown {
			updates["today_income"] = value.Income
		}
		return tx.Model(&model.CheckinAccount{}).Where("id = ?", current.Account.ID).Updates(updates).Error
	})
}

// Reward inference is best effort. A resumed workflow has no baseline, and a
// credential shared by accounts cannot attribute an upstream deposit reliably.
func (s *CheckinStore) FillBalanceReward(ctx context.Context, result *model.CheckinResult, before, after *CheckinBalanceSnapshot, reward string) (bool, error) {
	if before == nil || after == nil || !before.Exclusive || !after.Exclusive || before.Account.ID != result.AccountID || after.Account.ID != result.AccountID ||
		before.Fingerprint != after.Fingerprint || after.Account.BalanceVersion != before.Account.BalanceVersion+1 || after.Credential.BalanceObservation != before.Credential.BalanceObservation+1 || result.Replayed || reward == "" {
		return false, nil
	}
	if before.Account.CheckinPending == nil || after.Account.CheckinPending == nil || before.Account.CheckinPending.LogID != result.LogID ||
		after.Account.CheckinPending.LogID != result.LogID || before.Account.CheckinPending.Fingerprint != after.Account.CheckinPending.Fingerprint {
		return false, nil
	}
	outcome := result.CheckinOutcome
	outcome.Reward = reward
	if err := validateCheckinOutcome(outcome); err != nil {
		return false, err
	}
	saved := false
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		current, err := loadCheckinBalance(tx, result.AccountID)
		if err != nil {
			return err
		}
		if err := checkinBalanceMatches(current, after); err != nil {
			return err
		}
		if current.Credential.BalanceObservation != after.Credential.BalanceObservation {
			return ErrCheckinBalanceChanged
		}
		written := tx.Model(&model.CheckinLog{}).Where("id = ? AND account_id = ? AND status = ? AND reason = ? AND reward = ?", result.LogID, result.AccountID, model.SiteExecutionStatusSuccess, model.SiteCheckinReasonCheckedIn, "").Update("reward", reward)
		saved = written.RowsAffected == 1
		return written.Error
	})
	return saved, err
}
