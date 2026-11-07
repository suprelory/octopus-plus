package op

import (
	"sync"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	cachelib "github.com/suprelory/octopus-plus/internal/utils/cache"
)

// Pause after an actual cache read so settlement can occur before the caller
// handles its miss. This makes the lost-update interleaving deterministic.
type pausedStatsRead[V any] struct {
	cachelib.Cache[int, V]
	once         sync.Once
	read, resume chan struct{}
}

func (c *pausedStatsRead[V]) Get(id int) (V, bool) {
	v, ok := c.Cache.Get(id)
	c.once.Do(func() { close(c.read); <-c.resume })
	return v, ok
}

func checkStatsReadInterleaving[V any](t *testing.T, slot *cachelib.Cache[int, V], get func(), update func() error, total func() float64) {
	t.Helper()
	original := *slot
	paused := &pausedStatsRead[V]{Cache: original, read: make(chan struct{}), resume: make(chan struct{})}
	*slot = paused
	defer func() { *slot = original }()
	done := make(chan struct{})
	go func() { defer close(done); get() }()
	select {
	case <-paused.read:
	case <-time.After(5 * time.Second):
		close(paused.resume)
		<-done
		t.Fatal("reader did not reach cache")
	}
	err := update()
	close(paused.resume)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if got := total(); got != 2 {
		t.Fatalf("settled cost overwritten: got %v, want 2", got)
	}
}

func TestStatsReadDoesNotOverwriteConcurrentSettlement(t *testing.T) {
	resetStatsTestState(t)
	t.Run("channel", func(t *testing.T) {
		checkStatsReadInterleaving(t, &statsChannelCache,
			func() { StatsChannelGet(701) },
			func() error { return StatsChannelUpdate(701, model.StatsMetrics{InputCost: 2}) },
			func() float64 { return StatsChannelGet(701).InputCost })
	})
	t.Run("api_key", func(t *testing.T) {
		apiKeyCache.Set(702, model.APIKey{ID: 702, Enabled: true})
		defer apiKeyCache.Del(702)
		checkStatsReadInterleaving(t, &statsAPIKeyCache,
			func() { StatsAPIKeyGet(702) },
			func() error { return StatsAPIKeyUpdate(702, model.StatsMetrics{InputCost: 2}) },
			func() float64 { return StatsAPIKeyGet(702).InputCost })
	})
}

func TestMissingStatsReadsDoNotCreateRecords(t *testing.T) {
	resetStatsTestState(t)
	if StatsAPIKeyGet(703).APIKeyID != 703 || StatsChannelGet(704).ChannelID != 704 {
		t.Fatal("missing identity in empty stats")
	}
	if statsAPIKeyCache.Len() != 0 || statsChannelCache.Len() != 0 {
		t.Fatal("read populated statistics")
	}
	if len(statsAPIKeyCacheNeedUpdate) != 0 || len(statsChannelCacheNeedUpdate) != 0 {
		t.Fatal("read scheduled a database write")
	}
}
