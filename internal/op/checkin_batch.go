package op

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/snowflake"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrCheckinBatchActive  = errors.New("a different check-in batch is active")
	ErrCheckinBatchStopped = errors.New("check-in batch is no longer owned by this worker")
)

const CheckinBatchLeaseDuration = time.Minute

// Take the singleton lock before reading either the job or its candidate sites.
// SQLite has no FOR UPDATE; a write as the first statement takes its writer lock.
func lockCheckinBatch(tx *gorm.DB) (model.CheckinBatchLease, error) {
	var lease model.CheckinBatchLease
	if tx.Dialector.Name() == "sqlite" {
		if err := tx.Model(&lease).Where("id = 1").UpdateColumn("job_id", gorm.Expr("job_id")).Error; err != nil {
			return lease, err
		}
	}
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lease, 1).Error
	return lease, err
}

func runningCheckinBatch(tx *gorm.DB, id int64) (*model.CheckinBatchJob, error) {
	lease, err := lockCheckinBatch(tx)
	if err != nil {
		return nil, err
	}
	if lease.JobID != id || lease.UntilMilli <= time.Now().UnixMilli() {
		return nil, ErrCheckinBatchStopped
	}
	var job model.CheckinBatchJob
	if err := tx.First(&job, id).Error; err != nil {
		return nil, err
	}
	if job.Status != model.SiteCheckinBatchJobStatusRunning {
		return nil, ErrCheckinBatchStopped
	}
	return &job, nil
}

func setCheckinBatchLease(tx *gorm.DB, id int64, until int64) error {
	return tx.Model(&model.CheckinBatchLease{}).Where("id = 1").Updates(map[string]any{"job_id": id, "until_milli": until}).Error
}

func interruptExpiredBatch(tx *gorm.DB, lease model.CheckinBatchLease, now time.Time) error {
	if lease.JobID == 0 || lease.UntilMilli > now.UnixMilli() {
		return nil
	}
	var job model.CheckinBatchJob
	err := tx.First(&job, lease.JobID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil && slices.Contains(activeCheckinBatchStatuses, job.Status) {
		updates := map[string]any{"status": model.SiteCheckinBatchJobStatusInterrupted, "finished_at": now, "duration_ms": max(int64(0), now.Sub(job.StartedAt).Milliseconds()),
			"current_site_id": 0, "current_account_id": 0, "current_site_name": "", "current_account_name": "", "error_message": "签到批次执行已中断"}
		if err := tx.Model(&job).Updates(updates).Error; err != nil {
			return err
		}
	}
	return setCheckinBatchLease(tx, 0, 0)
}

// ReserveBatch returns created=false to callers that joined the same active selection.
func (s *CheckinStore) ReserveBatch(ctx context.Context, selection model.CheckinBatchSelection) (*model.CheckinBatchJob, bool, error) {
	ids, err := selection.Normalize()
	if err != nil {
		return nil, false, err
	}
	var job model.CheckinBatchJob
	created := false
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		lease, err := lockCheckinBatch(tx)
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			var found []int
			if err := tx.Model(&model.CheckinSite{}).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id IN ? AND archived = ?", ids, false).Order("id").Pluck("id", &found).Error; err != nil {
				return err
			}
			if !slices.Equal(ids, found) {
				return model.ErrCheckinBatchSelection
			}
		}
		now := time.Now().UTC()
		if lease.JobID != 0 && lease.UntilMilli > now.UnixMilli() {
			if err := tx.First(&job, lease.JobID).Error; err != nil {
				return err
			}
			if !slices.Equal(job.SiteIDs, ids) {
				return ErrCheckinBatchActive
			}
			return nil
		}
		if err := interruptExpiredBatch(tx, lease, now); err != nil {
			return err
		}
		var maxID int64
		if err := tx.Model(&job).Select("COALESCE(MAX(id), 0)").Scan(&maxID).Error; err != nil {
			return err
		}
		id, err := snowflake.GenerateIDAfter(maxID)
		if err != nil {
			return err
		}
		job = model.CheckinBatchJob{ID: id, SiteIDs: ids, Status: model.SiteCheckinBatchJobStatusQueued, Trigger: string(model.CheckinTriggerManual), StartedAt: now, UpdatedAt: now}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		created = true
		return setCheckinBatchLease(tx, id, now.Add(CheckinBatchLeaseDuration).UnixMilli())
	})
	if err != nil {
		return nil, false, err
	}
	return &job, created, nil
}

