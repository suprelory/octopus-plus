package v2

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func conversionFixture() *LegacyInput {
	snapshot := fixture()
	for i := range snapshot.Sites {
		s := &snapshot.Sites[i]
		s.Name = []string{"Source", "Token rewards", "Cookie rewards"}[i]
		s.Enabled = true
		s.CheckinTimezone = model.DefaultSiteCheckinTimezone
		s.CheckinWindowStart, s.CheckinWindowEnd = "00:00", "23:59"
		if s.CheckinHTTPEnabled {
			s.CheckinHTTPMethod, s.CheckinHTTPPath = "POST", "/daily"
		}
	}
	for i := range snapshot.SiteAccounts {
		a := &snapshot.SiteAccounts[i]
		a.Name = []string{"Source session", "Token account", "Cookie account"}[i]
		a.Enabled = true
		a.AutoCheckin = a.SiteID != 1
	}
	return &LegacyInput{Configuration: *snapshot, CapturedAt: time.Date(2026, 11, 20, 6, 0, 0, 0, time.UTC), HistoryComplete: true}
}

func requireConversion(t *testing.T, input *LegacyInput) *Conversion {
	t.Helper()
	c, err := Convert(input)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Ready() {
		t.Fatalf("conversion blocked: %+v", c.Report.Issues)
	}
	return c
}

func conversionIssue(c *Conversion, code string) bool {
	return slices.ContainsFunc(c.Report.Issues, func(issue Issue) bool { return issue.Code == code })
}

func TestConversionPreservesChannelsSharedCredentialsAndIndependentBalance(t *testing.T) {
	input := conversionFixture()
	input.Configuration.Sites[0].CustomHeader = []model.CustomHeader{{HeaderKey: "User-Agent", HeaderValue: "source-agent"}}
	input.Configuration.Sites[1].CustomHeader = []model.CustomHeader{{HeaderKey: "User-Agent", HeaderValue: "old-agent"}, {HeaderKey: "Authorization", HeaderValue: "retired-token"}}
	input.Configuration.Sites[1].CheckinHTTPHeaders = []model.CustomHeader{{HeaderKey: "Cookie", HeaderValue: "retired=cookie"}}
	input.Configuration.Sites[1].CheckinHTTPBody = `{"token":"{{access_token}}","id":"{{platform_user_id}}"}`
	input.Configuration.SiteAccounts[1].Enabled = false
	input.Configuration.SiteAccounts[2].AutoCheckin = false
	input.Configuration.Channels[0].Managed = true
	input.Configuration.Channels[0].ManagedSource = &model.ManagedChannelSource{SiteID: 1, SiteAccountID: 10}
	input.Configuration.ChannelKeys[0].Enabled = false
	input.Configuration.GroupItems[0].Weight, input.Configuration.GroupItems[0].Priority = 7, 4
	input.ChannelStats = []model.StatsChannel{{ChannelID: 100}}
	input.ArchivedSiteStats = []model.StatsSiteModelHourly{{SiteAccountID: 10}}
	input.GroupPresets = []model.GroupPreset{{ID: 600, GroupID: 400, Name: "Pinned routes", Items: []model.GroupPresetItem{{ChannelID: 100, Weight: 7, Priority: 4}}}}
	input.Configuration.Groups[0].ActivePresetID = id(600)
	before, _ := json.Marshal(input)
	c := requireConversion(t, input)
	if len(c.Sites) != 2 || len(c.Accounts) != 2 || len(c.Credentials) != 2 {
		t.Fatalf("unexpected counts: %+v", c.Report)
	}
	if c.Accounts[0].ID != 20 || c.Accounts[0].Enabled || c.Accounts[0].CredentialID != 10 || c.Accounts[0].AuthMode != model.CheckinAuthBearer {
		t.Fatal("token identity or enablement changed")
	}
	if c.Accounts[1].ID != 30 || c.Accounts[1].CredentialID != 30 || c.Accounts[1].AutoCheckin || c.Accounts[1].BalanceConfig.CredentialID == nil || *c.Accounts[1].BalanceConfig.CredentialID != 10 {
		t.Fatal("cookie/balance ownership changed")
	}
	if c.Accounts[0].ProxyMode != model.ProxyUsageModePool || *c.Accounts[0].ProxyConfigID != 7 || c.Accounts[1].ProxyMode != model.ProxyUsageModeInherit {
		t.Fatal("request proxy changed")
	}
	if c.Credentials[0].RefreshToken != "source-refresh" || c.Credentials[0].AuthBaseURL != "https://login.example" || c.Credentials[0].AuthHeaders[0].HeaderValue != "source-agent" {
		t.Fatal("authentication context lost")
	}
	if c.Sites[0].CheckinHTTPBody != `{"token":"","id":""}` || c.Sites[0].CheckinHTTPHeaders[0].HeaderValue != "source-agent" {
		t.Fatal("retired placeholders or effective user agent changed")
	}
	channel := c.Channels[0]
	if channel.ID != 100 || channel.Enabled || channel.AutoSync || channel.Managed || channel.ManagedSource != nil || channel.AutoGroup != model.AutoGroupTypeExact {
		t.Fatal("channel conversion changed preserved settings")
	}
	if !reflect.DeepEqual(c.ChannelKeys, input.Configuration.ChannelKeys) || !reflect.DeepEqual(c.GroupItems, input.Configuration.GroupItems) || !reflect.DeepEqual(c.GroupPresets, input.GroupPresets) || !reflect.DeepEqual(c.ChannelStats, input.ChannelStats) || !reflect.DeepEqual(c.ArchivedSiteStats, input.ArchivedSiteStats) {
		t.Fatal("keys, group associations or stats changed")
	}
	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatal("conversion mutated source")
	}
	report, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"source-token", "source-refresh", "external-cookie", "channel-secret", "proxy-secret", "source-agent", "login.example"} {
		if strings.Contains(string(report), secret) {
			t.Fatal("conversion report leaked sensitive data")
		}
	}
	again := requireConversion(t, input)
	if !reflect.DeepEqual(c, again) {
		t.Fatal("conversion is not deterministic")
	}
}

