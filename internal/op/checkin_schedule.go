package op

import (
	"context"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin/schedule"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

type CheckinTarget struct {
	ID, SiteID     int
	Name, SiteName string
}

func (s *CheckinStore) checkinTargets(ctx context.Context) *gorm.DB {
	return s.conn.WithContext(ctx).Table("checkin_accounts AS a").
		Select("a.id, a.site_id, a.name, s.name AS site_name").
		Joins("JOIN checkin_sites AS s ON s.id = a.site_id").
		Where("a.enabled = ? AND a.auto_checkin = ? AND s.enabled = ? AND s.archived = ? AND s.checkin_mode <> ?", true, true, true, false, model.SiteCheckinModeDisabled).
		Order("a.site_id, a.id")
}

func (s *CheckinStore) DueCandidates(ctx context.Context, now time.Time) ([]CheckinTarget, error) {
	var targets []CheckinTarget
	err := s.checkinTargets(ctx).Where("a.next_auto_checkin_at IS NULL OR a.next_auto_checkin_at <= ?", now.UTC()).Scan(&targets).Error
	return targets, err
}

func checkinAutoEnabled(site model.CheckinSite, account model.CheckinAccount) bool {
	return site.Enabled && !site.Archived && account.Enabled && account.AutoCheckin && site.ResolveCheckinCapability().Enabled
}

func canConfirmCheckin(site model.CheckinSite) bool {
	return site.CheckinHTTPEnabled && site.CheckinStatus != nil || site.CheckinAdapter() == model.SiteCheckinAdapterAgentRouter
}

func retryUnsubmitted(w *model.SiteCheckinPending) bool {
	return w != nil && !w.Submitted && w.Status == model.SiteExecutionStatusFailed &&
		(w.Reason == "authentication_unavailable" || w.Reason == "preparation_failed")
}

func nextCheckinAt(site model.CheckinSite, a model.CheckinAccount, now time.Time, keep bool) (*time.Time, error) {
	if !checkinAutoEnabled(site, a) {
		return nil, nil
	}
	location, err := time.LoadLocation(site.CheckinTimezone)
	if err != nil {
		return nil, err
	}
	local := now.In(location)
	window := schedule.ParseWindow(site.CheckinWindowStart, site.CheckinWindowEnd)
	w := a.CheckinPending
	next := a.NextAutoCheckinAt
	if w != nil && !w.Finished {
		at := now.Add(10 * time.Minute)
		switch {
		case w.Submitted && !canConfirmCheckin(site):
			// Allow the owner to finish an in-flight single-request submission.
			at = w.Deadline
		case keep && next != nil:
			at = *next
		case keep:
			at = now
		}
		if !w.Submitted {
			at, _ = window.Next(at.In(location), nil)
		}
		if at.After(w.Deadline) {
			at = w.Deadline
		}
		return checkinTime(at), nil
	}

	lastDay := a.LastCheckinSuccessAt
	target := local
	minutes := 0
	if a.RandomCheckin {
		minutes = a.CheckinRandomWindowMinutes
	}
	if w != nil {
		// A late confirmation counts for the workflow's day. Its receipt time
		// can be on the next calendar day, which is still eligible.
		day, err := time.ParseInLocation(time.DateOnly, w.Date, location)
		if err != nil {
			return nil, err
		}
		lastDay = &day
		if retryUnsubmitted(w) && a.LastCheckinAt != nil {
			lastDay = nil
			retry := a.LastCheckinAt.Add(schedule.RetryDelay(w.Status, w.Reason, a.CheckinFailureCount))
			if retry.After(target) {
				target = retry.In(location)
			}
			minutes = schedule.RetryJitterMinutes
		}
	}
	start, end := window.Next(target, lastDay)
	if keep && next != nil {
		saved := next.In(location)
		from, until := window.Bounds(saved)
		validDay := lastDay == nil || saved.Format(time.DateOnly) > lastDay.In(location).Format(time.DateOnly)
		overdueToday := !now.Before(saved) && !local.Before(from) && local.Before(until) && !target.After(local)
		if validDay && !saved.Before(from) && saved.Before(until) && saved.Before(end) && (!saved.Before(start) || overdueToday) {
			return next, nil
		}
	}
	return checkinTime(schedule.Spread(start, end, minutes)), nil
}

func checkinTime(t time.Time) *time.Time {
	value := t.UTC().Truncate(time.Millisecond)
	return &value
}

// PrepareScheduled rereads edits under the same locks used for submission.
// A scan result only authorizes dispatch for this version of the workflow.
func (s *CheckinStore) PrepareScheduled(ctx context.Context, id int, now time.Time) (*CheckinInvocation, error) {
	var request *CheckinInvocation
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		site, a, err := lockCheckinAccount(tx, id)
		if err != nil {
			return err
		}
		next, err := nextCheckinAt(site, a, now, true)
		if err != nil {
			return err
		}
		if next == nil && a.NextAutoCheckinAt != nil || next != nil && (a.NextAutoCheckinAt == nil || !next.Equal(*a.NextAutoCheckinAt)) {
			if err := tx.Model(&a).Update("next_auto_checkin_at", next).Error; err != nil {
				return err
			}
		}
		if next == nil || now.Before(*next) {
			return nil
		}
		w := a.CheckinPending
		request = &CheckinInvocation{Trigger: model.CheckinTriggerScheduled, ScheduledVersion: &a.CheckinWorkflowVersion,
			Confirm: w != nil && !w.Finished && w.Submitted && canConfirmCheckin(site)}
		return nil
	})
	return request, err
}

// Back off a dispatch error without advancing a concurrent worker's workflow.
// Its original reservation can still receive a response or finish preparation.
func (s *CheckinStore) DeferScheduled(ctx context.Context, id int, due time.Time) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		site, a, err := lockCheckinAccount(tx, id)
		if err != nil {
			return err
		}
		if a.NextAutoCheckinAt == nil || a.NextAutoCheckinAt.After(due) || !checkinAutoEnabled(site, a) {
			return nil
		}
		location, err := time.LoadLocation(site.CheckinTimezone)
		if err != nil {
			return err
		}
		w := a.CheckinPending
		now := time.Now().UTC()
		next := now.Add(schedule.RetryDelay(model.SiteExecutionStatusFailed, "", a.CheckinFailureCount+1)).In(location)
		if w != nil && !w.Finished && w.Submitted {
			next = now.Add(10 * time.Minute)
		}
		if w == nil || !w.Submitted {
			window := schedule.ParseWindow(site.CheckinWindowStart, site.CheckinWindowEnd)
			next, _ = window.Next(next, nil)
		}
		if w != nil && !w.Finished && w.Deadline.After(due) && next.After(w.Deadline) {
			next = w.Deadline
		}
		updates := map[string]any{"next_auto_checkin_at": next.UTC()}
		if w == nil || w.Finished {
			updates["checkin_failure_count"] = a.CheckinFailureCount + 1
		}
		return tx.Model(&a).Updates(updates).Error
	})
}
