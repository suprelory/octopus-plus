package sitesync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestConfiguredHTTPCheckinUsesAccountAuthPathHeadersAndBody(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/root/api/checkin" || r.URL.Query().Get("day") != "today" {
			t.Errorf("request target = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if got := r.Header.Get("Cookie"); got != `session=token"slash\` {
			t.Errorf("cookie = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("external checkin sent platform authorization: %q", got)
		}
		if got := r.Header.Get("X-Checkin-User"); got != `Alice "A"` {
			t.Errorf("custom header = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q", got)
		}
		if got := r.Header.Get("Origin"); got != server.URL {
			t.Errorf("origin = %q, want %q", got, server.URL)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if body["token"] != `session=token"slash\` || body["username"] != `Alice "A"` {
			t.Errorf("request body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"reward":"8 points"}}`))
	}))
	defer server.Close()

	site := &model.Site{
		Platform:           model.SitePlatformAPI,
		BaseURL:            server.URL + "/root",
		CheckinHTTPEnabled: true,
		CheckinHTTPMethod:  "POST",
		CheckinHTTPPath:    "/api/checkin?day=today",
		CheckinHTTPBody:    `{"token":"{{cookie}}","username":"{{username}}"}`,
		CheckinHTTPHeaders: []model.CustomHeader{{HeaderKey: "X-Checkin-User", HeaderValue: "{{username}}"}, {HeaderKey: "Cookie", HeaderValue: "obsolete=shared-cookie"}},
	}
	account := &model.SiteAccount{
		CredentialType: model.SiteCredentialTypeCookie,
		Cookie:         `session=token"slash\`,
		AccessToken:    "stale-platform-token",
		Username:       `Alice "A"`,
	}
	result, token, err := checkinAccountState(context.Background(), site, account, nil)
	if err != nil {
		t.Fatalf("configured checkin failed: %v", err)
	}
	if token != "" || result.Status != model.SiteExecutionStatusSuccess || result.Reward != "8 points" {
		t.Fatalf("unexpected result/token: result=%+v token=%q", result, token)
	}
}

func TestConfiguredHTTPCheckinInterpretsAlreadyCheckedInAndFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/already":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"success":false,"message":"今日已签到"}`))
		case "/rejected":
			_, _ = w.Write([]byte(`{"success":false,"message":"余额不足"}`))
		case "/plain":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	account := &model.SiteAccount{CredentialType: model.SiteCredentialTypeCookie, Cookie: "session=cookie-value"}
	for _, test := range []struct {
		path        string
		wantStatus  model.SiteExecutionStatus
		wantReason  string
		wantMessage string
	}{
		{path: "/already", wantStatus: model.SiteExecutionStatusSuccess, wantReason: model.SiteCheckinReasonAlreadyCheckedIn},
		{path: "/rejected", wantStatus: model.SiteExecutionStatusFailed, wantMessage: "余额不足"},
		{path: "/plain", wantStatus: model.SiteExecutionStatusFailed, wantReason: model.SiteCheckinReasonUnconfirmed},
	} {
		site := &model.Site{
			Platform:           model.SitePlatformAPI,
			BaseURL:            server.URL,
			CheckinHTTPEnabled: true,
			CheckinHTTPMethod:  "GET",
			CheckinHTTPPath:    test.path,
		}
		result, _, err := checkinAccountState(context.Background(), site, account, nil)
		if err != nil {
			t.Fatalf("checkin %s failed: %v", test.path, err)
		}
		if result.Status != test.wantStatus || result.Reason != test.wantReason || (test.wantMessage != "" && result.Message != test.wantMessage) {
			t.Errorf("checkin %s result = %+v", test.path, result)
		}
	}
}

func TestConfiguredHTTPCheckinAlreadyCheckedInHintOverridesStatus(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		body        string
		message     string
		cfChallenge bool
	}{
		{"success flag false", http.StatusOK, `{"success":false,"message":"今日已签到","data":{"reward":99}}`, "今日已签到", false},
		{"redirect status", http.StatusFound, `{"message":"提示：今日已签到（请明日再来）"}`, "提示：今日已签到（请明日再来）", false},
		{"bad request", http.StatusBadRequest, `{"message":"今日已签到"}`, "今日已签到", false},
		{"unauthorized msg", http.StatusUnauthorized, `{"success":false,"msg":"今日已签到：请勿重复提交"}`, "今日已签到：请勿重复提交", false},
		{"forbidden challenge header", http.StatusForbidden, `{"message":"今日已签到"}`, "今日已签到", true},
		{"not found", http.StatusNotFound, `{"message":"今日已签到"}`, "今日已签到", false},
		{"conflict", http.StatusConflict, `{"message":"今日已签到"}`, "今日已签到", false},
		{"rate limited error message", http.StatusTooManyRequests, `{"error":{"message":"今日已签到，请明日再来"}}`, "今日已签到，请明日再来", false},
		{"server error escaped JSON", http.StatusInternalServerError, `{"message":"\u4eca\u65e5\u5df2\u7b7e\u5230"}`, "今日已签到", false},
		{"plain text", http.StatusBadGateway, "提示：今日已签到，请明日再来", "今日已签到", false},
		{"HTML hint", http.StatusServiceUnavailable, "<html><body><p>今日已签到</p></body></html>", "今日已签到", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.cfChallenge {
					w.Header().Set("Cf-Mitigated", "challenge")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			site := &model.Site{Platform: model.SitePlatformAPI, BaseURL: server.URL, CheckinHTTPEnabled: true, CheckinHTTPMethod: "GET", CheckinHTTPPath: "/checkin"}
			account := &model.SiteAccount{CredentialType: model.SiteCredentialTypeCookie, Cookie: "session=test-cookie"}
			result, token, err := checkinAccountState(context.Background(), site, account, nil)
			if err != nil || result == nil {
				t.Fatalf("already-checked-in hint was rejected: %+v, %v", result, err)
			}
			if result.Status != model.SiteExecutionStatusSuccess || result.Reason != model.SiteCheckinReasonAlreadyCheckedIn || result.Reward != "" || result.Message != tc.message || result.CapabilityEvidence != model.SiteCheckinSupportSupported || token != "" {
				t.Fatalf("incorrect already-checked-in result: %+v", result)
			}
		})
	}
}

