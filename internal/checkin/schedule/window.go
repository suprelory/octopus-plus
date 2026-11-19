// Package schedule calculates daily check-in times in the site's calendar.
package schedule

import (
	"math/rand"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

const RetryJitterMinutes = 5

type Window struct{ start, end int }

func ParseWindow(start, end string) Window {
	minute := func(clock string, fallback int) int {
		t, err := time.Parse("15:04", clock)
		if err != nil {
			return fallback
		}
		return t.Hour()*60 + t.Minute()
	}
	return Window{minute(start, 0), minute(end, 23*60+59)}
}

// Bounds includes the entire configured end minute. local carries the site timezone.
func (w Window) Bounds(local time.Time) (time.Time, time.Time) {
	at := func(minute int) time.Time {
		return time.Date(local.Year(), local.Month(), local.Day(), minute/60, minute%60, 0, 0, local.Location())
	}
	return at(w.start), at(w.end).Add(time.Minute)
}

func (w Window) Next(local time.Time, lastDay *time.Time) (time.Time, time.Time) {
	day := local
	if lastDay != nil && !lastDay.IsZero() {
		nextDay := lastDay.In(local.Location()).AddDate(0, 0, 1)
		if nextDay.Format(time.DateOnly) > local.Format(time.DateOnly) {
			day = nextDay
		}
	}
	start, end := w.Bounds(day)
	if !local.Before(end) {
		return w.Bounds(day.AddDate(0, 0, 1))
	}
	if start.Before(local) {
		start = local
	}
	return start, end
}

// Spread never pushes a selected time beyond the last minute of its window.
func Spread(start, end time.Time, minutes int) time.Time {
	available := int(end.Add(-time.Minute).Sub(start) / time.Minute)
	if limit := min(minutes, available); limit > 0 {
		return start.Add(time.Duration(rand.Intn(limit+1)) * time.Minute)
	}
	return start
}

func RetryDelay(status model.SiteExecutionStatus, reason string, failures int) time.Duration {
	if status == model.SiteExecutionStatusSkipped {
		return 7 * 24 * time.Hour
	}
	reason = strings.ToLower(strings.TrimSpace(reason))
	for _, hint := range []string{"authentication_unavailable", "unauthorized", "forbidden", "invalid token", "token expired", "http 401", "http 403", "登录失效", "凭证失效"} {
		if strings.Contains(reason, hint) {
			return 6 * time.Hour
		}
	}
	return (15 * time.Minute) << min(max(0, failures-1), 3)
}
