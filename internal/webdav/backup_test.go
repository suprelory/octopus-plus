package webdav

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/utils/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type retentionTestClient struct {
	files     []os.FileInfo
	listErr   error
	removeErr error
	removed   []string
}

func (c *retentionTestClient) ReadDir(string) ([]os.FileInfo, error) { return c.files, c.listErr }
func (c *retentionTestClient) Remove(name string) error {
	c.removed = append(c.removed, name)
	return c.removeErr
}

type retentionFile string

func (f retentionFile) Name() string       { return string(f) }
func (f retentionFile) Size() int64        { return 1 }
func (f retentionFile) Mode() os.FileMode  { return 0600 }
func (f retentionFile) ModTime() time.Time { return time.Time{} }
func (f retentionFile) IsDir() bool        { return false }
func (f retentionFile) Sys() any           { return nil }

func TestRetentionReportsListingAndRemovalFailures(t *testing.T) {
	for _, listing := range []bool{true, false} {
		t.Run(fmt.Sprint("listing=", listing), func(t *testing.T) {
			core, entries := observer.New(zap.InfoLevel)
			previous := log.Logger
			log.Logger = zap.New(core).Sugar()
			t.Cleanup(func() { log.Logger = previous })
			client := &retentionTestClient{files: []os.FileInfo{
				retentionFile("octopus-backup-20260101000000.json"), retentionFile("octopus-backup-20260102000000.json"),
				retentionFile("unrelated.json"),
			}}
			failure := errors.New("HTTP 403 from https://user:private-password@example.com/private-token")
			if listing {
				client.listErr = failure
			} else {
				client.removeErr = failure
			}
			enforceRetention(client, "/backups", 1)
			if entries.Len() != 1 || entries.All()[0].Level != zap.WarnLevel {
				t.Fatalf("missing retention warning: %+v", entries.All())
			}
			fields := entries.All()[0].ContextMap()
			if text := fmt.Sprint(fields); !strings.Contains(text, "403") || strings.Contains(text, "private-") {
				t.Fatalf("unsafe retention diagnostic: %s", text)
			}
			if listing && len(client.removed) != 0 {
				t.Fatal("deleted without listing backups")
			}
			if !listing && (len(client.removed) != 1 || fields["failed"] != int64(1) || fields["remaining"] != int64(2)) {
				t.Fatalf("incorrect retention counts: %+v, removed=%v", fields, client.removed)
			}
		})
	}
}
