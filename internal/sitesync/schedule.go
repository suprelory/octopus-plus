package sitesync

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

const (
	checkinRetryBaseDelay       = 15 * time.Minute
	checkinRetryMaxDelay        = 2 * time.Hour
	checkinCredentialRetryDelay = 6 * time.Hour
	checkinSkippedRetryDelay    = 7 * 24 * time.Hour
	checkinRetryJitterMinutes   = 5
)

type siteCheckinWindow struct {
	location    *time.Location
	startMinute int
	endMinute   int
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
	return siteCheckinWindow{
		location:    location,
		startMinute: parseCheckinClockMinute(windowStart, 0),
		endMinute:   parseCheckinClockMinute(windowEnd, 23*60+59),
	}
}

func parseCheckinClockMinute(value string, fallback int) int {
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return fallback
	}
	return parsed.Hour()*60 + parsed.Minute()
}

func checkinWindowBounds(window siteCheckinWindow, localDate time.Time) (time.Time, time.Time) {
	startHour, startMinute := window.startMinute/60, window.startMinute%60
	endHour, endMinute := window.endMinute/60, window.endMinute%60
	start := time.Date(localDate.Year(), localDate.Month(), localDate.Day(), startHour, startMinute, 0, 0, window.location)
	endExclusive := time.Date(localDate.Year(), localDate.Month(), localDate.Day(), endHour, endMinute, 0, 0, window.location).Add(time.Minute)
	return start, endExclusive
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

func checkinIntervalDays(account *model.SiteAccount) int {
	intervalHours := 24
	if account != nil && account.CheckinIntervalHours > 0 {
		intervalHours = account.CheckinIntervalHours
	}
	days := (intervalHours + 23) / 24
	if days < 1 {
		return 1
	}
	return days
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
	targetDate := localDateStart(localNow, window.location)
	if lastSuccess := effectiveLastCheckinSuccessAt(account); lastSuccess != nil {
		targetDate = localDateStart(*lastSuccess, window.location).AddDate(0, 0, checkinIntervalDays(account))
		if targetDate.Before(localDateStart(localNow, window.location)) {
			targetDate = localDateStart(localNow, window.location)
		}
	}

	windowStart, windowEndExclusive := checkinWindowBounds(window, targetDate)
	base := windowStart
	if sameLocalDate(targetDate, localNow, window.location) {
		switch {
		case !localNow.Before(windowEndExclusive):
			targetDate = targetDate.AddDate(0, 0, 1)
			windowStart, windowEndExclusive = checkinWindowBounds(window, targetDate)
			base = windowStart
		case localNow.After(windowStart):
			base = localNow
		}
	}

	if account.RandomCheckin && account.CheckinRandomWindowMinutes > 0 {
		latest := windowEndExclusive.Add(-time.Minute)
		availableMinutes := int(latest.Sub(base) / time.Minute)
		if availableMinutes > 0 {
			maxDelay := account.CheckinRandomWindowMinutes
			if maxDelay > availableMinutes {
				maxDelay = availableMinutes
			}
			base = base.Add(time.Duration(rand.Intn(maxDelay+1)) * time.Minute)
		}
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
		sameDayAsSuccess := lastSuccess != nil && sameLocalDate(*lastSuccess, *nextAt, window.location)
		if !sameDayAsSuccess && (now.Before(*nextAt) || checkinTimeWithinWindow(siteRecord, now)) {
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

func checkinRetryDelay(status model.SiteExecutionStatus, message string, failureCount int) time.Duration {
	if status == model.SiteExecutionStatusSkipped {
		return checkinSkippedRetryDelay
	}
	lowered := strings.ToLower(strings.TrimSpace(message))
	credentialFailure := strings.Contains(lowered, "unauthorized") ||
		strings.Contains(lowered, "forbidden") ||
		strings.Contains(lowered, "invalid token") ||
		strings.Contains(lowered, "token expired") ||
		strings.Contains(lowered, "http 401") ||
		strings.Contains(lowered, "http 403") ||
		strings.Contains(message, "登录失效") ||
		strings.Contains(message, "凭证失效")
	if credentialFailure {
		return checkinCredentialRetryDelay
	}
	if failureCount < 1 {
		failureCount = 1
	}
	delay := checkinRetryBaseDelay
	for attempt := 1; attempt < failureCount && delay < checkinRetryMaxDelay; attempt++ {
		delay *= 2
	}
	if delay > checkinRetryMaxDelay {
		return checkinRetryMaxDelay
	}
	return delay
}

func alignCheckinRetryToWindow(siteRecord *model.Site, target time.Time) time.Time {
	window := resolveSiteCheckinWindow(siteRecord)
	localTarget := target.In(window.location)
	start, endExclusive := checkinWindowBounds(window, localTarget)
	switch {
	case localTarget.Before(start):
		localTarget = start
	case !localTarget.Before(endExclusive):
		localTarget = localDateStart(localTarget, window.location).AddDate(0, 0, 1)
		localTarget, _ = checkinWindowBounds(window, localTarget)
	}

	latest := func() time.Time {
		_, currentEndExclusive := checkinWindowBounds(window, localTarget)
		return currentEndExclusive.Add(-time.Minute)
	}()
	if latest.After(localTarget) {
		availableMinutes := int(latest.Sub(localTarget) / time.Minute)
		maxJitter := checkinRetryJitterMinutes
		if maxJitter > availableMinutes {
			maxJitter = availableMinutes
		}
		if maxJitter > 0 {
			localTarget = localTarget.Add(time.Duration(rand.Intn(maxJitter+1)) * time.Minute)
		}
	}
	return localTarget
}

func buildNextCheckinRetryAt(siteRecord *model.Site, now time.Time, status model.SiteExecutionStatus, message string, failureCount int) *time.Time {
	if siteRecord == nil || !siteRecord.IsCheckinOnly() || !siteRecord.ResolveCheckinCapability().Enabled {
		return nil
	}
	target := now.Add(checkinRetryDelay(status, message, failureCount))
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