func TestConversionMissingCredentialsPreservesOrphanAndSubmittedState(t *testing.T) {
	input := conversionFixture()
	a := &input.Configuration.SiteAccounts[1]
	a.LinkedAccountID, a.AccessToken = id(999), "stale-must-not-be-used"
	a.CheckinPending = &model.SiteCheckinPending{LogID: 9007199254740993, Date: "2026-11-20", Submitted: true,
		StartedAt: input.CapturedAt, Deadline: input.CapturedAt.Add(time.Hour), Status: model.SiteExecutionStatusPending,
		Fingerprint: input.Configuration.Sites[1].CheckinWorkflowFingerprint(a)}
	input.Configuration.SiteAccounts[2].LinkedAccountID = id(999)
	c := requireConversion(t, input)
	orphan := c.Accounts[0]
	if orphan.ID != a.ID || orphan.CredentialID != 0 || orphan.AutoCheckin || orphan.NextAutoCheckinAt != nil || !orphan.Enabled || !orphan.CheckinPending.Submitted {
		t.Fatal("orphan or submission state lost")
	}
	if orphan.CheckinPending.Fingerprint != "migration:invalid-legacy-workflow" || c.Report.RebuiltLogs != 1 || c.Logs[0].ID != a.CheckinPending.LogID {
		t.Fatal("workflow was authorized or history was lost")
	}
	if c.Accounts[1].CredentialID != 30 || c.Accounts[1].BalanceConfig.Mode != model.CheckinBalanceDisabled {
		t.Fatal("failed balance link blocked cookie identity")
	}
	if len(c.Credentials) != 1 || c.Credentials[0].AccessToken != "" || !conversionIssue(c, "checkin_credentials_unavailable") {
		t.Fatal("stale credentials used")
	}
}

