package sitesync

import (
	"errors"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestMarkAccountSyncFailurePersistsFailedStatus(t *testing.T) {
	ctx := setupProjectTestDB(t)
	_, account := createProjectionFixture(t, ctx)

	markAccountSyncFailure(ctx, account.ID, errors.New("site channel projection failed"), "")

	reloaded, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil {
		t.Fatalf("SiteAccountGet failed: %v", err)
	}
	if reloaded.LastSyncStatus != model.SiteExecutionStatusFailed {
		t.Fatalf("expected failed last_sync_status, got %q", reloaded.LastSyncStatus)
	}
	if !strings.Contains(reloaded.LastSyncMessage, "projection failed") {
		t.Fatalf("expected projection failure message, got %q", reloaded.LastSyncMessage)
	}
}

// A nil snapshot must not be dereferenced while reporting a persist failure:
// persistSyncSnapshot rejects it with an error, and the failure path used to
// read snapshot.accessToken straight away.
func TestSnapshotAccessTokenToleratesNilSnapshot(t *testing.T) {
	if token := snapshotAccessToken(nil); token != "" {
		t.Fatalf("expected empty access token for nil snapshot, got %q", token)
	}
	if token := snapshotAccessToken(&syncSnapshot{accessToken: "token-1"}); token != "token-1" {
		t.Fatalf("expected snapshot access token, got %q", token)
	}
}

func TestSyncAccountReportsNilSnapshotWithoutPanic(t *testing.T) {
	ctx := setupProjectTestDB(t)
	_, account := createProjectionFixture(t, ctx)

	err := persistSyncSnapshot(ctx, account.ID, nil)
	if err == nil {
		t.Fatal("expected persistSyncSnapshot to reject a nil snapshot")
	}
	markAccountSyncFailure(ctx, account.ID, err, snapshotAccessToken(nil))

	reloaded, reloadErr := op.SiteAccountGet(account.ID, ctx)
	if reloadErr != nil {
		t.Fatalf("SiteAccountGet failed: %v", reloadErr)
	}
	if reloaded.LastSyncStatus != model.SiteExecutionStatusFailed {
		t.Fatalf("expected failed last_sync_status, got %q", reloaded.LastSyncStatus)
	}
}