func TestConfiguredHTTPCheckinDoesNotOverrideFailuresWithoutAnAlreadyCheckedInHint(t *testing.T) {
	for _, body := range []string{
		`{"success":false,"message":"今日未签到"}`,
		`{"success":false,"message":"签到失败","example":"今日已签到"}`,
		"今日尚未签到",
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(body))
		}))
		site := &model.Site{Platform: model.SitePlatformAPI, BaseURL: server.URL, CheckinHTTPEnabled: true, CheckinHTTPMethod: "GET", CheckinHTTPPath: "/checkin"}
		account := &model.SiteAccount{CredentialType: model.SiteCredentialTypeCookie, Cookie: "session=test-cookie"}
		result, _, err := checkinAccountState(context.Background(), site, account, nil)
		server.Close()
		if err == nil || result != nil {
			t.Fatalf("failure without a matching hint was overridden: %+v, %v", result, err)
		}
	}
}

func TestConfiguredHTTPCheckinPersistsAlreadyCheckedInDespiteServerError(t *testing.T) {
	ctx := setupProjectTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"success":false,"message":"今日已签到：session=test-cookie","data":{"reward":99}}`))
	}))
	defer server.Close()
	site := &model.Site{Name: "Custom checkin", Kind: model.SiteKindCheckin, Platform: model.SitePlatformAPI,
		BaseURL: server.URL, Enabled: true, CheckinHTTPEnabled: true, CheckinHTTPMethod: "POST", CheckinHTTPPath: "/checkin"}
	if err := op.SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "Cookie account", CredentialType: model.SiteCredentialTypeCookie,
		Cookie: "session=test-cookie", Enabled: true, CheckinFailureCount: 3}
	if err := op.SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	result, err := CheckinAccount(ctx, account.ID)
	if err != nil || result == nil || result.LogID == 0 {
		t.Fatalf("check-in result was not persisted: %+v, %v", result, err)
	}
	var entry model.SiteCheckinLog
	if err := db.GetDB().First(&entry, result.LogID).Error; err != nil {
		t.Fatal(err)
	}
	if entry.Status != model.SiteExecutionStatusSuccess || entry.Reason != model.SiteCheckinReasonAlreadyCheckedIn || entry.Reward != "" || strings.Contains(entry.Message, account.Cookie) || !strings.Contains(entry.Message, "今日已签到") {
		t.Fatalf("incorrect or unsanitized persisted result: %+v", entry)
	}
	saved, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || saved.LastCheckinStatus != model.SiteExecutionStatusSuccess || saved.CheckinFailureCount != 0 || saved.LastCheckinSuccessAt == nil {
		t.Fatalf("account still records a failure: %+v, %v", saved, err)
	}
}

func TestBuildConfiguredCheckinURLKeepsSitePrefixAndRejectsTraversal(t *testing.T) {
	got, err := buildConfiguredCheckinURL("https://example.com/root/", "/api/checkin?date=today")
	if err != nil || got != "https://example.com/root/api/checkin?date=today" {
		t.Fatalf("configured URL = %q, error=%v", got, err)
	}
	for _, path := range []string{"", "https://other.example/path", "//other.example/path", "/api/../admin"} {
		if _, err := buildConfiguredCheckinURL("https://example.com", path); err == nil {
			t.Errorf("unsafe checkin path accepted: %q", path)
		}
	}
}

func TestCheckinHTTPClientDoesNotForwardCredentialsAcrossRedirectOrigins(t *testing.T) {
	var secondServerHit bool
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondServerHit = true
		if got := r.Header.Get("Cookie"); got != "" {
			t.Errorf("redirect leaked Cookie header: %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("redirect leaked Authorization header: %q", got)
		}
	}))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/landing", http.StatusFound)
	}))
	defer first.Close()

	request, err := http.NewRequest(http.MethodPost, first.URL+"/checkin", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Cookie", "session=secret")
	request.Header.Set("Authorization", "Bearer secret")
	response, err := checkinHTTPClient(first.Client()).Do(request)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusFound || secondServerHit {
		t.Fatalf("cross-origin redirect was followed: status=%d hit=%v", response.StatusCode, secondServerHit)
	}
}
