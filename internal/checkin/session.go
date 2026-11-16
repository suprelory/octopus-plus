package checkin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

// Session is an internal authentication snapshot, never an API response.
type Session struct {
	Credential model.CheckinCredential `json:"-"`
}

type sessionPolicy struct {
	workTimeout, exchangeTimeout, saveTimeout, leaseTTL time.Duration
	poll, saveDelay, cooldown, pendingDelay             time.Duration
	saveAttempts                                        int
}

var defaultSessionPolicy = sessionPolicy{
	workTimeout: 70 * time.Second, exchangeTimeout: 30 * time.Second, saveTimeout: 5 * time.Second,
	leaseTTL: 45 * time.Second, poll: 100 * time.Millisecond, saveDelay: 100 * time.Millisecond,
	cooldown: 30 * time.Second, pendingDelay: time.Second, saveAttempts: 3,
}

type pendingSession struct {
	claim op.CheckinSessionClaim
	pair  *op.CheckinTokenPair
	login *op.CheckinLoginSession
}

type sessionFailure struct {
	fingerprint [32]byte
	retryAt     time.Time
	err         error
	pending     *pendingSession
}

// Keep one Sessions instance per store/service lifetime. A second instance or
// process is still coordinated by the durable database claim and dispatch flag.
type Sessions struct {
	store    SessionStore
	auth     Authenticator
	policy   sessionPolicy
	flight   singleflight.Group
	mu       sync.Mutex
	failures map[int]sessionFailure
}

func NewSessions(store SessionStore, auth Authenticator) *Sessions {
	return &Sessions{store: store, auth: auth, policy: defaultSessionPolicy, failures: make(map[int]sessionFailure)}
}

// Ensure loads the current independent credential, refreshing only when due.
// For AgentRouter, it can use an existing session but can never perform login.
func (s *Sessions) Ensure(ctx context.Context, id int) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	original, err := s.store.GetCredential(ctx, id)
	if err != nil {
		return Session{}, ErrSessionUnavailable
	}
	return s.resolve(ctx, *original, false)
}

// RecoverRead prepares a replacement after an authentication rejection. The
// caller may retry one status/balance read with it, never a check-in submission.
// Rejecting the same old snapshot concurrently still rotates only one session.
func (s *Sessions) RecoverRead(ctx context.Context, rejected Session) (Session, error) {
	return s.resolve(ctx, rejected.Credential, true)
}

func (s *Sessions) resolve(ctx context.Context, original model.CheckinCredential, force bool) (Session, error) {
	if original.ID <= 0 {
		return Session{}, ErrUnsupportedSession
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return Session{}, err
		}
		completed := s.flight.DoChan(strconv.Itoa(original.ID), func() (any, error) {
			// A cancelled waiter must not cancel another account's shared exchange.
			workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.policy.workTimeout)
			defer cancel()
			return s.loadOrExchange(workCtx, original, force)
		})
		select {
		case <-ctx.Done():
			return Session{}, ctx.Err()
		case result := <-completed:
			credential, _ := result.Val.(*model.CheckinCredential)
			if credential == nil {
				return Session{}, result.Err
			}
			// Each caller owns its snapshot, including pointers/slices; singleflight
			// results must never be shared as mutable credential state.
			session := Session{Credential: cloneCredential(*credential)}
			if force && (credential.Type != original.Type || credential.IdentityRevision != original.IdentityRevision) {
				return session, ErrSessionChanged
			}
			if result.Err != nil {
				if !force && canFallback(result.Err, *credential) {
					latest, err := s.store.GetCredential(ctx, original.ID)
					if err != nil {
						return Session{}, ErrSessionUnavailable
					}
					if latest.IdentityRevision != credential.IdentityRevision || sessionFingerprint(*latest) != sessionFingerprint(*credential) {
						return Session{}, ErrSessionChanged
					}
					return session, nil
				}
				return session, result.Err
			}
			if force && !sessionAdvanced(*credential, original) {
				// A forced recovery may have joined a no-op proactive read.
				continue
			}
			return session, nil
		}
	}
	return Session{}, ErrSessionBackoff
}

