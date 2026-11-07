package op

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestCheckinStatsRewardsWindowsAndFilters(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// The seven-day window crosses a DST change and must use calendar days.
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, location)
	today := time.Date(2026, 11, 2, 0, 0, 0, 0, location)
	rows := []model.SiteCheckinLog{
		{ID: 1, SiteID: 1, AccountID: 11, SiteName: "Old name", Status: model.SiteExecutionStatusSuccess, Reward: "1.25", FinishedAt: today},
		{ID: 2, SiteID: 1, AccountID: 11, SiteName: "Current name", Status: model.SiteExecutionStatusSuccess, Reward: "2", FinishedAt: today.AddDate(0, 0, -6)},
		{ID: 3, SiteID: 2, AccountID: 22, SiteName: "Second site", Status: model.SiteExecutionStatusSuccess, Reward: "4", FinishedAt: today.AddDate(0, 0, -7)},
		{ID: 4, SiteID: 2, AccountID: 22, SiteName: "Second site", Status: model.SiteExecutionStatusSuccess, Reward: "8", FinishedAt: today.AddDate(0, 0, -29)},
		{ID: 5, SiteID: 2, AccountID: 22, SiteName: "Second site", Status: model.SiteExecutionStatusSuccess, Reward: "16", FinishedAt: today.AddDate(0, 0, -30)},
		{ID: 6, SiteID: 1, AccountID: 11, SiteName: "Current name", Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonAlreadyCheckedIn, Reward: "900", FinishedAt: today},
		{ID: 7, SiteID: 1, AccountID: 11, SiteName: "Current name", Status: model.SiteExecutionStatusFailed, Reward: "900", FinishedAt: today},
		{ID: 8, SiteID: 1, AccountID: 12, SiteName: "Current name", Status: model.SiteExecutionStatusSkipped, Reward: "900", FinishedAt: today},
		{ID: 9, SiteID: 1, AccountID: 12, SiteName: "Current name", Status: model.SiteExecutionStatusSuccess, Reward: "1 USD", FinishedAt: today},
		{ID: 10, SiteID: 1, AccountID: 12, SiteName: "Current name", Status: model.SiteExecutionStatusSuccess, Reward: "", FinishedAt: today},
		{ID: 11, SiteID: 1, AccountID: 12, SiteName: "Current name", Status: model.SiteExecutionStatusSuccess, Reward: "0", FinishedAt: today},
		{ID: 12, SiteID: 1, AccountID: 12, Status: model.SiteExecutionStatusSuccess, Reward: "900", FinishedAt: now.Add(time.Hour)},
	}
	// Production check-in outcomes persist UTC timestamps on every dialect.
	for i := range rows {
		rows[i].FinishedAt = rows[i].FinishedAt.UTC()
	}
	if err := db.GetDB().Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	stats, err := siteCheckinStatsAt(ctx, SiteCheckinLogFilter{Location: location}, now)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TodayReward != 1.25 || stats.Recent7DaysReward != 3.25 || stats.Recent30DaysReward != 15.25 || stats.TotalReward != 31.25 {
		t.Fatalf("incorrect reward windows: %+v", stats)
	}
	if stats.TotalCount != 11 || stats.SuccessCount != 9 || stats.FailedCount != 1 || stats.SkippedCount != 1 || stats.InvalidRewardCount != 1 || stats.UnknownRewardCount != 1 {
		t.Fatalf("incorrect outcomes or unknown rewards: %+v", stats)
	}
	if stats.Timezone != location.String() || len(stats.BySite) != 2 || stats.BySite[0].SiteID != 2 || stats.BySite[0].Reward != 28 || stats.BySite[1].SiteName != "Current name" {
		t.Fatalf("incorrect breakdown: %+v", stats)
	}
	from, until := today.AddDate(0, 0, -6), today
	filtered, err := siteCheckinStatsAt(ctx, SiteCheckinLogFilter{SiteID: 1, AccountID: 11, From: &from, Until: &until, Location: location}, now)
	if err != nil || filtered.TotalCount != 1 || filtered.TotalReward != 2 || filtered.TodayReward != 0 {
		t.Fatalf("filters or exclusive upper bound ignored: %+v, %v", filtered, err)
	}
	empty, err := siteCheckinStatsAt(ctx, SiteCheckinLogFilter{AccountID: 999}, now)
	if err != nil || empty.TotalCount != 0 || empty.BySite == nil || len(empty.BySite) != 0 {
		t.Fatalf("empty stats should have zero counts and a non-nil breakdown: %+v, %v", empty, err)
	}
}

