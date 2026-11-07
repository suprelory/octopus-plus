package op

import (
	"context"
	"fmt"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func DBExportAll(ctx context.Context, includeLogs, includeStats bool) (*model.DBDump, error) {
	var dump *model.DBDump
	err := withBackupSnapshot(ctx, func(conn *gorm.DB) error {
		var err error
		dump, err = exportAllSnapshot(ctx, conn, includeLogs, includeStats)
		return err
	})
	if err != nil {
		return nil, err
	}
	return dump, nil
}

func exportAllSnapshot(ctx context.Context, conn *gorm.DB, includeLogs, includeStats bool) (*model.DBDump, error) {
	d := &model.DBDump{
		Version:      dbDumpVersion,
		ExportedAt:   time.Now().UTC(),
		IncludeLogs:  includeLogs,
		IncludeStats: includeStats,
	}

	if err := conn.Find(&d.Channels).Error; err != nil {
		return nil, fmt.Errorf("export channels: %w", err)
	}
	if err := conn.Find(&d.ChannelKeys).Error; err != nil {
		return nil, fmt.Errorf("export channel_keys: %w", err)
	}
	if err := conn.Find(&d.ProxyConfigurations).Error; err != nil {
		return nil, fmt.Errorf("export proxy_configurations: %w", err)
	}
	if err := conn.Find(&d.Sites).Error; err != nil {
		return nil, fmt.Errorf("export sites: %w", err)
	}
	if err := conn.Find(&d.SiteAccounts).Error; err != nil {
		return nil, fmt.Errorf("export site_accounts: %w", err)
	}
	if err := conn.Find(&d.SiteTokens).Error; err != nil {
		return nil, fmt.Errorf("export site_tokens: %w", err)
	}
	if err := conn.Find(&d.SiteUserGroups).Error; err != nil {
		return nil, fmt.Errorf("export site_user_groups: %w", err)
	}
	if err := conn.Find(&d.SiteModels).Error; err != nil {
		return nil, fmt.Errorf("export site_models: %w", err)
	}
	if err := conn.Find(&d.SiteChannelBindings).Error; err != nil {
		return nil, fmt.Errorf("export site_channel_bindings: %w", err)
	}
	if err := conn.Find(&d.Groups).Error; err != nil {
		return nil, fmt.Errorf("export groups: %w", err)
	}
	if err := conn.Find(&d.GroupItems).Error; err != nil {
		return nil, fmt.Errorf("export group_items: %w", err)
	}
	if err := conn.Find(&d.GroupPresets).Error; err != nil {
		return nil, fmt.Errorf("export group_presets: %w", err)
	}
	if err := conn.Find(&d.LLMInfos).Error; err != nil {
		return nil, fmt.Errorf("export llm_infos: %w", err)
	}
	if err := conn.Find(&d.APIKeys).Error; err != nil {
		return nil, fmt.Errorf("export api_keys: %w", err)
	}
	if err := conn.Find(&d.Settings).Error; err != nil {
		return nil, fmt.Errorf("export settings: %w", err)
	}

	if includeStats {
		if err := conn.Find(&d.StatsTotal).Error; err != nil {
			return nil, fmt.Errorf("export stats_total: %w", err)
		}
		if err := conn.Find(&d.StatsDaily).Error; err != nil {
			return nil, fmt.Errorf("export stats_daily: %w", err)
		}
		if err := conn.Find(&d.StatsHourly).Error; err != nil {
			return nil, fmt.Errorf("export stats_hourly: %w", err)
		}
		legacyStats, err := loadLegacyStatsModels(conn)
		if err != nil {
			return nil, fmt.Errorf("export stats_model: %w", err)
		}
		d.StatsModel = legacyStats
		if err := conn.Find(&d.StatsChannel).Error; err != nil {
			return nil, fmt.Errorf("export stats_channel: %w", err)
		}
		if err := conn.Find(&d.StatsAPIKey).Error; err != nil {
			return nil, fmt.Errorf("export stats_api_key: %w", err)
		}
		if err := conn.Find(&d.StatsSiteModelHourly).Error; err != nil {
			return nil, fmt.Errorf("export stats_site_model_hourly: %w", err)
		}
	}

	if includeLogs {
		if err := exportRelayLogsPaged(ctx, conn, d); err != nil {
			return nil, err
		}
		if err := exportSiteCheckinLogsPaged(conn, d); err != nil {
			return nil, err
		}
		if err := conn.Order("id ASC").Find(&d.SiteCheckinBatchJobs).Error; err != nil {
			return nil, fmt.Errorf("export site_checkin_batch_jobs: %w", err)
		}
	}

	return d, nil
}

func exportSiteCheckinLogsPaged(conn *gorm.DB, dump *model.DBDump) error {
	var lastID int64
	for {
		var batch []model.SiteCheckinLog
		if err := conn.Where("id > ?", lastID).Order("id ASC").Limit(dbExportLogBatchSize).Find(&batch).Error; err != nil {
			return fmt.Errorf("export site_checkin_logs: %w", err)
		}
		if len(batch) == 0 {
			return nil
		}
		dump.SiteCheckinLogs = append(dump.SiteCheckinLogs, batch...)
		lastID = batch[len(batch)-1].ID
	}
}

func exportRelayLogsPaged(ctx context.Context, conn *gorm.DB, d *model.DBDump) error {
	var lastID int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var batch []model.RelayLog
		if err := conn.Where("id > ?", lastID).Order("id ASC").Limit(dbExportLogBatchSize).Find(&batch).Error; err != nil {
			return fmt.Errorf("export relay_logs: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		d.RelayLogs = append(d.RelayLogs, batch...)
		ids := make([]int64, len(batch))
		for i := range batch {
			ids[i] = batch[i].ID
		}
		var clients []model.RelayRequestContent
		var attempts []model.RelayAttemptContent
		if err := conn.Where("log_id IN ?", ids).Find(&clients).Error; err != nil {
			return err
		}
		if err := conn.Where("log_id IN ?", ids).Find(&attempts).Error; err != nil {
			return err
		}
		d.RelayRequestContents = append(d.RelayRequestContents, clients...)
		d.RelayAttemptContents = append(d.RelayAttemptContents, attempts...)
		lastID = batch[len(batch)-1].ID
		if len(batch) < dbExportLogBatchSize {
			break
		}
	}
	return nil
}
