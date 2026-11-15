package op

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func independentCheckinStore(t *testing.T) *CheckinStore {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "checkin.db")), &gorm.Config{Logger: logger.Discard})
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
	if err := dbpkg.PrepareCheckinSchema(conn); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sites", "site_accounts", "site_tokens", "site_models", "site_channel_bindings", "channels"} {
		if conn.Migrator().HasTable(name) {
			t.Fatalf("independent schema created %s", name)
		}
	}
	return NewCheckinStore(conn)
}

func testCheckinCredential() model.CheckinCredential {
	return model.CheckinCredential{Name: "shared login", Type: model.CheckinCredentialAccessToken,
		Platform: model.SitePlatformSub2API, AuthBaseURL: "https://login.example", AccessToken: "access", RefreshToken: "refresh"}
}

func createIndependentSite(t *testing.T, store *CheckinStore) *model.CheckinSite {
	t.Helper()
	site, err := store.CreateSite(context.Background(), model.CheckinSite{Name: "checkin", Platform: model.SitePlatformSub2API,
		BaseURL: "https://checkin.example", CheckinHTTPEnabled: true, CheckinHTTPPath: "/checkin", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	return site
}

func TestIndependentCheckinAtomicCreationAndReferences(t *testing.T) {
	store := independentCheckinStore(t)
	ctx := context.Background()
	site := createIndependentSite(t, store)
	credential := testCheckinCredential()
	account, err := store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: "one", AuthMode: model.CheckinAuthBearer, Enabled: false, AutoCheckin: false}, &credential)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: "two", AuthMode: model.CheckinAuthBearer, CredentialID: account.CredentialID, Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetAccount(ctx, account.ID)
	if err != nil || stored.Enabled || stored.AutoCheckin {
		t.Fatalf("false settings lost: %+v %v", stored, err)
	}
	storedSite, err := store.GetSite(ctx, site.ID)
	if err != nil || storedSite.Enabled {
		t.Fatalf("disabled site enabled: %+v %v", storedSite, err)
	}
	if err := store.DeleteCredential(ctx, account.CredentialID); !errors.Is(err, ErrCheckinCredentialInUse) {
		t.Fatalf("shared credential deleted: %v", err)
	}
	if err := store.DeleteAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCredential(ctx, second.CredentialID); !errors.Is(err, ErrCheckinCredentialInUse) {
		t.Fatalf("remaining account lost its credential: %v", err)
	}
	// An invalid account rolls its inline credential creation back.
	_, err = store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: "invalid", AuthMode: model.CheckinAuthCookie}, &credential)
	if err == nil {
		t.Fatal("mismatched Cookie and Access Token accepted")
	}
	credentials, err := store.ListCredentials(ctx)
	if err != nil || len(credentials) != 1 {
		t.Fatalf("inline credential was not rolled back: %d %v", len(credentials), err)
	}
	if err := store.DeleteAccount(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCredential(ctx, second.CredentialID); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentCheckinBalanceCredentialReferences(t *testing.T) {
	store := independentCheckinStore(t)
	ctx := context.Background()
	site := createIndependentSite(t, store)
	balance, err := store.CreateCredential(ctx, testCheckinCredential())
	if err != nil {
		t.Fatal(err)
	}
	cookie := model.CheckinCredential{Name: "external cookie", Type: model.CheckinCredentialCookie, Platform: model.SitePlatformAPI, AuthBaseURL: site.BaseURL, Cookie: "session=cookie"}
	account, err := store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: "external", AuthMode: model.CheckinAuthCookie,
		BalanceConfig: model.CheckinBalanceConfig{Mode: model.CheckinBalanceCustom, Platform: model.SitePlatformSub2API, BaseURL: balance.AuthBaseURL, CredentialID: &balance.ID}}, &cookie)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.CredentialReferences(ctx, balance.ID)
	if err != nil || len(refs.PrimaryAccountIDs) != 0 || len(refs.BalanceAccountIDs) != 1 || refs.BalanceAccountIDs[0] != account.ID {
		t.Fatalf("balance reference missing: %+v %v", refs, err)
	}
	if err := store.DeleteCredential(ctx, balance.ID); !errors.Is(err, ErrCheckinCredentialInUse) {
		t.Fatalf("balance credential deleted: %v", err)
	}
	changed := *balance
	changed.Type, changed.Cookie = model.CheckinCredentialCookie, "session=changed"
	if _, err := store.EditCredential(ctx, changed, balance.CredentialVersion); err == nil {
		t.Fatal("credential edit broke dependent balance query")
	}
}