func TestConversionWorkflowEvidenceAndHistoryArePreserved(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "valid", false: "invalid"}[valid], func(t *testing.T) {
			input := conversionFixture()
			s, a := &input.Configuration.Sites[2], &input.Configuration.SiteAccounts[2]
			s.CheckinStatus = &model.SiteCheckinStatusConfig{Method: "GET", Path: "/status", CheckedPath: "checked"}
			s.CheckinStatus.Normalize()
			a.CheckinPending = &model.SiteCheckinPending{LogID: 91, Date: "2026-11-20", Submitted: true, Finished: true, Status: model.SiteExecutionStatusSuccess,
				StartedAt: input.CapturedAt, Deadline: input.CapturedAt.Add(time.Hour), Fingerprint: s.CheckinWorkflowFingerprint(a)}
			if !valid {
				a.CheckinPending.Fingerprint = "obsolete"
			}
			input.Logs = []model.CheckinLog{{ID: 91, SiteID: 3, AccountID: 30, SiteName: "Historical name", Source: "batch", BatchJobID: 42, Status: model.SiteExecutionStatusSuccess, Reward: "0", StartedAt: input.CapturedAt, FinishedAt: input.CapturedAt}}
			input.Batches = []model.CheckinBatchJob{{ID: 42, Status: model.SiteCheckinBatchJobStatusRunning, SiteIDs: []int{3}, Attempted: 1, Success: 1, StartedAt: input.CapturedAt}}
			c := requireConversion(t, input)
			account := c.Accounts[1]
			matches := account.CheckinPending.Fingerprint == c.Sites[1].WorkflowFingerprint(account, c.Credentials[1])
			if matches != valid || !account.CheckinPending.Submitted || !account.CheckinPending.Finished || account.CheckinPending.BatchJobID != 42 || account.CheckinPending.Source != model.CheckinTriggerBatch {
				t.Fatal("workflow evidence changed")
			}
			if !reflect.DeepEqual(c.Logs, input.Logs) || c.Report.RebuiltLogs != 0 {
				t.Fatal("history or reward rewritten")
			}
			if c.Batches[0].Status != model.SiteCheckinBatchJobStatusInterrupted || c.Batches[0].Success != 1 || !c.Batches[0].FinishedAt.Equal(input.CapturedAt) {
				t.Fatal("active batch was resumed or counts lost")
			}
		})
	}
}

func TestConversionPreservesExpiredRefreshUncertaintyWithoutRestoringLocks(t *testing.T) {
	input := conversionFixture()
	a := &input.Configuration.SiteAccounts[0]
	a.TokenRefreshLease, a.TokenRefreshLeaseUntil = "expired-owner", 1
	a.CredentialVersion = 18
	c := requireConversion(t, input)
	credential := c.Credentials[0]
	if !credential.TokenRefreshStarted || credential.TokenRefreshLease != "" || credential.TokenRefreshLeaseUntil != 0 || credential.TokenRefreshRetryAt != 0 || credential.CredentialVersion != 18 {
		t.Fatal("refresh uncertainty was cleared or runtime lock restored")
	}
	oldFingerprint := c.Fingerprint
	a.TokenRefreshLease = "another-owner"
	if requireConversion(t, input).Fingerprint == oldFingerprint {
		t.Fatal("source fingerprint ignores runtime exchange state")
	}
}