func (s *Sessions) loadOrExchange(ctx context.Context, original model.CheckinCredential, force bool) (*model.CheckinCredential, error) {
	for {
		current, err := s.store.GetCredential(ctx, original.ID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				s.clear(original.ID)
			}
			return nil, ErrSessionUnavailable
		}
		failure, failed := s.failure(*current)
		if failed && failure.pending != nil {
			if time.Now().Before(failure.retryAt) {
				return current, failure.err
			}
			saved, err := s.save(ctx, failure.pending)
			if err == nil {
				s.clear(current.ID)
				return saved, nil
			}
			if errors.Is(err, ErrSessionChanged) {
				s.clear(current.ID)
				return current, err
			}
			s.remember(*current, err, failure.pending, s.policy.pendingDelay)
			return current, err
		}
		if force && (current.Type != original.Type || current.IdentityRevision != original.IdentityRevision) {
			return current, ErrSessionChanged
		}
		advanced := sessionAdvanced(*current, original)
		if advanced && usable(*current, time.Now()) && !current.TokenRefreshStarted {
			return current, nil
		}
		if failed && (failure.retryAt.IsZero() || time.Now().Before(failure.retryAt)) {
			return current, failure.err
		}
		if current.TokenRefreshStarted || current.TokenRefreshLeaseUntil > time.Now().UnixMilli() {
			if current.TokenRefreshLeaseUntil <= time.Now().UnixMilli() {
				return current, ErrSessionUncertain
			}
			if !waitSession(ctx, s.policy.poll) {
				return current, ErrSessionBackoff
			}
			continue
		}
		login := current.Type == model.CheckinCredentialPassword
		needsExchange := force || !usable(*current, time.Now()) || (current.Platform == model.SitePlatformSub2API && current.Type == model.CheckinCredentialAccessToken && refreshDue(*current, time.Now()))
		if !needsExchange {
			return current, nil
		}
		if login && current.Platform == model.SitePlatformAgentRouter {
			return current, ErrLoginRequiresCheckin
		}
		if (login && !current.SupportsSessionLogin()) || (!login && (current.Type != model.CheckinCredentialAccessToken || current.Platform != model.SitePlatformSub2API)) {
			return current, ErrUnsupportedSession
		}
		if !login && current.RefreshToken == "" {
			return current, ErrSessionExpired
		}
		if current.TokenRefreshRetryAt > time.Now().UnixMilli() {
			return current, ErrSessionBackoff
		}
		var claim *op.CheckinSessionClaim
		if login {
			claim, err = s.store.ClaimLogin(ctx, current.ID, current.CredentialVersion, s.policy.leaseTTL)
		} else {
			claim, err = s.store.ClaimRefresh(ctx, current.ID, current.CredentialVersion, s.policy.leaseTTL)
		}
		if errors.Is(err, op.ErrCheckinCredentialConflict) || errors.Is(err, op.ErrCheckinRefreshLease) || errors.Is(err, op.ErrCheckinSessionUncertain) {
			if !waitSession(ctx, s.policy.poll) {
				return current, ErrSessionBackoff
			}
			continue
		}
		if errors.Is(err, op.ErrCheckinSessionBackoff) {
			return current, ErrSessionBackoff
		}
		if err != nil {
			return current, ErrSessionUnavailable
		}
		return s.exchange(ctx, *claim, login)
	}
}

func (s *Sessions) exchange(ctx context.Context, claim op.CheckinSessionClaim, login bool) (*model.CheckinCredential, error) {
	if err := s.store.BeginSessionExchange(ctx, claim); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.policy.saveTimeout)
		defer cancel()
		_ = s.store.ReleaseRefresh(cleanup, claim)
		return &claim.Before, ErrSessionUnavailable
	}
	pending := &pendingSession{claim: claim}
	requestCtx, cancel := context.WithTimeout(ctx, s.policy.exchangeTimeout)
	var err error
	if login {
		var session op.CheckinLoginSession
		session, err = s.auth.Login(requestCtx, claim.Before)
		pending.login = &session
	} else {
		var pair op.CheckinTokenPair
		pair, err = s.auth.Refresh(requestCtx, claim.Before)
		pending.pair = &pair
	}
	cancel()
	if err != nil {
		failure := exchangeFailure(err)
		backoff := time.Duration(0)
		if failure.Failure == ExchangeRetryable {
			backoff = s.policy.cooldown
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.policy.saveTimeout)
			_ = s.store.RetrySessionExchange(cleanup, claim, time.Now().Add(backoff))
			cancel()
		}
		s.remember(claim.Before, failure, nil, backoff)
		return &claim.Before, failure
	}
	saved, err := s.save(ctx, pending)
	if err != nil {
		if !errors.Is(err, ErrSessionChanged) {
			s.remember(claim.Before, err, pending, s.policy.pendingDelay)
		}
		return &claim.Before, err
	}
	s.clear(claim.Before.ID)
	return saved, nil
}