func TestIndependentCheckinSessionOwnershipAndVersions(t *testing.T) {
	store := independentCheckinStore(t)
	ctx := context.Background()
	credential, err := store.CreateCredential(ctx, testCheckinCredential())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claims := make(chan *CheckinRefreshClaim, 2)
	errorsSeen := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			claim, err := store.ClaimRefresh(ctx, credential.ID, 0, time.Minute)
			claims <- claim
			errorsSeen <- err
		})
	}
	wg.Wait()
	close(claims)
	close(errorsSeen)
	var claim *CheckinRefreshClaim
	count := 0
	for candidate := range claims {
		if candidate != nil {
			claim = candidate
			count++
		}
	}
	if count != 1 {
		t.Fatalf("refresh owners = %d", count)
	}
	for err := range errorsSeen {
		if err != nil && !errors.Is(err, ErrCheckinRefreshLease) {
			t.Fatal(err)
		}
	}
	// A metadata edit during rotation must survive the token commit.
	edit := *credential
	edit.Name = "renamed"
	updated, err := store.EditCredential(ctx, edit, 0)
	if err != nil {
		t.Fatal(err)
	}
	if updated.IdentityRevision != 0 || updated.TokenRefreshLease != claim.Lease {
		t.Fatal("metadata edit changed identity or released active lease")
	}
	pair := CheckinTokenPair{AccessToken: "rotated-access", RefreshToken: "rotated-refresh", TokenExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	rotated, err := store.CommitRefresh(ctx, *claim, pair)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.CredentialVersion != 2 || rotated.IdentityRevision != 0 || rotated.Name != "renamed" || rotated.AccessToken != pair.AccessToken || rotated.RefreshToken != pair.RefreshToken || rotated.TokenRefreshLease != "" {
		t.Fatalf("invalid rotated state: version=%d identity=%d name=%s", rotated.CredentialVersion, rotated.IdentityRevision, rotated.Name)
	}
	if _, err := store.EditCredential(ctx, edit, 0); !errors.Is(err, ErrCheckinCredentialConflict) {
		t.Fatalf("stale editor overwrote refresh: %v", err)
	}
	if replayed, err := store.CommitRefresh(ctx, *claim, pair); err != nil || replayed.CredentialVersion != 2 {
		t.Fatalf("save acknowledgement recovery failed: %v", err)
	}
}

func TestIndependentCheckinRefreshCannotReplaceNewIdentity(t *testing.T) {
	store := independentCheckinStore(t)
	ctx := context.Background()
	credential, err := store.CreateCredential(ctx, testCheckinCredential())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRefresh(ctx, credential.ID, 0, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	edit := *credential
	edit.AccessToken, edit.RefreshToken = "manual-access", "manual-refresh"
	updated, err := store.EditCredential(ctx, edit, 0)
	if err != nil {
		t.Fatal(err)
	}
	if updated.IdentityRevision != 1 || updated.TokenRefreshLease != "" {
		t.Fatal("manual identity replacement retained old lease")
	}
	if _, err := store.CommitRefresh(ctx, *claim, CheckinTokenPair{AccessToken: "late-access", RefreshToken: "late-refresh"}); !errors.Is(err, ErrCheckinCredentialConflict) {
		t.Fatalf("late refresh replaced manual identity: %v", err)
	}
	stored, err := store.GetCredential(ctx, credential.ID)
	if err != nil || stored.AccessToken != "manual-access" || stored.RefreshToken != "manual-refresh" {
		t.Fatal("manual credentials lost")
	}
}

func TestIndependentCheckinRefreshPreservesProxyEdits(t *testing.T) {
	store := independentCheckinStore(t)
	ctx := context.Background()
	credential, err := store.CreateCredential(ctx, testCheckinCredential())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRefresh(ctx, credential.ID, 0, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	edit := *credential
	edit.ProxyMode = model.ProxyUsageModeSystem
	updated, err := store.EditCredential(ctx, edit, 0)
	if err != nil {
		t.Fatal(err)
	}
	if updated.IdentityRevision != 1 || updated.TokenRefreshLease != claim.Lease {
		t.Fatal("proxy edit abandoned consumed refresh token")
	}
	rotated, err := store.CommitRefresh(ctx, *claim, CheckinTokenPair{AccessToken: "new-access", RefreshToken: "new-refresh"})
	if err != nil || rotated.ProxyMode != model.ProxyUsageModeSystem || rotated.IdentityRevision != 1 {
		t.Fatalf("refresh lost concurrent settings: %v", err)
	}
}

func TestIndependentCheckinRefreshLeaseCannotBeStolenOrReleased(t *testing.T) {
	store := independentCheckinStore(t)
	ctx := context.Background()
	credential, err := store.CreateCredential(ctx, testCheckinCredential())
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.ClaimRefresh(ctx, credential.ID, 0, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCredential(ctx, credential.ID); !errors.Is(err, ErrCheckinRefreshLease) {
		t.Fatalf("active refresh owner deleted: %v", err)
	}
	if err := store.conn.Model(&model.CheckinCredential{}).Where("id = ?", credential.ID).Update("token_refresh_lease_until", 0).Error; err != nil {
		t.Fatal(err)
	}
	current, err := store.ClaimRefresh(ctx, credential.ID, 0, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseRefresh(ctx, *old); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetCredential(ctx, credential.ID)
	if err != nil || stored.TokenRefreshLease != current.Lease {
		t.Fatal("old owner released replacement lease")
	}
	if _, err := store.CommitRefresh(ctx, *old, CheckinTokenPair{AccessToken: "late-access", RefreshToken: "late-refresh"}); !errors.Is(err, ErrCheckinRefreshLease) {
		t.Fatalf("old owner committed after lease takeover: %v", err)
	}
	if _, err := store.CommitRefresh(ctx, *current, CheckinTokenPair{AccessToken: "new-access", RefreshToken: "new-refresh"}); err != nil {
		t.Fatal(err)
	}
}
