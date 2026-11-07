package op

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestSiteCapabilityEvidenceCannotBeInjectedAndInvalidatesOnEdit(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	now := time.Now().UTC()
	site := model.Site{Name: "DoneHub", Platform: model.SitePlatformDoneHub, BaseURL: "https://site.example", CheckinMode: model.SiteCheckinModeAuto}
	site.CheckinVerificationStatus = model.SiteCheckinSupportSupported
	site.CheckinVerifiedAt = &now
	site.CheckinVerificationFingerprint = site.CheckinConfigFingerprint()
	if err := SiteCreate(&site, ctx); err != nil {
		t.Fatal(err)
	}
	if site.ResolveCheckinCapability().Support != model.SiteCheckinSupportUnknown {
		t.Fatal("create accepted injected evidence")
	}

	// Seed evidence as the execution layer would.
	if err := db.GetDB().Model(&site).Updates(map[string]any{
		"checkin_verification_status":      model.SiteCheckinSupportSupported,
		"checkin_verification_fingerprint": site.CheckinConfigFingerprint(),
		"checkin_verified_at":              now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	var req model.SiteUpdateRequest
	if err := json.Unmarshal([]byte(`{"name":"Renamed","checkin_mode":"disabled","checkin_verification_status":"unsupported","checkin_verified_at":null}`), &req); err != nil {
		t.Fatal(err)
	}
	req.ID = site.ID
	saved, err := SiteUpdate(&req, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CheckinVerificationStatus != model.SiteCheckinSupportSupported || saved.ResolveCheckinCapability().Enabled {
		t.Fatalf("policy edit or injected evidence corrupted state: %+v", saved)
	}

	newURL := "https://another.example"
	mode := model.SiteCheckinModeAuto
	saved, err = SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, BaseURL: &newURL, CheckinMode: &mode}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CheckinVerificationStatus != model.SiteCheckinSupportUnknown || saved.CheckinVerifiedAt != nil || saved.CheckinVerificationFingerprint != "" || saved.ResolveCheckinCapability().Enabled {
		t.Fatalf("URL change reused old evidence: %+v", saved)
	}
}

func TestImportedCheckinPreferencesOverrideDefaults(t *testing.T) {
	for _, platform := range []model.SitePlatform{model.SitePlatformNewAPI, model.SitePlatformDoneHub, model.SitePlatformSub2API, model.SitePlatformAPI} {
		for _, preference := range []any{nil, true, false} {
			want := model.PlatformCheckinDefaults(platform).Enabled
			if preference != nil {
				want = preference.(bool)
			}
			for _, authType := range []string{"access_token", "api_key"} {
				allAPIHub, warning, ok := parseAllAPIHubAccountRow(rawImportObject{
					"site_url": "https://site.example", "site_type": string(platform), "username": "account",
					"authType": authType, "access_token": "fake-token",
					"checkIn": map[string]any{"autoCheckInEnabled": preference},
				})
				if !ok || allAPIHub.AutoCheckin != want {
					t.Fatalf("ALL-API-Hub platform=%s auth=%s preference=%v: %+v, %s", platform, authType, preference, allAPIHub, warning)
				}
			}
			metAPI, warning, ok := parseMetAPIAccountRow(rawImportObject{
				"id": 1, "siteId": 1, "username": "account", "accessToken": "fake-session", "apiToken": "fake-key", "checkinEnabled": preference,
			}, map[int]importedSiteInput{1: {Name: "Site", Platform: platform, BaseURL: "https://site.example"}}, nil)
			if !ok || metAPI.AutoCheckin != want {
				t.Fatalf("metapi platform=%s preference=%v: %+v, %s", platform, preference, metAPI, warning)
			}
		}
	}
}
