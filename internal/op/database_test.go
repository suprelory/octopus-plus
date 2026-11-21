package op

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/suprelory/octopus-plus/internal/db"
)

func setupOpTestDB(t *testing.T) context.Context {
	t.Helper()
	return openOpTestDB(t, db.InitDB)
}

func openOpTestDB(t *testing.T, initialize func(string, string, bool) error) context.Context {
	t.Helper()
	if db.GetDB() != nil {
		_ = db.Close()
	}
	if err := initialize("sqlite", filepath.Join(t.TempDir(), "octopus-test.db"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	RelayLogTotalCacheInvalidate()
	t.Cleanup(RelayLogTotalCacheInvalidate)
	return context.Background()
}