func (s *CheckinStore) ClaimBatch(ctx context.Context, id int64) (*model.CheckinBatchJob, error) {
	var job model.CheckinBatchJob
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		lease, err := lockCheckinBatch(tx)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if id <= 0 || lease.JobID != id || lease.UntilMilli <= now.UnixMilli() {
			return ErrCheckinBatchStopped
		}
		result := tx.Model(&job).Where("id = ? AND status = ?", id, model.SiteCheckinBatchJobStatusQueued).
			Update("status", model.SiteCheckinBatchJobStatusRunning)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCheckinBatchStopped
		}
		if err := setCheckinBatchLease(tx, id, now.Add(CheckinBatchLeaseDuration).UnixMilli()); err != nil {
			return err
		}
		return tx.First(&job, id).Error
	})
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *CheckinStore) RenewBatch(ctx context.Context, id int64) error {
	now := time.Now()
	result := s.conn.WithContext(ctx).Model(&model.CheckinBatchLease{}).Where("id = 1 AND job_id = ? AND until_milli > ?", id, now.UnixMilli()).
		Update("until_milli", now.Add(CheckinBatchLeaseDuration).UnixMilli())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCheckinBatchStopped
	}
	return nil
}

func (s *CheckinStore) BatchTargets(ctx context.Context, job *model.CheckinBatchJob) ([]CheckinTarget, error) {
	query := s.checkinTargets(ctx)
	if len(job.SiteIDs) != 0 {
		query = query.Where("a.site_id IN ?", job.SiteIDs)
	}
	var targets []CheckinTarget
	err := query.Scan(&targets).Error
	return targets, err
}

// A single claimed worker writes ordered progress snapshots. A late update
// cannot reopen an interrupted batch or release another worker's reservation.
func (s *CheckinStore) SaveBatchProgress(ctx context.Context, job *model.CheckinBatchJob) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if _, err := runningCheckinBatch(tx, job.ID); err != nil {
			return err
		}
		fields := []string{"total", "attempted", "success", "pending", "failed", "skipped", "current_site_id", "current_site_name",
			"current_account_id", "current_account_name", "status", "duration_ms", "finished_at", "updated_at", "canceled", "cancel_reason", "error_message"}
		if err := tx.Model(&model.CheckinBatchJob{}).Where("id = ?", job.ID).Select(fields).Updates(job).Error; err != nil {
			return err
		}
		if job.FinishedAt != nil {
			return setCheckinBatchLease(tx, 0, 0)
		}
		return nil
	})
}

func (s *CheckinStore) RecoverBatches(ctx context.Context) error {
	// A progress poll normally needs only a read; take the writer lock only
	// for an expired lease, then recheck it in case the heartbeat won the race.
	var observed model.CheckinBatchLease
	if err := s.conn.WithContext(ctx).First(&observed, 1).Error; err != nil {
		return err
	}
	if observed.JobID == 0 || observed.UntilMilli > time.Now().UnixMilli() {
		return nil
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		lease, err := lockCheckinBatch(tx)
		if err != nil {
			return err
		}
		return interruptExpiredBatch(tx, lease, time.Now().UTC())
	})
}

func (s *CheckinStore) GetBatch(ctx context.Context, id int64) (*model.CheckinBatchJob, error) {
	if err := s.RecoverBatches(ctx); err != nil {
		return nil, err
	}
	var job model.CheckinBatchJob
	if err := s.conn.WithContext(ctx).First(&job, id).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *CheckinStore) ListBatches(ctx context.Context, limit int, beforeID int64) (*model.CheckinBatchJobPage, error) {
	if err := s.RecoverBatches(ctx); err != nil {
		return nil, err
	}
	return listCheckinBatchJobs(s.conn.WithContext(ctx), limit, beforeID)
}
