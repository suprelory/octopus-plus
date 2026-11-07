package sitesync

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestIndependentCheckinUsesOwnCredentialsAndURL(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var relayRequests, checkinRequests atomic.Int32
	relayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayRequests.Add(1)
		http.Error(w, "checkin must not use the subscription endpoint", http.StatusBadRequest)
	}))
	defer relayServer.Close()
	checkinServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/checkin" {
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "session=checkin-cookie-value" {
				t.Errorf("checkin used another account's credential")
			}
			checkinRequests.Add(1)
			_, _ = w.Write([]byte(`{"success":true,"data":{"reward":2}}`))
			return
		}
		if r.URL.Path == "/api/user/self" {
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":1000000,"used_quota":0}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer checkinServer.Close()
	relay := &model.Site{Name: "Subscription", Platform: model.SitePlatformOneAPI, BaseURL: relayServer.URL, Enabled: true}
	if err := op.SiteCreate(relay, ctx); err != nil {
		t.Fatal(err)
	}
	relayAccount := &model.SiteAccount{SiteID: relay.ID, Name: "Subscription account", CredentialType: model.SiteCredentialTypeAccessToken,
		AccessToken: "subscription-secret", Enabled: true, AutoSync: true, AutoCheckin: true}
	if err := op.SiteAccountCreate(relayAccount, ctx); err != nil {
		t.Fatal(err)
	}
	checkin, account := createCheckinFixture(t, ctx, checkinServer.URL)
	custom, path := true, "/api/user/checkin"
	if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: checkin.ID, LinkedSiteID: &relay.ID, LinkedSiteIDSet: true, CheckinHTTPEnabled: &custom, CheckinHTTPPath: &path}, ctx); err != nil {
		t.Fatal(err)
	}
	setCheckinTestCookie(t, ctx, account.ID)
	if relayAccount.AutoCheckin || account.AutoSync {
		t.Fatal("account creation enabled automation for the other site kind")
	}
	enabled := true
	if updated, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: account.ID, AutoSync: &enabled}, ctx); err != nil || updated.AutoSync {
		t.Fatalf("checkin account allowed automatic sync: %+v, %v", updated, err)
	}
	if updated, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: relayAccount.ID, AutoCheckin: &enabled}, ctx); err != nil || updated.AutoCheckin {
		t.Fatalf("subscription account allowed automatic checkin: %+v, %v", updated, err)
	}
	if _, err := CheckinAccount(ctx, relayAccount.ID); apperror.Code(err) != CodeSiteCheckinSiteRequired {
		t.Fatalf("subscription account accepted a checkin: %v", err)
	}
	if _, err := SyncAccount(ctx, account.ID); apperror.Code(err) != CodeSiteSyncCheckinOnly {
		t.Fatalf("checkin account accepted a sync: %v", err)
	}
	if _, err := PreviewManualSync(ctx, account.ID, ManualSyncRequest{}); apperror.Code(err) != CodeSiteSyncCheckinOnly {
		t.Fatalf("checkin account accepted a manual import: %v", err)
	}
	if _, err := CreateAccountToken(ctx, account.ID, model.SiteChannelKeyCreateRequest{}); apperror.Code(err) != CodeSiteSyncCheckinOnly {
		t.Fatalf("checkin account accepted key creation: %v", err)
	}
	if ids, err := ProjectAccount(ctx, account.ID); err != nil || len(ids) != 0 {
		t.Fatalf("checkin account created managed channels: %v, %v", ids, err)
	}
	if cards, err := op.SiteChannelListWithOptions(ctx, op.SiteChannelListOptions{}); err != nil || len(cards) != 1 || cards[0].SiteID != relay.ID {
		t.Fatalf("checkin site appeared among subscription channels: %+v, %v", cards, err)
	}
	if result, err := CheckinAccount(ctx, account.ID); err != nil || result.Status != model.SiteExecutionStatusSuccess || checkinRequests.Load() != 1 || relayRequests.Load() != 0 {
		t.Fatalf("independent checkin failed: %+v, %v, checkin=%d relay=%d", result, err, checkinRequests.Load(), relayRequests.Load())
	}
	if err := DeleteSite(ctx, relay.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := op.SiteGet(checkin.ID, ctx)
	if err != nil || saved.LinkedSiteID != nil || saved.BaseURL != checkinServer.URL || len(saved.Accounts) != 1 || saved.Accounts[0].AccessToken != account.AccessToken {
		t.Fatalf("deleting subscription affected the independent checkin site: %+v, %v", saved, err)
	}
}

func TestBatchSchedulingSeparatesSiteKinds(t *testing.T) {
	sites := []model.Site{
		{ID: 1, Kind: model.SiteKindRelay, Enabled: true, Platform: model.SitePlatformOneAPI,
			Accounts: []model.SiteAccount{{ID: 10, Enabled: true, AutoCheckin: true, AutoSync: true}}},
		{ID: 2, Kind: model.SiteKindCheckin, Enabled: true, Platform: model.SitePlatformOneAPI,
			Accounts: []model.SiteAccount{{ID: 20, Enabled: true, AutoCheckin: true, AutoSync: true}}},
	}
	checkins, syncs := eligibleCheckinAccounts(sites), eligibleSyncAccounts(sites)
	if len(checkins) != 1 || checkins[0].account.ID != 20 || len(syncs) != 1 || syncs[0].account.ID != 10 {
		t.Fatalf("batch selection mixed site kinds: checkins=%+v syncs=%+v", checkins, syncs)
	}
	if next := buildNextAutoCheckinAt(&sites[0], &sites[0].Accounts[0], time.Now()); next != nil {
		t.Fatalf("subscription account was scheduled for checkin: %v", next)
	}
}
