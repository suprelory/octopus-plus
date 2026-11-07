package sitesync

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/turnstile"
)

const turnstileTestSiteKey = "0x4AAAAAAATEST"

type turnstileSite struct {
	server             *httptest.Server
	checkins, statuses atomic.Int32
}

// newTurnstileSite mimics new-api's TurnstileCheck on the check-in route
// (middleware/turnstile-check.go): rejections are HTTP 200 with success=false
// and the English message text. status serves /api/status; the token
// "dropped-token" closes the connection instead of answering.
func newTurnstileSite(t *testing.T, status http.HandlerFunc) *turnstileSite {
	t.Helper()
	site := &turnstileSite{}
	site.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/status":
			site.statuses.Add(1)
			status(w, r)
		case "/api/user/checkin":
			site.checkins.Add(1)
			switch r.URL.Query().Get("turnstile") {
			case "":
				_, _ = w.Write([]byte(`{"success":false,"message":"Turnstile token is empty"}`))
			case "solved-token":
				_, _ = w.Write([]byte(`{"success":true,"message":"签到成功","data":{"reward":1}}`))
			case "dropped-token":
				dropConnection(w, r)
			default:
				_, _ = w.Write([]byte(`{"success":false,"message":"Turnstile verification failed, please refresh and try again!"}`))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"message":"not found"}`))
		}
	}))
	t.Cleanup(site.server.Close)
	return site
}

// publishSiteKey answers /api/status like new-api, which always includes
// turnstile_site_key, empty when the site has none.
func publishSiteKey(siteKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"turnstile_check":true,"turnstile_site_key":"` + siteKey + `"}}`))
	}
}

// dropConnection closes the connection without a response, like a reset or a
// crashed proxy.
func dropConnection(w http.ResponseWriter, r *http.Request) {
	if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
		_ = conn.Close()
	}
}

