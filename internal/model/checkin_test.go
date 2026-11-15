package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIndependentCheckinIdentity(t *testing.T) {
	site := CheckinSite{ID: 1, Name: "checkin", Platform: SitePlatformNewAPI, BaseURL: "https://checkin.example"}
	credential := CheckinCredential{ID: 2, Name: "login", Type: CheckinCredentialAccessToken, Platform: SitePlatformNewAPI, AuthBaseURL: "https://checkin.example", AccessToken: "Bearer token"}
	account := CheckinAccount{SiteID: 1, Name: "account", CredentialID: 2, AuthMode: CheckinAuthPlatform}
	if err := site.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := credential.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := account.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCheckinIdentity(site, account, credential); err != nil {
		t.Fatal(err)
	}
	credential.AuthBaseURL = "https://login.example"
	if err := ValidateCheckinIdentity(site, account, credential); err == nil {
		t.Fatal("cross-site builtin identity accepted")
	}
	site.CheckinHTTPEnabled, site.CheckinHTTPPath = true, "/checkin"
	account.AuthMode = CheckinAuthBearer
	if err := ValidateCheckinIdentity(site, account, credential); err != nil {
		t.Fatalf("external HTTP bearer rejected: %v", err)
	}
	credential.Type = CheckinCredentialAPIKey
	if err := ValidateCheckinIdentity(site, account, credential); err == nil {
		t.Fatal("API key used as login access token")
	}
	credential.Type, credential.Platform = CheckinCredentialAccessToken, SitePlatformAgentRouter
	credential.AuthBaseURL = site.BaseURL
	site.Platform, site.CheckinHTTPEnabled = SitePlatformAgentRouter, false
	account.AuthMode = CheckinAuthPlatform
	if err := ValidateCheckinIdentity(site, account, credential); err == nil {
		t.Fatal("AgentRouter login requirement lost")
	}
}

func TestCheckinWorkflowIdentitySurvivesTokenRotation(t *testing.T) {
	site := CheckinSite{ID: 1, Platform: SitePlatformSub2API, BaseURL: "https://checkin.example", CheckinTimezone: "Asia/Shanghai"}
	credential := CheckinCredential{ID: 2, IdentityRevision: 4, AccessToken: "old", RefreshToken: "refresh"}
	account := CheckinAccount{SiteID: 1, CredentialID: 2, AuthMode: CheckinAuthBearer}
	before := site.WorkflowFingerprint(account, credential)
	credential.AccessToken, credential.RefreshToken, credential.CredentialVersion = "new", "rotated", 10
	if site.WorkflowFingerprint(account, credential) != before {
		t.Fatal("automatic token rotation invalidated pending workflow")
	}
	credential.IdentityRevision++
	if site.WorkflowFingerprint(account, credential) == before {
		t.Fatal("manual identity change retained workflow authorization")
	}
	credential.IdentityRevision--
	account.CheckinNetwork.ProxyMode = ProxyUsageModeSystem
	if site.WorkflowFingerprint(account, credential) == before {
		t.Fatal("request network change retained workflow authorization")
	}
}

func TestCheckinCredentialJSONOmitsSecrets(t *testing.T) {
	credential := CheckinCredential{Name: "test", Password: "private-password", AccessToken: "private-access", APIKey: "private-api", RefreshToken: "private-refresh", Cookie: "private-cookie", TokenRefreshLease: "private-lease", AuthHeaders: []CustomHeader{{HeaderKey: "Authorization", HeaderValue: "private-header"}}}
	encoded, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-") {
		t.Fatalf("credential response exposed a secret: %s", encoded)
	}
}

func TestCheckinValidationRejectsUnsafeAndLegacyCredentials(t *testing.T) {
	for _, mutate := range []func(*CheckinCredential){
		func(c *CheckinCredential) { c.Type = "linked_account" },
		func(c *CheckinCredential) { c.AuthBaseURL = "https://user:private-password@login.example" },
		func(c *CheckinCredential) { c.AccessToken = "private-token\r\nInjected: yes" },
		func(c *CheckinCredential) {
			c.AuthHeaders = []CustomHeader{{HeaderKey: "X Bad", HeaderValue: "private-header"}}
		},
	} {
		credential := CheckinCredential{Name: "test", Type: CheckinCredentialAccessToken, Platform: SitePlatformSub2API, AuthBaseURL: "https://login.example", AccessToken: "token"}
		mutate(&credential)
		err := credential.Validate()
		if err == nil || strings.Contains(err.Error(), "private-") {
			t.Fatalf("unsafe validation error: %v", err)
		}
	}
}
