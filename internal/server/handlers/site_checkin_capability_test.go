package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestSiteCheckinAPIExposesDefaultsAndVerifiesDoneHub(t *testing.T) {
	ctx := setupSiteHandlerTestDB(t)
	defaultResponse := httptest.NewRecorder()
	defaultContext, _ := gin.CreateTestContext(defaultResponse)
	defaultContext.Request = httptest.NewRequest(http.MethodGet, "/api/v1/site/checkin-capabilities", nil)
	getSiteCheckinCapabilities(defaultContext)
	var defaults struct {
		Data map[model.SitePlatform]model.SiteCheckinDefaults `json:"data"`
	}
	if err := json.Unmarshal(defaultResponse.Body.Bytes(), &defaults); err != nil {
		t.Fatal(err)
	}
	if defaultResponse.Code != 200 || defaults.Data[model.SitePlatformDoneHub].Enabled || !defaults.Data[model.SitePlatformDoneHub].HasBuiltin {
		t.Fatalf("DoneHub defaults prevent verification: %s", defaultResponse.Body.String())
	}

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/checkin" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer handler-test-token" {
			t.Error("invalid verification request")
		}
		calls.Add(1)
		_, _ = w.Write([]byte(`{"success":true,"message":"checkin success"}`))
	}))
	defer upstream.Close()
	site := &model.Site{Name: "DoneHub API test", Kind: model.SiteKindCheckin, Platform: model.SitePlatformDoneHub, BaseURL: upstream.URL, Enabled: true}
	if err := op.SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "Test", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "handler-test-token", Enabled: true, AutoCheckin: false, AutoCheckinSet: true}
	if err := op.SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	w, _ := requestSiteMutation(t, "/api/v1/site/account/checkin/"+strconv.Itoa(account.ID), func(c *gin.Context) {
		c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(account.ID)}}
		checkinSiteAccount(c)
	}, map[string]any{})
	var response struct {
		Data model.SiteCheckinResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || calls.Load() != 1 || response.Data.Capability == nil || !response.Data.Capability.Enabled || response.Data.Capability.Support != model.SiteCheckinSupportSupported {
		t.Fatalf("API did not perform and record verification: %s", w.Body.String())
	}
	saved, err := op.SiteGet(site.ID, ctx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	var siteResponse struct {
		Capability model.SiteCheckinCapability `json:"checkin_capability"`
	}
	if err := json.Unmarshal(encoded, &siteResponse); err != nil {
		t.Fatal(err)
	}
	if !siteResponse.Capability.Enabled || siteResponse.Capability.Source != "verified" || siteResponse.Capability.VerifiedAt == nil {
		t.Fatalf("site API omits resolved capability: %s", encoded)
	}
}