// newCloudflareSolverStub answers like Cloudflare Solver, or fails task creation
// with errorCode when it is set.
func newCloudflareSolverStub(t *testing.T, siteURL, token, errorCode string, solves *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer sk-solver-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errorId":1,"errorCode":"ERROR_KEY_DOES_NOT_EXIST"}`))
			return
		}
		switch r.URL.Path {
		case "/turnstile":
			solves.Add(1)
			if !strings.HasPrefix(r.URL.Query().Get("url"), siteURL) || r.URL.Query().Get("sitekey") != turnstileTestSiteKey {
				t.Errorf("unexpected solve request: %s", r.URL.RawQuery)
			}
			if errorCode != "" {
				w.WriteHeader(http.StatusPaymentRequired)
				_, _ = w.Write([]byte(`{"errorId":1,"errorCode":"` + errorCode + `","errorDescription":"raw platform text"}`))
				return
			}
			_, _ = w.Write([]byte(`{"errorId":0,"taskId":"task-1"}`))
		case "/result":
			_, _ = w.Write([]byte(`{"errorId":0,"status":"ready","solution":{"token":"` + token + `"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func useTurnstileSolverConfig(t *testing.T, config turnstileSolverConfig) {
	t.Helper()
	previous := loadTurnstileSolverConfig
	loadTurnstileSolverConfig = func() turnstileSolverConfig { return config }
	t.Cleanup(func() { loadTurnstileSolverConfig = previous })
}

func TestCheckinRetriesWithSolvedTurnstileToken(t *testing.T) {
	ctx := setupProjectTestDB(t)
	site := newTurnstileSite(t, publishSiteKey(turnstileTestSiteKey))
	var solves atomic.Int32
	solver := newCloudflareSolverStub(t, site.server.URL, "solved-token", "", &solves)
	useTurnstileSolverConfig(t, turnstileSolverConfig{enabled: true, solvers: []turnstile.Solver{
		{Platform: turnstile.PlatformCloudflareSolver, APIKey: "sk-solver-secret", BaseURL: solver.URL},
	}})
	_, account := createCheckinFixture(t, ctx, site.server.URL)
	result, err := CheckinAccount(ctx, account.ID)
	if err != nil || result == nil || result.Status != model.SiteExecutionStatusSuccess || result.Reason != model.SiteCheckinReasonCheckedIn || result.Reward != "1" {
		t.Fatalf("unexpected outcome: %+v, error=%v", result, err)
	}
	if site.checkins.Load() != 2 || site.statuses.Load() != 1 || solves.Load() != 1 {
		t.Fatalf("checkins=%d statuses=%d solves=%d", site.checkins.Load(), site.statuses.Load(), solves.Load())
	}
}

func TestCheckinTurnstileRejectionUnchangedWhileSolverDisabled(t *testing.T) {
	ctx := setupProjectTestDB(t)
	site := newTurnstileSite(t, publishSiteKey(turnstileTestSiteKey))
	useTurnstileSolverConfig(t, turnstileSolverConfig{})
	_, account := createCheckinFixture(t, ctx, site.server.URL)
	result, err := CheckinAccount(ctx, account.ID)
	if err == nil || result == nil || result.Status != model.SiteExecutionStatusFailed ||
		result.Reason != string(SiteBatchReasonUpstreamBusinessError) || result.Message != "Turnstile token is empty" {
		t.Fatalf("unexpected outcome: %+v, error=%v", result, err)
	}
	if site.checkins.Load() != 1 || site.statuses.Load() != 0 {
		t.Fatalf("checkins=%d statuses=%d", site.checkins.Load(), site.statuses.Load())
	}
}

func TestCheckinTurnstileSolverFailures(t *testing.T) {
	for _, tc := range []struct {
		name, siteKey, token, errorCode, want string
		noSolvers                             bool
		checkins, statuses, solves            int32
	}{
		{name: "no usable platform", siteKey: turnstileTestSiteKey, noSolvers: true, want: "no enabled solver platform has an API key", checkins: 1},
		{name: "missing site key", want: "the site did not publish a Turnstile site key", checkins: 1, statuses: 1},
		{name: "token rejected", siteKey: turnstileTestSiteKey, token: "stale-token", want: "the site rejected the solved token", checkins: 2, statuses: 1, solves: 1},
		{name: "solver error", siteKey: turnstileTestSiteKey, errorCode: "ERROR_ZERO_BALANCE", want: "solving failed: Cloudflare Solver: ERROR_ZERO_BALANCE", checkins: 1, statuses: 1, solves: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			site := newTurnstileSite(t, publishSiteKey(tc.siteKey))
			var solves atomic.Int32
			solver := newCloudflareSolverStub(t, site.server.URL, tc.token, tc.errorCode, &solves)
			config := turnstileSolverConfig{enabled: true}
			if !tc.noSolvers {
				config.solvers = []turnstile.Solver{{Platform: turnstile.PlatformCloudflareSolver, APIKey: "sk-solver-secret", BaseURL: solver.URL}}
			}
			useTurnstileSolverConfig(t, config)
			_, account := createCheckinFixture(t, ctx, site.server.URL)
			result, err := CheckinAccount(ctx, account.ID)
			want := "Turnstile token is empty (Turnstile solver: " + tc.want + ")"
			if err == nil || result == nil || result.Status != model.SiteExecutionStatusFailed ||
				result.Reason != string(SiteBatchReasonUpstreamBusinessError) || result.Message != want {
				t.Fatalf("unexpected outcome: %+v, error=%v", result, err)
			}
			if strings.Contains(result.Message, "sk-solver-secret") || strings.Contains(result.Message, "raw platform text") {
				t.Fatalf("message leaked solver details: %q", result.Message)
			}
			if site.checkins.Load() != tc.checkins || site.statuses.Load() != tc.statuses || solves.Load() != tc.solves {
				t.Fatalf("checkins=%d statuses=%d solves=%d", site.checkins.Load(), site.statuses.Load(), solves.Load())
			}
		})
	}
}

func TestCheckinTurnstileSiteKeyReadFailures(t *testing.T) {
	respond := func(status int, contentType, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}
	challenge := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cf-Mitigated", "challenge")
		respond(http.StatusForbidden, "text/html", "<html><title>Just a moment...</title></html>")(w, r)
	}
	const unreadable = "Turnstile token is empty (Turnstile solver: could not read the site key)"
	// A site that answers without a key keeps the Turnstile failure; only an
	// unreachable site keeps the request's own reason.
	for _, tc := range []struct {
		name   string
		status http.HandlerFunc
		reason SiteBatchReason
		want   string
	}{
		{name: "forbidden", status: respond(http.StatusForbidden, "application/json", `{"success":false,"message":"forbidden"}`), reason: SiteBatchReasonUpstreamBusinessError, want: unreadable},
		{name: "bad gateway page", status: respond(http.StatusBadGateway, "text/html", "<html><title>502 Bad Gateway</title></html>"), reason: SiteBatchReasonUpstreamBusinessError, want: unreadable},
		{name: "html instead of json", status: respond(http.StatusOK, "text/html", "<html><title>New API</title></html>"), reason: SiteBatchReasonUpstreamBusinessError, want: unreadable},
		{name: "no status route", status: respond(http.StatusNotFound, "application/json", `{"success":false,"message":"not found"}`), reason: SiteBatchReasonUpstreamBusinessError, want: unreadable},
		{name: "cloudflare challenge", status: challenge, reason: SiteBatchReasonCloudflareProtection},
		{name: "connection dropped", status: dropConnection, reason: SiteBatchReasonNetworkError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			site := newTurnstileSite(t, tc.status)
			var solves atomic.Int32
			solver := newCloudflareSolverStub(t, site.server.URL, "solved-token", "", &solves)
			useTurnstileSolverConfig(t, turnstileSolverConfig{enabled: true, solvers: []turnstile.Solver{
				{Platform: turnstile.PlatformCloudflareSolver, APIKey: "sk-solver-secret", BaseURL: solver.URL},
			}})
			_, account := createCheckinFixture(t, ctx, site.server.URL)
			result, err := CheckinAccount(ctx, account.ID)
			if err == nil || result == nil || result.Status != model.SiteExecutionStatusFailed || result.Reason != string(tc.reason) {
				t.Fatalf("unexpected outcome: %+v, error=%v", result, err)
			}
			if tc.want != "" && result.Message != tc.want {
				t.Fatalf("message = %q, want %q", result.Message, tc.want)
			}
			if tc.want == "" && strings.Contains(result.Message, "Turnstile solver") {
				t.Fatalf("an unreachable site was reported as a solver failure: %q", result.Message)
			}
			// Transport retries may repeat the status request; the check-in is not retried.
			if site.checkins.Load() != 1 || site.statuses.Load() == 0 || solves.Load() != 0 {
				t.Fatalf("checkins=%d statuses=%d solves=%d", site.checkins.Load(), site.statuses.Load(), solves.Load())
			}
		})
	}
}

func TestCheckinTurnstileRetryErrorRedactsToken(t *testing.T) {
	ctx := setupProjectTestDB(t)
	site := newTurnstileSite(t, publishSiteKey(turnstileTestSiteKey))
	var solves atomic.Int32
	solver := newCloudflareSolverStub(t, site.server.URL, "dropped-token", "", &solves)
	useTurnstileSolverConfig(t, turnstileSolverConfig{enabled: true, solvers: []turnstile.Solver{
		{Platform: turnstile.PlatformCloudflareSolver, APIKey: "sk-solver-secret", BaseURL: solver.URL},
	}})
	_, account := createCheckinFixture(t, ctx, site.server.URL)
	result, err := CheckinAccount(ctx, account.ID)
	// The transport error quotes the retried URL, which carries the token.
	if err == nil || result == nil || result.Status != model.SiteExecutionStatusFailed ||
		result.Reason != string(SiteBatchReasonNetworkError) || !strings.Contains(result.Message, "turnstile=[redacted]") {
		t.Fatalf("unexpected outcome: %+v, error=%v", result, err)
	}
	if strings.Contains(result.Message, "dropped-token") || strings.Contains(err.Error(), "dropped-token") {
		t.Fatalf("token leaked: message=%q error=%v", result.Message, err)
	}
	if site.checkins.Load() != 2 || site.statuses.Load() != 1 || solves.Load() != 1 {
		t.Fatalf("checkins=%d statuses=%d solves=%d", site.checkins.Load(), site.statuses.Load(), solves.Load())
	}
}
