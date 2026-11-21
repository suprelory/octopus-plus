package handlers

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/suprelory/octopus-plus/internal/db"
)

func setupHandlerTestDB(t *testing.T) context.Context {
	t.Helper()
	if db.GetDB() != nil {
		_ = db.Close()
	}
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "handlers.db"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return t.Context()
}