func TestConversionDetectsIncompatibleSharedSessionContexts(t *testing.T) {
	input := conversionFixture()
	input.Configuration.Sites = append(input.Configuration.Sites, model.Site{ID: 4, Name: "Platform", Kind: model.SiteKindCheckin, Platform: model.SitePlatformSub2API,
		BaseURL: "https://login.example", LinkedSiteID: id(1), ProxyMode: model.ProxyUsageModeDirect})
	input.Configuration.SiteAccounts = append(input.Configuration.SiteAccounts, model.SiteAccount{ID: 40, SiteID: 4, Name: "Linked", CredentialType: model.SiteCredentialTypeLinkedAccount, LinkedAccountID: id(10)})
	c, err := Convert(input)
	if err != nil || c.Ready() || !conversionIssue(c, "shared_credential_network_conflict") {
		t.Fatalf("conflicting shared context accepted: %v", err)
	}
	if len(c.Credentials) != 2 {
		t.Fatal("conflict cloned the refresh token")
	}
}

func TestConversionUnsplitSitesAndEarlierProvenance(t *testing.T) {
	input := conversionFixture()
	input.Configuration.Sites[0].Platform = model.SitePlatformNewAPI
	input.Configuration.Sites[0].Name = "Cookie rewards"
	input.Configuration.SiteAccounts[0].AutoCheckin = true
	c := requireConversion(t, input)
	if len(c.Sites) != 3 || c.Sites[0].ID != 1 || c.Accounts[0].ID != 10 || !conversionIssue(c, "legacy_site_name_disambiguated") {
		t.Fatal("unsplit legacy config lost")
	}
	input = conversionFixture()
	input.Configuration.SiteAccounts[1].CheckinSourceAccountID = id(10)
	input.Configuration.SiteAccounts[0].AutoCheckin = true
	input.Configuration.SiteAccounts[1].Name = "Independently edited"
	input.Logs = []model.CheckinLog{{ID: 99, SiteID: 1, AccountID: 10, SiteName: "Original site", AccountName: "Original account", Reward: "2"}}
	c = requireConversion(t, input)
	if len(c.Accounts) != 2 || c.Accounts[0].Name != "Independently edited" || c.Logs[0].SiteID != 2 || c.Logs[0].AccountID != 20 || c.Logs[0].AccountName != "Original account" || c.Logs[0].Reward != "2" {
		t.Fatal("split provenance was duplicated or history snapshot lost")
	}
}

func TestConversionBlocksBrokenRelationsAndChangedTemplateIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		edit       func(*LegacyInput)
	}{
		{"binding", "binding_channel_missing", func(i *LegacyInput) { i.Configuration.SiteChannelBindings[0].ChannelID = 999 }},
		{"group", "group_missing", func(i *LegacyInput) { i.Configuration.GroupItems[0].GroupID = 999 }},
		{"template", "request_template_identity_conflict", func(i *LegacyInput) {
			i.Configuration.Sites[1].CheckinHTTPBody = `{"name":"{{username}}"}`
			i.Configuration.SiteAccounts[0].Username = "source"
			i.Configuration.SiteAccounts[1].Username = "target"
		}},
		{"dual identity", "legacy_http_identity_header_conflict", func(i *LegacyInput) {
			i.Configuration.Sites[2].CustomHeader = []model.CustomHeader{{HeaderKey: "Authorization", HeaderValue: "secret"}}
		}},
		{"history owner", "history_account_site_mismatch", func(i *LegacyInput) { i.Logs = []model.CheckinLog{{ID: 9, SiteID: 2, AccountID: 30}} }},
		{"preset channel", "preset_channel_missing", func(i *LegacyInput) {
			i.GroupPresets = []model.GroupPreset{{ID: 600, GroupID: 400, Items: []model.GroupPresetItem{{ChannelID: 999}}}}
		}},
		{"active preset", "active_group_preset_missing", func(i *LegacyInput) { i.Configuration.Groups[0].ActivePresetID = id(999) }},
		{"managed channel", "managed_channel_binding_missing", func(i *LegacyInput) { i.Configuration.Channels[1].Managed = true }},
		{"ambiguous provenance", "ambiguous_split_provenance", func(i *LegacyInput) {
			i.Configuration.SiteAccounts[1].CheckinSourceAccountID = id(10)
			i.Configuration.SiteAccounts[2].CheckinSourceAccountID = id(10)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := conversionFixture()
			tc.edit(input)
			c, err := Convert(input)
			if err != nil || c.Ready() || !conversionIssue(c, tc.code) {
				t.Fatalf("expected %s: %v %+v", tc.code, err, c)
			}
		})
	}
}

