package op

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

type SiteCheckinLogFilter struct {
	SiteID     int
	AccountID  int
	Status     model.SiteExecutionStatus
	Source     string
	BatchJobID int64
	BeforeID   int64
	Limit      int
	From       *time.Time
	Until      *time.Time
	Location   *time.Location
}

func SiteCheckinLogList(ctx context.Context, filter SiteCheckinLogFilter) (*model.SiteCheckinLogPage, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	query := db.GetDB().WithContext(ctx).Model(&model.SiteCheckinLog{})
	if filter.SiteID > 0 {
		query = query.Where("site_id = ?", filter.SiteID)
	}
	if filter.AccountID > 0 {
		query = query.Where("account_id = ?", filter.AccountID)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.Source != "" {
		query = query.Where("source = ?", filter.Source)
	}
	if filter.BatchJobID > 0 {
		query = query.Where("batch_job_id = ?", filter.BatchJobID)
	}
	if filter.BeforeID > 0 {
		query = query.Where("id < ?", filter.BeforeID)
	}
	if filter.From != nil {
		query = query.Where("finished_at >= ?", filter.From.UTC())
	}
	if filter.Until != nil {
		query = query.Where("finished_at < ?", filter.Until.UTC())
	}
	page := &model.SiteCheckinLogPage{Items: make([]model.SiteCheckinLog, 0, limit+1)}
	if err := query.Order("id DESC").Limit(limit + 1).Find(&page.Items).Error; err != nil {
		return nil, fmt.Errorf("list site checkin logs: %w", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextBeforeID = page.Items[limit-1].ID
	}
	return page, nil
}

const siteCheckinStatsBatchSize = 1000

var checkinRewardNumberRE = regexp.MustCompile(`^\+?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$`)

// SiteCheckinStats aggregates check-in outcomes and rewards from the durable
// execution log. The query is paged by snowflake ID so large histories do not
// require one unbounded result set.
func SiteCheckinStats(ctx context.Context, filter SiteCheckinLogFilter) (*model.SiteCheckinStats, error) {
	return siteCheckinStatsAt(ctx, filter, time.Now())
}

func siteCheckinStatsAt(ctx context.Context, filter SiteCheckinLogFilter, now time.Time) (*model.SiteCheckinStats, error) {
	location := filter.Location
	if location == nil {
		location = time.Local
	}
	now = now.In(location)
	stats := &model.SiteCheckinStats{BySite: make([]model.SiteCheckinSiteStats, 0), Timezone: location.String()}
	bySite := make(map[int]*model.SiteCheckinSiteStats)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	recent7Start := todayStart.AddDate(0, 0, -6)
	recent30Start := todayStart.AddDate(0, 0, -29)

	query := db.GetDB().WithContext(ctx).Model(&model.SiteCheckinLog{}).Where("finished_at <= ?", now.UTC())
	if filter.SiteID > 0 {
		query = query.Where("site_id = ?", filter.SiteID)
	}
	if filter.AccountID > 0 {
		query = query.Where("account_id = ?", filter.AccountID)
	}
	if filter.From != nil {
		query = query.Where("finished_at >= ?", filter.From.UTC())
	}
	if filter.Until != nil {
		query = query.Where("finished_at < ?", filter.Until.UTC())
	}

	// Freeze the upper ID so concurrent check-ins cannot extend this scan.
	var maxID int64
	if err := query.Session(&gorm.Session{}).Select("COALESCE(MAX(id), 0)").Scan(&maxID).Error; err != nil {
		return nil, fmt.Errorf("aggregate site checkin stats: %w", err)
	}
	var lastID int64
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		var entries []model.SiteCheckinLog
		if err := query.Session(&gorm.Session{}).
			Select("id", "site_id", "site_name", "status", "reason", "reward", "finished_at").
			Where("id > ? AND id <= ?", lastID, maxID).Order("id ASC").Limit(siteCheckinStatsBatchSize).Find(&entries).Error; err != nil {
			return nil, fmt.Errorf("aggregate site checkin stats: %w", err)
		}
		if len(entries) == 0 {
			break
		}
		for _, entry := range entries {
			stats.TotalCount++
			switch entry.Status {
			case model.SiteExecutionStatusSuccess:
				stats.SuccessCount++
			case model.SiteExecutionStatusFailed:
				stats.FailedCount++
			case model.SiteExecutionStatusSkipped:
				stats.SkippedCount++
			}
			siteStats := bySite[entry.SiteID]
			if siteStats == nil {
				siteStats = &model.SiteCheckinSiteStats{SiteID: entry.SiteID, SiteName: entry.SiteName}
				bySite[entry.SiteID] = siteStats
			}
			siteStats.SiteName = entry.SiteName
			siteStats.TotalCount++
			switch entry.Status {
			case model.SiteExecutionStatusSuccess:
				siteStats.SuccessCount++
			case model.SiteExecutionStatusFailed:
				siteStats.FailedCount++
			case model.SiteExecutionStatusSkipped:
				siteStats.SkippedCount++
			}

			if entry.Status != model.SiteExecutionStatusSuccess || entry.Reason == model.SiteCheckinReasonAlreadyCheckedIn {
				continue
			}
			if strings.TrimSpace(entry.Reward) == "" {
				stats.UnknownRewardCount++
				continue
			}
			reward, ok := parseCheckinReward(entry.Reward)
			if !ok || math.IsInf(stats.TotalReward+reward, 0) {
				stats.InvalidRewardCount++
				continue
			}
			stats.TotalReward += reward
			siteStats.Reward += reward
			finishedAt := entry.FinishedAt
			if finishedAt.IsZero() {
				continue
			}
			if !finishedAt.Before(todayStart) {
				stats.TodayReward += reward
			}
			if !finishedAt.Before(recent7Start) {
				stats.Recent7DaysReward += reward
			}
			if !finishedAt.Before(recent30Start) {
				stats.Recent30DaysReward += reward
			}
		}
		lastID = entries[len(entries)-1].ID
		if len(entries) < siteCheckinStatsBatchSize {
			break
		}
	}

	// Log names are historical snapshots. Use the current name for existing
	// sites (including archived sites), retaining snapshots for deleted sites.
	siteIDs := make([]int, 0, len(bySite))
	for siteID := range bySite {
		siteIDs = append(siteIDs, siteID)
	}
	for start := 0; start < len(siteIDs); start += siteCheckinStatsBatchSize {
		var sites []model.Site
		end := min(start+siteCheckinStatsBatchSize, len(siteIDs))
		if err := db.GetDB().WithContext(ctx).Select("id", "name").
			Where("id IN ?", siteIDs[start:end]).Find(&sites).Error; err != nil {
			return nil, fmt.Errorf("load site names for checkin stats: %w", err)
		}
		for _, site := range sites {
			bySite[site.ID].SiteName = site.Name
		}
	}
	for _, item := range bySite {
		item.Reward = roundCheckinStat(item.Reward)
		stats.BySite = append(stats.BySite, *item)
	}
	sort.Slice(stats.BySite, func(i, j int) bool {
		if stats.BySite[i].Reward != stats.BySite[j].Reward {
			return stats.BySite[i].Reward > stats.BySite[j].Reward
		}
		return stats.BySite[i].SiteID < stats.BySite[j].SiteID
	})
	stats.TodayReward = roundCheckinStat(stats.TodayReward)
	stats.Recent7DaysReward = roundCheckinStat(stats.Recent7DaysReward)
	stats.Recent30DaysReward = roundCheckinStat(stats.Recent30DaysReward)
	stats.TotalReward = roundCheckinStat(stats.TotalReward)
	return stats, nil
}

func parseCheckinReward(raw string) (float64, bool) {
	value := strings.TrimSpace(raw)
	if !checkinRewardNumberRE.MatchString(value) {
		return 0, false
	}
	reward, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(reward) || math.IsInf(reward, 0) || reward < 0 {
		return 0, false
	}
	return reward, true
}

func roundCheckinStat(value float64) float64 {
	if value > math.MaxFloat64/1_000_000 {
		return value
	}
	return math.Round(value*1_000_000) / 1_000_000
}
