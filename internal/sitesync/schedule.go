package sitesync

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin/schedule"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

const checkinRetryJitterMinutes = schedule.RetryJitterMinutes

type siteCheckinWindow struct {
	location *time.Location
	schedule.Window
}

func resolveSiteCheckinWindow(siteRecord *model.Site) siteCheckinWindow {
	timezone := model.DefaultSiteCheckinTimezone
	windowStart := model.DefaultSiteCheckinWindowStart
	windowEnd := model.DefaultSiteCheckinWindowEnd
	if siteRecord != nil {
		if value := strings.TrimSpace(siteRecord.CheckinTimezone); value != "" {
			timezone = value
		}
		if value := strings.TrimSpace(siteRecord.CheckinWindowStart); value != "" {
			windowStart = value
		}
		if value := strings.TrimSpace(siteRecord.CheckinWindowEnd); value != "" {
			windowEnd = value
		}
	}

	location, err := time.LoadLocation(timezone)
	if err != nil {
		location, _ = time.LoadLocation(model.DefaultSiteCheckinTimezone)
	}
	return siteCheckinWindow{location: location, Window: schedule.ParseWindow(windowStart, windowEnd)}
}

func checkinWindowBounds(window siteCheckinWindow, localDate time.Time) (time.Time, time.Time) {
	return window.Bounds(localDate.In(window.location))
}

func localDateStart(value time.Time, location *time.Location) time.Time {
	local := value.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
}

func sameLocalDate(left, right time.Time, location *time.Location) bool {
	leftLocal := left.In(location)
	rightLocal := right.In(location)
	return leftLocal.Year() == rightLocal.Year() && leftLocal.YearDay() == rightLocal.YearDay()
}

func effectiveLastCheckinSuccessAt(account *model.SiteAccount) *time.Time {
	if account == nil {
		return nil
	}
	if account.LastCheckinSuccessAt != nil && !account.LastCheckinSuccessAt.IsZero() {
		return account.LastCheckinSuccessAt
	}
	return nil
}

// nextDailyCheckinWindow returns the earliest time and the exclusive end of
// the next eligible local day's window. Random delays stay within this date.
func nextDailyCheckinWindow(window siteCheckinWindow, account *model.SiteAccount, now time.Time) (time.Time, time.Time) {
	return window.Next(now.In(window.location), effectiveLastCheckinSuccessAt(account))
}

func buildNextAutoCheckinAt(siteRecord *model.Site, account *model.SiteAccount, now time.Time) *time.Time {
	if siteRecord == nil || !siteRecord.IsCheckinOnly() || account == nil || !siteRecord.Enabled || !account.Enabled || !account.AutoCheckin || !siteRecord.ResolveCheckinCapability().Enabled {
		return nil
	}

	window := resolveSiteCheckinWindow(siteRecord)
	localNow := now.In(window.location)
	if pending := account.CheckinPending; pending != nil {
		if !pending.Finished {
			next := now.Add(10 * time.Minute)
			return &next
		}
		if pending.Date == localNow.Format("2006-01-02") && pending.Status != model.SiteExecutionStatusSuccess {
			next, _ := checkinWindowBounds(window, localDateStart(localNow, window.location).AddDate(0, 0, 1))
			return &next
		}
	}
	base, windowEndExclusive := nextDailyCheckinWindow(window, account, now)

	if account.RandomCheckin {
		base = schedule.Spread(base, windowEndExclusive, account.CheckinRandomWindowMinutes)
	}

	next := base
	return &next
}

func checkinTimeWithinWindow(siteRecord *model.Site, value time.Time) bool {
	window := resolveSiteCheckinWindow(siteRecord)
	local := value.In(window.location)
	start, endExclusive := checkinWindowBounds(window, local)
	return !local.Before(start) && local.Before(endExclusive)
}