func TestConversionOldCredentialFormsAndArchivedSites(t *testing.T) {
	input := conversionFixture()
	site, account := &input.Configuration.Sites[2], &input.Configuration.SiteAccounts[2]
	site.Archived, site.Enabled, site.ArchivedAt = true, false, clonePointer(&input.CapturedAt)
	site.CustomHeader = []model.CustomHeader{{HeaderKey: "Cookie", HeaderValue: "session=legacy"}}
	account.CredentialType, account.Cookie, account.AccessToken = model.SiteCredentialTypeAccessToken, "", "old=token"
	account.NextAutoCheckinAt = clonePointer(&input.CapturedAt)
	site.CheckinHTTPHeaders = []model.CustomHeader{{HeaderKey: "Cookie", HeaderValue: "session={{access_token}}"}}
	c := requireConversion(t, input)
	if c.Credentials[1].Cookie != "session=old=token" || c.Credentials[1].AccessToken != "" || !c.Sites[1].Archived || c.Sites[1].Enabled || c.Accounts[1].NextAutoCheckinAt != nil {
		t.Fatal("old Cookie or archived availability changed")
	}
	if len(c.Sites[1].CustomHeader) != 0 || len(c.Sites[1].CheckinHTTPHeaders) != 0 {
		t.Fatal("legacy Cookie headers were not moved to the credential")
	}
	// The older platform split had provenance but no linked_account_id yet.
	input = conversionFixture()
	input.Configuration.Sites = input.Configuration.Sites[:2]
	input.Configuration.SiteAccounts = input.Configuration.SiteAccounts[:2]
	input.Configuration.Sites[1].CheckinHTTPEnabled, input.Configuration.Sites[1].CheckinUseSubscriptionToken = false, false
	input.Configuration.Sites[1].BaseURL = input.Configuration.Sites[0].BaseURL
	account = &input.Configuration.SiteAccounts[1]
	account.CheckinSourceAccountID, account.LinkedAccountID = id(10), nil
	account.CredentialType, account.AccessToken = model.SiteCredentialTypeAccessToken, "stale-copy"
	c = requireConversion(t, input)
	if len(c.Credentials) != 1 || c.Credentials[0].ID != 10 || c.Credentials[0].AccessToken != "source-token" || c.Accounts[0].CredentialID != 10 {
		t.Fatal("old split duplicated or restored a stale session")
	}
	account.CredentialType = model.SiteCredentialTypeLinkedAccount
	c = requireConversion(t, input)
	if c.Accounts[0].CredentialID != 0 || c.Accounts[0].AutoCheckin || len(c.Credentials) != 0 {
		t.Fatal("explicitly removed link was restored from provenance")
	}
}

func TestConversionInputRequiresHistoryCoverageAndPreservesLongIDs(t *testing.T) {
	input := conversionFixture()
	dump := &model.DBDump{Version: 1, ExportedAt: input.CapturedAt, Sites: input.Configuration.Sites, SiteAccounts: input.Configuration.SiteAccounts,
		ProxyConfigurations: input.Configuration.ProxyConfigurations, SiteCheckinLogs: []model.CheckinLog{{ID: 9007199254740993, SiteID: 3, AccountID: 30, Reward: "1"}}}
	converted, err := InputFromDump(dump)
	if err != nil {
		t.Fatal(err)
	}
	c := requireConversion(t, converted)
	if c.Logs[0].ID != 9007199254740993 || !conversionIssue(c, "history_not_included") {
		t.Fatal("history coverage or long ID lost")
	}
	if _, err := Convert(&LegacyInput{Configuration: *fixture()}); err == nil {
		t.Fatal("accepted missing capture time")
	}
}
