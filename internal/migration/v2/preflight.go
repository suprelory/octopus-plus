package v2

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

// Report deliberately exposes only IDs, enums, booleans and counts from the
// snapshot. Names, URLs, headers, templates and credential values never escape.
// A report is a conversion inventory, not authorization to run a migration.
type Report struct {
	Version         int             `json:"report_version"`
	BackupVersion   int             `json:"backup_version"`
	ExportedAt      time.Time       `json:"exported_at"`
	HistoryIncluded bool            `json:"history_included"`
	StatsIncluded   bool            `json:"stats_included"`
	Summary         Summary         `json:"summary"`
	Channels        []ChannelPlan   `json:"channels"`
	Accounts        []AccountPlan   `json:"checkin_accounts"`
	Credentials     []CredentialUse `json:"credential_sources"`
	Issues          []Issue         `json:"issues"`
}

type Summary struct {
	SubscriptionSites int `json:"subscription_sites"`
	CheckinSites      int `json:"checkin_sites"`
	OrdinaryChannels  int `json:"ordinary_channels"`
	ProjectedChannels int `json:"projected_channels"`
	CheckinAccounts   int `json:"checkin_accounts"`
	SharedCredentials int `json:"shared_credential_sources"`
	PendingWorkflows  int `json:"pending_workflows"`
	Blockers          int `json:"blockers"`
	Reviews           int `json:"reviews"`
}

type Issue struct {
	Severity  string `json:"severity"` // blocker or review
	Code      string `json:"code"`
	Entity    string `json:"entity"`
	ID        int    `json:"id"`
	RelatedID int    `json:"related_id,omitempty"`
}

type NetworkPlan struct {
	Mode          model.ProxyUsageMode `json:"mode"`
	ProxyConfigID *int                 `json:"proxy_config_id,omitempty"`
	HeaderSiteID  int                  `json:"header_site_id,omitempty"`
}

type ChannelPlan struct {
	ChannelID int                 `json:"channel_id"`
	SiteID    int                 `json:"source_site_id"`
	AccountID int                 `json:"source_account_id"`
	Enabled   bool                `json:"enabled"`
	AutoSync  bool                `json:"auto_sync"`
	AutoGroup model.AutoGroupType `json:"effective_auto_group"`
	Network   NetworkPlan         `json:"network"`
	KeyIDs    []int               `json:"key_ids"`
	GroupIDs  []int               `json:"group_ids"`
}

type AccountPlan struct {
	AccountID           int           `json:"account_id"`
	SiteID              int           `json:"site_id"`
	AuthMode            string        `json:"auth_mode"`
	CredentialAccountID int           `json:"credential_source_account_id,omitempty"`
	AuthSiteID          int           `json:"auth_source_site_id,omitempty"`
	NeedsConfiguration  bool          `json:"needs_configuration"`
	BalanceMode         string        `json:"balance_mode"`
	BalanceAccountID    int           `json:"balance_source_account_id,omitempty"`
	BalanceSiteID       int           `json:"balance_source_site_id,omitempty"`
	Network             NetworkPlan   `json:"request_network"`
	BalanceNetwork      *NetworkPlan  `json:"balance_network,omitempty"`
	Workflow            *WorkflowPlan `json:"workflow,omitempty"`
}

type WorkflowPlan struct {
	Submitted        bool `json:"submitted"`
	Finished         bool `json:"finished"`
	FingerprintValid bool `json:"fingerprint_valid"`
}

type CredentialUse struct {
	SourceAccountID int   `json:"source_account_id"`
	PrimaryUsers    []int `json:"primary_account_ids"`
	BalanceUsers    []int `json:"balance_account_ids"`
}

type preflight struct {
	snapshot    *Snapshot
	report      Report
	sites       map[int]model.LegacySite
	accounts    map[int]model.LegacySiteAccount
	channels    map[int]model.Channel
	proxies     map[int]model.ProxyConfiguration
	groups      map[int]model.Group
	userGroups  map[int]model.LegacySiteUserGroup
	settings    map[model.SettingKey]string
	credentials map[int]*CredentialUse
	contexts    map[int]string
}

