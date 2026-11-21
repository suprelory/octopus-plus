package op

import (
	"sync"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
)

func resetStatsTestState(t *testing.T) {
	t.Helper()
	statsChannelCache.Clear()
	statsAPIKeyCache.Clear()
	statsChannelCacheNeedUpdateLock.Lock()
	statsChannelCacheNeedUpdate = make(map[int]struct{})
	statsChannelCacheNeedUpdateLock.Unlock()
	statsAPIKeyCacheNeedUpdateLock.Lock()
	statsAPIKeyCacheNeedUpdate = make(map[int]struct{})
	statsAPIKeyCacheNeedUpdateLock.Unlock()
	t.Cleanup(func() {
		statsChannelCache.Clear()
		statsAPIKeyCache.Clear()
	})
}

func TestStatsChannelUpdateIsAtomic(t *testing.T) {
	resetStatsTestState(t)

	const (
		workers = 32
		updates = 100
	)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < updates; j++ {
				if err := StatsChannelUpdate(7, model.StatsMetrics{RequestSuccess: 1}); err != nil {
					t.Errorf("StatsChannelUpdate failed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got := StatsChannelGet(7)
	if got.RequestSuccess != workers*updates {
		t.Fatalf("request success count = %d, want %d", got.RequestSuccess, workers*updates)
	}
}

func TestChannelKeyUpdateWithDeltaIsAtomic(t *testing.T) {
	resetStatsTestState(t)
	channelCache.Clear()
	channelKeyCache.Clear()
	channel := model.Channel{
		ID:   1,
		Keys: []model.ChannelKey{{ID: 11, ChannelID: 1, Enabled: true, ChannelKey: "key"}},
	}
	setChannelRuntimeCache(channel.ID, channel)
	channelKeyCache.Set(11, channel.Keys[0])
	t.Cleanup(func() {
		channelCache.Clear()
		channelKeyCache.Clear()
	})

	const (
		workers = 32
		updates = 100
		delta   = 0.25
	)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < updates; j++ {
				if err := ChannelKeyUpdateWithDelta(channel.Keys[0], delta); err != nil {
					t.Errorf("ChannelKeyUpdateWithDelta failed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got, ok := channelKeyCache.Get(11)
	if !ok {
		t.Fatal("channel key missing from cache")
	}
	want := float64(workers*updates) * delta
	if got.TotalCost != want {
		t.Fatalf("total cost = %f, want %f", got.TotalCost, want)
	}
	gotChannel, ok := channelCache.Get(1)
	if !ok || len(gotChannel.Keys) != 1 || gotChannel.Keys[0].TotalCost != want {
		t.Fatalf("channel cache key cost is not synchronized: %+v", gotChannel.Keys)
	}
}

func TestChannelKeyUpdateWithDeltaPreservesNewerConfiguration(t *testing.T) {
	channelCache.Clear()
	channelKeyCache.Clear()
	t.Cleanup(func() {
		channelCache.Clear()
		channelKeyCache.Clear()
	})

	current := model.ChannelKey{
		ID: 11, ChannelID: 1, Enabled: false, ChannelKey: "new-key", Remark: "new", TotalCost: 10,
		StatusCode: 201, LastUseTimeStamp: 200,
	}
	staleRequestSnapshot := model.ChannelKey{
		ID: 11, ChannelID: 1, Enabled: true, ChannelKey: "old-key", Remark: "old", TotalCost: 3,
		StatusCode: 500, LastUseTimeStamp: 100,
	}
	setChannelRuntimeCache(1, model.Channel{ID: 1, Keys: []model.ChannelKey{current}})
	channelKeyCache.Set(11, current)

	if err := ChannelKeyUpdateWithDelta(staleRequestSnapshot, 2); err != nil {
		t.Fatalf("ChannelKeyUpdateWithDelta failed: %v", err)
	}
	got, _ := channelKeyCache.Get(11)
	if got.Enabled || got.ChannelKey != "new-key" || got.Remark != "new" {
		t.Fatalf("stale relay snapshot overwrote key configuration: %+v", got)
	}
	if got.TotalCost != 12 {
		t.Fatalf("total cost = %f, want 12", got.TotalCost)
	}
	if got.StatusCode != 201 || got.LastUseTimeStamp != 200 {
		t.Fatalf("older runtime status overwrote newer status: %+v", got)
	}
}
