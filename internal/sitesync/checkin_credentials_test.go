package sitesync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func setCheckinTestCookie(t *testing.T, ctx context.Context, accountID int) {
	t.Helper()
	cookie, kind := "session=checkin-cookie-value", model.SiteCredentialTypeCookie
	if _, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: accountID, CredentialType: &kind, Cookie: &cookie}, ctx); err != nil {
		t.Fatal(err)
	}
}

func createLinkedCheckinFixture(t *testing.T, ctx context.Context, baseURL string) (*model.Site, *model.SiteAccount, *model.SiteAccount) {
	t.Helper()
	sourceSite := &model.Site{Name: "Subscription", Platform: model.SitePlatformNewAPI, BaseURL: baseURL, Enabled: true}
	if err := op.SiteCreate(sourceSite, ctx); err != nil {
		t.Fatal(err)
	}
	userID := 42
	source := &model.SiteAccount{SiteID: sourceSite.ID, Name: "Source", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "source-token-one", PlatformUserID: &userID, Enabled: true}
	if err := op.SiteAccountCreate(source, ctx); err != nil {
		t.Fatal(err)
	}
	site := &model.Site{Name: "Platform rewards", Kind: model.SiteKindCheckin, LinkedSiteID: &sourceSite.ID, Platform: sourceSite.Platform, BaseURL: baseURL, Enabled: true}
	if err := op.SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "Rewards", CredentialType: model.SiteCredentialTypeLinkedAccount, LinkedAccountID: &source.ID, Enabled: true, AutoCheckin: true}
	if err := op.SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	return site, account, source
}

func TestPlatformCheckinUsesLatestLinkedCredentialsAndStopsAfterDeletion(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var wantToken atomic.Value
	wantToken.Store("source-token-one")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/checkin" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+wantToken.Load().(string) || r.Header.Get("New-Api-User") != "42" {
			t.Errorf("wrong linked credentials: %v", r.Header)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"reward":2}}`))
	}))
	defer server.Close()
	_, account, source := createLinkedCheckinFixture(t, ctx, server.URL)
	for _, token := range []string{"source-token-one", "source-token-two"} {
		wantToken.Store(token)
		if _, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: source.ID, AccessToken: &token}, ctx); err != nil {
			t.Fatal(err)
		}
		if result, err := CheckinAccount(ctx, account.ID); err != nil || result.Status != model.SiteExecutionStatusSuccess {
			t.Fatalf("checkin: %+v, %v", result, err)
		}
	}
	saved, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || saved.AccessToken != "" || saved.LastCheckinSuccessAt == nil {
		t.Fatalf("copied token or lost status: %+v, %v", saved, err)
	}
	if err := op.SiteAccountDel(source.ID, ctx); err != nil {
		t.Fatal(err)
	}
	if result, err := CheckinAccount(ctx, account.ID); err == nil || result.Status != model.SiteExecutionStatusFailed || calls.Load() != 2 {
		t.Fatalf("deleted source still used: %+v, %v", result, err)
	}
}

func TestLinkedLoginPersistsTokenOnSubscriptionAndRedactsErrors(t *testing.T) {
	ctx := setupProjectTestDB(t)
	const token = "fresh-login-token-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/login":
			_, _ = fmt.Fprintf(w, `{"success":true,"data":"%s"}`, token)
		case "/api/user/checkin":
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Error("refreshed token not used")
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"message":"failed %s source-password-secret"}`, token)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	_, account, source := createLinkedCheckinFixture(t, ctx, server.URL)
	kind, user, password := model.SiteCredentialTypeUsernamePassword, "source-user", "source-password-secret"
	if _, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: source.ID, CredentialType: &kind, Username: &user, Password: &password}, ctx); err != nil {
		t.Fatal(err)
	}
	result, err := CheckinAccount(ctx, account.ID)
	if err == nil || result == nil {
		t.Fatalf("missing failure: %+v, %v", result, err)
	}
	text := result.Message + err.Error() + fmt.Sprint(apperror.Params(err))
	if strings.Contains(text, token) || strings.Contains(text, password) {
		t.Fatalf("leaked linked credentials: %s", text)
	}
	savedSource, _ := op.SiteAccountGet(source.ID, ctx)
	savedAccount, _ := op.SiteAccountGet(account.ID, ctx)
	if savedSource.AccessToken != token || savedAccount.AccessToken != "" {
		t.Fatal("refreshed credential was not saved on the source alone")
	}
	page, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
	if err != nil || len(page.Items) != 1 || strings.Contains(page.Items[0].Message, token) || strings.Contains(page.Items[0].Message, password) {
		t.Fatalf("unsafe log: %+v, %v", page, err)
	}
}

func TestCustomCheckinRequiresCookieAndNeverResolvesPlatformCredentials(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/daily" || r.Header.Get("Cookie") != "session=external-cookie" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected external request: %s %v", r.URL.Path, r.Header)
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()
	site := &model.Site{Platform: model.SitePlatformNewAPI, BaseURL: server.URL, CheckinHTTPEnabled: true, CheckinHTTPPath: "/daily"}
	account := &model.SiteAccount{CredentialType: model.SiteCredentialTypeUsernamePassword, Username: "legacy", Password: "legacy-password", AccessToken: "legacy-token"}
	for _, cookie := range []string{"", "session=bad\r\nInjected: value"} {
		account.Cookie = cookie
		if _, _, err := checkinAccountState(context.Background(), site, account, nil); err == nil || calls.Load() != 0 {
			t.Fatal("invalid cookie issued a request")
		}
	}
	account.Cookie = "session=external-cookie"
	result, token, err := checkinAccountState(context.Background(), site, account, nil)
	if err != nil || result.Status != model.SiteExecutionStatusSuccess || token != "" || calls.Load() != 1 {
		t.Fatalf("unexpected result: %+v, token=%q, %v", result, token, err)
	}
	if got := expandCheckinHTTPTemplate("{{access_token}}|{{password}}|{{cookie}}", account, "resolved-platform-token"); got != "||session=external-cookie" {
		t.Fatalf("platform credential expanded: %q", got)
	}
}

func TestCookieCheckinDoesNotPersistTokenOrProbePlatformBalance(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/daily" {
			t.Errorf("cookie sent to platform endpoint %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"message":"session=external-cookie external-cookie"}`))
	}))
	defer server.Close()
	site := &model.Site{Name: "Cookie rewards", Kind: model.SiteKindCheckin, Platform: model.SitePlatformNewAPI, BaseURL: server.URL, CheckinHTTPEnabled: true, CheckinHTTPPath: "/daily"}
	if err := op.SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "Cookie account", CredentialType: model.SiteCredentialTypeCookie, Cookie: "session=external-cookie"}
	if err := op.SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	result, err := CheckinAccount(ctx, account.ID)
	if err != nil || result.Status != model.SiteExecutionStatusSuccess || strings.Contains(result.Message, "external-cookie") {
		t.Fatalf("cookie checkin: %+v, %v", result, err)
	}
	saved, _ := op.SiteAccountGet(account.ID, ctx)
	if saved.AccessToken != "" || saved.Cookie != account.Cookie || calls.Load() != 1 {
		t.Fatal("cookie was used as platform credentials")
	}
	var entry model.SiteCheckinLog
	if err := db.GetDB().First(&entry).Error; err != nil || strings.Contains(entry.Message, "external-cookie") {
		t.Fatalf("cookie leaked into history: %+v, %v", entry, err)
	}
}
