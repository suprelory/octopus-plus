package op

import (
	"errors"
	"sync"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestAPIKeyCostAdmissionReservesBeforeConcurrentRequests(t *testing.T) {
	ctx := setupChannelSupportTestDB(t)
	if err := InitCache(); err != nil {
		t.Fatal(err)
	}
	key := model.APIKey{Name: "limited", APIKey: "test-budget", Enabled: true, MaxCost: 1}
	if err := APIKeyCreate(&key, ctx); err != nil {
		t.Fatal(err)
	}
	const workers = 16
	start := make(chan struct{})
	results := make(chan *APIKeyCostReservation, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			reservation, err := APIKeyReserveCost(key.ID)
			if err != nil && !errors.Is(err, ErrAPIKeyCostInFlight) {
				t.Errorf("admission: %v", err)
			}
			results <- reservation
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var winner *APIKeyCostReservation
	admitted := 0
	for reservation := range results {
		if reservation != nil {
			admitted++
			winner = reservation
			t.Cleanup(reservation.Release)
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted %d simultaneous requests against the same balance", admitted)
	}
	if err := winner.ReserveAttempt(.75); err != nil {
		t.Fatal(err)
	}
	if err := winner.ReserveAttempt(.5); !errors.Is(err, ErrAPIKeyCostExceeded) {
		t.Fatalf("retry bypassed remaining budget: %v", err)
	}
	if err := StatsAPIKeyUpdate(key.ID, model.StatsMetrics{InputCost: 1}); err != nil {
		t.Fatal(err)
	}
	winner.Release()
	winner.Release()
	if reservation, err := APIKeyReserveCost(key.ID); !errors.Is(err, ErrAPIKeyCostExceeded) {
		reservation.Release()
		t.Fatalf("settled usage was not charged before release: %v", err)
	}
}

func TestAPIKeyCostReservationSurvivesRefreshAndLimitChanges(t *testing.T) {
	ctx := setupChannelSupportTestDB(t)
	if err := InitCache(); err != nil {
		t.Fatal(err)
	}
	key := model.APIKey{Name: "changing", APIKey: "test-changing-budget", Enabled: true}
	if err := APIKeyCreate(&key, ctx); err != nil {
		t.Fatal(err)
	}
	first, err := APIKeyReserveCost(key.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := APIKeyReserveCost(key.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	key.MaxCost = 1
	if err := APIKeyUpdate(&key, ctx); err != nil {
		t.Fatal(err)
	}
	if err := RefreshCacheAfterImport(false); err != nil {
		t.Fatal(err)
	}
	if got, err := APIKeyReserveCost(key.ID); !errors.Is(err, ErrAPIKeyCostInFlight) {
		got.Release()
		t.Fatalf("limit change ignored outstanding requests: %v", err)
	}
	first.Release()
	if got, err := APIKeyReserveCost(key.ID); !errors.Is(err, ErrAPIKeyCostInFlight) {
		got.Release()
		t.Fatalf("released another request's reservation: %v", err)
	}
	second.Release()
	limited, err := APIKeyReserveCost(key.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Release()
	if err := APIKeyDelete(key.ID, ctx); err != nil {
		t.Fatal(err)
	}
	if err := limited.ReserveAttempt(.1); !errors.Is(err, ErrAPIKeyUnavailable) {
		t.Fatalf("deleted key can still submit: %v", err)
	}
}
