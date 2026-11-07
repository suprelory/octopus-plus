package balancer

import (
	"sync"

	"github.com/suprelory/octopus-plus/internal/model"
)

// keyReservationStore tracks only process-local in-flight work. Persisted key
// cost remains the source of truth for long-term balancing; reservations keep a
// burst from selecting the same otherwise-idle key before its cost is updated.
type keyReservationStore struct {
	mu       sync.Mutex
	reserved map[int]int
}

var keyReservations = keyReservationStore{reserved: make(map[int]int)}

func (s *keyReservationStore) count(keyID int) int {
	if keyID <= 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserved[keyID]
}

func (s *keyReservationStore) release(keyID int) {
	if keyID <= 0 {
		return
	}
	s.mu.Lock()
	if count := s.reserved[keyID]; count <= 1 {
		delete(s.reserved, keyID)
	} else {
		s.reserved[keyID] = count - 1
	}
	s.mu.Unlock()
}

// InFlightKeyCount is suitable for model.ChannelKeySelectOptions.
func InFlightKeyCount(keyID int) int {
	return keyReservations.count(keyID)
}

// SelectAndReserveChannelKey performs key scoring and reservation under one
// lock. This closes the check-then-reserve race that otherwise lets concurrent
// requests select the same lowest-cost key.
func SelectAndReserveChannelKey(channel *model.Channel, opts model.ChannelKeySelectOptions) (model.ChannelKey, func()) {
	if channel == nil {
		return model.ChannelKey{}, func() {}
	}
	if opts.ExcludeKeyIDs == nil {
		opts.ExcludeKeyIDs = make(map[int]struct{})
	}
	keyReservations.mu.Lock()
	defer keyReservations.mu.Unlock()
	opts.InFlightCount = func(keyID int) int { return keyReservations.reserved[keyID] }
	if opts.InFlightPenalty <= 0 {
		opts.InFlightPenalty = 1
	}
	key := channel.GetChannelKey(opts)
	if key.ChannelKey == "" {
		return model.ChannelKey{}, func() {}
	}
	keyReservations.reserved[key.ID]++
	var once sync.Once
	return key, func() {
		once.Do(func() { keyReservations.release(key.ID) })
	}
}

// ResetKeyReservations clears runtime state between tests and after a full
// in-process balancer reset.
func ResetKeyReservations() {
	keyReservations.mu.Lock()
	keyReservations.reserved = make(map[int]int)
	keyReservations.mu.Unlock()
}
