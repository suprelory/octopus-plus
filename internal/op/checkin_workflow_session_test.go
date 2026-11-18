package op

import (
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestIndependentCheckinPlatformSessionCAS(t *testing.T) {
	testPlatformSessionCAS(t, independentCheckinStore(t))
}

func testPlatformSessionCAS(t *testing.T, store *CheckinStore) {
	t.Helper()
	ctx := t.Context()
	site, err := store.CreateSite(ctx, model.CheckinSite{Name: "router session", Platform: model.SitePlatformAnyRouter, BaseURL: "https://platform.example", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	c := model.CheckinCredential{Name: "account", Platform: site.Platform, Type: model.CheckinCredentialAccessToken, AuthBaseURL: site.BaseURL, AccessToken: "initial"}
	a, err := store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: "first", Enabled: true, AuthMode: model.CheckinAuthPlatform}, &c)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.BeginWorkflow(ctx, a.ID, model.CheckinTriggerManual, false)
	if err != nil {
		t.Fatal(err)
	}
	edited := before.Credential
	edited.Name = "operator rename"
	if _, err := store.EditCredential(ctx, edited, edited.CredentialVersion); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	winners := make(chan CheckinLoginSession, 2)
	for _, name := range []string{"a", "b"} {
		wg.Go(func() {
			id := 17
			s := CheckinLoginSession{AccessToken: "initial", Cookie: "session=" + name, PlatformUserID: &id,
				SessionCookies: []model.CheckinSessionCookie{{URL: site.BaseURL + "/api/user/self", Cookie: http.Cookie{Name: "session", Value: name, Path: "/api"}}}}
			_, err := store.SaveWorkflowSession(ctx, before, s)
			if err == nil {
				winners <- s
			} else if !errors.Is(err, ErrCheckinCredentialConflict) {
				t.Errorf("unexpected concurrent update: %v", err)
			}
		})
	}
	wg.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("session write winners=%d", len(winners))
	}
	winner := <-winners
	again, err := store.SaveWorkflowSession(ctx, before, winner)
	if err != nil || again.CredentialVersion != 2 || again.IdentityRevision != 0 || again.Name != edited.Name || again.Cookie != winner.Cookie || len(again.SessionCookies) != 1 {
		t.Fatal("lost-ack replay overwrote settings or session", err)
	}
	if _, err := store.ReserveWorkflowSubmission(ctx, before, false); !errors.Is(err, ErrCheckinCredentialConflict) {
		t.Fatal("stale request gained submission ownership", err)
	}
	after, err := store.CheckWorkflow(ctx, before)
	if err != nil {
		t.Fatal(err)
	}
	edited = *again
	edited.AccessToken = "operator-replacement"
	replaced, err := store.EditCredential(ctx, edited, again.CredentialVersion)
	if err != nil || replaced.Cookie != "" || len(replaced.SessionCookies) != 0 || replaced.IdentityRevision == 0 {
		t.Fatal("manual replacement retained old issued cookies", err)
	}
	if _, err := store.SaveWorkflowSession(ctx, after, winner); !errors.Is(err, ErrCheckinCredentialConflict) {
		t.Fatal("late cookie response overwrote new identity", err)
	}
}