func ensureAccountCheckinSchedule(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, now time.Time) (*time.Time, error) {
	if siteRecord == nil || !siteRecord.IsCheckinOnly() || account == nil || !siteRecord.Enabled || !account.Enabled || !account.AutoCheckin || !siteRecord.ResolveCheckinCapability().Enabled {
		if account != nil && account.NextAutoCheckinAt != nil {
			if err := persistNextAutoCheckinAt(ctx, account.ID, nil); err != nil {
				return nil, err
			}
			account.NextAutoCheckinAt = nil
		}
		return nil, nil
	}
	if siteRecord.CheckinStatus != nil && account.CheckinPending != nil && !account.CheckinPending.Finished {
		if account.NextAutoCheckinAt != nil {
			return account.NextAutoCheckinAt, nil
		}
		next := now
		if err := persistNextAutoCheckinAt(ctx, account.ID, &next); err != nil {
			return nil, err
		}
		return &next, nil
	}

	if nextAt := account.NextAutoCheckinAt; nextAt != nil && !nextAt.IsZero() && checkinTimeWithinWindow(siteRecord, *nextAt) {
		window := resolveSiteCheckinWindow(siteRecord)
		lastSuccess := effectiveLastCheckinSuccessAt(account)
		beforeNextDayAfterSuccess := lastSuccess != nil && nextAt.Before(localDateStart(*lastSuccess, window.location).AddDate(0, 0, 1))
		// Reconcile schedules created by the former multi-day interval, including
		// restored backups. Keep persisted random delays and failure backoff.
		beyondDailyWindow := false
		if account.LastCheckinStatus != model.SiteExecutionStatusFailed && account.LastCheckinStatus != model.SiteExecutionStatusSkipped {
			_, windowEnd := nextDailyCheckinWindow(window, account, now)
			beyondDailyWindow = !nextAt.Before(windowEnd)
		}
		if !beforeNextDayAfterSuccess && !beyondDailyWindow && (now.Before(*nextAt) || checkinTimeWithinWindow(siteRecord, now)) {
			return nextAt, nil
		}
	}

	nextAt := buildNextAutoCheckinAt(siteRecord, account, now)
	if err := persistNextAutoCheckinAt(ctx, account.ID, nextAt); err != nil {
		return nil, err
	}
	account.NextAutoCheckinAt = nextAt
	return nextAt, nil
}

func alignCheckinRetryToWindow(siteRecord *model.Site, target time.Time) time.Time {
	window := resolveSiteCheckinWindow(siteRecord)
	start, end := window.Next(target.In(window.location), nil)
	return schedule.Spread(start, end, checkinRetryJitterMinutes)
}

func buildNextCheckinRetryAt(siteRecord *model.Site, now time.Time, status model.SiteExecutionStatus, message string, failureCount int) *time.Time {
	if siteRecord == nil || !siteRecord.IsCheckinOnly() || !siteRecord.ResolveCheckinCapability().Enabled {
		return nil
	}
	target := now.Add(schedule.RetryDelay(status, message, failureCount))
	next := alignCheckinRetryToWindow(siteRecord, target)
	return &next
}

func persistNextAutoCheckinAt(ctx context.Context, accountID int, nextAt *time.Time) error {
	return db.GetDB().WithContext(ctx).
		Model(&model.SiteAccount{}).
		Where("id = ?", accountID).
		Update("next_auto_checkin_at", nextAt).Error
}

func RefreshAccountCheckinSchedule(ctx context.Context, accountID int) error {
	siteRecord, account, err := loadSiteAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("site account not found")
	}

	nextAt := buildNextAutoCheckinAt(siteRecord, account, time.Now())
	if err := persistNextAutoCheckinAt(ctx, account.ID, nextAt); err != nil {
		return err
	}
	return nil
}

func RefreshSiteCheckinSchedules(ctx context.Context, siteID int) error {
	siteRecord, err := op.SiteGet(siteID, ctx)
	if err != nil {
		return fmt.Errorf("site not found")
	}
	now := time.Now()
	for index := range siteRecord.Accounts {
		account := &siteRecord.Accounts[index]
		nextAt := buildNextAutoCheckinAt(siteRecord, account, now)
		if err := persistNextAutoCheckinAt(ctx, account.ID, nextAt); err != nil {
			return err
		}
	}
	return nil
}
