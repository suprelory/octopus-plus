package op

import (
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestSiteCheckinBatchJobCanBeQueriedAndUpdated(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	job := &model.SiteCheckinBatchJob{
		ID: 101, Status: model.SiteCheckinBatchJobStatusQueued,
		Trigger: "manual", StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := SiteCheckinBatchJobCreate(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := SiteCheckinBatchJobUpdate(ctx, job.ID, map[string]any{
		"status":               model.SiteCheckinBatchJobStatusRunning,
		"total":                2,
		"current_site_name":    "Site",
		"current_account_name": "Account",
	}); err != nil {
		t.Fatal(err)
	}

	active, err := SiteCheckinBatchJobActive(ctx)
	if err != nil || active == nil || active.ID != job.ID || active.Total != 2 || active.CurrentAccountName != "Account" {
		t.Fatalf("unexpected active job: %+v, %v", active, err)
	}
	byID, err := SiteCheckinBatchJobGet(ctx, job.ID)
	if err != nil || byID == nil || byID.Status != model.SiteCheckinBatchJobStatusRunning {
		t.Fatalf("job query by id failed: %+v, %v", byID, err)
	}
	latest, err := SiteCheckinBatchJobLatest(ctx)
	if err != nil || latest == nil || latest.ID != job.ID {
		t.Fatalf("latest job query failed: %+v, %v", latest, err)
	}
	if err := SiteCheckinBatchJobUpdate(ctx, job.ID, map[string]any{
		"status":            model.SiteCheckinBatchJobStatusCompleted,
		"current_site_id":   0,
		"current_site_name": "",
	}); err != nil {
		t.Fatal(err)
	}
	active, err = SiteCheckinBatchJobActive(ctx)
	if err != nil || active != nil {
		t.Fatalf("completed job remained active: %+v, %v", active, err)
	}
}
