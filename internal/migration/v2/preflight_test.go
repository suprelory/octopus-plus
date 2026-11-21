package v2

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
)

func id(value int) *int { return &value }

func fixture() *Snapshot {
	return &Snapshot{
		Version: 1, IncludeLogs: true, IncludeStats: true,
		Sites: []model.LegacySite{
			{ID: 1, Kind: model.SiteKindRelay, Platform: model.SitePlatformSub2API, BaseURL: "https://login.example", ProxyMode: model.ProxyUsageModePool, ProxyConfigID: id(7)},
			{ID: 2, Kind: model.SiteKindCheckin, Platform: model.SitePlatformSub2API, BaseURL: "https://checkin.example", LinkedSiteID: id(1), CheckinHTTPEnabled: true, CheckinUseSubscriptionToken: true, ProxyMode: model.ProxyUsageModeDirect},
			{ID: 3, Kind: model.SiteKindCheckin, Platform: model.SitePlatformAPI, BaseURL: "https://external.example", LinkedSiteID: id(1), CheckinHTTPEnabled: true, ProxyMode: model.ProxyUsageModeDirect},
		},
		SiteAccounts: []model.LegacySiteAccount{
			{ID: 10, SiteID: 1, CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "source-token", RefreshToken: "source-refresh", ProxyMode: model.ProxyUsageModeInherit},
			{ID: 20, SiteID: 2, CredentialType: model.SiteCredentialTypeLinkedAccount, LinkedAccountID: id(10), ProxyMode: model.ProxyUsageModeInherit},
			{ID: 30, SiteID: 3, CredentialType: model.SiteCredentialTypeCookie, Cookie: "session=external-cookie", LinkedAccountID: id(10), ProxyMode: model.ProxyUsageModeInherit},
		},
		Channels:            []model.Channel{{ID: 100, Enabled: false, AutoGroup: model.AutoGroupTypeNone, ProxyMode: model.ProxyUsageModePool, ProxyConfigID: id(7)}, {ID: 101, Enabled: true}},
		ChannelKeys:         []model.ChannelKey{{ID: 200, ChannelID: 100, ChannelKey: "channel-secret"}},
		SiteChannelBindings: []model.LegacySiteChannelBinding{{ID: 300, SiteID: 1, SiteAccountID: 10, ChannelID: 100}},
		Groups:              []model.Group{{ID: 400}}, GroupItems: []model.GroupItem{{ID: 500, GroupID: 400, ChannelID: 100}},
		ProxyConfigurations: []model.ProxyConfiguration{{ID: 7, URL: "socks5h://user:proxy-secret@proxy.example:1080", Enabled: true}},
		Settings:            []model.Setting{{Key: model.SettingKeyProjectedChannelAutoGroupEnabled, Value: "2"}},
	}
}

func TestPreflightPreservesChannelsAndResolvesCredentialOwnership(t *testing.T) {
	snapshot := fixture()
	before, _ := json.Marshal(snapshot)
	report, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Blockers != 0 || report.Summary.ProjectedChannels != 1 || report.Summary.OrdinaryChannels != 1 || report.Summary.SharedCredentials != 1 {
		t.Fatalf("unexpected summary: %+v; issues=%+v", report.Summary, report.Issues)
	}
	channel := report.Channels[0]
	if channel.ChannelID != 100 || channel.Enabled || channel.AutoSync || channel.AutoGroup != model.AutoGroupTypeExact || !reflect.DeepEqual(channel.KeyIDs, []int{200}) || !reflect.DeepEqual(channel.GroupIDs, []int{400}) {
		t.Fatalf("channel conversion lost behavior: %+v", channel)
	}
	bearer, cookie := report.Accounts[0], report.Accounts[1]
	if bearer.CredentialAccountID != 10 || bearer.AuthSiteID != 1 || bearer.Network.Mode != model.ProxyUsageModePool || *bearer.Network.ProxyConfigID != 7 || bearer.Network.HeaderSiteID != 1 {
		t.Fatalf("Sub2API must retain source authentication and network: %+v", bearer)
	}
	if cookie.CredentialAccountID != 30 || cookie.Network.Mode != model.ProxyUsageModeDirect || cookie.BalanceAccountID != 10 || cookie.BalanceNetwork.Mode != model.ProxyUsageModePool {
		t.Fatalf("external cookie must retain independent balance credentials: %+v", cookie)
	}
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("preflight modified source snapshot")
	}
}

func TestPreflightNeverFallsBackToStaleLinkedCredentials(t *testing.T) {
	snapshot := fixture()
	snapshot.SiteAccounts[1].LinkedAccountID = id(999)
	snapshot.SiteAccounts[1].AccessToken = "retired-token"
	snapshot.SiteAccounts[2].LinkedAccountID = id(999)
	report, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Accounts[0].NeedsConfiguration || report.Accounts[0].CredentialAccountID != 0 {
		t.Fatal("missing linked credential silently used retained token")
	}
	if report.Accounts[1].NeedsConfiguration || report.Accounts[1].CredentialAccountID != 30 || report.Accounts[1].BalanceMode != "disabled" {
		t.Fatal("unavailable balance blocked independent cookie checkin")
	}
	assertIssue(t, report, "checkin_credentials_unavailable", "blocker")
	assertIssue(t, report, "balance_credentials_unavailable", "review")
}

