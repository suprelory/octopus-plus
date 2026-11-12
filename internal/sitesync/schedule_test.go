package sitesync

import (
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func checkinTestSite() *model.Site {
	return &model.Site{
		Kind:               model.SiteKindCheckin,
		Platform:           model.SitePlatformOneAPI,
		Enabled:            true,
		CheckinTimezone:    "Asia/Shanghai",
		CheckinWindowStart: "08:00",
		CheckinWindowEnd:   "23:59",
	}
}

func checkinTestAccount() *model.SiteAccount {
	return &model.SiteAccount{
		Enabled:                    true,
		AutoCheckin:                true,
		RandomCheckin:              true,
		CheckinRandomWindowMinutes: 120,
	}
}

func TestBuildNextAutoCheckinAtUsesNextLocalDayAfterSuccess(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	lastSuccess := time.Date(2026, 3, 24, 15, 0, 0, 0, location)
	now := time.Date(2026, 3, 24, 16, 0, 0, 0, location)
	account := checkinTestAccount()
	account.LastCheckinSuccessAt = &lastSuccess

	next := buildNextAutoCheckinAt(checkinTestSite(), account, now)
	if next == nil {
		t.Fatalf("expected next checkin time")
	}

	earliest := time.Date(2026, 3, 25, 8, 0, 0, 0, location)
	latest := earliest.Add(120 * time.Minute)
	if next.Before(earliest) || next.After(latest) {
		t.Fatalf("expected next checkin between %s and %s, got %s", earliest, latest, next)
	}
}

func TestBuildNextAutoCheckinAtSpreadsOverdueAccountsFromNow(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	lastSuccess := time.Date(2026, 3, 20, 10, 0, 0, 0, location)
	now := time.Date(2026, 3, 24, 16, 0, 0, 0, location)
	account := checkinTestAccount()
	account.CheckinRandomWindowMinutes = 60
	account.LastCheckinSuccessAt = &lastSuccess

	next := buildNextAutoCheckinAt(checkinTestSite(), account, now)
	if next == nil {
		t.Fatalf("expected next checkin time")
	}

	latest := now.Add(60 * time.Minute)
	if next.Before(now) || next.After(latest) {
		t.Fatalf("expected overdue account to be rescheduled between %s and %s, got %s", now, latest, next)
	}
}

func TestBuildNextAutoCheckinAtSchedulesNonRandomAccountAtWindowStart(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 24, 7, 30, 0, 0, location)
	account := checkinTestAccount()
	account.RandomCheckin = false

	next := buildNextAutoCheckinAt(checkinTestSite(), account, now)
	want := time.Date(2026, 3, 24, 8, 0, 0, 0, location)
	if next == nil || !next.Equal(want) {
		t.Fatalf("expected %s, got %v", want, next)
	}
}

func TestBuildNextAutoCheckinAtMovesPastWindowToTomorrow(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 24, 23, 30, 0, 0, location)
	account := checkinTestAccount()
	account.RandomCheckin = false
	siteRecord := checkinTestSite()
	siteRecord.CheckinWindowEnd = "23:00"

	next := buildNextAutoCheckinAt(siteRecord, account, now)
	want := time.Date(2026, 3, 25, 8, 0, 0, 0, location)
	if next == nil || !next.Equal(want) {
		t.Fatalf("expected %s, got %v", want, next)
	}
}

func TestBuildNextCheckinRetryAtUsesExponentialBackoff(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 24, 9, 0, 0, 0, location)

	next := buildNextCheckinRetryAt(checkinTestSite(), now, model.SiteExecutionStatusFailed, "temporary failure", 3)
	earliest := now.Add(time.Hour)
	latest := earliest.Add(checkinRetryJitterMinutes * time.Minute)
	if next == nil || next.Before(earliest) || next.After(latest) {
		t.Fatalf("expected retry between %s and %s, got %v", earliest, latest, next)
	}
}

func TestBuildNextCheckinRetryAtDoesNotCrossWindowEnd(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 24, 23, 50, 0, 0, location)

	next := buildNextCheckinRetryAt(checkinTestSite(), now, model.SiteExecutionStatusFailed, "temporary failure", 1)
	earliest := time.Date(2026, 3, 25, 8, 0, 0, 0, location)
	latest := earliest.Add(checkinRetryJitterMinutes * time.Minute)
	if next == nil || next.Before(earliest) || next.After(latest) {
		t.Fatalf("expected next-day retry between %s and %s, got %v", earliest, latest, next)
	}
}

func TestBuildNextAutoCheckinAtReturnsNilWhenDisabled(t *testing.T) {
	account := checkinTestAccount()
	account.AutoCheckin = false
	if next := buildNextAutoCheckinAt(checkinTestSite(), account, time.Now()); next != nil {
		t.Fatalf("expected nil next checkin time when automatic checkin is disabled")
	}
}