func Analyze(snapshot *Snapshot) (*Report, error) {
	if snapshot == nil || snapshot.Version != 1 {
		return nil, errors.New("preflight requires a version 1 snapshot")
	}
	p := &preflight{
		snapshot: snapshot,
		report: Report{Version: 1, BackupVersion: snapshot.Version, ExportedAt: snapshot.ExportedAt,
			HistoryIncluded: snapshot.IncludeLogs, StatsIncluded: snapshot.IncludeStats,
			Channels: []ChannelPlan{}, Accounts: []AccountPlan{}, Credentials: []CredentialUse{}, Issues: []Issue{}},
		settings: make(map[model.SettingKey]string), credentials: make(map[int]*CredentialUse), contexts: make(map[int]string),
	}
	p.sites = indexRows(p, "site", snapshot.Sites, func(v model.LegacySite) int { return v.ID })
	p.accounts = indexRows(p, "account", snapshot.SiteAccounts, func(v model.LegacySiteAccount) int { return v.ID })
	p.channels = indexRows(p, "channel", snapshot.Channels, func(v model.Channel) int { return v.ID })
	p.proxies = indexRows(p, "proxy", snapshot.ProxyConfigurations, func(v model.ProxyConfiguration) int { return v.ID })
	p.groups = indexRows(p, "group", snapshot.Groups, func(v model.Group) int { return v.ID })
	p.userGroups = indexRows(p, "site_group", snapshot.SiteUserGroups, func(v model.LegacySiteUserGroup) int { return v.ID })
	indexRows(p, "binding", snapshot.SiteChannelBindings, func(v model.LegacySiteChannelBinding) int { return v.ID })
	indexRows(p, "channel_key", snapshot.ChannelKeys, func(v model.ChannelKey) int { return v.ID })
	indexRows(p, "group_item", snapshot.GroupItems, func(v model.GroupItem) int { return v.ID })
	for _, setting := range snapshot.Settings {
		if _, exists := p.settings[setting.Key]; exists {
			p.issue("blocker", "duplicate_setting", "backup", 0, 0)
			continue
		}
		p.settings[setting.Key] = setting.Value
	}
	for _, site := range snapshot.Sites {
		if site.IsCheckinOnly() {
			p.report.Summary.CheckinSites++
			if !validHTTPURL(site.BaseURL) || site.Platform.Validate() != nil {
				p.issue("blocker", "invalid_checkin_site", "site", site.ID, 0)
			}
		} else {
			p.report.Summary.SubscriptionSites++
			if site.Kind != "" && site.Kind != model.LegacySiteKindRelay {
				p.issue("blocker", "invalid_site_kind", "site", site.ID, 0)
			}
			if site.CheckinHTTPEnabled || site.CheckinMode == model.SiteCheckinModeEnabled ||
				(site.ExternalCheckinURL != nil && strings.TrimSpace(*site.ExternalCheckinURL) != "") || site.CheckinVerifiedAt != nil {
				p.issue("review", "legacy_checkin_requires_split", "site", site.ID, 0)
			}
		}
		if len(site.Accounts) > 0 {
			p.issue("blocker", "nested_accounts_require_flat_export", "site", site.ID, 0)
		}
	}
	p.analyzeChannels()
	for _, account := range snapshot.SiteAccounts {
		site, exists := p.sites[account.SiteID]
		if !exists {
			p.issue("blocker", "account_site_missing", "account", account.ID, account.SiteID)
			continue
		}
		if !site.IsCheckinOnly() {
			if account.AutoCheckin || account.LastCheckinAt != nil || account.NextAutoCheckinAt != nil || account.CheckinPending != nil {
				p.issue("review", "legacy_checkin_requires_split", "account", account.ID, site.ID)
			}
			continue
		}
		p.analyzeAccount(site, account)
	}
	p.finish()
	return &p.report, nil
}

