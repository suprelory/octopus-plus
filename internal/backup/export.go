package backup

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func (s *Service) snapshot(ctx context.Context, read func(*gorm.DB) error) error {
	conn := s.conn.WithContext(ctx)
	if conn.Dialector.Name() == "sqlite" {
		return conn.Transaction(read)
	}
	return conn.Transaction(read, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}

func (s *Service) Export(ctx context.Context, includeLogs, includeStats bool) (*Document, error) {
	var document *Document
	err := s.snapshot(ctx, func(tx *gorm.DB) error {
		var err error
		document, err = exportConfiguration(tx, includeLogs, includeStats)
		if err != nil || !includeLogs {
			return err
		}
		targets := tables(document)
		return exportRelay(tx, func(name string, row any) error {
			if row == nil {
				return nil
			}
			value := reflect.ValueOf(targets[name]).Elem()
			value.Set(reflect.Append(value, reflect.ValueOf(row)))
			return nil
		})
	})
	if err != nil {
		return nil, ErrStorage
	}
	return document, nil
}

// ExportZIP releases its snapshot before sending any bytes to a slow client.
func (s *Service) ExportZIP(ctx context.Context, output io.Writer, includeLogs, includeStats bool) error {
	file, err := os.CreateTemp("", "octopus-v2-export-*.zip")
	if err != nil {
		return ErrStorage
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	err = s.snapshot(ctx, func(tx *gorm.DB) (err error) {
		d, err := exportConfiguration(tx, includeLogs, includeStats)
		if err != nil {
			return err
		}
		archive := zip.NewWriter(file)
		defer func() {
			if closeErr := archive.Close(); err == nil {
				err = closeErr
			}
		}()
		entries := tables(d)
		names := archiveTables(d)
		header := manifest{Version: Version, ExportedAt: d.ExportedAt, IncludeLogs: includeLogs, IncludeStats: includeStats, Format: "zip-v2", Tables: names}
		write := func(name string, data any) error {
			entry, err := archive.Create(name)
			if err != nil {
				return err
			}
			return json.NewEncoder(entry).Encode(data)
		}
		if err := write("manifest.json", header); err != nil {
			return err
		}
		for _, name := range names {
			if !relayTable(name) {
				if err := write(name+".json", entries[name]); err != nil {
					return err
				}
			}
		}
		if !includeLogs {
			return nil
		}
		var name string
		var encoder *json.Encoder
		return exportRelay(tx, func(table string, row any) error {
			if name != table {
				entry, err := archive.Create(table + ".ndjson")
				if err != nil {
					return err
				}
				name, encoder = table, json.NewEncoder(entry)
			}
			if row == nil {
				return nil
			}
			return encoder.Encode(row)
		})
	})
	if err != nil {
		return ErrStorage
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ErrStorage
	}
	_, err = io.Copy(output, contextReader{ctx, file})
	return err
}

func v2Table(name string) bool {
	switch name {
	case "sites", "site_accounts", "site_tokens", "site_user_groups", "site_models", "site_channel_bindings", "stats_site_model_hourly":
		return false
	}
	return true
}

func statisticsTable(name string) bool {
	return strings.HasPrefix(name, "stats_") || name == "archived_site_stats"
}

func exportConfiguration(tx *gorm.DB, includeLogs, includeStats bool) (*Document, error) {
	d := &Document{LegacyDBDump: model.LegacyDBDump{Version: Version, ExportedAt: time.Now().UTC(), IncludeLogs: includeLogs, IncludeStats: includeStats}}
	for name, rows := range tables(d) {
		if !v2Table(name) || relayTable(name) || strings.HasPrefix(name, "checkin_") || name == "site_checkin_logs" || statisticsTable(name) && !includeStats {
			continue
		}
		if (name == "stats_model" || name == "archived_site_stats") && !tx.Migrator().HasTable(rows) {
			continue
		}
		if err := tx.Find(rows).Error; err != nil {
			return nil, err
		}
	}
	d.Settings = slices.DeleteFunc(d.Settings, func(setting model.Setting) bool {
		return setting.Key == model.SettingKeyProjectedChannelAutoGroupEnabled
	})
	var sites []model.CheckinSite
	var accounts []model.CheckinAccount
	var credentials []model.CheckinCredential
	var deliveries []model.CheckinNotificationDelivery
	var cooldowns []model.CheckinNotificationCooldown
	for _, rows := range []any{&sites, &accounts, &credentials, &deliveries, &d.CheckinCompletions, &cooldowns} {
		if err := tx.Find(rows).Error; err != nil {
			return nil, err
		}
	}
	for _, site := range sites {
		d.CheckinSites = append(d.CheckinSites, Site{site, site.VerificationFingerprint})
	}
	for _, credential := range credentials {
		d.CheckinCredentials = append(d.CheckinCredentials, model.ExportCheckinCredential(credential))
	}
	ids := []int64{}
	for _, account := range accounts {
		d.CheckinAccounts = append(d.CheckinAccounts, Account{account, account.CheckinWorkflowVersion, account.BalanceVersion, account.BalanceConfigVersion})
		if account.CheckinPending != nil {
			ids = append(ids, account.CheckinPending.LogID)
		}
	}
	completions := map[string]model.CheckinCompletion{}
	for _, completion := range d.CheckinCompletions {
		completions[completion.ID] = completion
		ids = append(ids, completion.LogID)
	}
	contexts := map[string]Cooldown{}
	for _, delivery := range deliveries {
		d.CheckinDeliveries = append(d.CheckinDeliveries, Delivery{delivery, delivery.CooldownKey, delivery.CooldownMS})
		completion := completions[delivery.CompletionID]
		reason := completion.Entry.Reason
		if delivery.Event == "site_checkin_low_balance" {
			reason = "low_balance"
		}
		contexts[delivery.CooldownKey] = Cooldown{Key: delivery.CooldownKey, AccountID: completion.Entry.AccountID, Destination: delivery.Destination, Event: delivery.Event, Reason: reason}
	}
	for _, cooldown := range cooldowns {
		entry := contexts[cooldown.Key]
		entry.Key, entry.Until = cooldown.Key, cooldown.Until
		d.CheckinCooldowns = append(d.CheckinCooldowns, entry)
	}
	if includeLogs {
		if err := tx.Order("id ASC").Find(&d.SiteCheckinLogs).Error; err != nil {
			return nil, err
		}
	} else {
		slices.Sort(ids)
		for chunk := range slices.Chunk(slices.Compact(ids), 500) {
			var logs []model.CheckinLog
			if err := tx.Where("id IN ?", chunk).Order("id ASC").Find(&logs).Error; err != nil {
				return nil, err
			}
			d.SiteCheckinLogs = append(d.SiteCheckinLogs, logs...)
		}
	}
	if includeStats && tx.Migrator().HasTable(&model.LegacyStatsSiteModelHourly{}) {
		var legacy []model.LegacyStatsSiteModelHourly
		if err := tx.Find(&legacy).Error; err != nil {
			return nil, err
		}
		for _, row := range legacy {
			d.ArchivedSiteStats = append(d.ArchivedSiteStats, ArchivedSiteStat{Stats: row})
		}
	}
	return d, nil
}

func exportRelay(tx *gorm.DB, visit func(string, any) error) error {
	if err := pageRows[model.RelayLog](tx, "relay_logs", visit); err != nil {
		return err
	}
	if err := pageRows[model.RelayRequestContent](tx, "relay_request_contents", visit); err != nil {
		return err
	}
	return pageRows[model.RelayAttemptContent](tx, "relay_attempt_contents", visit)
}

func pageRows[T any](tx *gorm.DB, name string, visit func(string, any) error) error {
	if err := visit(name, nil); err != nil {
		return err
	}
	rows, err := tx.Model(new(T)).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row T
		if err := tx.ScanRows(rows, &row); err != nil {
			return err
		}
		if err := visit(name, row); err != nil {
			return err
		}
	}
	return rows.Err()
}
