package sitesync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestDoneHubManualVerificationEnablesOnlyThatSite(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/checkin" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer checkin-secret-value" {
			t.Errorf("verification did not use the management checkin API: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"reward":3}}`))
	}))
	defer server.Close()
	site, account := createCheckinFixture(t, ctx, server.URL)
	doneHub := model.SitePlatformDoneHub
	site, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, Platform: &doneHub}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	other := &model.Site{Name: "Independent DoneHub", Kind: model.SiteKindCheckin, Platform: doneHub, BaseURL: server.URL, Enabled: true}
	if err := op.SiteCreate(other, ctx); err != nil {
		t.Fatal(err)
	}

	skipped, err := checkinAccountWithTrigger(ctx, account.ID, SiteBatchTriggerManual)
	if err != nil || skipped.Reason != model.SiteCheckinReasonDefaultDisabled || calls.Load() != 0 {
		t.Fatalf("batch ignored conservative default: %+v, %v", skipped, err)
	}
	auto := false
	if _, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: account.ID, AutoCheckin: &auto}, ctx); err != nil {
		t.Fatal(err)
	}
	result, err := CheckinAccount(ctx, account.ID)
	if err != nil || result.Status != model.SiteExecutionStatusSuccess || calls.Load() != 1 ||
		result.Capability == nil || !result.Capability.Enabled || result.Capability.Support != model.SiteCheckinSupportSupported {
		t.Fatalf("manual verification did not reach DoneHub: %+v, %v", result, err)
	}
	savedAccount, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || savedAccount.AutoCheckin || savedAccount.NextAutoCheckinAt != nil {
		t.Fatalf("manual verification changed account preference: %+v, %v", savedAccount, err)
	}
	site, err = op.SiteGet(site.ID, ctx)
	if err != nil {
		t.Fatal(err)
	}
	other, err = op.SiteGet(other.ID, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if site.CheckinMode != model.SiteCheckinModeAuto || !site.ResolveCheckinCapability().Enabled || other.ResolveCheckinCapability().Enabled {
		t.Fatal("verification was not scoped to the concrete site")
	}
	site.Accounts[0].AutoCheckin = true
	if len(eligibleCheckinAccounts([]model.Site{*site, *other})) != 1 {
		t.Fatal("verified DoneHub was still excluded from batches")
	}
	if next := buildNextAutoCheckinAt(site, &site.Accounts[0], time.Now()); next == nil {
		t.Fatal("verified DoneHub was still excluded from scheduling")
	}
}

func TestInstanceCheckinPolicyAndCustomHTTPAdapters(t *testing.T) {
	for _, tc := range []struct {
		name              string
		platform          model.SitePlatform
		mode              model.SiteCheckinMode
		custom, shouldRun bool
		reason            string
	}{
		{"enabled DoneHub", model.SitePlatformDoneHub, model.SiteCheckinModeEnabled, false, true, ""},
		{"disabled DoneHub", model.SitePlatformDoneHub, model.SiteCheckinModeDisabled, false, false, model.SiteCheckinReasonDisabled},
		{"custom Sub2API", model.SitePlatformSub2API, model.SiteCheckinModeAuto, true, true, ""},
		{"custom API", model.SitePlatformAPI, model.SiteCheckinModeAuto, true, true, ""},
		{"disabled overrides custom", model.SitePlatformAPI, model.SiteCheckinModeDisabled, true, false, model.SiteCheckinReasonDisabled},
		{"unconfigured API", model.SitePlatformAPI, model.SiteCheckinModeAuto, false, false, model.SiteCheckinReasonNotConfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := "/api/user/checkin"
				if tc.custom {
					path = "/daily"
				}
				if r.URL.Path != path {
					http.NotFound(w, r)
					return
				}
				calls.Add(1)
				_, _ = w.Write([]byte(`{"success":true}`))
			}))
			defer server.Close()
			site, account := createCheckinFixture(t, ctx, server.URL)
			path := "/daily"
			site, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, Platform: &tc.platform, CheckinMode: &tc.mode, CheckinHTTPEnabled: &tc.custom, CheckinHTTPPath: &path}, ctx)
			if err != nil {
				t.Fatal(err)
			}
			if tc.custom {
				setCheckinTestCookie(t, ctx, account.ID)
			}
			result, err := checkinAccountWithTrigger(ctx, account.ID, SiteBatchTriggerManual)
			if err != nil || result == nil {
				t.Fatalf("unexpected error: %+v, %v", result, err)
			}
			if tc.shouldRun {
				if calls.Load() != 1 || result.Capability.Support != model.SiteCheckinSupportSupported {
					t.Fatalf("configured adapter did not execute: %+v", result)
				}
			} else {
				if result.Reason != tc.reason || calls.Load() != 0 {
					t.Fatalf("policy was ignored: %+v", result)
				}
				manual, err := CheckinAccount(ctx, account.ID)
				if err != nil || manual.Reason != tc.reason || calls.Load() != 0 {
					t.Fatalf("manual action bypassed explicit policy: %+v, %v", manual, err)
				}
				saved, err := op.SiteAccountGet(account.ID, ctx)
				if err != nil || saved.LastCheckinAt != nil || saved.CheckinFailureCount != 0 || saved.NextAutoCheckinAt != nil {
					t.Fatalf("policy skip changed execution state: %+v, %v", saved, err)
				}
				if len(eligibleCheckinAccounts([]model.Site{*site})) != 0 {
					t.Fatal("disabled site was eligible")
				}
			}
		})
	}
}