func indexRows[T any](p *preflight, entity string, rows []T, id func(T) int) map[int]T {
	result := make(map[int]T, len(rows))
	for _, row := range rows {
		key := id(row)
		if key <= 0 {
			p.issue("blocker", "invalid_id", entity, key, 0)
			continue
		}
		if _, exists := result[key]; exists {
			p.issue("blocker", "duplicate_id", entity, key, 0)
			continue
		}
		result[key] = row
	}
	return result
}

func (p *preflight) issue(severity, code, entity string, id, related int) {
	p.report.Issues = append(p.report.Issues, Issue{Severity: severity, Code: code, Entity: entity, ID: id, RelatedID: related})
}

func (p *preflight) analyzeChannels() {
	globalMode := model.AutoGroupTypeNone
	if value, exists := p.settings[model.SettingKeyProjectedChannelAutoGroupEnabled]; exists {
		var valid bool
		globalMode, valid = model.ParseAutoGroupSettingValue(value)
		if !valid {
			p.issue("blocker", "invalid_projected_auto_group", "backup", 0, 0)
		}
	}
	keys := make(map[int][]int)
	for _, key := range p.snapshot.ChannelKeys {
		if _, exists := p.channels[key.ChannelID]; !exists {
			p.issue("blocker", "key_channel_missing", "channel_key", key.ID, key.ChannelID)
		}
		keys[key.ChannelID] = append(keys[key.ChannelID], key.ID)
	}
	groups := make(map[int][]int)
	for _, item := range p.snapshot.GroupItems {
		if _, exists := p.channels[item.ChannelID]; !exists {
			p.issue("blocker", "group_channel_missing", "group_item", item.ID, item.ChannelID)
		}
		if _, exists := p.groups[item.GroupID]; !exists {
			p.issue("blocker", "group_missing", "group_item", item.ID, item.GroupID)
		}
		groups[item.ChannelID] = append(groups[item.ChannelID], item.GroupID)
	}
	bound := make(map[int]bool)
	for _, binding := range p.snapshot.SiteChannelBindings {
		channel, exists := p.channels[binding.ChannelID]
		if !exists {
			p.issue("blocker", "binding_channel_missing", "binding", binding.ID, binding.ChannelID)
			continue
		}
		if bound[channel.ID] {
			p.issue("blocker", "duplicate_channel_binding", "binding", binding.ID, channel.ID)
			continue
		}
		bound[channel.ID] = true
		site, siteOK := p.sites[binding.SiteID]
		account, accountOK := p.accounts[binding.SiteAccountID]
		if !siteOK || !accountOK || account.SiteID != binding.SiteID || site.IsCheckinOnly() {
			p.issue("blocker", "invalid_binding_source", "binding", binding.ID, binding.SiteAccountID)
		}
		if binding.SiteUserGroupID != nil {
			group, exists := p.userGroups[*binding.SiteUserGroupID]
			if !exists || group.SiteAccountID != binding.SiteAccountID {
				p.issue("blocker", "invalid_binding_group", "binding", binding.ID, *binding.SiteUserGroupID)
			}
		}
		autoGroup := channel.AutoGroup
		if globalMode != model.AutoGroupTypeNone {
			autoGroup = globalMode
		}
		if _, supported := outbound.Descriptor(channel.Type); !supported {
			autoGroup = model.AutoGroupTypeNone
			p.issue("review", "unsupported_channel_type", "channel", channel.ID, 0)
		}
		if !autoGroup.Valid() {
			p.issue("blocker", "invalid_channel_auto_group", "channel", channel.ID, 0)
		}
		network := p.network(channel.ProxyMode, channel.ProxyConfigID, 0, "channel", channel.ID, "blocker")
		p.report.Channels = append(p.report.Channels, ChannelPlan{ChannelID: channel.ID, SiteID: binding.SiteID,
			AccountID: binding.SiteAccountID, Enabled: channel.Enabled, AutoSync: channel.AutoSync,
			AutoGroup: autoGroup, Network: network, KeyIDs: sortedIDs(keys[channel.ID]), GroupIDs: sortedIDs(groups[channel.ID])})
	}
	for _, channel := range p.snapshot.Channels {
		if !bound[channel.ID] {
			p.network(channel.ProxyMode, channel.ProxyConfigID, 0, "channel", channel.ID, "blocker")
		}
		if len(channel.Keys) > 0 {
			p.issue("blocker", "nested_keys_require_flat_export", "channel", channel.ID, 0)
		}
	}
	p.report.Summary.ProjectedChannels = len(bound)
	p.report.Summary.OrdinaryChannels = len(p.channels) - len(bound)
}

