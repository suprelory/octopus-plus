package op

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin/finance"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const CheckinNotificationSendTimeout = 30 * time.Second

var ErrCheckinNotificationState = errors.New("notification state changed")

func completionID(logID int64, status model.SiteExecutionStatus) string {
	return fmt.Sprintf("%d:%s", logID, status)
}

func createCheckinCompletion(tx *gorm.DB, entry model.CheckinLog, failures int) error {
	if entry.Status != model.SiteExecutionStatusSuccess && entry.Status != model.SiteExecutionStatusFailed {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.CheckinCompletion{
		ID: completionID(entry.ID, entry.Status), LogID: entry.ID, Status: entry.Status, Entry: entry, FailureCount: failures,
		ReadyAt: time.Now().Add(30 * time.Second).UnixMilli(),
	}).Error
}

func lockNotificationGate(tx *gorm.DB) error {
	var gate model.CheckinNotificationGate
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&gate, 1).Error
}

func (s *CheckinStore) ReadyCompletion(ctx context.Context, result *model.CheckinResult, balance *float64) error {
	if balance != nil && !finance.Finite(*balance) {
		return errors.New("invalid notification balance")
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := lockNotificationGate(tx); err != nil {
			return err
		}
		var completion model.CheckinCompletion
		if err := tx.First(&completion, "id = ?", completionID(result.LogID, result.Status)).Error; err != nil {
			return err
		}
		if completion.Expanded {
			return nil
		}
		var entry model.CheckinLog
		if err := tx.First(&entry, result.LogID).Error; err != nil {
			return err
		}
		if entry.Status == completion.Status {
			completion.Entry = entry
		}
		completion.Balance, completion.ReadyAt = balance, time.Now().UnixMilli()
		return tx.Model(&completion).Select("entry", "balance", "ready_at").Updates(&completion).Error
	})
}

func (s *CheckinStore) DueCompletions(ctx context.Context) ([]model.CheckinCompletion, error) {
	var entries []model.CheckinCompletion
	err := s.conn.WithContext(ctx).Where("expanded = ? AND ready_at <= ?", false, time.Now().UnixMilli()).Order("ready_at ASC, id ASC").Limit(128).Find(&entries).Error
	return entries, err
}

// Expansion and cooldown reservation are database-only. The destination is an
// opaque fingerprint; notification secrets never enter these runtime tables.
func (s *CheckinStore) ExpandCompletion(ctx context.Context, expected model.CheckinCompletion, event, reason string, destinations []string, cooldown time.Duration, threshold float64) error {
	if cooldown < 0 || cooldown > 7*24*time.Hour || !finance.Finite(threshold) || threshold < 0 || len(destinations) > 5 {
		return errors.New("invalid notification policy")
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := lockNotificationGate(tx); err != nil {
			return err
		}
		var entry model.CheckinCompletion
		if err := tx.First(&entry, "id = ?", expected.ID).Error; err != nil {
			return err
		}
		if entry.Expanded {
			return nil
		}
		if entry.ReadyAt > time.Now().UnixMilli() || entry.ReadyAt != expected.ReadyAt || !reflect.DeepEqual(entry.Balance, expected.Balance) {
			return ErrCheckinNotificationState
		}
		// A crash can occur after reward backfill but before ReadyCompletion.
		// Refresh only the matching terminal state; a later confirmation must not
		// rewrite the earlier failure event's snapshot.
		var logEntry model.CheckinLog
		if err := tx.Where("id = ? AND status = ?", entry.LogID, entry.Status).First(&logEntry).Error; err == nil {
			entry.Entry = logEntry
			if err := tx.Model(&entry).Select("entry").Updates(&entry).Error; err != nil {
				return err
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		for _, target := range destinations {
			if len(target) != 64 {
				return errors.New("invalid notification destination fingerprint")
			}
			digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s:%s", target, entry.Entry.AccountID, event, reason)))
			delivery := model.CheckinNotificationDelivery{CompletionID: entry.ID, Destination: target, CooldownKey: hex.EncodeToString(digest[:]), Event: event, Threshold: threshold, CooldownMS: cooldown.Milliseconds(), State: "queued"}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&delivery).Error; err != nil {
				return err
			}
		}
		return tx.Model(&entry).Update("expanded", true).Error
	})
}