func TestCheckinCapabilityEvidenceRequiresConclusiveResponse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		httpStatus int
		custom     bool
		want       model.SiteCheckinSupport
	}{
		{"success", `{"success":true}`, 200, false, model.SiteCheckinSupportSupported},
		{"already", `{"success":false,"message":"今天已经签到过了"}`, 200, false, model.SiteCheckinSupportSupported},
		{"endpoint missing", `{"message":"not found"}`, 404, false, model.SiteCheckinSupportUnsupported},
		{"feature disabled", `{"success":false,"message":"签到功能未启用"}`, 200, false, model.SiteCheckinSupportUnsupported},
		{"unauthorized", `{"message":"not found"}`, 401, false, model.SiteCheckinSupportUnknown},
		{"forbidden", `{"message":"checkin is disabled"}`, 403, false, model.SiteCheckinSupportUnknown},
		{"rate limit", `{"message":"not found"}`, 429, false, model.SiteCheckinSupportUnknown},
		{"server error", `{"message":"not found"}`, 500, false, model.SiteCheckinSupportUnknown},
		{"missing account", `{"message":"account not found"}`, 404, false, model.SiteCheckinSupportUnknown},
		{"account disabled", `{"success":false,"message":"Account already disabled"}`, 200, false, model.SiteCheckinSupportUnknown},
		{"business rate limit", `{"success":false,"message":"签到过于频繁，请稍后再试"}`, 200, false, model.SiteCheckinSupportUnknown},
		{"management HTML login", "<html>Login</html>", 200, false, model.SiteCheckinSupportUnknown},
		{"management empty JSON", "{}", 200, false, model.SiteCheckinSupportUnknown},
		{"custom explicit success", `{"success":true}`, 200, true, model.SiteCheckinSupportSupported},
		{"custom HTML login", "<html>Login</html>", 200, true, model.SiteCheckinSupportUnknown},
		{"custom empty JSON", "{}", 200, true, model.SiteCheckinSupportUnknown},
		{"custom generic text", "OK", 200, true, model.SiteCheckinSupportUnknown},
		{"custom empty response", "", 204, true, model.SiteCheckinSupportUnknown},
		{"custom auth failure with misleading text", `{"message":"already checked in today"}`, 401, true, model.SiteCheckinSupportUnknown},
		{"custom missing endpoint", `{"message":"not found"}`, 404, true, model.SiteCheckinSupportUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/user/checkin" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tc.httpStatus)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			site, account := createCheckinFixture(t, ctx, server.URL)
			platform, path := model.SitePlatformDoneHub, "/api/user/checkin"
			_, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, Platform: &platform, CheckinHTTPEnabled: &tc.custom, CheckinHTTPPath: &path}, ctx)
			if err != nil {
				t.Fatal(err)
			}
			if tc.custom {
				setCheckinTestCookie(t, ctx, account.ID)
			}
			result, _ := CheckinAccount(ctx, account.ID)
			if result == nil || result.Capability == nil || result.Capability.Support != tc.want {
				t.Fatalf("response misclassified: %+v", result)
			}
			saved, err := op.SiteGet(site.ID, ctx)
			if err != nil {
				t.Fatal(err)
			}
			if saved.CheckinVerificationStatus != tc.want || (saved.CheckinVerifiedAt != nil) != (tc.want != model.SiteCheckinSupportUnknown) {
				t.Fatalf("invalid persisted evidence: %+v", saved)
			}
		})
	}
}

func TestUnsupportedSiteCanBeReverifiedAndUnknownFailurePreservesEvidence(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var state atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/checkin" {
			http.NotFound(w, r)
			return
		}
		switch state.Load() {
		case 0:
			http.NotFound(w, r)
		case 1:
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()
	site, account := createCheckinFixture(t, ctx, server.URL)
	first, err := CheckinAccount(ctx, account.ID)
	if err != nil || first.Capability.Enabled || first.Capability.Support != model.SiteCheckinSupportUnsupported {
		t.Fatalf("missing negative evidence: %+v, %v", first, err)
	}
	saved, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || saved.NextAutoCheckinAt != nil {
		t.Fatalf("unsupported site retained schedule: %+v, %v", saved, err)
	}
	state.Store(1)
	second, err := CheckinAccount(ctx, account.ID)
	if err != nil || !second.Capability.Enabled || second.Capability.Support != model.SiteCheckinSupportSupported {
		t.Fatalf("reverification was blocked: %+v, %v", second, err)
	}
	state.Store(2)
	third, err := CheckinAccount(ctx, account.ID)
	if err == nil || third.Capability.Support != model.SiteCheckinSupportSupported || !third.Capability.VerifiedAt.Equal(*second.Capability.VerifiedAt) {
		t.Fatalf("auth failure erased conclusive evidence: %+v, %v", third, err)
	}
	site, err = op.SiteGet(site.ID, ctx)
	if err != nil || !site.ResolveCheckinCapability().Enabled {
		t.Fatalf("transient failure disabled site: %+v, %v", site, err)
	}
}