func TestCheckinStatsUsesCurrentSiteNamesAndPreservesDeletedSiteHistory(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	now := time.Now()
	sites := []model.Site{
		{ID: 1, Kind: model.SiteKindCheckin, Name: "方舟", Platform: model.SitePlatformOneAPI, BaseURL: "https://current.example"},
		{ID: 2, Kind: model.SiteKindCheckin, Name: "已归档的新名称", Platform: model.SitePlatformOneAPI, BaseURL: "https://archived.example", Archived: true},
		{ID: 3, Kind: model.SiteKindCheckin, Name: "自定义 · 签到", Platform: model.SitePlatformOneAPI, BaseURL: "https://custom.example"},
		{ID: 4, Kind: model.SiteKindCheckin, Name: "没有签到记录", Platform: model.SitePlatformOneAPI, BaseURL: "https://empty.example"},
	}
	if err := db.GetDB().Create(&sites).Error; err != nil {
		t.Fatal(err)
	}
	rows := []model.SiteCheckinLog{
		{ID: 1, SiteID: 1, SiteName: "方舟 · 签到", Status: model.SiteExecutionStatusSuccess, Reward: "1", FinishedAt: now.Add(-time.Hour).UTC()},
		{ID: 2, SiteID: 2, SiteName: "归档前旧名称 · 签到", Status: model.SiteExecutionStatusSuccess, Reward: "2", FinishedAt: now.Add(-time.Hour).UTC()},
		{ID: 3, SiteID: 3, SiteName: "自定义旧名称", Status: model.SiteExecutionStatusSuccess, Reward: "3", FinishedAt: now.Add(-time.Hour).UTC()},
		{ID: 4, SiteID: 99, SiteName: "已删除站点 · 签到", Status: model.SiteExecutionStatusSuccess, Reward: "4", FinishedAt: now.Add(-time.Hour).UTC()},
	}
	if err := db.GetDB().Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, siteID := range []int{0, 1} {
		stats, err := siteCheckinStatsAt(ctx, SiteCheckinLogFilter{SiteID: siteID}, now)
		if err != nil {
			t.Fatal(err)
		}
		if siteID == 0 && (len(stats.BySite) != 4 || stats.TotalCount != 4 || stats.TotalReward != 10) {
			t.Fatalf("name resolution changed aggregation: %+v", stats)
		}
		if siteID == 1 && len(stats.BySite) != 1 {
			t.Fatalf("name resolution ignored the site filter: %+v", stats)
		}
		wantNames := map[int]string{1: "方舟", 2: "已归档的新名称", 3: "自定义 · 签到", 99: "已删除站点 · 签到"}
		for _, item := range stats.BySite {
			if item.SiteName != wantNames[item.SiteID] {
				t.Errorf("site %d name = %q, want %q", item.SiteID, item.SiteName, wantNames[item.SiteID])
			}
		}
	}
	var saved model.SiteCheckinLog
	if err := db.GetDB().First(&saved, rows[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.SiteName != rows[0].SiteName {
		t.Fatal("statistics rewrote the historical log name")
	}
}

func TestCheckinStatsPaginationAndCanceledQuery(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	now := time.Now()
	rows := make([]model.SiteCheckinLog, 2*siteCheckinStatsBatchSize+3)
	for i := range rows {
		rows[i] = model.SiteCheckinLog{ID: int64(i + 1), SiteID: 1, Status: model.SiteExecutionStatusSuccess, Reward: "0.1", FinishedAt: now.Add(-time.Minute).UTC()}
	}
	if err := db.GetDB().CreateInBatches(rows, 100).Error; err != nil {
		t.Fatal(err)
	}
	stats, err := siteCheckinStatsAt(ctx, SiteCheckinLogFilter{}, now)
	if err != nil || stats.TotalCount != len(rows) || stats.TotalReward != 200.3 {
		t.Fatalf("pagination skipped or repeated records: %+v, %v", stats, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := SiteCheckinStats(canceled, SiteCheckinLogFilter{}); err == nil {
		t.Fatal("canceled stats query succeeded")
	}
}

func TestCheckinRewardParsingAndJSONNumbers(t *testing.T) {
	for _, value := range []string{"", " ", "NaN", "Inf", "+Inf", "-1", "1e9999", "12 credits", "$1", "1,234", "0x1p2", "1_000"} {
		if _, ok := parseCheckinReward(value); ok {
			t.Errorf("accepted ambiguous or invalid reward %q", value)
		}
	}
	for value, want := range map[string]float64{"0": 0, " 1.25 ": 1.25, "+.5": .5, "1e-6": .000001} {
		if got, ok := parseCheckinReward(value); !ok || got != want {
			t.Errorf("reward %q = %v, %v; want %v", value, got, ok, want)
		}
	}
	if _, err := json.Marshal(roundCheckinStat(math.MaxFloat64)); err != nil {
		t.Fatalf("rounding produced an invalid JSON number: %v", err)
	}
}
