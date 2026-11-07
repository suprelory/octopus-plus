package op

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

// DBExportZip spools a consistent snapshot to a private temporary archive before
// sending it. Slow clients must not hold a database transaction or connection.
// Logs are still paged as NDJSON, keeping memory independent of archive size.
func DBExportZip(ctx context.Context, w io.Writer, includeLogs, includeStats bool) error {
	file, err := os.CreateTemp("", "octopus-backup-*.zip")
	if err != nil {
		return fmt.Errorf("create backup archive: %w", err)
	}
	defer func() { file.Close(); os.Remove(file.Name()) }()
	if err := withBackupSnapshot(ctx, func(conn *gorm.DB) error {
		return exportZipSnapshot(ctx, file, conn, includeLogs, includeStats)
	}); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = io.Copy(w, backupContextReader{ctx: ctx, reader: file})
	return err
}

type backupContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r backupContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func exportZipSnapshot(ctx context.Context, w io.Writer, conn *gorm.DB, includeLogs, includeStats bool) (err error) {
	zw := zip.NewWriter(w)
	defer func() {
		if closeErr := zw.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	manifest := map[string]any{
		"version":       dbDumpVersion,
		"exported_at":   time.Now().UTC().Format(time.RFC3339),
		"include_logs":  includeLogs,
		"include_stats": includeStats,
		"format":        "zip-v1",
	}
	if err := writeZipJSON(zw, "manifest.json", manifest); err != nil {
		return err
	}

	if err := writeZipTable(ctx, zw, conn, "channels.json", &[]model.Channel{}); err != nil {
		return err
	}
	if err := writeZipTable(ctx, zw, conn, "channel_keys.json", &[]model.ChannelKey{}); err != nil {
		return err
	}
	if err := writeZipTable(ctx, zw, conn, "proxy_configurations.json", &[]model.ProxyConfiguration{}); err != nil {
		return err
	}
	if err := writeZipTable(ctx, zw, conn, "sites.json", &[]model.Site{}); err != nil {
		return err
	}
	if err := writeZipTable(ctx, zw, conn, "site_accounts.json", &[]model.SiteAccount{}); err != nil {
		return err
	}
	if err := writeZipTable(ctx, zw, conn, "site_tokens.json", &[]model.SiteToken{}); err != nil {
		return err
	}
	if err := writeZipTable(ctx, zw, conn, "site_user_groups.json", &[]model.SiteUserGroup{}); err != nil {
		return err
	}
	if err := writeZipTable(ctx, zw, conn, "site_models.json", &[]model.SiteModel{}); err != nil {
		return err
	}
	if err := writeZipTable(ctx, zw, conn, "site_channel_bindings.json", &[]model.SiteChannelBinding{}); err != nil {
		return err
	}
	if err := writeZipTable(ctx, zw, conn, "groups.json", &[]model.Group{}); err != nil {
		return err
	}
	if err := writeZipTable(ctx, zw, conn, "group_items.json", &[]model.GroupItem{}); err != nil {
		return err
	}
	if err := writeZipTable(ctx, zw, conn, "group_presets.json", &[]model.GroupPreset{}); err != nil {
		return err
	}
	if err := writeZipTable(ctx, zw, conn, "llm_infos.json", &[]model.LLMInfo{}); err != nil {
		return err
	}
	if err := writeZipTable(ctx, zw, conn, "api_keys.json", &[]model.APIKey{}); err != nil {
		return err
	}
	if err := writeZipTable(ctx, zw, conn, "settings.json", &[]model.Setting{}); err != nil {
		return err
	}

	if includeStats {
		if err := writeZipTable(ctx, zw, conn, "stats_total.json", &[]model.StatsTotal{}); err != nil {
			return err
		}
		if err := writeZipTable(ctx, zw, conn, "stats_daily.json", &[]model.StatsDaily{}); err != nil {
			return err
		}
		if err := writeZipTable(ctx, zw, conn, "stats_hourly.json", &[]model.StatsHourly{}); err != nil {
			return err
		}
		legacyStats, err := loadLegacyStatsModels(conn)
		if err != nil {
			return fmt.Errorf("zip read stats_model: %w", err)
		}
		if err := writeZipJSON(zw, "stats_model.json", legacyStats); err != nil {
			return err
		}
		if err := writeZipTable(ctx, zw, conn, "stats_channel.json", &[]model.StatsChannel{}); err != nil {
			return err
		}
		if err := writeZipTable(ctx, zw, conn, "stats_api_key.json", &[]model.StatsAPIKey{}); err != nil {
			return err
		}
		if err := writeZipTable(ctx, zw, conn, "stats_site_model_hourly.json", &[]model.StatsSiteModelHourly{}); err != nil {
			return err
		}
	}

	if includeLogs {
		if err := writeZipRelayLogsNDJSON(ctx, zw, conn); err != nil {
			return err
		}
		if err := writeZipRelayContents[model.RelayRequestContent](zw, conn, "relay_request_contents.ndjson"); err != nil {
			return err
		}
		if err := writeZipRelayContents[model.RelayAttemptContent](zw, conn, "relay_attempt_contents.ndjson"); err != nil {
			return err
		}
		if err := writeZipSiteCheckinLogsNDJSON(zw, conn); err != nil {
			return err
		}
	}

	return nil
}

func writeZipSiteCheckinLogsNDJSON(zw *zip.Writer, conn *gorm.DB) error {
	f, err := zw.Create("site_checkin_logs.ndjson")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	var lastID int64
	for {
		var batch []model.SiteCheckinLog
		if err := conn.Where("id > ?", lastID).Order("id ASC").Limit(dbExportLogBatchSize).Find(&batch).Error; err != nil {
			return fmt.Errorf("zip read site_checkin_logs: %w", err)
		}
		if len(batch) == 0 {
			return nil
		}
		for _, entry := range batch {
			if err := enc.Encode(entry); err != nil {
				return err
			}
		}
		lastID = batch[len(batch)-1].ID
	}
}

func writeZipJSON(zw *zip.Writer, name string, value any) error {
	f, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("zip create %s: %w", name, err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "")
	if err := enc.Encode(value); err != nil {
		return fmt.Errorf("zip encode %s: %w", name, err)
	}
	return nil
}

func writeZipTable[T any](ctx context.Context, zw *zip.Writer, conn *gorm.DB, name string, dest *[]T) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if err := conn.Find(dest).Error; err != nil {
		return fmt.Errorf("zip read %s: %w", name, err)
	}
	return writeZipJSON(zw, name, dest)
}