func (s *Sessions) save(ctx context.Context, pending *pendingSession) (*model.CheckinCredential, error) {
	// Independent of waiter cancellation and the exchange timeout, but bounded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.policy.saveTimeout)
	defer cancel()
	for attempt := 0; attempt < s.policy.saveAttempts; attempt++ {
		var credential *model.CheckinCredential
		var err error
		if pending.pair != nil {
			credential, err = s.store.CommitRefresh(ctx, pending.claim, *pending.pair)
		} else {
			credential, err = s.store.CommitLogin(ctx, pending.claim, *pending.login)
		}
		if err == nil {
			return credential, nil
		}
		if errors.Is(err, op.ErrCheckinCredentialConflict) || errors.Is(err, op.ErrCheckinRefreshLease) || errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSessionChanged
		}
		if attempt+1 == s.policy.saveAttempts || !waitSession(ctx, s.policy.saveDelay*time.Duration(1<<attempt)) {
			break
		}
	}
	return nil, ErrSessionSave
}

func (s *Sessions) failure(c model.CheckinCredential) (sessionFailure, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	failure, found := s.failures[c.ID]
	if found && failure.fingerprint != sessionFingerprint(c) {
		delete(s.failures, c.ID)
		return sessionFailure{}, false
	}
	return failure, found
}

func (s *Sessions) remember(c model.CheckinCredential, err error, pending *pendingSession, delay time.Duration) {
	failure := sessionFailure{fingerprint: sessionFingerprint(c), err: err, pending: pending}
	if delay > 0 {
		failure.retryAt = time.Now().Add(delay)
	}
	s.mu.Lock()
	s.failures[c.ID] = failure
	s.mu.Unlock()
}

func (s *Sessions) clear(id int) {
	s.mu.Lock()
	delete(s.failures, id)
	s.mu.Unlock()
}

// Settings edits retain ownership of a received pair. Identity/session changes
// invalidate it. Match the store's session comparison rather than versions.
func sessionFingerprint(c model.CheckinCredential) [32]byte {
	encoded, _ := json.Marshal([]any{c.ID, c.Type, c.Platform, c.AuthBaseURL, c.Username, c.Password, c.AccessToken, c.RefreshToken, c.APIKey, c.Cookie})
	return sha256.Sum256(encoded)
}

func sessionAdvanced(current, original model.CheckinCredential) bool {
	return sessionFingerprint(current) != sessionFingerprint(original) ||
		(current.CredentialVersion > original.CredentialVersion && current.TokenRefreshedAt > original.TokenRefreshedAt)
}

func cloneCredential(c model.CheckinCredential) model.CheckinCredential {
	c.AuthHeaders = append([]model.CustomHeader(nil), c.AuthHeaders...)
	if c.PlatformUserID != nil {
		id := *c.PlatformUserID
		c.PlatformUserID = &id
	}
	if c.ProxyConfigID != nil {
		id := *c.ProxyConfigID
		c.ProxyConfigID = &id
	}
	return c
}

func canFallback(err error, c model.CheckinCredential) bool {
	var failure *ExchangeError
	expires, _ := tokenTimes(c)
	return errors.As(err, &failure) && failure.AllowAccessFallback && c.Type == model.CheckinCredentialAccessToken && c.AccessToken != "" && expires > time.Now().Add(5*time.Second).UnixMilli()
}

func waitSession(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
