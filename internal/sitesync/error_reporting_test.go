package sitesync

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func siteErrorTestServer(t *testing.T, status int, contentType, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestCheckinResponseStatusAndMessage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		want      model.SiteExecutionStatus
		message   string
		errorCode string
	}{
		{"no failure reason", 200, `{"success":false}`, model.SiteExecutionStatusFailed, "checkin failed", ""},
		{"disabled account", 200, `{"success":false,"message":"Account already disabled"}`, model.SiteExecutionStatusFailed, "Account already disabled", ""},
		{"negative checkin message", 200, `{"success":false,"message":"今日尚未签到过"}`, model.SiteExecutionStatusFailed, "今日尚未签到过", ""},
		{"checkin rate limit", 200, `{"success":false,"message":"签到过于频繁，请稍后再试"}`, model.SiteExecutionStatusFailed, "签到过于频繁，请稍后再试", ""},
		{"negative English checkin", 200, `{"success":false,"message":"You have not checked in today"}`, model.SiteExecutionStatusFailed, "You have not checked in today", ""},
		{"already checked in", 200, `{"success":false,"message":"You have already checked in today"}`, model.SiteExecutionStatusSuccess, "You have already checked in today", ""},
		{"already checked in Chinese", 200, `{"success":false,"message":"今天已经签到过了"}`, model.SiteExecutionStatusSuccess, "今天已经签到过了", ""},
		{"success without message", 200, `{"success":true}`, model.SiteExecutionStatusSuccess, "checkin success", ""},
		{"unsupported endpoint", 404, `{"message":"not found"}`, model.SiteExecutionStatusSkipped, "checkin endpoint is unavailable on this site", ""},
		{"account not found is a server failure", 500, `{"message":"account not found"}`, "", "http 500: account not found", CodeSiteUpstreamHTTPError},
		{"missing account is not an unsupported endpoint", 404, `{"message":"account not found"}`, "", "http 404: account not found", CodeSiteUpstreamHTTPError},
		{"404 in message is not the status", 502, `{"message":"upstream account 404 unavailable"}`, "", "http 502: upstream account 404 unavailable", CodeSiteUpstreamHTTPError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := siteErrorTestServer(t, tc.status, "application/json", tc.body)
			result, _, err := checkinAccountState(context.Background(), &model.Site{BaseURL: server.URL, Platform: model.SitePlatformOneAPI}, &model.SiteAccount{CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "test-token"}, nil)
			if tc.errorCode != "" {
				if apperror.Code(err) != tc.errorCode || err.Error() != tc.message {
					t.Fatalf("error = %v, want %s: %s", err, tc.errorCode, tc.message)
				}
				return
			}
			if err != nil || result == nil || result.Status != tc.want || result.Message != tc.message {
				t.Fatalf("result = %+v, error = %v; want %s: %s", result, err, tc.want, tc.message)
			}
		})
	}
}

func TestManagementBusinessFailuresDoNotBecomeEmptyData(t *testing.T) {
	server := siteErrorTestServer(t, 200, "application/json", `{"success":false,"message":"Access token expired"}`)
	site := &model.Site{BaseURL: server.URL, Platform: model.SitePlatformOneAPI}
	account := &model.SiteAccount{CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "test-token"}
	_, err := syncManagementPlatform(context.Background(), site, account)
	if apperror.Code(err) != CodeSiteUpstreamBusinessError || err.Error() != "Access token expired" {
		t.Fatalf("token failure must retain its cause, got %v", err)
	}
	models, err := fetchManagedSessionModels(context.Background(), site, account, account.AccessToken)
	if models != nil || apperror.Code(err) != CodeSiteUpstreamBusinessError {
		t.Fatalf("a failed session response must not become an authoritative empty model list: models=%v, err=%v", models, err)
	}
}

