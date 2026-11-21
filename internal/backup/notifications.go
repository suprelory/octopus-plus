package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func cooldownKey(destination string, accountID int, event, reason string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s:%s", destination, accountID, event, reason)))
	return hex.EncodeToString(digest[:])
}

func (s *importer) importNotifications() error {
	original := map[string]model.CheckinCompletion{}
	for _, source := range s.document.CheckinCompletions {
		if source.ID != fmt.Sprintf("%d:%s", source.LogID, source.Status) || source.Entry.ID != source.LogID || source.Entry.Status != source.Status || source.LogID <= 0 || original[source.ID].ID != "" {
			return ErrInvalid
		}
		if source.Status != model.SiteExecutionStatusSuccess && source.Status != model.SiteExecutionStatusFailed {
			return ErrInvalid
		}
		if source.Balance != nil && (math.IsNaN(*source.Balance) || math.IsInf(*source.Balance, 0)) {
			return ErrInvalid
		}
		original[source.ID] = source
		row := source
		var err error
		row.Entry, err = s.mapLog(row.Entry)
		if err != nil {
			return err
		}
		var existing int64
		if err := s.tx.Model(&model.CheckinCompletion{}).Where("id = ?", row.ID).Count(&existing).Error; err != nil {
			return err
		}
		if existing != 0 {
			return ErrConflict
		}
		if err := s.insert("checkin_completions", &row); err != nil {
			return err
		}
	}
	for _, source := range s.document.CheckinDeliveries {
		completion, ok := original[source.CompletionID]
		if !ok || !completion.Expanded || len(source.Destination) != 64 || source.Attempts < 0 || source.CooldownMS < 0 {
			return ErrInvalid
		}
		reason := completion.Entry.Reason
		if source.Event == "site_checkin_low_balance" {
			reason = "low_balance"
		}
		if source.CooldownKey != cooldownKey(source.Destination, completion.Entry.AccountID, source.Event, reason) {
			return ErrInvalid
		}
		row := source.CheckinNotificationDelivery
		row.CooldownKey = cooldownKey(row.Destination, s.accounts[completion.Entry.AccountID].ID, row.Event, reason)
		row.CooldownMS = source.CooldownMS
		switch row.State {
		case "sending":
			row.State = "uncertain"
		case "queued", "uncertain", "delivered", "canceled", "suppressed":
		default:
			return ErrInvalid
		}
		row.ID = 0
		if err := s.insert("checkin_notification_deliveries", &row); err != nil {
			return err
		}
		if err := s.tx.Create(&importMapping{Fingerprint: s.fingerprint, Entity: "delivery", SourceID: source.ID, TargetID: row.ID}).Error; err != nil {
			return err
		}
	}
	for _, source := range s.document.CheckinCooldowns {
		if len(source.Key) != 64 || source.Until < 0 {
			return ErrInvalid
		}
		row := model.CheckinNotificationCooldown{Key: source.Key, Until: source.Until}
		if source.Destination == "" && source.Until > s.document.ExportedAt.UnixMilli() {
			return ErrInvalid
		}
		if source.Destination != "" {
			if source.Key != cooldownKey(source.Destination, source.AccountID, source.Event, source.Reason) {
				return ErrInvalid
			}
			row.Key = cooldownKey(source.Destination, s.accounts[source.AccountID].ID, source.Event, source.Reason)
		}
		// Existing runtime cooldowns may have advanced since the backup was made.
		column := clause.Column{Table: "checkin_notification_cooldowns", Name: "until"}
		update := gorm.Expr("CASE WHEN ? > ? THEN ? ELSE ? END", row.Until, column, row.Until, column)
		result := s.tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.Assignments(map[string]any{"until": update})}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
		s.result.RowsAffected["checkin_notification_cooldowns"] += result.RowsAffected
	}
	return nil
}
