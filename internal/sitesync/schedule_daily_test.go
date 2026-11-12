package sitesync

import (
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestDailyCheckinUsesSiteCalendar(t *testing.T) {
	for _, tc := range []struct {
		name, timezone, lastSuccess, want string
	}{
		{"late success", "Asia/Shanghai", "2026-11-12T23:30:00+08:00", "2026-11-13T08:00:00+08:00"},
		{"month boundary", "Asia/Shanghai", "2026-11-30T23:30:00+08:00", "2026-12-01T08:00:00+08:00"},
		{"daylight saving", "America/New_York", "2026-03-07T23:30:00-05:00", "2026-03-08T08:00:00-04:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lastSuccess, err := time.Parse(time.RFC3339, tc.lastSuccess)
			if err != nil {
				t.Fatal(err)
			}
			want, err := time.Parse(time.RFC3339, tc.want)
			if err != nil {
				t.Fatal(err)
			}
			site := checkinTestSite()
			site.CheckinTimezone = tc.timezone
			account := checkinTestAccount()
			account.RandomCheckin = false
			account.LastCheckinSuccessAt = &lastSuccess
			next := buildNextAutoCheckinAt(site, account, lastSuccess.Add(time.Minute).UTC())
			if next == nil || !next.Equal(want) {
				t.Fatalf("expected next local day's window at %s, got %v", want, next)
			}
		})
	}
}

func TestEnsureDailyCheckinSchedule(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, location)
	yesterday := now.AddDate(0, 0, -1)
	today := now.Add(-time.Hour)
	tomorrowStart := time.Date(2026, 11, 13, 8, 0, 0, 0, location)
	randomNext := tomorrowStart.Add(77 * time.Minute)
	legacyNext := tomorrowStart.AddDate(0, 0, 2)
	skippedRetry := now.AddDate(0, 0, 7)
	beforeWindow := time.Date(2026, 11, 12, 7, 0, 0, 0, location)
	afterWindow := time.Date(2026, 11, 12, 23, 0, 0, 0, location)
	windowStart := time.Date(2026, 11, 12, 8, 0, 0, 0, location)
	pendingNext := now.Add(13 * time.Hour)
	for _, tc := range []struct {
		name         string
		now          time.Time
		lastSuccess  *time.Time
		next, want   time.Time
		status       model.SiteExecutionStatus
		random       bool
		pending      bool
		failureCount int
	}{
		{"old multi-day schedule due today", now, &yesterday, legacyNext, now, model.SiteExecutionStatusSuccess, false, false, 0},
		{"old multi-day schedule after today's success", now, &today, legacyNext, tomorrowStart, model.SiteExecutionStatusSuccess, false, false, 0},
		{"old multi-day schedule before window", beforeWindow, &yesterday, legacyNext, windowStart, model.SiteExecutionStatusSuccess, false, false, 0},
		{"old multi-day schedule after window", afterWindow, &yesterday, legacyNext, tomorrowStart, model.SiteExecutionStatusSuccess, false, false, 0},
		{"success today invalidates earlier schedule", now, &today, yesterday, tomorrowStart, model.SiteExecutionStatusSuccess, false, false, 0},
		{"same-day retry after success", now, &today, now.Add(time.Hour), tomorrowStart, model.SiteExecutionStatusFailed, false, false, 1},
		{"persisted random delay", now, &today, randomNext, randomNext, model.SiteExecutionStatusSuccess, true, false, 0},
		{"overdue daily schedule", now, &yesterday, windowStart, windowStart, model.SiteExecutionStatusSuccess, false, false, 0},
		{"failure retry", now, &yesterday, tomorrowStart, tomorrowStart, model.SiteExecutionStatusFailed, false, false, 1},
		{"skipped retry", now, &yesterday, skippedRetry, skippedRetry, model.SiteExecutionStatusSkipped, false, false, 1},
		{"pending confirmation outside window", now, &yesterday, pendingNext, pendingNext, model.SiteExecutionStatusPending, false, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			site, account := createCheckinFixture(t, ctx, "https://checkin.example")
			site.CheckinWindowStart, site.CheckinWindowEnd = "08:00", "18:00"
			account.LastCheckinSuccessAt = tc.lastSuccess
			account.LastCheckinStatus = tc.status
			account.CheckinFailureCount = tc.failureCount
			account.NextAutoCheckinAt = &tc.next
			account.RandomCheckin = tc.random
			account.CheckinRandomWindowMinutes = 120
			if tc.pending {
				site.CheckinStatus = &model.SiteCheckinStatusConfig{}
				account.CheckinPending = &model.SiteCheckinPending{
					Date: now.Format("2006-01-02"), Submitted: true, Status: model.SiteExecutionStatusPending,
				}
			}
			if err := db.GetDB().Model(account).Select("last_checkin_success_at", "last_checkin_status", "checkin_failure_count", "next_auto_checkin_at", "random_checkin", "checkin_random_window_minutes", "checkin_pending").Updates(account).Error; err != nil {
				t.Fatal(err)
			}
			// Reload between scans to verify that corrected schedules are persisted
			// and that a valid random delay or retry is reused on subsequent scans.
			for scan := 0; scan < 2; scan++ {
				account, err = op.SiteAccountGet(account.ID, ctx)
				if err != nil {
					t.Fatal(err)
				}
				next, err := ensureAccountCheckinSchedule(ctx, site, account, tc.now)
				if err != nil || next == nil || !next.Equal(tc.want) {
					t.Fatalf("scan %d: expected %s, got %v, error=%v", scan, tc.want, next, err)
				}
				var saved model.SiteAccount
				if err := db.GetDB().First(&saved, account.ID).Error; err != nil {
					t.Fatal(err)
				}
				if saved.NextAutoCheckinAt == nil || !saved.NextAutoCheckinAt.Equal(tc.want) || saved.CheckinFailureCount != tc.failureCount {
					t.Fatalf("unexpected persisted schedule: next=%v, failures=%d", saved.NextAutoCheckinAt, saved.CheckinFailureCount)
				}
			}
		})
	}
}
