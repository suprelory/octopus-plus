package checkin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestServicesCloseWaitsForDetachedSessionPersistence(t *testing.T) {
	store, _ := sessionStore(t)
	credential := newCredential(t, store)
	services := NewServices(store, nil)
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	services.Sessions.auth = fakeAuth{refresh: func(ctx context.Context, _ model.CheckinCredential) (op.CheckinTokenPair, error) {
		close(started)
		<-ctx.Done()
		<-release
		return rotatedPair(), nil
	}}
	request, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	go func() { _, err := services.Sessions.Ensure(request, credential.ID); finished <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("exchange never started")
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	deadline, stop := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer stop()
	if err := services.Close(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close returned while exchange could still save: %v", err)
	}
	if _, err := services.Sessions.Ensure(t.Context(), credential.ID); !errors.Is(err, ErrServicesStopped) {
		t.Fatal("closed service accepted work")
	}
	// Release the exchange before the test cleanup closes its database.
	release <- struct{}{}
	if err := services.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetCredential(t.Context(), credential.ID)
	if err != nil || saved.RefreshToken != "refresh-new" {
		t.Fatalf("issued session was not settled before close: %v", err)
	}
}

func TestServicesCloseInterruptsBatchAndPreservesSubmission(t *testing.T) {
	started := make(chan struct{}, 1)
	finishHandler := make(chan struct{})
	defer close(finishHandler)
	var submissions atomic.Int32
	_, store, conn, site, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		submissions.Add(1)
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-finishHandler:
		}
	}, false)
	enableJobAccount(t, store, account.ID)
	services := NewServices(store, nil)
	services.Jobs.interval = 0
	job, err := services.Jobs.StartBatch(t.Context(), model.CheckinBatchSelection{Scope: "selected", SiteIDs: []int{site.ID}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("batch did not submit")
	}
	deadline, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	if err := services.Close(deadline); err != nil {
		t.Fatal(err)
	}
	var saved model.CheckinBatchJob
	if err := conn.First(&saved, job.ID).Error; err != nil || saved.Status != model.SiteCheckinBatchJobStatusInterrupted {
		t.Fatalf("batch did not settle: %v, %s", err, saved.Status)
	}
	a, err := store.GetAccount(t.Context(), account.ID)
	if err != nil || a.CheckinPending == nil || !a.CheckinPending.Submitted {
		t.Fatal("shutdown erased submission evidence")
	}
	restarted := NewServices(store, nil)
	defer restarted.Close(t.Context())
	_, _ = restarted.Engine.Execute(t.Context(), account.ID, model.CheckinTriggerManual)
	if submissions.Load() != 1 {
		t.Fatal("restart submitted twice")
	}
}