func TestLateCheckinResponseCannotVerifyEditedConfiguration(t *testing.T) {
	ctx := setupProjectTestDB(t)
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/user/checkin") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/api/user/checkin" {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()
	defer unblock()
	site, account := createCheckinFixture(t, ctx, server.URL)
	platform := model.SitePlatformDoneHub
	if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, Platform: &platform}, ctx); err != nil {
		t.Fatal(err)
	}
	outcome := make(chan *model.SiteCheckinResult, 1)
	go func() { result, _ := CheckinAccount(runCtx, account.ID); outcome <- result }()
	select {
	case <-entered:
	case <-runCtx.Done():
		t.Fatal("request never reached server")
	}
	newURL := server.URL + "/v2"
	if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, BaseURL: &newURL}, ctx); err != nil {
		t.Fatal(err)
	}
	unblock()
	result := <-outcome
	if result == nil || result.Status != model.SiteExecutionStatusSuccess || result.Capability.Support != model.SiteCheckinSupportUnknown || result.Capability.Enabled {
		t.Fatalf("late response verified new config: %+v", result)
	}
	var saved model.Site
	if err := db.GetDB().First(&saved, site.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.CheckinVerifiedAt != nil {
		t.Fatal("old request persisted evidence for new URL")
	}
	next, err := CheckinAccount(ctx, account.ID)
	if err != nil || !next.Capability.Enabled {
		t.Fatalf("new URL could not be verified: %+v, %v", next, err)
	}
}

func TestCanceledCheckinDoesNotDecideCapability(t *testing.T) {
	ctx := setupProjectTestDB(t)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	_, account := createCheckinFixture(t, ctx, server.URL)
	result, err := CheckinAccount(runCtx, account.ID)
	if err == nil || result == nil || result.Capability.Support != model.SiteCheckinSupportUnknown {
		t.Fatalf("cancellation decided support: %+v, %v", result, err)
	}
}

func TestCheckinStillRecordsOutcomeAfterSiteDeletion(t *testing.T) {
	ctx := setupProjectTestDB(t)
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/checkin" {
			http.NotFound(w, r)
			return
		}
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()
	defer unblock()
	site, account := createCheckinFixture(t, ctx, server.URL)
	outcome := make(chan *model.SiteCheckinResult, 1)
	go func() { result, _ := CheckinAccount(runCtx, account.ID); outcome <- result }()
	select {
	case <-entered:
	case <-runCtx.Done():
		t.Fatal("request never reached server")
	}
	if err := op.SiteDel(site.ID, ctx); err != nil {
		t.Fatal(err)
	}
	unblock()
	result := <-outcome
	if result == nil || result.Status != model.SiteExecutionStatusSuccess || result.LogID == 0 {
		t.Fatalf("deletion discarded upstream outcome: %+v", result)
	}
	logs, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{SiteID: site.ID})
	if err != nil || len(logs.Items) != 1 || logs.Items[0].SiteName != site.Name {
		t.Fatalf("missing historical outcome: %+v, %v", logs, err)
	}
}

func TestAnyRouterChecksFallbacksBeforeRecordingUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name         string
		signInStatus int
		body         string
		want         model.SiteCheckinSupport
	}{
		{"fallback works", 200, `{"success":true}`, model.SiteCheckinSupportSupported},
		{"fallback auth uncertain", 403, `{"message":"forbidden"}`, model.SiteCheckinSupportUnknown},
		{"fallback null is uncertain", 200, "null", model.SiteCheckinSupportUnknown},
		{"all routes absent", 404, `{"message":"not found"}`, model.SiteCheckinSupportUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/user/self":
					_, _ = w.Write([]byte(`{"success":true,"data":{"id":1}}`))
				case "/api/user/sign_in":
					w.WriteHeader(tc.signInStatus)
					_, _ = w.Write([]byte(tc.body))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			result, _, err := checkinAccountState(context.Background(), &model.Site{Platform: model.SitePlatformAnyRouter, BaseURL: server.URL}, &model.SiteAccount{CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "fake-token"}, nil)
			if tc.want == model.SiteCheckinSupportUnknown {
				if err == nil || result != nil {
					t.Fatalf("uncertain fallback decided support: %+v, %v", result, err)
				}
			} else if err != nil || result == nil || result.CapabilityEvidence != tc.want {
				t.Fatalf("fallback result: %+v, %v", result, err)
			}
		})
	}
}