func (s *CheckinStore) PendingNotifications(ctx context.Context) ([]model.CheckinNotificationDelivery, error) {
	var deliveries []model.CheckinNotificationDelivery
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		if err := lockNotificationGate(tx); err != nil {
			return err
		}
		// A sender may have delivered before crashing. Expiration never grants
		// permission to send it again; an operator can explicitly retry it.
		if err := tx.Model(&model.CheckinNotificationDelivery{}).Where("state = ? AND started_at <= ?", "sending", time.Now().Add(-CheckinNotificationSendTimeout).UnixMilli()).Update("state", "uncertain").Error; err != nil {
			return err
		}
		return tx.Where("state = ?", "queued").Order("id ASC").Limit(128).Find(&deliveries).Error
	})
	return deliveries, err
}

func (s *CheckinStore) ClaimNotification(ctx context.Context, id int64, enabled, manualEnabled bool) (*model.CheckinNotificationDelivery, *model.CheckinCompletion, error) {
	var delivery model.CheckinNotificationDelivery
	var completion model.CheckinCompletion
	claimed := false
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		if err := lockNotificationGate(tx); err != nil {
			return err
		}
		if err := tx.First(&delivery, id).Error; err != nil {
			return err
		}
		if delivery.State != "queued" {
			return nil
		}
		if err := tx.First(&completion, "id = ?", delivery.CompletionID).Error; err != nil {
			return err
		}
		if !enabled || completion.Entry.Source != string(model.CheckinTriggerScheduled) && !manualEnabled {
			return tx.Model(&delivery).Update("state", "canceled").Error
		}
		now := time.Now().UnixMilli()
		var cooldown model.CheckinNotificationCooldown
		err := tx.Where(map[string]any{"key": delivery.CooldownKey}).First(&cooldown).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if cooldown.Until > now {
			return tx.Model(&delivery).Update("state", "suppressed").Error
		}
		var active int64
		if err := tx.Model(&model.CheckinNotificationDelivery{}).Where("cooldown_key = ? AND state = ?", delivery.CooldownKey, "sending").Count(&active).Error; err != nil {
			return err
		}
		if active != 0 {
			return nil
		}
		delivery.State, delivery.StartedAt, delivery.FinishedAt = "sending", now, 0
		delivery.Attempts++
		claimed = true
		return tx.Model(&delivery).Select("state", "started_at", "finished_at", "attempts").Updates(&delivery).Error
	})
	if err != nil || !claimed {
		return nil, nil, err
	}
	return &delivery, &completion, nil
}

func (s *CheckinStore) FinishNotification(ctx context.Context, expected model.CheckinNotificationDelivery, delivered bool) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := lockNotificationGate(tx); err != nil {
			return err
		}
		var current model.CheckinNotificationDelivery
		if err := tx.First(&current, expected.ID).Error; err != nil {
			return err
		}
		if current.Attempts != expected.Attempts || current.State != "sending" && current.State != "uncertain" {
			return ErrCheckinNotificationState
		}
		now := time.Now().UnixMilli()
		state := "uncertain"
		if delivered {
			state = "delivered"
			cooldown := model.CheckinNotificationCooldown{Key: current.CooldownKey, Until: now + current.CooldownMS}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"until"})}).Create(&cooldown).Error; err != nil {
				return err
			}
		}
		return tx.Model(&current).Updates(map[string]any{"state": state, "finished_at": now}).Error
	})
}

// RetryNotification is explicit: providers cannot guarantee exactly-once
// delivery, so retrying an uncertain send can duplicate a delivered message.
func (s *CheckinStore) RetryNotification(ctx context.Context, id int64) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := lockNotificationGate(tx); err != nil {
			return err
		}
		var delivery model.CheckinNotificationDelivery
		if err := tx.First(&delivery, id).Error; err != nil {
			return err
		}
		if delivery.State != "uncertain" || delivery.StartedAt > time.Now().Add(-CheckinNotificationSendTimeout).UnixMilli() {
			return ErrCheckinNotificationState
		}
		return tx.Model(&delivery).Update("state", "queued").Error
	})
}

func (s *CheckinStore) ListNotifications(ctx context.Context, beforeID int64, limit int) ([]model.CheckinNotificationDelivery, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	return s.listNotifications(ctx, beforeID, limit)
}

func (s *CheckinStore) NotificationPage(ctx context.Context, beforeID int64, limit int) (*model.CheckinNotificationPage, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	items, err := s.listNotifications(ctx, beforeID, limit+1)
	if err != nil {
		return nil, err
	}
	page := &model.CheckinNotificationPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextBeforeID = items[limit-1].ID
	}
	return page, nil
}

func (s *CheckinStore) listNotifications(ctx context.Context, beforeID int64, limit int) ([]model.CheckinNotificationDelivery, error) {
	query := s.conn.WithContext(ctx).Order("id DESC").Limit(limit)
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	deliveries := []model.CheckinNotificationDelivery{}
	err := query.Find(&deliveries).Error
	return deliveries, err
}
