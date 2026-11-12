package sitesync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestAnyRouterSessionsAreIsolated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("New-API-User")
		cookie, err := r.Cookie("session")
		if err != nil {
			t.Error("missing account session")
			return
		}
		if r.URL.Path == "/api/user/self" {
			if cookie.Value != "original-"+id {
				t.Error("another account's cookie was used for discovery")
			}
			w.Header().Add("Set-Cookie", "session=updated-"+id+"; Path=/")
			_, _ = fmt.Fprintf(w, `{"success":true,"data":{"id":%s}}`, id)
			return
		}
		if cookie.Value != "updated-"+id {
			t.Error("updated session was lost or crossed accounts")
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()
	var workers sync.WaitGroup
	for _, id := range []int{24680, 13579} {
		workers.Add(1)
		go func(id int) {
			defer workers.Done()
			account := &model.SiteAccount{CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: fmt.Sprintf("session=original-%d", id), PlatformUserID: &id}
			result, _, err := checkinAnyRouter(context.Background(), &model.Site{Platform: model.SitePlatformAnyRouter, BaseURL: server.URL}, account, nil)
			if err != nil || result == nil || result.Status != model.SiteExecutionStatusSuccess {
				t.Errorf("account %d: result=%+v err=%v", id, result, err)
			}
		}(id)
	}
	workers.Wait()
}

func TestAnyRouterResponseCookiesRespectScopeAndDeletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			w.Header().Add("Set-Cookie", "session=; Max-Age=0; Path=/")
			w.Header().Add("Set-Cookie", "private=only-here; Path=/private")
			w.Header().Add("Set-Cookie", "foreign=wrong-domain; Domain=example.org; Path=/")
		} else {
			for _, name := range []string{"session", "foreign"} {
				if _, err := r.Cookie(name); err == nil {
					t.Errorf("unexpected cookie %s on %s", name, r.URL.Path)
				}
			}
			_, privateErr := r.Cookie("private")
			if (privateErr == nil) != strings.HasPrefix(r.URL.Path, "/private/") {
				t.Error("cookie path was not respected")
			}
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()
	site := &model.Site{Platform: model.SitePlatformAnyRouter, BaseURL: server.URL}
	account := &model.SiteAccount{}
	ctx := withAnyRouterSession(context.Background(), site, account)
	for _, path := range []string{"/start", "/api/user/self", "/private/details"} {
		_, _, err := anyRouterRequestJSONWithCookies(ctx, site, http.MethodGet, server.URL+path, nil, map[string]string{"Cookie": "session=old"}, account)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnyRouterSecureCookiesRequireHTTPS(t *testing.T) {
	// Go treats loopback HTTP as secure like browsers; use a non-local domain
	// for this cookie-jar-only test without making any network request.
	session := newAnyRouterSession(&model.Site{BaseURL: "https://example.org"}, nil)
	secureURL, _ := url.Parse("https://example.org/api/user/self")
	plainURL, _ := url.Parse("http://example.org/api/user/self")
	session.receiveCookies(secureURL, []*http.Cookie{{Name: "session", Value: "secure-session", Secure: true, Path: "/"}})
	if session.cookieHeader(plainURL, "") != "" || session.cookieHeader(secureURL, "") != "session=secure-session" {
		t.Fatal("secure cookie was not restricted to HTTPS")
	}
}

func TestAnyRouterSessionSavePreservesCredentialEdits(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprint(linked), func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			var sourceID int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/user/sign_in" {
					// Simulate a user replacing credentials while the POST is in flight.
					if err := db.GetDB().Model(&model.SiteAccount{}).Where("id = ?", sourceID).Updates(map[string]any{
						"access_token": "session=manual-replacement", "platform_user_id": 99999, "credential_version": 1,
					}).Error; err != nil {
						t.Error(err)
					}
					w.Header().Add("Set-Cookie", "session=old-operation-result; Path=/")
					_, _ = w.Write([]byte(`{"success":true}`))
					return
				}
				_, _ = w.Write([]byte(`{"success":true,"data":{"id":24680,"quota":100000,"used_quota":0,"today_income":0}}`))
			}))
			defer server.Close()
			var site *model.Site
			var account, source *model.SiteAccount
			if linked {
				site, account, source = createLinkedCheckinFixture(t, ctx, server.URL)
			} else {
				site, account = createCheckinFixture(t, ctx, server.URL)
				source = account
			}
			sourceID = source.ID
			if err := db.GetDB().Model(&model.Site{}).Where("id IN ?", []int{site.ID, source.SiteID}).Update("platform", model.SitePlatformAnyRouter).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.GetDB().Model(source).Updates(map[string]any{"access_token": "session=old", "platform_user_id": 24680}).Error; err != nil {
				t.Fatal(err)
			}
			result, err := CheckinAccount(ctx, account.ID)
			if err != nil || result.Status != model.SiteExecutionStatusSuccess {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			saved, err := op.SiteAccountGet(source.ID, ctx)
			if err != nil || saved.AccessToken != "session=manual-replacement" || firstManagedPlatformUserID(saved) != 99999 {
				t.Fatal("in-flight operation overwrote the manual credential edit")
			}
		})
	}
}
