package balancer

import (
	"sync"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestSelectAndReserveChannelKeyDistributesConcurrentSelections(t *testing.T) {
	ResetKeyReservations()
	channel := &model.Channel{Keys: []model.ChannelKey{
		{ID: 1, Enabled: true, ChannelKey: "a"},
		{ID: 2, Enabled: true, ChannelKey: "b"},
	}}

	start := make(chan struct{})
	keys := make(chan int, 2)
	releaseAll := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			key, release := SelectAndReserveChannelKey(channel, model.ChannelKeySelectOptions{})
			keys <- key.ID
			<-releaseAll
			release()
		}()
	}
	close(start)
	seen := make(map[int]struct{})
	for i := 0; i < 2; i++ {
		seen[<-keys] = struct{}{}
	}
	close(releaseAll)
	wg.Wait()
	if len(seen) != 2 {
		t.Fatalf("expected concurrent selections to use both keys, got %#v", seen)
	}
	if got := InFlightKeyCount(1) + InFlightKeyCount(2); got != 0 {
		t.Fatalf("expected reservations released, got %d", got)
	}
}

func TestSelectAndReserveChannelKeyHonorsPreferredKey(t *testing.T) {
	ResetKeyReservations()
	channel := &model.Channel{Keys: []model.ChannelKey{
		{ID: 1, Enabled: true, ChannelKey: "a", TotalCost: 0},
		{ID: 2, Enabled: true, ChannelKey: "b", TotalCost: 100},
	}}
	key, release := SelectAndReserveChannelKey(channel, model.ChannelKeySelectOptions{PreferredKeyID: 2})
	defer release()
	if key.ID != 2 {
		t.Fatalf("expected preferred key 2, got %d", key.ID)
	}
}
