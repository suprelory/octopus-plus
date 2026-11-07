package op

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

const relayLogStreamTokenTTL = 30 * time.Second

var ErrRelayLogSessionExpired = errors.New("administrator session has expired")

type relayLogStreamToken struct {
	issuedAt   time.Time
	expiresAt  time.Time
	generation uint64
}

// One lock covers token consumption, subscription registration and revocation.
// When both are needed, userCacheLock must be acquired before this lock.
var relayLogStreamsLock sync.Mutex
var relayLogStreamGeneration uint64
var relayLogStreamTokens = make(map[string]relayLogStreamToken)
var relayLogSubscribers = make(map[chan model.RelayLog]*RelayLogSubscription)

type RelayLogSubscription struct {
	logs      chan model.RelayLog
	done      chan struct{}
	expiresAt time.Time
	timer     *time.Timer
}

func (s *RelayLogSubscription) Logs() <-chan model.RelayLog { return s.logs }
func (s *RelayLogSubscription) Done() <-chan struct{}       { return s.done }
func (s *RelayLogSubscription) Close()                      { RelayLogUnsubscribe(s.logs) }
func (s *RelayLogSubscription) Expired() bool {
	return !s.expiresAt.IsZero() && !time.Now().Before(s.expiresAt)
}

func RelayLogStreamTokenCreate() (string, error) {
	return RelayLogStreamTokenCreateForUser(UserGet())
}

// Use the same user snapshot that authenticated the request. An old JWT that
// passed middleware just before a password change cannot mint a new stream.
func RelayLogStreamTokenCreateForUser(user model.User) (string, error) {
	return relayLogStreamTokenCreate(user, time.Time{})
}

// HTTP tickets must carry the verified JWT expiry, separate from the short
// connection-admission TTL. Renewal never extends an already-open stream.
func RelayLogStreamTokenCreateForSession(user model.User, expiresAt time.Time) (string, error) {
	if expiresAt.IsZero() || !time.Now().Before(expiresAt) {
		return "", ErrRelayLogSessionExpired
	}
	return relayLogStreamTokenCreate(user, expiresAt)
}

func relayLogStreamTokenCreate(user model.User, expiresAt time.Time) (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(bytes)

	userCacheLock.RLock()
	defer userCacheLock.RUnlock()
	if user.ID == 0 || user.Password == "" || user.ID != userCache.ID || user.Password != userCache.Password {
		return "", ErrRelayLogSessionExpired
	}
	now := time.Now()
	if !expiresAt.IsZero() && !now.Before(expiresAt) {
		return "", ErrRelayLogSessionExpired
	}
	relayLogStreamsLock.Lock()
	defer relayLogStreamsLock.Unlock()
	relayLogStreamTokensPruneLocked(now)
	relayLogStreamTokens[token] = relayLogStreamToken{issuedAt: now, expiresAt: expiresAt, generation: relayLogStreamGeneration}
	return token, nil
}

func RelayLogStreamTokenVerify(token string) bool {
	relayLogStreamsLock.Lock()
	defer relayLogStreamsLock.Unlock()
	return relayLogStreamTokenValidLocked(token, time.Now())
}

func relayLogStreamTokenValidLocked(token string, now time.Time) bool {
	entry, ok := relayLogStreamTokens[token]
	if !ok {
		return false
	}
	if entry.generation != relayLogStreamGeneration || now.Sub(entry.issuedAt) >= relayLogStreamTokenTTL ||
		(!entry.expiresAt.IsZero() && !now.Before(entry.expiresAt)) {
		delete(relayLogStreamTokens, token)
		return false
	}
	return true
}

func RelayLogStreamTokenRevoke(token string) {
	relayLogStreamsLock.Lock()
	delete(relayLogStreamTokens, token)
	relayLogStreamsLock.Unlock()
}

func relayLogStreamTokensPruneLocked(now time.Time) {
	for token := range relayLogStreamTokens {
		relayLogStreamTokenValidLocked(token, now)
	}
}

// Consume and register atomically so a token admits only one connection and a
// password change cannot fall between verification and subscription creation.
func RelayLogSubscribeWithToken(token string) (*RelayLogSubscription, bool) {
	relayLogStreamsLock.Lock()
	defer relayLogStreamsLock.Unlock()
	if !relayLogStreamTokenValidLocked(token, time.Now()) {
		return nil, false
	}
	expiresAt := relayLogStreamTokens[token].expiresAt
	delete(relayLogStreamTokens, token)
	sub := relayLogSubscribeLocked()
	sub.expiresAt = expiresAt
	if !expiresAt.IsZero() {
		sub.timer = time.AfterFunc(time.Until(expiresAt), sub.Close)
	}
	return sub, true
}

func relayLogSubscribeLocked() *RelayLogSubscription {
	sub := &RelayLogSubscription{logs: make(chan model.RelayLog, 10), done: make(chan struct{})}
	relayLogSubscribers[sub.logs] = sub
	return sub
}

// RelayLogSubscribe is for trusted in-process consumers. HTTP subscriptions
// must use RelayLogSubscribeWithToken.
func RelayLogSubscribe() chan model.RelayLog {
	relayLogStreamsLock.Lock()
	defer relayLogStreamsLock.Unlock()
	return relayLogSubscribeLocked().logs
}

func RelayLogUnsubscribe(ch chan model.RelayLog) {
	relayLogStreamsLock.Lock()
	defer relayLogStreamsLock.Unlock()
	relayLogUnsubscribeLocked(ch)
}

func relayLogUnsubscribeLocked(ch chan model.RelayLog) {
	if sub, exists := relayLogSubscribers[ch]; exists {
		delete(relayLogSubscribers, ch)
		if sub.timer != nil {
			sub.timer.Stop()
		}
		close(sub.done)
		close(ch)
	}
}

// Called while publishing changed administrator credentials, before releasing
// userCacheLock. Revocation never waits for a client network write.
func revokeRelayLogStreams() {
	relayLogStreamsLock.Lock()
	defer relayLogStreamsLock.Unlock()
	relayLogStreamGeneration++
	clear(relayLogStreamTokens)
	for ch := range relayLogSubscribers {
		relayLogUnsubscribeLocked(ch)
	}
}

func notifySubscribers(relayLog model.RelayLog) {
	relayLogStreamsLock.Lock()
	defer relayLogStreamsLock.Unlock()
	for ch, sub := range relayLogSubscribers {
		if sub.Expired() {
			relayLogUnsubscribeLocked(ch)
			continue
		}
		select {
		case ch <- relayLog:
		default:
		}
	}
}