func TestSyncAccountPersistsUpstreamModelFailure(t *testing.T) {
	ctx := setupProjectTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/token/":
			_, _ = w.Write([]byte(`{"success":true,"data":[{"id":1,"key":"test-key","group":"default","status":1}]}`))
		case "/api/user/self/groups", "/api/user_group_map":
			_, _ = w.Write([]byte(`{"success":true,"data":{"default":"Default"}}`))
		case "/api/user/self":
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":1,"quota":500000,"used_quota":0,"today_income":0}}`))
		default:
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"message":"Bad Gateway"}`))
		}
	}))
	defer server.Close()
	site := &model.Site{Name: "Model errors", BaseURL: server.URL, Platform: model.SitePlatformOneAPI, Enabled: true}
	if err := op.SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	userID := 1
	account := &model.SiteAccount{SiteID: site.ID, Name: "Test account", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "test-token", PlatformUserID: &userID, Enabled: true}
	if err := op.SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	result, err := SyncAccount(ctx, account.ID)
	if err == nil || result == nil || result.Status != model.SiteExecutionStatusFailed {
		t.Fatalf("expected a failed sync result, got %+v, %v", result, err)
	}
	stored, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{result.Message, result.GroupResults[0].Message, stored.LastSyncMessage} {
		if !strings.Contains(message, "http 502: Bad Gateway") || strings.Contains(message, "上游当前没有可用模型") {
			t.Fatalf("model failure cause was replaced: %q", message)
		}
	}
}

func TestAnyRouterLoginPreservesResponseErrors(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, code, message string
		status                                 int
	}{
		{"JSON gateway error", "application/json", `{"success":false,"message":"Bad Gateway"}`, CodeSiteUpstreamHTTPError, "http 502: Bad Gateway", 502},
		{"maintenance HTML", "text/html", `<html><title>Maintenance</title><body>Temporarily unavailable</body></html>`, CodeSiteUpstreamDecodeFailed, "Maintenance", 200},
		{"successful-status challenge", "text/html", `<html><title>Just a moment...</title><script src="/cdn-cgi/challenge-platform/test"></script></html>`, CodeSiteUpstreamCloudflareChallenge, "Cloudflare 保护", 200},
		{"empty JSON object", "application/json", `null`, CodeSiteUpstreamDecodeFailed, "JSON object", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := siteErrorTestServer(t, tc.status, tc.contentType, tc.body)
			_, err := resolveAnyRouterManagedAccessToken(context.Background(), &model.Site{BaseURL: server.URL, Platform: model.SitePlatformAnyRouter}, &model.SiteAccount{CredentialType: model.SiteCredentialTypeUsernamePassword, Username: "test", Password: "test"})
			if apperror.Code(err) != tc.code || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error = %v, want %s containing %q", err, tc.code, tc.message)
			}
		})
	}
}

func TestRequestJSONRecognizesChallengeOnSuccessStatus(t *testing.T) {
	server := siteErrorTestServer(t, 200, "text/html", `<html><title>Just a moment...</title><script src="/cdn-cgi/challenge-platform/test"></script></html>`)
	_, err := requestJSON(context.Background(), &model.Site{BaseURL: server.URL}, http.MethodGet, server.URL, nil, nil)
	if !IsCloudflareProtectionError(err) || siteBatchReason(err) != SiteBatchReasonCloudflareProtection {
		t.Fatalf("challenge was misclassified: %v", err)
	}
	jsonServer := siteErrorTestServer(t, 200, "application/json", `{"success":false,"message":"Please wait just a moment"}`)
	payload, err := requestJSON(context.Background(), &model.Site{BaseURL: jsonServer.URL}, http.MethodGet, jsonServer.URL, nil, nil)
	if err != nil || payload == nil {
		t.Fatalf("ordinary JSON must not be classified by challenge wording: %v", err)
	}
}

func TestNetworkFailuresRetainTypeAndSafeReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   string
		reason SiteBatchReason
	}{
		{"DNS", &url.Error{Op: "Get", URL: "https://site.invalid/api/token/?api_key=private-value", Err: &net.DNSError{Err: "no such host", Name: "site.invalid", IsNotFound: true}}, CodeSiteUpstreamNetworkError, SiteBatchReasonNetworkError},
		{"deadline", fmt.Errorf("request failed: %w", context.DeadlineExceeded), CodeSiteUpstreamTimeout, SiteBatchReasonContextDeadlineExceeded},
		{"network timeout", &net.DNSError{Err: "i/o timeout", IsTimeout: true}, CodeSiteUpstreamTimeout, SiteBatchReasonTimeout},
		{"canceled", context.Canceled, CodeSiteOperationCanceled, SiteBatchReasonContextCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := sanitizeSiteError(tc.err)
			if apperror.Code(err) != tc.code || siteBatchReason(err) != tc.reason || !errors.Is(err, tc.err) {
				t.Fatalf("error lost its classification or cause: %v, code=%s, reason=%s", err, apperror.Code(err), siteBatchReason(err))
			}
			if apperror.Params(err)["reason"] != err.Error() || strings.Contains(err.Error(), "private-value") {
				t.Fatalf("display reason must match the sanitized status message: %v", err)
			}
		})
	}
}

func TestCheckinBatchRetainsCloudflareReason(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Cf-Mitigated", "challenge")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<html><title>Just a moment...</title></html>`))
	}))
	defer server.Close()
	site := &model.Site{Name: "Checkin errors", Kind: model.SiteKindCheckin, BaseURL: server.URL, Platform: model.SitePlatformOneAPI, Enabled: true}
	if err := op.SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	for index := range 2 {
		account := &model.SiteAccount{SiteID: site.ID, Name: fmt.Sprintf("Account %d", index), CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "test-token", Enabled: true, AutoCheckin: true}
		if err := op.SiteAccountCreate(account, ctx); err != nil {
			t.Fatal(err)
		}
	}
	summary := CheckinAllWithOptions(ctx, SiteBatchOptions{Trigger: SiteBatchTriggerManual})
	if summary.Failed != 1 || summary.Skipped != 1 || requests.Load() != 1 {
		t.Fatalf("expected one challenge and one skipped account: summary=%+v, requests=%d", summary, requests.Load())
	}
	if len(summary.Samples) != 1 || summary.Samples[0].Reason != SiteBatchReasonCloudflareProtection {
		t.Fatalf("checkin error reason was lost: %+v", summary.Samples)
	}
	stored, err := op.SiteAccountGet(summary.Samples[0].AccountID, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastCheckinStatus != model.SiteExecutionStatusFailed || !strings.Contains(stored.LastCheckinMessage, "Cloudflare 保护") {
		t.Fatalf("stored checkin state lost the failure: %+v", stored)
	}
}

func TestStatusMessagesKeepGatewayTitleAndHTTPStatus(t *testing.T) {
	body := `<html><title>upstream.example | 502: Bad gateway</title><body>Cloudflare Ray ID: test</body></html>`
	err := formatSiteHTTPError(502, http.Header{"Content-Type": {"text/html"}}, []byte(body))
	if err.Error() != "http 502: 502: Bad gateway" {
		t.Fatalf("gateway reason was lost: %v", err)
	}
	message := sanitizeSiteStatusText("http 502: " + body)
	if message != "http 502: 上游返回 HTML 页面：502: Bad gateway" {
		t.Fatalf("HTML sanitization lost the HTTP status: %q", message)
	}
}