func writeZipRelayLogsNDJSON(ctx context.Context, zw *zip.Writer, conn *gorm.DB) error {
	f, err := zw.Create("relay_logs.ndjson")
	if err != nil {
		return fmt.Errorf("zip create relay_logs.ndjson: %w", err)
	}
	enc := json.NewEncoder(f)
	var lastID int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var batch []model.RelayLog
		if err := conn.Where("id > ?", lastID).Order("id ASC").Limit(dbExportLogBatchSize).Find(&batch).Error; err != nil {
			return fmt.Errorf("zip read relay_logs: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		for i := range batch {
			if err := enc.Encode(&batch[i]); err != nil {
				return fmt.Errorf("zip encode relay_log: %w", err)
			}
		}
		lastID = batch[len(batch)-1].ID
		if len(batch) < dbExportLogBatchSize {
			break
		}
	}
	return nil
}

func writeZipRelayContents[T any](zw *zip.Writer, conn *gorm.DB, name string) error {
	f, err := zw.Create(name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	var lastID int64
	for {
		var ids []int64
		if err := conn.Model(&model.RelayLog{}).Where("id > ?", lastID).Order("id ASC").Limit(20).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		var batch []T
		if err := conn.Where("log_id IN ?", ids).Find(&batch).Error; err != nil {
			return err
		}
		for _, row := range batch {
			if err := enc.Encode(row); err != nil {
				return err
			}
		}
		lastID = ids[len(ids)-1]
	}
}
