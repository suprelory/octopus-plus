package op

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
	"gorm.io/gorm"
)

func createLifecycleChannel(t *testing.T, ctx context.Context) *model.Channel {
	t.Helper()
	channel := &model.Channel{Name: t.Name(), Type: outbound.OutboundTypeOpenAIChat, Enabled: true,
		Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "test-only-key"}}}
	if err := ChannelCreate(channel, ctx); err != nil {
		t.Fatal(err)
	}
	return channel
}

func TestChannelUpdateProxyUsesTransactionConnection(t *testing.T) {
	for _, state := range []string{"missing", "uncached", "disabled", "stale-cache"} {
		t.Run(state, func(t *testing.T) {
			ctx := setupChannelSupportTestDB(t)
			channel := createLifecycleChannel(t, ctx)
			proxyConfigurationCache.Clear()
			proxy := model.ProxyConfiguration{ID: 123, Name: "test-proxy", URL: "http://127.0.0.1:8080", Enabled: state != "disabled"}
			if state == "uncached" || state == "disabled" {
				if err := db.GetDB().Create(&proxy).Error; err != nil {
					t.Fatal(err)
				}
				if state == "disabled" {
					if err := db.GetDB().Model(&proxy).Update("enabled", false).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			if state == "stale-cache" {
				proxyConfigurationCache.Set(proxy.ID, proxy)
			}
			pool, err := db.GetDB().DB()
			if err != nil {
				t.Fatal(err)
			}
			before := pool.Stats().WaitCount
			deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			mode := model.ProxyUsageModePool
			_, err = ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, ProxyMode: &mode, ProxyConfigID: &proxy.ID}, deadline)
			if state == "uncached" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid proxy was accepted")
			}
			if errors.Is(err, context.DeadlineExceeded) || deadline.Err() != nil {
				t.Fatalf("transaction waited on its own connection: %v", err)
			}
			if got := pool.Stats().WaitCount; got != before {
				t.Fatalf("nested pool acquisition: wait count %d -> %d", before, got)
			}
			if err := db.GetDB().Exec("SELECT 1").Error; err != nil {
				t.Fatalf("connection was not released: %v", err)
			}
		})
	}
}

func TestProxyLookupPreservesCancellation(t *testing.T) {
	ctx := setupChannelSupportTestDB(t)
	proxyConfigurationCache.Clear()
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ProxyURLForConfig(123, ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestDeletedChannelKeyCannotBeResurrected(t *testing.T) {
	for _, removeChannel := range []bool{false, true} {
		t.Run(map[bool]string{false: "key", true: "channel"}[removeChannel], func(t *testing.T) {
			ctx := setupChannelSupportTestDB(t)
			channel := createLifecycleChannel(t, ctx)
			stale := channel.Keys[0]
			if err := ChannelKeyUpdateWithDelta(stale, 1); err != nil {
				t.Fatal(err)
			}
			if removeChannel {
				if err := ChannelDel(channel.ID, ctx); err != nil {
					t.Fatal(err)
				}
			} else if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, KeysToDelete: []int{stale.ID}}, ctx); err != nil {
				t.Fatal(err)
			}
			if err := ChannelKeyUpdateWithDelta(stale, 3); err == nil {
				t.Fatal("late settlement accepted a deleted key")
			}
			if err := ChannelKeyUpdate(stale); err == nil {
				t.Fatal("snapshot update accepted a deleted key")
			}
			if err := ChannelKeySaveDB(ctx); err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := db.GetDB().Model(&model.ChannelKey{}).Where("id = ?", stale.ID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("deleted key reappeared: count=%d err=%v", count, err)
			}
			if err := channelRefreshCache(ctx); err != nil {
				t.Fatal(err)
			}
			if channelKeyCache.Exists(stale.ID) {
				t.Fatal("deleted credential reappeared in routing cache")
			}
		})
	}
}

func TestChannelKeyFlushOnlyUpdatesRuntimeOfExistingRows(t *testing.T) {
	ctx := setupChannelSupportTestDB(t)
	channel := createLifecycleChannel(t, ctx)
	key := channel.Keys[0]
	if err := ChannelKeyUpdateWithDelta(key, 7.25); err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Model(&model.ChannelKey{}).Where("id = ?", key.ID).Updates(map[string]any{"channel_key": "rotated", "enabled": false, "remark": "updated"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ChannelKeySaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	var saved model.ChannelKey
	if err := db.GetDB().First(&saved, key.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.TotalCost != 7.25 || saved.ChannelKey != "rotated" || saved.Enabled || saved.Remark != "updated" {
		t.Fatalf("runtime flush overwrote configuration: %+v", saved)
	}
	if err := ChannelKeyUpdateWithDelta(key, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Delete(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if err := ChannelKeySaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.GetDB().Model(&model.ChannelKey{}).Where("id = ?", key.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("flush inserted a missing row: count=%d err=%v", count, err)
	}
}

func TestChannelConfigurationRefreshPreservesPendingRuntime(t *testing.T) {
	ctx := setupChannelSupportTestDB(t)
	channel := createLifecycleChannel(t, ctx)
	key := channel.Keys[0]
	key.StatusCode, key.LastUseTimeStamp = 201, 100
	if err := ChannelKeyUpdateWithDelta(key, 7.25); err != nil {
		t.Fatal(err)
	}
	name := "renamed"
	updated, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, Name: &name}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Keys[0].TotalCost != 7.25 {
		t.Fatalf("update discarded pending cost: %+v", updated.Keys[0])
	}
	if _, err := ChannelGetByName(name, ctx); err != nil {
		t.Fatal(err)
	}
	if err := channelRefreshCache(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ChannelKeySaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	var saved model.ChannelKey
	if err := db.GetDB().First(&saved, key.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.TotalCost != 7.25 || saved.StatusCode != 201 || saved.LastUseTimeStamp != 100 {
		t.Fatalf("refresh lost runtime state/dirty flag: %+v", saved)
	}
}

func TestChannelDeleteOrdersLateSettlementAndFlush(t *testing.T) {
	ctx := setupChannelSupportTestDB(t)
	channel := createLifecycleChannel(t, ctx)
	key := channel.Keys[0]
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	name := "test:block_key_delete"
	if err := db.GetDB().Callback().Delete().After("gorm:delete").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "channel_keys" {
			once.Do(func() { close(entered); <-release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.GetDB().Callback().Delete().Remove(name) })
	done := make(chan error, 1)
	go func() {
		_, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, KeysToDelete: []int{key.ID}}, ctx)
		done <- err
	}()
	<-entered
	settled, flushed := make(chan error, 1), make(chan error, 1)
	go func() { settled <- ChannelKeyUpdateWithDelta(key, 1) }()
	go func() { flushed <- ChannelKeySaveDB(ctx) }()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-settled; err == nil {
		t.Fatal("settlement crossed key deletion")
	}
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}
	if channelKeyCache.Exists(key.ID) {
		t.Fatal("late settlement restored deleted key")
	}
}