func TestPreflightRetainsInvalidSubmittedWorkflows(t *testing.T) {
	snapshot := fixture()
	snapshot.SiteAccounts[1].CheckinPending = &model.SiteCheckinPending{Submitted: true, Fingerprint: "changed"}
	snapshot.SiteAccounts[2].CheckinPending = &model.SiteCheckinPending{Submitted: true, Finished: true, Fingerprint: snapshot.Sites[2].CheckinWorkflowFingerprint(&snapshot.SiteAccounts[2])}
	report, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.PendingWorkflows != 1 || !report.Accounts[0].Workflow.Submitted || report.Accounts[0].Workflow.FingerprintValid || !report.Accounts[1].Workflow.FingerprintValid {
		t.Fatalf("workflow semantics changed: %+v", report.Accounts)
	}
	assertIssue(t, report, "workflow_fingerprint_changed", "review")
	assertIssue(t, report, "submitted_workflow_must_not_resubmit", "review")
}

func TestPreflightDetectsBrokenRelationsAndProxy(t *testing.T) {
	snapshot := fixture()
	snapshot.ProxyConfigurations[0].Enabled = false
	snapshot.SiteChannelBindings = append(snapshot.SiteChannelBindings, model.LegacySiteChannelBinding{ID: 301, ChannelID: 999})
	snapshot.GroupItems = append(snapshot.GroupItems, model.GroupItem{ID: 501, ChannelID: 101, GroupID: 999})
	snapshot.SiteAccounts = append(snapshot.SiteAccounts, model.LegacySiteAccount{ID: 40, SiteID: 999})
	report, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"proxy_unavailable", "binding_channel_missing", "group_missing", "account_site_missing"} {
		assertIssue(t, report, code, "blocker")
	}
}

func TestPreflightReportDoesNotExposeSensitiveInput(t *testing.T) {
	snapshot := fixture()
	secret := "never-print-this-credential"
	snapshot.Sites[0].Name = secret
	snapshot.Sites[1].CheckinHTTPBody = secret
	snapshot.Sites[1].CustomHeader = []model.CustomHeader{{HeaderKey: "Authorization", HeaderValue: secret}}
	snapshot.SiteAccounts[0].Password = secret
	snapshot.SiteAccounts[0].AccessToken = secret
	snapshot.SiteAccounts[0].RefreshToken = secret
	snapshot.SiteAccounts[1].ProxyMode = model.ProxyUsageMode(secret)
	snapshot.Channels[0].Name = secret
	snapshot.ChannelKeys[0].ChannelKey = secret
	snapshot.ProxyConfigurations[0].URL = "https://" + secret + "@proxy.example"
	report, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatal("preflight report exposed a secret")
	}
}

func TestPreflightUsesStableIDsForSharedSessions(t *testing.T) {
	snapshot := fixture()
	copy := snapshot.SiteAccounts[0]
	copy.ID = 11 // Equal tokens do not establish a shared session relationship.
	snapshot.SiteAccounts = append(snapshot.SiteAccounts, copy)
	snapshot.SiteAccounts[2].LinkedAccountID = id(11)
	report, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.SharedCredentials != 0 || len(report.Credentials) != 3 {
		t.Fatalf("unrelated source identities merged: %+v", report.Credentials)
	}
}

func TestPreflightIgnoresUnusedSubscriptionTokenProxy(t *testing.T) {
	snapshot := fixture()
	snapshot.SiteAccounts[1].ProxyMode = model.ProxyUsageModePool
	snapshot.SiteAccounts[1].ProxyConfigID = id(999)
	report, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Blockers != 0 || report.Accounts[0].Network.ProxyConfigID == nil || *report.Accounts[0].Network.ProxyConfigID != 7 {
		t.Fatalf("retained unused proxy affected effective network: %+v", report.Issues)
	}
}

func TestPreflightDetectsSharedSessionNetworkConflict(t *testing.T) {
	snapshot := fixture()
	for i := range snapshot.Sites {
		snapshot.Sites[i].Platform = model.SitePlatformNewAPI
	}
	snapshot.Sites[1].CheckinHTTPEnabled, snapshot.Sites[1].CheckinUseSubscriptionToken = false, false
	snapshot.Sites[1].BaseURL = snapshot.Sites[0].BaseURL
	report, err := Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	assertIssue(t, report, "shared_credential_network_conflict", "review")
}

func assertIssue(t *testing.T, report *Report, code, severity string) {
	t.Helper()
	for _, issue := range report.Issues {
		if issue.Code == code && issue.Severity == severity {
			return
		}
	}
	t.Fatalf("missing %s/%s in %+v", severity, code, report.Issues)
}