func (p *preflight) accountNetwork(site model.LegacySite, account model.LegacySiteAccount, severity string) NetworkPlan {
	mode, id := account.ProxyMode, account.ProxyConfigID
	if mode == "" || mode == model.ProxyUsageModeInherit {
		mode, id = site.ProxyMode, site.ProxyConfigID
	}
	return p.network(mode, id, site.ID, "account", account.ID, severity)
}

func (p *preflight) network(mode model.ProxyUsageMode, id *int, headerSiteID int, entity string, entityID int, severity string) NetworkPlan {
	if mode == "" {
		mode = model.ProxyUsageModeDirect
	}
	plan := NetworkPlan{Mode: mode, HeaderSiteID: headerSiteID}
	switch mode {
	case model.ProxyUsageModeDirect:
	case model.ProxyUsageModeSystem:
		if value := p.settings[model.SettingKeyProxyURL]; value != "" {
			if _, err := model.NormalizeProxyURL(value); err != nil {
				p.issue(severity, "invalid_system_proxy", entity, entityID, 0)
			}
		}
	case model.ProxyUsageModePool:
		if id == nil || *id <= 0 {
			p.issue(severity, "proxy_reference_missing", entity, entityID, 0)
			break
		}
		proxyID := *id
		plan.ProxyConfigID = &proxyID
		proxy, exists := p.proxies[proxyID]
		if !exists || !proxy.Enabled {
			p.issue(severity, "proxy_unavailable", entity, entityID, proxyID)
		} else if _, err := model.NormalizeProxyURL(proxy.URL); err != nil {
			p.issue(severity, "invalid_proxy_url", entity, entityID, proxyID)
		}
	default:
		// Do not reflect arbitrary input strings into a report.
		plan.Mode = "invalid"
		p.issue(severity, "invalid_proxy_mode", entity, entityID, 0)
	}
	return plan
}

func (p *preflight) linkedSource(site model.LegacySite, account model.LegacySiteAccount) (model.LegacySite, model.LegacySiteAccount, bool) {
	if site.LinkedSiteID == nil || account.LinkedAccountID == nil {
		return model.LegacySite{}, model.LegacySiteAccount{}, false
	}
	sourceSite, exists := p.sites[*site.LinkedSiteID]
	source, accountExists := p.accounts[*account.LinkedAccountID]
	if !exists || !accountExists || sourceSite.IsCheckinOnly() || source.SiteID != sourceSite.ID {
		return model.LegacySite{}, model.LegacySiteAccount{}, false
	}
	return sourceSite, source, true
}

