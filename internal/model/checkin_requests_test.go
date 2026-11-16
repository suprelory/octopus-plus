package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCheckinPatchOmissionNullAndZero(t *testing.T) {
	manualURL := "https://manual.example"
	proxyID := 8
	site := CheckinSite{Name: "original", Enabled: true, SortOrder: 12,
		CheckinNetwork:     CheckinNetwork{ProxyMode: ProxyUsageModePool, ProxyConfigID: &proxyID},
		ExternalCheckinURL: &manualURL, CheckinStatus: &SiteCheckinStatusConfig{Path: "/status"}, Tags: []string{"old"}}
	var patch CheckinSitePatch
	input := `{"enabled":false,"sort_order":0,"proxy_mode":"direct","proxy_config_id":null,"external_checkin_url":null,"checkin_status":null,"tags":[]}`
	if err := json.Unmarshal([]byte(input), &patch); err != nil {
		t.Fatal(err)
	}
	patch.Apply(&site)
	if site.Name != "original" || site.Enabled || site.SortOrder != 0 || site.ProxyMode != ProxyUsageModeDirect || site.ProxyConfigID != nil || site.ExternalCheckinURL != nil || site.CheckinStatus != nil || len(site.Tags) != 0 {
		t.Fatalf("patch lost omission/null/zero semantics: %+v", site)
	}
	// Forwarding a parsed patch must not turn omitted fields into explicit nulls.
	encoded, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	_ = json.Unmarshal([]byte(input), &before)
	_ = json.Unmarshal(encoded, &after)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("patch JSON changed field presence: %s", encoded)
	}
	account := CheckinAccount{AutoCheckin: true, BalanceConfig: CheckinBalanceConfig{Mode: CheckinBalanceCustom, CredentialID: &proxyID}}
	var accountPatch CheckinAccountPatch
	if err := json.Unmarshal([]byte(`{"auto_checkin":false,"balance_config":null}`), &accountPatch); err != nil {
		t.Fatal(err)
	}
	accountPatch.Apply(&account)
	if account.AutoCheckin || account.BalanceConfig.Mode != CheckinBalanceDisabled || account.BalanceConfig.CredentialID != nil {
		t.Fatal("null balance configuration did not disable and clear the reference")
	}
}

func TestCheckinPatchRejectsRuntimeFieldsAndInvalidNulls(t *testing.T) {
	for _, input := range []string{`null`, `[]`, `{"enabled":null}`, `{"archived":true}`, `{"verification_status":"supported"}`, `{"id":1}`, `{"checkin_status":{"private-secret":"secret"}}`} {
		var patch CheckinSitePatch
		if err := json.Unmarshal([]byte(input), &patch); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatalf("invalid site patch accepted or echoed input: %s: %v", input, err)
		}
	}
	for _, input := range []string{`{"site_id":2}`, `{"checkin_pending":null}`, `{"balance":10}`, `{"next_auto_checkin_at":null}`, `{"credential_id":null}`} {
		var patch CheckinAccountPatch
		if err := json.Unmarshal([]byte(input), &patch); err == nil {
			t.Fatalf("invalid account patch accepted: %s", input)
		}
	}
	for _, input := range []string{`{"expected_version":null}`, `{"access_token":null}`, `{"identity_revision":99}`, `{"token_refresh_lease":"private-secret"}`} {
		var patch CheckinCredentialPatch
		if err := json.Unmarshal([]byte(input), &patch); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatalf("invalid credential patch accepted or echoed input: %v", err)
		}
	}
}

func TestCheckinCredentialSummaryAndBackupBoundary(t *testing.T) {
	credential := CheckinCredential{ID: 7, Name: "login", Type: CheckinCredentialAccessToken, Platform: SitePlatformSub2API,
		AuthBaseURL: "https://login.example", CheckinNetwork: CheckinNetwork{ProxyMode: ProxyUsageModeDirect},
		Username: "user", Password: "private-password", AccessToken: "private-access", APIKey: "private-api",
		RefreshToken: "private-refresh", Cookie: "session=private-cookie", AuthHeaders: []CustomHeader{{HeaderKey: "X-Auth", HeaderValue: "private-header"}},
		CredentialVersion: 5, IdentityRevision: 2, TokenExpiresAt: 100, TokenRefreshedAt: 50, TokenRefreshLease: "private-lease", TokenRefreshLeaseUntil: 200}
	summary := credential.Summary(2, 1)
	encoded, err := json.Marshal(summary)
	if err != nil || strings.Contains(string(encoded), "private-") {
		t.Fatal("summary exposed credential secrets")
	}
	if !summary.Configured || !summary.HasPassword || !summary.HasAccessToken || !summary.HasAPIKey || !summary.HasRefreshToken || !summary.HasCookie || !summary.HasAuthHeaders || summary.ReferenceCount != 3 {
		t.Fatal("summary omitted configuration or references")
	}
	encoded, err = json.Marshal(ExportCheckinCredential(credential))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-lease") || strings.Contains(string(encoded), "token_refresh_lease") {
		t.Fatal("backup included runtime refresh ownership")
	}
	var exported CheckinCredentialBackup
	if err := json.Unmarshal(encoded, &exported); err != nil {
		t.Fatal(err)
	}
	restored := exported.Credential()
	credential.TokenRefreshLease, credential.TokenRefreshLeaseUntil = "", 0
	if !reflect.DeepEqual(credential, restored) {
		t.Fatal("credential backup lost secrets, versions or configuration")
	}
}

func TestCheckinCredentialBackupRetainsUncertainSessionWithoutLease(t *testing.T) {
	c := CheckinCredential{TokenRefreshStarted: true, TokenRefreshLease: "private-owner", TokenRefreshLeaseUntil: 1, TokenRefreshRetryAt: 123}
	summary := c.Summary(0, 0)
	if summary.SessionState != "needs_credentials" {
		t.Fatal("uncertain session was hidden from configuration summary")
	}
	encoded, err := json.Marshal(ExportCheckinCredential(c))
	if err != nil || strings.Contains(string(encoded), "private-owner") || strings.Contains(string(encoded), "token_refresh_lease") {
		t.Fatal("backup exposed runtime refresh ownership")
	}
	var backup CheckinCredentialBackup
	if err := json.Unmarshal(encoded, &backup); err != nil {
		t.Fatal(err)
	}
	restored := backup.Credential()
	if !restored.TokenRefreshStarted || restored.TokenRefreshLease != "" || restored.TokenRefreshLeaseUntil != 0 || restored.TokenRefreshRetryAt != 0 {
		t.Fatal("backup made an uncertain refresh token reusable or restored runtime ownership")
	}
}
