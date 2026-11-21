package v2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const snapshotDocumentVersion = 1

type legacyAccountRuntime struct {
	AccountID              int    `json:"account_id"`
	TokenRefreshedAt       int64  `json:"token_refreshed_at"`
	TokenRefreshLease      string `json:"token_refresh_lease"`
	TokenRefreshLeaseUntil int64  `json:"token_refresh_lease_until"`
}

type migrationSnapshotDocument struct {
	Version        int                    `json:"version"`
	Source         string                 `json:"source"`
	Input          *LegacyInput           `json:"input"`
	AccountRuntime []legacyAccountRuntime `json:"account_runtime"`
}

// CaptureDatabaseInput reads the v1 tables without starting legacy services.
func CaptureDatabaseInput(ctx context.Context, conn *gorm.DB) (*LegacyInput, error) {
	if conn == nil {
		return nil, errors.New("migration database is required")
	}
	var input *LegacyInput
	read := func(tx *gorm.DB) error {
		var err error
		input, err = readDatabaseInput(tx, time.Now().UTC().Truncate(time.Second))
		return err
	}
	err := readSnapshotTransaction(conn.WithContext(ctx), read)
	return input, err
}

func readSnapshotTransaction(conn *gorm.DB, read func(*gorm.DB) error) error {
	if conn.Dialector.Name() == "sqlite" {
		return conn.Transaction(read)
	}
	return conn.Transaction(read, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}

func readDatabaseInput(tx *gorm.DB, capturedAt time.Time) (*LegacyInput, error) {
	input := &LegacyInput{Configuration: Snapshot{Version: 1, ExportedAt: capturedAt, IncludeLogs: true, IncludeStats: true}, CapturedAt: capturedAt, HistoryComplete: true}
	config := &input.Configuration
	for _, item := range []struct {
		table string
		rows  any
		order []string
		// A companion column introduced with the optional table distinguishes
		// an older schema from a missing table in a newer one.
		companion string
	}{
		{"sites", &config.Sites, []string{"id"}, ""},
		{"site_accounts", &config.SiteAccounts, []string{"id"}, ""},
		{"channels", &config.Channels, []string{"id"}, ""},
		{"channel_keys", &config.ChannelKeys, []string{"id"}, ""},
		{"proxy_configurations", &config.ProxyConfigurations, []string{"id"}, ""},
		{"site_user_groups", &config.SiteUserGroups, []string{"id"}, ""},
		{"site_channel_bindings", &config.SiteChannelBindings, []string{"id"}, ""},
		{"groups", &config.Groups, []string{"id"}, ""},
		{"group_items", &config.GroupItems, []string{"id"}, ""},
		{"group_presets", &input.GroupPresets, []string{"id"}, "groups.active_preset_id"},
		{"settings", &config.Settings, []string{"key"}, ""},
		{"stats_channels", &input.ChannelStats, []string{"channel_id"}, ""},
		{"stats_site_model_hourlies", &input.ArchivedSiteStats, []string{"site_account_id", "hour", "group_key", "model_name"}, ""},
		{"site_checkin_logs", &input.Logs, []string{"id"}, ""},
		{"site_checkin_batch_jobs", &input.Batches, []string{"id"}, "site_checkin_logs.batch_job_id"},
	} {
		if !tx.Migrator().HasTable(item.rows) {
			if table, column, ok := strings.Cut(item.companion, "."); ok && !tx.Migrator().HasColumn(table, column) {
				continue
			}
			return nil, fmt.Errorf("legacy table %s is missing", item.table)
		}
		statement := &gorm.Statement{DB: tx}
		if err := statement.Parse(item.rows); err != nil {
			return nil, fmt.Errorf("cannot inspect legacy %s", item.table)
		}
		columnTypes, err := tx.Migrator().ColumnTypes(item.rows)
		if err != nil {
			return nil, fmt.Errorf("cannot inspect legacy %s columns", item.table)
		}
		columns := make(map[string]bool, len(columnTypes))
		for _, column := range columnTypes {
			columns[strings.ToLower(column.Name())] = true
		}
		for _, field := range statement.Schema.Fields {
			if field.DBName == "" || columns[strings.ToLower(field.DBName)] || slices.Contains(optionalLegacyColumns[item.table], field.DBName) {
				continue
			}
			return nil, fmt.Errorf("legacy table %s is missing required column %s", item.table, field.DBName)
		}
		query := tx.Table(item.table).Select("*")
		for _, column := range item.order {
			query = query.Order(clause.OrderByColumn{Column: clause.Column{Name: column}})
		}
		if err := query.Find(item.rows).Error; err != nil {
			return nil, fmt.Errorf("cannot read legacy %s", item.table)
		}
	}
	// Canonical empty sets and instants round-trip through the checkpoint and
	// through shared tables on drivers with different session time zones.
	if len(config.SiteChannelBindings) == 0 {
		config.SiteChannelBindings = nil
	}
	if len(config.Settings) == 0 {
		config.Settings = nil
	}
	if len(input.Logs) == 0 {
		input.Logs = nil
	}
	if len(input.Batches) == 0 {
		input.Batches = nil
	}
	for i := range input.Logs {
		row := &input.Logs[i]
		row.StartedAt, row.FinishedAt = row.StartedAt.UTC(), row.FinishedAt.UTC()
	}
	for i := range input.Batches {
		row := &input.Batches[i]
		row.StartedAt, row.UpdatedAt = row.StartedAt.UTC(), row.UpdatedAt.UTC()
		if row.FinishedAt != nil {
			utc := row.FinishedAt.UTC()
			row.FinishedAt = &utc
		}
	}
	return input, nil
}

func normalizeRebuiltLogTimestamps(conn *gorm.DB, input *LegacyInput, conversion *Conversion) error {
	if conversion.Report.RebuiltLogs == 0 {
		return nil
	}
	unit := time.Nanosecond
	truncate := false
	if conn.Dialector.Name() != "sqlite" {
		schema := "current_schema()"
		if conn.Dialector.Name() == "mysql" {
			schema = "DATABASE()"
			if err := conn.Raw("SELECT FIND_IN_SET('TIME_TRUNCATE_FRACTIONAL', @@SESSION.sql_mode) > 0").Scan(&truncate).Error; err != nil {
				return errors.New("cannot inspect MySQL timestamp mode")
			}
		}
		var precision int
		row := conn.Raw("SELECT datetime_precision FROM information_schema.columns WHERE table_schema = " + schema + " AND table_name = 'site_checkin_logs' AND column_name = 'started_at'").Row()
		if err := row.Scan(&precision); err != nil || precision < 0 || precision > 9 {
			return errors.New("cannot inspect legacy log timestamp precision")
		}
		unit = time.Duration(math.Pow10(9 - precision))
	}
	original := make(map[int64]bool, len(input.Logs))
	for _, log := range input.Logs {
		original[log.ID] = true
	}
	for i := range conversion.Logs {
		log := &conversion.Logs[i]
		if original[log.ID] {
			continue
		}
		start := log.StartedAt.UTC()
		if truncate {
			log.StartedAt = start.Truncate(unit)
		} else {
			log.StartedAt = start.Round(unit)
		}
	}
	return nil
}

// Later v1 releases added these fields. Conversion supplies the execution
// defaults for older sites and treats absent session evidence as empty.
var optionalLegacyColumns = map[string][]string{
	"sites": {"kind", "linked_site_id", "checkin_http_enabled", "checkin_use_subscription_token", "checkin_http_method",
		"checkin_http_path", "checkin_http_body", "checkin_http_headers", "checkin_reward_extractor", "checkin_status",
		"checkin_mode", "checkin_verification_status", "checkin_verification_fingerprint", "checkin_verified_at"},
	"site_accounts": {"cookie", "linked_account_id", "checkin_pending", "checkin_source_account_id", "credential_version",
		"token_refreshed_at", "token_refresh_lease", "token_refresh_lease_until"},
	"groups":                  {"active_preset_id"},
	"site_checkin_logs":       {"batch_job_id"},
	"site_checkin_batch_jobs": {"pending"},
}

// WriteSnapshotFile creates a mode-0600 checkpoint without replacing an
// existing file. It includes old refresh lease state omitted by v1 JSON.
func WriteSnapshotFile(path, source string, input *LegacyInput) error {
	if path == "" || source == "" || input == nil || input.CapturedAt.IsZero() {
		return errors.New("invalid migration snapshot")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("prepare migration snapshot directory failed")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".v2-snapshot-*")
	if err != nil {
		return errors.New("migration snapshot already exists or cannot be created")
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()
	state := make([]legacyAccountRuntime, 0, len(input.Configuration.SiteAccounts))
	for _, account := range input.Configuration.SiteAccounts {
		state = append(state, legacyAccountRuntime{account.ID, account.TokenRefreshedAt, account.TokenRefreshLease, account.TokenRefreshLeaseUntil})
	}
	if err := json.NewEncoder(file).Encode(migrationSnapshotDocument{Version: snapshotDocumentVersion, Source: source, Input: input, AccountRuntime: state}); err != nil {
		return errors.New("write migration snapshot failed")
	}
	if err := file.Sync(); err != nil {
		return errors.New("flush migration snapshot failed")
	}
	if err := file.Close(); err != nil {
		return errors.New("close migration snapshot failed")
	}
	// Publish the fully written file atomically without overwriting an existing checkpoint.
	if err := os.Link(file.Name(), path); err != nil {
		return errors.New("cannot publish snapshot; use a new path on a filesystem supporting hard links")
	}
	return nil
}

func ReadSnapshotFile(path string) (string, *LegacyInput, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", nil, errors.New("open migration snapshot failed")
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var document migrationSnapshotDocument
	if err := decoder.Decode(&document); err != nil {
		return "", nil, errors.New("invalid migration snapshot")
	}
	if document.Version != snapshotDocumentVersion || document.Source == "" || document.Input == nil || document.Input.CapturedAt.IsZero() {
		return "", nil, errors.New("invalid migration snapshot")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", nil, errors.New("migration snapshot contains trailing data")
	}
	states := make(map[int]legacyAccountRuntime, len(document.AccountRuntime))
	for _, state := range document.AccountRuntime {
		if state.AccountID <= 0 {
			return "", nil, errors.New("invalid migration snapshot runtime state")
		}
		if _, exists := states[state.AccountID]; exists {
			return "", nil, errors.New("duplicate migration snapshot runtime state")
		}
		states[state.AccountID] = state
	}
	for i := range document.Input.Configuration.SiteAccounts {
		account := &document.Input.Configuration.SiteAccounts[i]
		state, exists := states[account.ID]
		if !exists {
			return "", nil, errors.New("incomplete migration snapshot runtime state")
		}
		account.TokenRefreshedAt = state.TokenRefreshedAt
		account.TokenRefreshLease = state.TokenRefreshLease
		account.TokenRefreshLeaseUntil = state.TokenRefreshLeaseUntil
		delete(states, account.ID)
	}
	if len(states) != 0 {
		return "", nil, errors.New("migration snapshot runtime state has unknown accounts")
	}
	return document.Source, document.Input, nil
}