func (p *preflight) analyzeAccount(site model.LegacySite, account model.LegacySiteAccount) {
	p.report.Summary.CheckinAccounts++
	plan := AccountPlan{AccountID: account.ID, SiteID: site.ID, BalanceMode: "disabled"}
	primarySite, primary := site, account
	primaryValid := true
	// The retained check-in proxy is not used in subscription-token mode.
	if !site.CheckinHTTPEnabled || !site.CheckinUseSubscriptionToken {
		plan.Network = p.accountNetwork(site, account, "blocker")
	}
	if site.CheckinHTTPEnabled {
		if site.CheckinUseSubscriptionToken {
			plan.AuthMode = "bearer"
			primarySite, primary, primaryValid = p.linkedSource(site, account)
			primaryValid = primaryValid && site.Platform == model.SitePlatformSub2API && primarySite.Platform == model.SitePlatformSub2API &&
				primary.CredentialType == model.LegacySiteCredentialTypeAccessToken && strings.TrimSpace(primary.AccessToken) != ""
			if primaryValid {
				plan.Network = p.accountNetwork(primarySite, primary, "blocker")
			}
		} else {
			plan.AuthMode = "cookie"
			primaryValid = model.ValidateSiteCheckinCookie(account.Cookie) == nil
		}
	} else {
		plan.AuthMode = "platform"
		if site.LinkedSiteID != nil || account.LinkedAccountID != nil || account.CredentialType == model.LegacySiteCredentialTypeLinkedAccount {
			primarySite, primary, primaryValid = p.linkedSource(site, account)
			primaryValid = primaryValid && site.Platform == primarySite.Platform && strings.TrimRight(site.BaseURL, "/") == strings.TrimRight(primarySite.BaseURL, "/")
		}
		primaryValid = primaryValid && platformCredentialValid(site.Platform, primary)
		if site.CheckinAdapter() == "" {
			p.issue("review", "checkin_adapter_not_configured", "account", account.ID, site.ID)
		}
	}
	if primaryValid {
		if !validHTTPURL(primarySite.BaseURL) {
			p.issue("blocker", "invalid_auth_source_url", "account", account.ID, primarySite.ID)
		}
		plan.CredentialAccountID, plan.AuthSiteID = primary.ID, primarySite.ID
		p.credentialUse(primary, account.ID, false, plan.Network)
	} else {
		plan.NeedsConfiguration = true
		p.issue("blocker", "checkin_credentials_unavailable", "account", account.ID, account.SiteID)
	}
	if !site.CheckinHTTPEnabled && primaryValid {
		plan.BalanceMode, plan.BalanceAccountID, plan.BalanceSiteID = "primary", primary.ID, primarySite.ID
		network := plan.Network
		plan.BalanceNetwork = &network
		p.credentialUse(primary, account.ID, true, network)
	} else if site.CheckinHTTPEnabled && account.LinkedAccountID != nil {
		balanceSite, balance, valid := p.linkedSource(site, account)
		valid = valid && balanceCredentialValid(balanceSite.Platform, balance)
		if valid {
			if !validHTTPURL(balanceSite.BaseURL) {
				p.issue("review", "invalid_balance_source_url", "account", account.ID, balanceSite.ID)
			}
			plan.BalanceMode, plan.BalanceAccountID, plan.BalanceSiteID = "custom", balance.ID, balanceSite.ID
			network := p.accountNetwork(balanceSite, balance, "review")
			plan.BalanceNetwork = &network
			p.credentialUse(balance, account.ID, true, network)
		} else {
			p.issue("review", "balance_credentials_unavailable", "account", account.ID, *account.LinkedAccountID)
		}
	}
	if pending := account.CheckinPending; pending != nil {
		valid := pending.Fingerprint == site.CheckinWorkflowFingerprint(&account)
		plan.Workflow = &WorkflowPlan{Submitted: pending.Submitted, Finished: pending.Finished, FingerprintValid: valid}
		if !pending.Finished {
			p.report.Summary.PendingWorkflows++
		}
		if !valid {
			p.issue("review", "workflow_fingerprint_changed", "account", account.ID, site.ID)
		}
		if pending.Submitted {
			p.issue("review", "submitted_workflow_must_not_resubmit", "account", account.ID, site.ID)
		}
	}
	p.report.Accounts = append(p.report.Accounts, plan)
}

func platformCredentialValid(platform model.SitePlatform, account model.LegacySiteAccount) bool {
	if account.CredentialType == model.LegacySiteCredentialTypeUsernamePassword {
		return strings.TrimSpace(account.Username) != "" && strings.TrimSpace(account.Password) != ""
	}
	return platform != model.SitePlatformAgentRouter && account.CredentialType == model.LegacySiteCredentialTypeAccessToken && strings.TrimSpace(account.AccessToken) != ""
}

