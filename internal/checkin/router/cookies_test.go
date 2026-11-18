package router

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestCheckinScopedCookiesSurviveBackupExpiryAndDeletion(t *testing.T) {
	const base = "https://test.example"
	u, _ := url.Parse(base + "/api/user/login")
	jar := NewCookies(base, "session=old", nil)
	jar.SetCookies(u, []*http.Cookie{
		{Name: "session", Value: "deleted-secret", Domain: "test.example", Path: "/", MaxAge: -1},
		{Name: "session", Value: "scoped-secret", MaxAge: 1800, Secure: true},
		{Name: "private", Value: "another-secret", Path: "/api/log/"},
		{Name: "foreign", Value: "invalid-secret", Domain: "unrelated.example", Path: "/"},
		{Name: "expired", Value: "expired-secret", Path: "/", Expires: time.Now().Add(-time.Hour)},
	})
	c := model.CheckinCredential{Name: "password", Platform: model.SitePlatformAnyRouter, Type: model.CheckinCredentialPassword, Username: "account", Password: "password", AuthBaseURL: base,
		SessionCookies: jar.Snapshot(), Cookie: jar.Header(base + "/api/user/self")}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	ordinary, err := json.Marshal(c)
	if err != nil || strings.Contains(string(ordinary), "secret") {
		t.Fatal("ordinary response exposed a session cookie")
	}
	encoded, err := json.Marshal(model.ExportCheckinCredential(c))
	if err != nil {
		t.Fatal(err)
	}
	var backup model.CheckinCredentialBackup
	if err := json.Unmarshal(encoded, &backup); err != nil {
		t.Fatal(err)
	}
	saved := backup.Credential()
	restarted := NewCookies(base, "session=old", saved.SessionCookies)
	for endpoint, want := range map[string]string{
		base + "/api/user/self": "session=scoped-secret", base + "/api/log/self/": "private=another-secret", base + "/other": "",
		"http://test.example/api/user/self": "", "https://other.example/api/user/self": "",
	} {
		if got := restarted.Header(endpoint); got != want {
			t.Errorf("unexpected restored cookies for %s", endpoint)
		}
	}
	state := restarted.Snapshot()
	if len(state) != 4 {
		t.Fatalf("unexpected stored cookie count %d", len(state))
	}
	for i, item := range state {
		if item.Cookie.Value == "scoped-secret" {
			if item.Cookie.MaxAge != 0 || !item.Cookie.Expires.Equal(c.SessionCookies[i].Cookie.Expires) {
				t.Fatal("restart extended a relative expiry")
			}
		}
	}
}
