package checkin

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeAuth struct {
	refresh func(context.Context, model.CheckinCredential) (op.CheckinTokenPair, error)
	login   func(context.Context, model.CheckinCredential) (op.CheckinLoginSession, error)
}

func (a fakeAuth) Refresh(ctx context.Context, c model.CheckinCredential) (op.CheckinTokenPair, error) {
	if a.refresh == nil {
		return op.CheckinTokenPair{}, ErrUnsupportedSession
	}
	return a.refresh(ctx, c)
}
func (a fakeAuth) Login(ctx context.Context, c model.CheckinCredential) (op.CheckinLoginSession, error) {
	if a.login == nil {
		return op.CheckinLoginSession{}, ErrUnsupportedSession
	}
	return a.login(ctx, c)
}

func sessionStore(t *testing.T) (*op.CheckinStore, *gorm.DB) {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "sessions.db")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	if err := dbpkg.PrepareCheckinSchema(conn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, name := range []string{"sites", "site_accounts", "site_tokens", "site_channel_bindings"} {
			if conn.Migrator().HasTable(name) {
				t.Errorf("session layer created legacy table %s", name)
			}
		}
	})
	return op.NewCheckinStore(conn), conn
}

func newCredential(t *testing.T, store *op.CheckinStore) *model.CheckinCredential {
	t.Helper()
	c, err := store.CreateCredential(context.Background(), model.CheckinCredential{Name: "session", Platform: model.SitePlatformSub2API,
		Type: model.CheckinCredentialAccessToken, AuthBaseURL: "https://auth.example", AccessToken: "access-old", RefreshToken: "refresh-old", TokenExpiresAt: time.Now().Add(-time.Minute).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testSessions(store SessionStore, auth Authenticator) *Sessions {
	s := NewSessions(store, auth)
	s.policy.workTimeout = 3 * time.Second
	s.policy.exchangeTimeout = time.Second
	s.policy.saveTimeout = 200 * time.Millisecond
	s.policy.leaseTTL = 2 * time.Second
	s.policy.poll, s.policy.saveDelay = time.Millisecond, time.Millisecond
	s.policy.pendingDelay = 0
	s.policy.cooldown = time.Second
	return s
}

func rotatedPair() op.CheckinTokenPair {
	return op.CheckinTokenPair{AccessToken: "access-new", RefreshToken: "refresh-new", TokenExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
}

func TestSessionsSharedCredentialRefreshOnceAcrossManagers(t *testing.T) {
	store, _ := sessionStore(t)
	c := newCredential(t, store)
	var count atomic.Int32
	auth := fakeAuth{refresh: func(ctx context.Context, got model.CheckinCredential) (op.CheckinTokenPair, error) {
		count.Add(1)
		if got.RefreshToken != "refresh-old" {
			t.Error("unexpected session re-exchange")
		}
		return rotatedPair(), nil
	}}
	// Two manager instances bypass process-local singleflight and exercise the DB.
	managers := []*Sessions{testSessions(store, auth), testSessions(store, auth)}
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Go(func() {
			var session Session
			var err error
			if i%3 == 0 {
				session, err = managers[i%2].RecoverRead(context.Background(), Session{Credential: *c})
			} else {
				session, err = managers[i%2].Ensure(context.Background(), c.ID)
			}
			if err != nil || session.Credential.AccessToken != "access-new" {
				t.Errorf("shared session resolution failed: %v", err)
			}
		})
	}
	wg.Wait()
	saved, err := store.GetCredential(context.Background(), c.ID)
	if err != nil || count.Load() != 1 || saved.CredentialVersion != 1 || saved.IdentityRevision != 0 || saved.TokenRefreshStarted || saved.TokenRefreshLease != "" {
		t.Fatalf("session ownership invalid: calls=%d err=%v", count.Load(), err)
	}
}

func TestSessionsCancelledWaiterStillSavesIssuedPair(t *testing.T) {
	store, _ := sessionStore(t)
	c := newCredential(t, store)
	started, proceed := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	s := testSessions(store, fakeAuth{refresh: func(ctx context.Context, _ model.CheckinCredential) (op.CheckinTokenPair, error) {
		count.Add(1)
		close(started)
		select {
		case <-proceed:
			return rotatedPair(), nil
		case <-ctx.Done():
			return op.CheckinTokenPair{}, ctx.Err()
		}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Ensure(ctx, c.ID); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter did not leave promptly: %v", err)
	}
	close(proceed)
	if _, err := s.Ensure(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetCredential(context.Background(), c.ID)
	if err != nil || saved.RefreshToken != "refresh-new" || count.Load() != 1 {
		t.Fatalf("cancelled caller lost received tokens: %v", err)
	}
}

type failingStore struct {
	SessionStore
	fail, lostAck atomic.Bool
	saves         atomic.Int32
}

func (s *failingStore) CommitRefresh(ctx context.Context, claim op.CheckinSessionClaim, pair op.CheckinTokenPair) (*model.CheckinCredential, error) {
	s.saves.Add(1)
	if s.fail.Load() {
		return nil, errors.New("synthetic storage outage")
	}
	c, err := s.SessionStore.CommitRefresh(ctx, claim, pair)
	if err == nil && s.lostAck.Swap(false) {
		return nil, errors.New("synthetic lost commit acknowledgement")
	}
	return c, err
}

func TestSessionsPendingPairRetriesSaveWithoutReexchange(t *testing.T) {
	store, conn := sessionStore(t)
	c := newCredential(t, store)
	failing := &failingStore{SessionStore: store}
	failing.fail.Store(true)
	var calls atomic.Int32
	auth := fakeAuth{refresh: func(context.Context, model.CheckinCredential) (op.CheckinTokenPair, error) {
		calls.Add(1)
		return rotatedPair(), nil
	}}
	s := testSessions(failing, auth)
	if _, err := s.Ensure(context.Background(), c.ID); !errors.Is(err, ErrSessionSave) {
		t.Fatalf("save failure hidden: %v", err)
	}
	if failing.saves.Load() != int32(s.policy.saveAttempts) {
		t.Fatal("save attempts were not bounded")
	}
	// Simulate an elapsed lease and another process, without discarding the dispatch flag.
	if err := conn.Model(c).Update("token_refresh_lease_until", 0).Error; err != nil {
		t.Fatal(err)
	}
	other := testSessions(store, auth)
	if _, err := other.Ensure(context.Background(), c.ID); !errors.Is(err, ErrSessionUncertain) {
		t.Fatalf("another manager reused a consumed refresh token: %v", err)
	}
	// A settings edit during the outage must survive the eventual pair save.
	updated, err := store.PatchCredential(context.Background(), c.ID, model.CheckinCredentialPatch{
		ExpectedVersion:     model.CheckinPatchField[int64]{Present: true, Value: 0},
		Name:                model.CheckinPatchField[string]{Present: true, Value: "renamed"},
		CheckinNetworkPatch: model.CheckinNetworkPatch{ProxyMode: model.CheckinPatchField[model.ProxyUsageMode]{Present: true, Value: model.ProxyUsageModeSystem}},
	})
	if err != nil || !updated.TokenRefreshStarted {
		t.Fatalf("settings edit released exchange ownership: %v", err)
	}
	failing.fail.Store(false)
	failing.lostAck.Store(true)
	result, err := s.Ensure(context.Background(), c.ID)
	if err != nil || result.Credential.AccessToken != "access-new" || result.Credential.Name != "renamed" || result.Credential.ProxyMode != model.ProxyUsageModeSystem || result.Credential.IdentityRevision != 1 || result.Credential.CredentialVersion != 2 || calls.Load() != 1 {
		t.Fatalf("save-only recovery or acknowledgement failed: calls=%d err=%v", calls.Load(), err)
	}
}

func TestSessionsManualReplacementWinsOverInFlightRefresh(t *testing.T) {
	store, _ := sessionStore(t)
	c := newCredential(t, store)
	started, release := make(chan struct{}), make(chan struct{})
	s := testSessions(store, fakeAuth{refresh: func(context.Context, model.CheckinCredential) (op.CheckinTokenPair, error) {
		close(started)
		<-release
		return rotatedPair(), nil
	}})
	done := make(chan error, 1)
	go func() { _, err := s.Ensure(context.Background(), c.ID); done <- err }()
	<-started
	replacement := *c
	replacement.AccessToken, replacement.RefreshToken, replacement.TokenExpiresAt = "manual-access", "manual-refresh", time.Now().Add(time.Hour).UnixMilli()
	if _, err := store.EditCredential(context.Background(), replacement, 0); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("late refresh did not report identity replacement: %v", err)
	}
	resolved, err := s.Ensure(context.Background(), c.ID)
	if err != nil || resolved.Credential.AccessToken != "manual-access" || resolved.Credential.RefreshToken != "manual-refresh" {
		t.Fatalf("late refresh replaced manual credentials: %v", err)
	}
	if _, err := s.RecoverRead(context.Background(), Session{Credential: *c}); !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("old workflow silently adopted another identity: %v", err)
	}
}

func TestSessionsBackoffAndAmbiguousResponses(t *testing.T) {
	for _, kind := range []ExchangeFailure{ExchangeRetryable, ExchangeRejected, ExchangeUncertain} {
		t.Run(string(kind), func(t *testing.T) {
			store, conn := sessionStore(t)
			c := newCredential(t, store)
			var count atomic.Int32
			auth := fakeAuth{refresh: func(context.Context, model.CheckinCredential) (op.CheckinTokenPair, error) {
				count.Add(1)
				return op.CheckinTokenPair{}, &ExchangeError{Failure: kind}
			}}
			s := testSessions(store, auth)
			if _, err := s.Ensure(context.Background(), c.ID); err == nil {
				t.Fatal("exchange failure hidden")
			}
			if _, err := s.Ensure(context.Background(), c.ID); err == nil || count.Load() != 1 {
				t.Fatalf("local failure was re-exchanged: calls=%d err=%v", count.Load(), err)
			}
			if err := conn.Model(c).Update("token_refresh_lease_until", 0).Error; err != nil {
				t.Fatal(err)
			}
			other := testSessions(store, auth)
			_, err := other.Ensure(context.Background(), c.ID)
			want := ErrSessionUncertain
			if kind == ExchangeRetryable {
				want = ErrSessionBackoff
			}
			if !errors.Is(err, want) || count.Load() != 1 {
				t.Fatalf("cross-manager guard lost: calls=%d err=%v", count.Load(), err)
			}
		})
	}
}

func TestSessionsPasswordLoginAndAgentRouterGuard(t *testing.T) {
	store, _ := sessionStore(t)
	ctx := context.Background()
	credential, err := store.CreateCredential(ctx, model.CheckinCredential{Name: "login", Platform: model.SitePlatformAnyRouter,
		Type: model.CheckinCredentialPassword, AuthBaseURL: "https://login.example", Username: "user", Password: "password"})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s := testSessions(store, fakeAuth{login: func(context.Context, model.CheckinCredential) (op.CheckinLoginSession, error) {
		calls.Add(1)
		id := 123
		return op.CheckinLoginSession{Cookie: "session=issued", PlatformUserID: &id}, nil
	}})
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			got, err := s.Ensure(ctx, credential.ID)
			if err != nil || got.Credential.Cookie != "session=issued" || got.Credential.PlatformUserID == nil || *got.Credential.PlatformUserID != 123 {
				t.Errorf("login session failed: %v", err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("shared password login count=%d", calls.Load())
	}
	snapshot, err := s.Ensure(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Some deployments issue the same cookie again. Receipt time still proves the
	// rejected session has undergone one login and must not trigger a retry loop.
	if _, err := s.RecoverRead(ctx, snapshot); err != nil || calls.Load() != 2 {
		t.Fatalf("same-cookie login repeated or rejected: calls=%d err=%v", calls.Load(), err)
	}
	agent := *credential
	agent.Name, agent.Platform = "agent", model.SitePlatformAgentRouter
	created, err := store.CreateCredential(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ensure(ctx, created.ID); !errors.Is(err, ErrLoginRequiresCheckin) || calls.Load() != 2 {
		t.Fatalf("read/probe implicitly submitted AgentRouter checkin: %v", err)
	}
	if _, err := store.ClaimLogin(ctx, created.ID, 0, time.Minute); err == nil {
		t.Fatal("store permitted implicit AgentRouter login")
	}
}

func TestSessionsExpirySchedulingAndFallback(t *testing.T) {
	now := time.Now()
	short := model.CheckinCredential{AccessToken: "short", RefreshToken: "refresh", TokenRefreshedAt: now.UnixMilli(), TokenExpiresAt: now.Add(3 * time.Minute).UnixMilli()}
	if refreshDue(short, now) || refreshDue(short, now.Add(2*time.Minute)) || !refreshDue(short, now.Add(150*time.Second)) {
		t.Fatal("short-lived token scheduling causes a refresh loop")
	}
	store, conn := sessionStore(t)
	c := newCredential(t, store)
	if err := conn.Model(c).Update("token_expires_at", now.Add(time.Minute).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s := testSessions(store, fakeAuth{refresh: func(context.Context, model.CheckinCredential) (op.CheckinTokenPair, error) {
		calls.Add(1)
		return op.CheckinTokenPair{}, &ExchangeError{Failure: ExchangeRetryable, AllowAccessFallback: true}
	}})
	first, err := s.Ensure(context.Background(), c.ID)
	if err != nil || first.Credential.AccessToken != "access-old" {
		t.Fatalf("known non-consuming failure lost still-valid access: %v", err)
	}
	if _, err := s.RecoverRead(context.Background(), first); err == nil || calls.Load() != 1 {
		t.Fatalf("forced recovery reused rejected token or ignored cooldown: %v", err)
	}
}

func TestSessionsPasswordEditRetiresOldLogin(t *testing.T) {
	store, _ := sessionStore(t)
	ctx := context.Background()
	id := 123
	c, err := store.CreateCredential(ctx, model.CheckinCredential{Name: "password", Type: model.CheckinCredentialPassword,
		Platform: model.SitePlatformAnyRouter, AuthBaseURL: "https://login.example", Username: "old-user", Password: "old-password",
		Cookie: "session=old-user", AccessToken: "old-access", PlatformUserID: &id})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.PatchCredential(ctx, c.ID, model.CheckinCredentialPatch{
		ExpectedVersion: model.CheckinPatchField[int64]{Present: true, Value: 0},
		Username:        model.CheckinPatchField[string]{Present: true, Value: "new-user"},
		Password:        model.CheckinPatchField[string]{Present: true, Value: "new-password"},
	})
	if err != nil || updated.Cookie != "" || updated.AccessToken != "" || updated.PlatformUserID != nil {
		t.Fatalf("new principal retained the former user's session: %v", err)
	}
	s := testSessions(store, fakeAuth{login: func(_ context.Context, c model.CheckinCredential) (op.CheckinLoginSession, error) {
		if c.Username != "new-user" || c.Password != "new-password" {
			t.Error("login used stale principal")
		}
		return op.CheckinLoginSession{Cookie: "session=new-user"}, nil
	}})
	result, err := s.Ensure(ctx, c.ID)
	if err != nil || result.Credential.Cookie != "session=new-user" || result.Credential.IdentityRevision != 1 {
		t.Fatalf("new principal could not obtain its own session: %v", err)
	}
}

func TestSessionsLoginPreservesConcurrentUserIDEdit(t *testing.T) {
	store, _ := sessionStore(t)
	ctx := context.Background()
	c, err := store.CreateCredential(ctx, model.CheckinCredential{Name: "login", Platform: model.SitePlatformAnyRouter,
		Type: model.CheckinCredentialPassword, AuthBaseURL: "https://auth.example", Username: "user", Password: "password"})
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	s := testSessions(store, fakeAuth{login: func(context.Context, model.CheckinCredential) (op.CheckinLoginSession, error) {
		close(started)
		<-release
		id := 123
		return op.CheckinLoginSession{Cookie: "session=issued", PlatformUserID: &id}, nil
	}})
	done := make(chan error, 1)
	go func() { _, err := s.Ensure(ctx, c.ID); done <- err }()
	<-started
	manualID := 456
	edited, err := store.PatchCredential(ctx, c.ID, model.CheckinCredentialPatch{
		ExpectedVersion: model.CheckinPatchField[int64]{Present: true, Value: 0},
		PlatformUserID:  model.CheckinPatchField[*int]{Present: true, Value: &manualID},
	})
	if err != nil || !edited.TokenRefreshStarted {
		t.Fatalf("user ID edit abandoned in-flight session ownership: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetCredential(ctx, c.ID)
	if err != nil || saved.PlatformUserID == nil || *saved.PlatformUserID != manualID || saved.Cookie != "session=issued" || saved.IdentityRevision != 1 {
		t.Fatalf("login overwrote manual metadata or lost issued session: %v", err)
	}
}
