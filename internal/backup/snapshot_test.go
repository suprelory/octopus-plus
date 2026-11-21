package backup

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func TestBackupSnapshotKeepsReferencesConsistent(t *testing.T) {
	for _, format := range []string{"json", "zip"} {
		for _, mutation := range []string{"insert", "delete"} {
			t.Run(format+"/"+mutation, func(t *testing.T) {
				ctx := t.Context()
				service := testService(t)
				conn := service.conn
				if err := conn.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
					t.Fatal(err)
				}
				pool, err := conn.DB()
				if err != nil {
					t.Fatal(err)
				}
				// A second WAL connection models a concurrent management request.
				pool.SetMaxOpenConns(2)
				channel := model.Channel{ID: 41, Name: "snapshot", Enabled: true}
				key := model.ChannelKey{ID: 51, ChannelID: 41, ChannelKey: "test-only"}
				create := func(tx *gorm.DB) error {
					if err := tx.Create(&channel).Error; err != nil {
						return err
					}
					return tx.Create(&key).Error
				}
				if mutation == "delete" {
					if err := conn.Transaction(create); err != nil {
						t.Fatal(err)
					}
				}
				mutated := false
				if err := conn.Callback().Query().After("gorm:query").Register("test:mutate_after_channels", func(tx *gorm.DB) {
					if tx.Statement.Table != "channels" || mutated {
						return
					}
					mutated = true
					writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
					defer cancel()
					err := conn.WithContext(writeCtx).Transaction(func(write *gorm.DB) error {
						if mutation == "insert" {
							return create(write)
						}
						if err := write.Delete(&key).Error; err != nil {
							return err
						}
						return write.Delete(&channel).Error
					})
					if err != nil {
						tx.AddError(err)
					}
				}); err != nil {
					t.Fatal(err)
				}
				defer conn.Callback().Query().Remove("test:mutate_after_channels")
				var dump *Document
				if format == "json" {
					dump, err = service.Export(ctx, true, true)
				} else {
					var output bytes.Buffer
					err = service.ExportZIP(ctx, &output, true, true)
					if err == nil {
						dump, err = Read(ctx, &output)
						if err == nil {
							defer dump.Close()
						}
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if !mutated {
					t.Fatal("concurrent write did not run")
				}
				want := 0
				if mutation == "delete" {
					want = 1
				}
				if len(dump.Channels) != want || len(dump.ChannelKeys) != want {
					t.Fatalf("inconsistent snapshot: channels=%d keys=%d", len(dump.Channels), len(dump.ChannelKeys))
				}
				if want == 1 && dump.ChannelKeys[0].ChannelID != dump.Channels[0].ID {
					t.Fatal("orphan key")
				}
			})
		}
	}
}

type backupWriterFunc func([]byte) (int, error)

func (f backupWriterFunc) Write(p []byte) (int, error) { return f(p) }

func TestBackupZipReleasesConnectionBeforeTransfer(t *testing.T) {
	ctx := t.Context()
	service := testService(t)
	pool, err := service.conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	transferErr := errors.New("client disconnected")
	err = service.ExportZIP(ctx, backupWriterFunc(func(p []byte) (int, error) {
		writes++
		if pool.Stats().InUse != 0 {
			t.Fatal("network transfer holds database connection")
		}
		return 0, transferErr
	}), false, false)
	if writes == 0 || !errors.Is(err, transferErr) {
		t.Fatalf("transfer error lost: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	err = service.ExportZIP(ctx, backupWriterFunc(func(p []byte) (int, error) { t.Fatal("cancelled export wrote output"); return len(p), nil }), false, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
