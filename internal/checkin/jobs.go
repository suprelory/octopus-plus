package checkin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/utils/safe"
	"gorm.io/gorm"
)

// Jobs is driven explicitly until runtime cutover. Scheduled scans and manual
// batches use the same long-lived engine as individual check-in requests.
type Jobs struct {
	store    *op.CheckinStore
	engine   *Engine
	interval time.Duration
}

func NewJobs(store *op.CheckinStore, engine *Engine) *Jobs {
	return &Jobs{store: store, engine: engine, interval: 500 * time.Millisecond}
}

func skipCheckin(err error) bool {
	return errors.Is(err, ErrCheckinAlreadyRunning) || errors.Is(err, op.ErrCheckinWorkflowConflict) ||
		errors.Is(err, op.ErrCheckinUnavailable) || errors.Is(err, op.ErrCheckinNotDue) || errors.Is(err, gorm.ErrRecordNotFound)
}

// RunDue handles one scan; failures for one account do not starve other sites.
func (j *Jobs) RunDue(ctx context.Context) error {
	if err := j.store.RecoverBatches(ctx); err != nil {
		return err
	}
	targets, err := j.store.DueCandidates(ctx, time.Now())
	if err != nil {
		return err
	}
	var failures []error
	challengedSite := 0
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if target.SiteID == challengedSite {
			continue
		}
		now := time.Now().UTC()
		request, err := j.store.PrepareScheduled(ctx, target.ID, now)
		if err == nil && request != nil {
			if !waitSession(ctx, j.interval) {
				return ctx.Err()
			}
			var result *model.CheckinResult
			result, err = j.engine.run(ctx, target.ID, *request)
			if err == nil && result.Replayed && result.Status == model.SiteExecutionStatusPending {
				// A login owner can still be receiving its response. Schedule the
				// next status read without advancing that owner's workflow version.
				err = j.store.DeferScheduled(ctx, target.ID, now)
			}
			if result != nil && result.Reason == "verification_required" {
				challengedSite = target.SiteID
			}
		}
		if err != nil && !skipCheckin(err) {
			failures = append(failures, fmt.Errorf("schedule account %d: %w", target.ID, err))
			if request != nil && ctx.Err() == nil {
				if deferErr := j.store.DeferScheduled(ctx, target.ID, now); deferErr != nil {
					failures = append(failures, deferErr)
				}
			}
		}
	}
	return errors.Join(failures...)
}

// StartBatch detaches execution from the initiating request. Only the caller
// that creates the job starts a worker; its progress is accessible via GetBatch.
func (j *Jobs) StartBatch(ctx context.Context, selection model.CheckinBatchSelection) (*model.CheckinBatchJob, error) {
	job, created, err := j.store.ReserveBatch(ctx, selection)
	if err != nil || !created {
		return job, err
	}
	id := job.ID
	safe.Go(fmt.Sprintf("checkin-batch-%d", id), func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		if err := j.RunBatch(ctx, id); err != nil {
			log.Warnf("check-in batch %d stopped: %v", id, err)
		}
	})
	return job, nil
}

func (j *Jobs) RunBatch(ctx context.Context, id int64) error {
	job, err := j.store.ClaimBatch(ctx, id)
	if err != nil {
		return err
	}
	work, cancel := context.WithCancelCause(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(op.CheckinBatchLeaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-work.Done():
				return
			case <-ticker.C:
				if err := j.store.RenewBatch(work, id); err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	defer func() { cancel(nil); <-stopped }()

	err = j.runBatchAccounts(work, job)
	if cause := context.Cause(work); cause != nil {
		err = cause
	}
	now := time.Now().UTC()
	job.CurrentSiteID, job.CurrentSiteName, job.CurrentAccountID, job.CurrentAccountName = 0, "", 0, ""
	job.FinishedAt, job.DurationMs = &now, max(int64(0), now.Sub(job.StartedAt).Milliseconds())
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		job.Status, job.Canceled = model.SiteCheckinBatchJobStatusCanceled, true
		job.CancelReason = err.Error()
		job.Skipped = job.Total - job.Attempted
	case err != nil:
		job.Status, job.ErrorMessage = model.SiteCheckinBatchJobStatusFailed, "签到批次执行失败"
	case job.Failed > 0:
		job.Status = model.SiteCheckinBatchJobStatusCompletedWithErrors
	default:
		job.Status = model.SiteCheckinBatchJobStatusCompleted
	}
	saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	return errors.Join(err, j.store.SaveBatchProgress(saveCtx, job))
}

func (j *Jobs) runBatchAccounts(ctx context.Context, job *model.CheckinBatchJob) error {
	targets, err := j.store.BatchTargets(ctx, job)
	if err != nil {
		return err
	}
	job.Total = len(targets)
	challengedSite := 0
	for _, target := range targets {
		if target.SiteID == challengedSite {
			job.Skipped++
			continue
		}
		job.CurrentSiteID, job.CurrentSiteName = target.SiteID, target.SiteName
		job.CurrentAccountID, job.CurrentAccountName = target.ID, target.Name
		if err := j.store.SaveBatchProgress(ctx, job); err != nil {
			return err
		}
		if !waitSession(ctx, j.interval) {
			return ctx.Err()
		}
		result, err := j.engine.run(ctx, target.ID, op.CheckinInvocation{Trigger: model.CheckinTriggerBatch, BatchJobID: job.ID})
		switch {
		case skipCheckin(err):
			job.Skipped++
			continue
		case errors.Is(err, op.ErrCheckinBatchStopped), ctx.Err() != nil && err != nil:
			return err
		case err != nil || result.Status == model.SiteExecutionStatusFailed:
			job.Failed++
		case result.Status == model.SiteExecutionStatusPending:
			job.Pending++
		case result.Replayed:
			job.Skipped++
			continue
		default:
			job.Success++
		}
		job.Attempted++
		if result != nil && result.Reason == "verification_required" {
			challengedSite = target.SiteID
		}
	}
	return nil
}