func balanceCredentialValid(platform model.SitePlatform, account model.LegacySiteAccount) bool {
	switch platform {
	case model.SitePlatformSub2API:
		return (account.CredentialType == model.LegacySiteCredentialTypeAccessToken && strings.TrimSpace(account.AccessToken) != "") ||
			(account.CredentialType == model.LegacySiteCredentialTypeAPIKey && strings.TrimSpace(account.APIKey) != "")
	case model.SitePlatformNewAPI, model.SitePlatformAnyRouter, model.SitePlatformAgentRouter, model.SitePlatformOneAPI, model.SitePlatformOneHub, model.SitePlatformDoneHub:
		// AgentRouter balance queries can use an existing token without logging in.
		return platformCredentialValid(model.SitePlatformNewAPI, account)
	default:
		return false
	}
}

func (p *preflight) credentialUse(source model.LegacySiteAccount, accountID int, balance bool, network NetworkPlan) {
	use := p.credentials[source.ID]
	if use == nil {
		use = &CredentialUse{SourceAccountID: source.ID, PrimaryUsers: []int{}, BalanceUsers: []int{}}
		p.credentials[source.ID] = use
	}
	if balance {
		use.BalanceUsers = append(use.BalanceUsers, accountID)
	} else {
		use.PrimaryUsers = append(use.PrimaryUsers, accountID)
	}
	proxyID := 0
	if network.ProxyConfigID != nil {
		proxyID = *network.ProxyConfigID
	}
	// The context key is never exposed. Compare actual header values instead of
	// site IDs so identical network contexts on different sites remain shareable.
	headers := make([]string, 0)
	for _, header := range p.sites[network.HeaderSiteID].CustomHeader {
		headers = append(headers, strings.ToLower(strings.TrimSpace(header.HeaderKey))+"\x00"+header.HeaderValue)
	}
	slices.Sort(headers)
	key := fmt.Sprintf("%s:%d:%q", network.Mode, proxyID, headers)
	if previous, exists := p.contexts[source.ID]; exists && previous != key {
		p.issue("review", "shared_credential_network_conflict", "account", source.ID, accountID)
	} else {
		p.contexts[source.ID] = key
	}
}

func validHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func sortedIDs(ids []int) []int {
	result := append([]int{}, ids...)
	slices.Sort(result)
	return slices.Compact(result)
}

func (p *preflight) finish() {
	for _, use := range p.credentials {
		use.PrimaryUsers, use.BalanceUsers = sortedIDs(use.PrimaryUsers), sortedIDs(use.BalanceUsers)
		users := append(slices.Clone(use.PrimaryUsers), use.BalanceUsers...)
		if len(sortedIDs(users)) > 1 {
			p.report.Summary.SharedCredentials++
		}
		p.report.Credentials = append(p.report.Credentials, *use)
	}
	slices.SortFunc(p.report.Credentials, func(a, b CredentialUse) int { return compareID(a.SourceAccountID, b.SourceAccountID) })
	slices.SortFunc(p.report.Channels, func(a, b ChannelPlan) int { return compareID(a.ChannelID, b.ChannelID) })
	slices.SortFunc(p.report.Accounts, func(a, b AccountPlan) int { return compareID(a.AccountID, b.AccountID) })
	slices.SortFunc(p.report.Issues, func(a, b Issue) int {
		if n := strings.Compare(a.Entity, b.Entity); n != 0 {
			return n
		}
		if n := compareID(a.ID, b.ID); n != 0 {
			return n
		}
		if n := strings.Compare(a.Code, b.Code); n != 0 {
			return n
		}
		if n := compareID(a.RelatedID, b.RelatedID); n != 0 {
			return n
		}
		return strings.Compare(a.Severity, b.Severity)
	})
	p.report.Issues = slices.Compact(p.report.Issues)
	for _, issue := range p.report.Issues {
		if issue.Severity == "blocker" {
			p.report.Summary.Blockers++
		} else {
			p.report.Summary.Reviews++
		}
	}
}

func compareID(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
