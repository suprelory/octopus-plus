package v2

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
)

// Convert is pure: it performs no SQL, HTTP, login, token refresh or old business
// deletion. Its output can be shared by the database migrator and v1 importer.
func Convert(input *LegacyInput) (*Conversion, error) {
	if input == nil || input.CapturedAt.IsZero() {
		return nil, errors.New("conversion requires a frozen input and capture time")
	}
	baseline, err := Analyze(&input.Configuration)
	if err != nil {
		return nil, err
	}
	fingerprint, err := inputFingerprint(input)
	if err != nil {
		return nil, err
	}
	c := &Conversion{Fingerprint: fingerprint, Report: ConversionReport{Issues: []Issue{}, Mappings: []Mapping{}, PausedAccounts: []int{}}}
	// Account semantics are analyzed again below after resolving pre-split data.
	for _, issue := range baseline.Issues {
		switch issue.Code {
		case "legacy_checkin_requires_split", "checkin_credentials_unavailable", "balance_credentials_unavailable",
			"shared_credential_network_conflict", "workflow_fingerprint_changed", "submitted_workflow_must_not_resubmit",
			"checkin_adapter_not_configured", "invalid_auth_source_url", "invalid_balance_source_url":
			continue
		}
		if issue.Code == "unsupported_channel_type" {
			issue.Severity = "blocker"
		}
		c.Report.Issues = append(c.Report.Issues, issue)
	}
	p := &preflight{snapshot: &input.Configuration,
		sites: map[int]model.LegacySite{}, accounts: map[int]model.LegacySiteAccount{}, proxies: map[int]model.ProxyConfiguration{},
		settings: map[model.SettingKey]string{}, credentials: map[int]*CredentialUse{}, contexts: map[int]string{}}
	for _, site := range input.Configuration.Sites {
		p.sites[site.ID] = site
	}
	for _, account := range input.Configuration.SiteAccounts {
		p.accounts[account.ID] = account
	}
	for _, proxy := range input.Configuration.ProxyConfigurations {
		p.proxies[proxy.ID] = proxy
	}
	for _, setting := range input.Configuration.Settings {
		p.settings[setting.Key] = setting.Value
	}
	b := conversionBuilder{input: input, result: c, planner: p, sites: map[int]model.CheckinSite{}, accounts: map[int]model.CheckinAccount{},
		credentials: map[int]model.CheckinCredential{}, accountTargets: map[int]int{}, siteTargets: map[int]int{}, original: map[int]model.LegacySiteAccount{}}
	b.selectRecords()
	b.convertSites()
	b.convertAccounts()
	b.convertHistory()
	for _, issue := range p.report.Issues {
		if issue.Code == "checkin_credentials_unavailable" {
			issue.Severity = "review"
		}
		if issue.Code == "shared_credential_network_conflict" {
			issue.Severity = "blocker"
		}
		c.Report.Issues = append(c.Report.Issues, issue)
	}
	for _, site := range b.sites {
		c.Sites = append(c.Sites, site)
	}
	for _, account := range b.accounts {
		c.Accounts = append(c.Accounts, account)
	}
	for _, credential := range b.credentials {
		c.Credentials = append(c.Credentials, credential)
	}
	slices.SortFunc(c.Sites, func(a, b model.CheckinSite) int { return compareID(a.ID, b.ID) })
	slices.SortFunc(c.Accounts, func(a, b model.CheckinAccount) int { return compareID(a.ID, b.ID) })
	slices.SortFunc(c.Credentials, func(a, b model.CheckinCredential) int { return compareID(a.ID, b.ID) })
	b.convertChannels(baseline)
	c.finishReport()
	return c, nil
}

type conversionBuilder struct {
	input          *LegacyInput
	result         *Conversion
	planner        *preflight
	sites          map[int]model.CheckinSite
	accounts       map[int]model.CheckinAccount
	credentials    map[int]model.CheckinCredential
	siteTargets    map[int]int
	accountTargets map[int]int
	original       map[int]model.LegacySiteAccount
}

func (b *conversionBuilder) selectRecords() {
	// Earlier versions split subscription check-ins, retaining source account IDs.
	// Existing independent edits win; provenance remaps history, not credentials.
	aliases := map[int]map[int]bool{}
	historySites := map[int]bool{}
	accountsBySite := map[int][]model.LegacySiteAccount{}
	for _, entry := range b.input.Logs {
		historySites[entry.SiteID] = true
	}
	for _, a := range b.input.Configuration.SiteAccounts {
		accountsBySite[a.SiteID] = append(accountsBySite[a.SiteID], a)
		site := b.planner.sites[a.SiteID]
		if !site.IsCheckinOnly() {
			continue
		}
		b.accountTargets[a.ID], b.siteTargets[site.ID] = a.ID, site.ID
		b.original[a.ID] = a
		if a.CheckinSourceAccountID == nil {
			continue
		}
		source, exists := b.planner.accounts[*a.CheckinSourceAccountID]
		if !exists {
			continue
		} // A removed source does not invalidate an independent account.
		if b.planner.sites[source.SiteID].Kind == model.LegacySiteKindCheckin || site.LinkedSiteID == nil || source.SiteID != *site.LinkedSiteID {
			b.result.issue("blocker", "invalid_split_provenance", "account", a.ID, source.ID)
			continue
		}
		if previous, exists := b.accountTargets[source.ID]; exists && previous != a.ID {
			b.result.issue("blocker", "ambiguous_split_provenance", "account", source.ID, a.ID)
			continue
		}
		b.accountTargets[source.ID] = a.ID
		if aliases[source.SiteID] == nil {
			aliases[source.SiteID] = map[int]bool{}
		}
		aliases[source.SiteID][site.ID] = true
	}
	for _, site := range b.input.Configuration.Sites {
		if site.IsCheckinOnly() {
			b.siteTargets[site.ID] = site.ID
			continue
		}
		hasData := legacySiteHasCheckin(site) || historySites[site.ID]
		var unmapped []model.LegacySiteAccount
		for _, a := range accountsBySite[site.ID] {
			hasData = hasData || a.AutoCheckin || a.LastCheckinAt != nil || a.LastCheckinSuccessAt != nil || a.NextAutoCheckinAt != nil || a.CheckinPending != nil || a.CheckinFailureCount > 0
			if _, exists := b.accountTargets[a.ID]; !exists {
				unmapped = append(unmapped, a)
			}
		}
		if len(aliases[site.ID]) == 1 && (len(unmapped) == 0 || !hasData) {
			for target := range aliases[site.ID] {
				b.siteTargets[site.ID] = target
			}
			continue
		}
		if len(aliases[site.ID]) > 1 {
			b.result.issue("blocker", "ambiguous_split_site", "site", site.ID, 0)
			continue
		}
		if !hasData {
			continue
		}
		b.siteTargets[site.ID] = site.ID
		for _, a := range unmapped {
			b.accountTargets[a.ID], b.original[a.ID] = a.ID, a
		}
		b.result.issue("review", "legacy_checkin_converted", "site", site.ID, 0)
	}
	for source, target := range b.siteTargets {
		b.result.mapID(EntitySite, source, target)
	}
	for source, target := range b.accountTargets {
		b.result.mapID(EntityAccount, source, target)
	}
}

func legacySiteHasCheckin(s model.LegacySite) bool {
	return s.CheckinHTTPEnabled || s.CheckinMode == model.SiteCheckinModeEnabled || s.CheckinVerifiedAt != nil ||
		(s.ExternalCheckinURL != nil && strings.TrimSpace(*s.ExternalCheckinURL) != "") ||
		(s.CheckinVerificationStatus != "" && s.CheckinVerificationStatus != model.SiteCheckinSupportUnknown)
}

func (b *conversionBuilder) convertSites() {
	rows := slices.Clone(b.input.Configuration.Sites)
	// Keep existing check-in names first; only unsplit subscription names may be
	// disambiguated. Names are never used to infer identity or merge records.
	slices.SortFunc(rows, func(a, c model.LegacySite) int {
		if a.IsCheckinOnly() != c.IsCheckinOnly() {
			if a.IsCheckinOnly() {
				return -1
			}
			return 1
		}
		return compareID(a.ID, c.ID)
	})
	names := map[string]bool{}
	for _, old := range rows {
		if b.siteTargets[old.ID] != old.ID {
			continue
		}
		s := model.CheckinSite{ID: old.ID, Name: old.Name, Platform: old.Platform, BaseURL: old.BaseURL,
			Enabled: old.Enabled, Archived: old.Archived, ArchivedAt: clonePointer(old.ArchivedAt),
			CheckinNetwork: model.CheckinNetwork{ProxyMode: old.ProxyMode, ProxyConfigID: clonePointer(old.ProxyConfigID)},
			CustomHeader:   slices.Clone(old.CustomHeader), ExternalCheckinURL: clonePointer(old.ExternalCheckinURL),
			CheckinMode: old.CheckinMode, CheckinHTTPEnabled: old.CheckinHTTPEnabled,
			CheckinHTTPMethod: old.CheckinHTTPMethod, CheckinHTTPPath: old.CheckinHTTPPath, CheckinHTTPBody: old.CheckinHTTPBody,
			CheckinHTTPHeaders: slices.Clone(old.CheckinHTTPHeaders), CheckinStatus: cloneStatus(old.CheckinStatus),
			CheckinRewardExtractor: old.CheckinRewardExtractor, CheckinTimezone: old.CheckinTimezone,
			CheckinWindowStart: old.CheckinWindowStart, CheckinWindowEnd: old.CheckinWindowEnd,
			VerificationStatus: old.CheckinVerificationStatus, IsPinned: old.IsPinned, SortOrder: old.SortOrder, Tags: slices.Clone(old.Tags)}
		s.Name = strings.TrimSpace(s.Name)
		if names[s.Name] && !old.IsCheckinOnly() {
			suffix := fmt.Sprintf(" (legacy #%d)", old.ID)
			runes := []rune(s.Name)
			if len(runes) > 191-len(suffix) {
				runes = runes[:191-len(suffix)]
			}
			s.Name = string(runes) + suffix
			b.result.issue("review", "legacy_site_name_disambiguated", "site", old.ID, 0)
		}
		if names[s.Name] {
			b.result.issue("blocker", "duplicate_checkin_site_name", "site", old.ID, 0)
		}
		names[s.Name] = true
		if s.CheckinHTTPEnabled {
			b.convertHTTPSettings(old, &s)
		}
		if err := s.Validate(); err != nil {
			b.result.issue("blocker", "invalid_converted_site", "site", s.ID, 0)
		}
		if old.CheckinVerifiedAt != nil && old.CheckinVerificationFingerprint == old.CheckinConfigFingerprint() {
			s.VerifiedAt, s.VerificationFingerprint = clonePointer(old.CheckinVerifiedAt), s.ConfigFingerprint()
		} else if old.CheckinVerifiedAt != nil {
			b.result.issue("review", "invalid_capability_evidence_retained_as_unknown", "site", s.ID, 0)
		}
		b.sites[s.ID] = s
	}
}

func (b *conversionBuilder) convertAccounts() {
	ids := make([]int, 0, len(b.original))
	for id := range b.original {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	names := map[string]bool{}
	for _, id := range ids {
		old := b.original[id]
		oldSite := b.planner.sites[old.SiteID]
		site, exists := b.sites[old.SiteID]
		if !exists {
			continue
		}
		account := old
		// Only the oldest credential forms support cookie extraction. Explicit
		// linked/cookie records never fall back to dormant tokens or headers.
		if oldSite.CheckinHTTPEnabled && !oldSite.CheckinUseSubscriptionToken && account.CredentialType != model.LegacySiteCredentialTypeCookie && account.CredentialType != model.LegacySiteCredentialTypeLinkedAccount {
			account.Cookie = legacyCookie(oldSite, account)
			b.planner.accounts[id] = account
		}
		// Match the earliest split format's explicit provenance upgrade. Newer
		// Cookie/linked records never fall back to a removed or edited binding.
		if !oldSite.CheckinHTTPEnabled && account.CredentialType != model.LegacySiteCredentialTypeCookie && account.CredentialType != model.LegacySiteCredentialTypeLinkedAccount && account.LinkedAccountID == nil && account.CheckinSourceAccountID != nil && oldSite.LinkedSiteID != nil {
			source, found := b.planner.accounts[*account.CheckinSourceAccountID]
			sourceSite := b.planner.sites[source.SiteID]
			if found && !sourceSite.IsCheckinOnly() && source.SiteID == *oldSite.LinkedSiteID && sourceSite.Platform == oldSite.Platform && sourceSite.BaseURL == oldSite.BaseURL &&
				(source.CredentialType == model.LegacySiteCredentialTypeAccessToken || source.CredentialType == model.LegacySiteCredentialTypeUsernamePassword) {
				account.LinkedAccountID = clonePointer(account.CheckinSourceAccountID)
				account.CredentialType = model.LegacySiteCredentialTypeLinkedAccount
			}
		}
		b.planner.analyzeAccount(oldSite, account)
		plan := b.planner.report.Accounts[len(b.planner.report.Accounts)-1]
		a := model.CheckinAccount{ID: old.ID, SiteID: site.ID, Name: old.Name, AuthMode: model.CheckinAuthMode(plan.AuthMode),
			CheckinNetwork: model.CheckinNetwork{ProxyMode: old.ProxyMode, ProxyConfigID: clonePointer(old.ProxyConfigID)},
			Enabled:        old.Enabled, AutoCheckin: old.AutoCheckin, RandomCheckin: old.RandomCheckin, CheckinRandomWindowMinutes: old.CheckinRandomWindowMinutes,
			BalanceConfig: model.CheckinBalanceConfig{Mode: model.CheckinBalanceDisabled}, Balance: old.Balance, BalanceUsed: old.BalanceUsed, TodayIncome: old.TodayIncome,
			NextAutoCheckinAt: clonePointer(old.NextAutoCheckinAt), LastCheckinAt: clonePointer(old.LastCheckinAt), LastCheckinSuccessAt: clonePointer(old.LastCheckinSuccessAt),
			CheckinFailureCount: old.CheckinFailureCount, LastCheckinStatus: old.LastCheckinStatus, LastCheckinMessage: old.LastCheckinMessage,
			CheckinPending: clonePointer(old.CheckinPending)}
		if oldSite.CheckinUseSubscriptionToken {
			a.CheckinNetwork = networkFromPlan(plan.Network)
		}
		if plan.CredentialAccountID > 0 {
			a.CredentialID = b.credential(plan.CredentialAccountID, plan.AuthSiteID, plan.Network, a.AuthMode == model.CheckinAuthCookie)
		}
		if a.CredentialID == 0 {
			a.AutoCheckin, a.NextAutoCheckinAt = false, nil
			b.result.Report.PausedAccounts = append(b.result.Report.PausedAccounts, a.ID)
		}
		if plan.BalanceMode == "primary" && a.CredentialID != 0 {
			a.BalanceConfig.Mode = model.CheckinBalancePrimary
		} else if plan.BalanceMode == "custom" && plan.BalanceNetwork != nil {
			credentialID := b.credential(plan.BalanceAccountID, plan.BalanceSiteID, *plan.BalanceNetwork, false)
			if credentialID != 0 {
				balance := model.CheckinBalanceConfig{Mode: model.CheckinBalanceCustom, Platform: b.planner.sites[plan.BalanceSiteID].Platform,
					BaseURL: b.planner.sites[plan.BalanceSiteID].BaseURL, CredentialID: &credentialID, CheckinNetwork: networkFromPlan(*plan.BalanceNetwork),
					Headers: authHeaders(b.planner.sites[plan.BalanceSiteID].CustomHeader)}
				if err := balance.Validate(); err == nil {
					a.BalanceConfig = balance
				} else {
					b.result.issue("review", "balance_configuration_disabled", "account", a.ID, plan.BalanceAccountID)
				}
			}
		}
		if site.Archived {
			a.NextAutoCheckinAt = nil
		}
		// Orphan accounts are intentional migration records with credential_id=0;
		// regular CRUD continues to require a valid credential. Validate all other
		// fields using a copy, without inventing a credential or copying stale secrets.
		validation := a
		if validation.CredentialID == 0 {
			validation.CredentialID = 1
		}
		if err := validation.Validate(); err != nil {
			b.result.issue("blocker", "invalid_converted_account", "account", a.ID, 0)
		}
		a.Name, a.CheckinNetwork = validation.Name, validation.CheckinNetwork
		key := fmt.Sprintf("%d:%s", a.SiteID, a.Name)
		if names[key] {
			b.result.issue("blocker", "duplicate_checkin_account_name", "account", a.ID, a.SiteID)
		}
		names[key] = true
		credential, ready := b.credentials[a.CredentialID]
		if ready && model.ValidateCheckinIdentity(site, a, credential) != nil {
			b.result.issue("blocker", "converted_identity_mismatch", "account", a.ID, a.CredentialID)
			ready = false
		}
		if ready && site.CheckinHTTPEnabled {
			b.validateTemplateIdentity(oldSite, old, credential)
		}
		if pending := a.CheckinPending; pending != nil {
			a.CheckinWorkflowVersion = 1
			if pending.Fingerprint != "" && pending.Fingerprint == oldSite.CheckinWorkflowFingerprint(&old) && ready && account.Cookie == old.Cookie {
				pending.Fingerprint = site.WorkflowFingerprint(a, credential)
			} else {
				pending.Fingerprint = "migration:invalid-legacy-workflow"
				b.result.issue("review", "workflow_requires_repair", "account", a.ID, a.SiteID)
			}
		}
		b.accounts[a.ID] = a
	}
}

func (b *conversionBuilder) credential(accountID, siteID int, network NetworkPlan, cookie bool) int {
	old, exists := b.planner.accounts[accountID]
	site := b.planner.sites[siteID]
	if !exists {
		return 0
	}
	c := model.CheckinCredential{ID: old.ID, Name: strings.TrimSpace(old.Name), Type: model.CheckinCredentialType(old.CredentialType),
		Platform: site.Platform, AuthBaseURL: site.BaseURL, CheckinNetwork: networkFromPlan(network),
		AuthHeaders: authHeaders(b.planner.sites[network.HeaderSiteID].CustomHeader), Username: old.Username,
		Password: old.Password, AccessToken: old.AccessToken, RefreshToken: old.RefreshToken, APIKey: old.APIKey, Cookie: old.Cookie,
		PlatformUserID: clonePointer(old.PlatformUserID), TokenExpiresAt: old.TokenExpiresAt,
		TokenRefreshedAt: old.TokenRefreshedAt, CredentialVersion: max(old.CredentialVersion, 1)}
	if c.Name == "" {
		c.Name = fmt.Sprintf("Credential #%d", c.ID)
	}
	if cookie {
		c.Type = model.CheckinCredentialCookie
		c.Password, c.AccessToken, c.RefreshToken, c.APIKey = "", "", "", ""
		c.PlatformUserID, c.TokenExpiresAt, c.TokenRefreshedAt = nil, 0, 0
	} else {
		switch c.Type {
		case model.CheckinCredentialPassword:
			c.APIKey = ""
		case model.CheckinCredentialAccessToken:
			c.Password, c.APIKey = "", ""
		case model.CheckinCredentialAPIKey:
			c.Password, c.AccessToken, c.RefreshToken, c.Cookie = "", "", "", ""
		}
	}
	// Legacy leases do not prove a Refresh Token exchange finished. Do not
	// restore locks or authorize another exchange merely because its lease expired.
	if !cookie && (old.TokenRefreshLease != "" || old.TokenRefreshLeaseUntil != 0) {
		c.TokenRefreshStarted = true
		b.result.issue("review", "credential_session_requires_repair", "account", old.ID, 0)
	}
	if err := c.Validate(); err != nil {
		b.result.issue("review", "credential_configuration_unavailable", "account", old.ID, 0)
		return 0
	}
	if previous, exists := b.credentials[c.ID]; exists {
		// Identity is source-based. Never duplicate one refresh session to conceal
		// different authentication proxies or header contexts.
		if !sameCredentialContext(previous, c) {
			b.result.issue("blocker", "shared_credential_network_conflict", "account", c.ID, 0)
		}
		return previous.ID
	}
	b.credentials[c.ID] = c
	b.result.mapID(EntityCredential, old.ID, c.ID)
	return c.ID
}

func networkFromPlan(p NetworkPlan) model.CheckinNetwork {
	return model.CheckinNetwork{ProxyMode: p.Mode, ProxyConfigID: clonePointer(p.ProxyConfigID)}
}

func (b *conversionBuilder) convertChannels(report *Report) {
	c := b.result
	plans := map[int]ChannelPlan{}
	for _, plan := range report.Channels {
		plans[plan.ChannelID] = plan
	}
	c.Channels = slices.Clone(b.input.Configuration.Channels)
	for i := range c.Channels {
		channel := &c.Channels[i]
		c.mapID(EntityChannel, channel.ID, channel.ID)
		if plan, exists := plans[channel.ID]; exists {
			channel.AutoGroup = plan.AutoGroup
		} else if channel.Managed || channel.ManagedSource != nil {
			c.issue("blocker", "managed_channel_binding_missing", "channel", channel.ID, 0)
		}
		channel.BaseUrls, channel.CustomHeader = slices.Clone(channel.BaseUrls), slices.Clone(channel.CustomHeader)
		channel.Keys, channel.Stats = slices.Clone(channel.Keys), clonePointer(channel.Stats)
		channel.ProxyConfigID = clonePointer(channel.ProxyConfigID)
		channel.ParamOverride, channel.MatchRegex = clonePointer(channel.ParamOverride), clonePointer(channel.MatchRegex)
		channel.Managed, channel.ManagedSource = false, nil
	}
	for _, binding := range b.input.Configuration.SiteChannelBindings {
		c.RetiredBindingIDs = append(c.RetiredBindingIDs, binding.ID)
	}
	c.RetiredBindingIDs = sortedIDs(c.RetiredBindingIDs)
	c.ChannelKeys, c.Groups, c.GroupItems = slices.Clone(b.input.Configuration.ChannelKeys), slices.Clone(b.input.Configuration.Groups), slices.Clone(b.input.Configuration.GroupItems)
	c.GroupPresets, c.ChannelStats, c.ArchivedSiteStats = slices.Clone(b.input.GroupPresets), slices.Clone(b.input.ChannelStats), slices.Clone(b.input.ArchivedSiteStats)
	groups, channels := map[int]bool{}, map[int]bool{}
	for _, channel := range c.Channels {
		channels[channel.ID] = true
	}
	for i := range c.Groups {
		group := &c.Groups[i]
		groups[group.ID] = true
		group.Items = slices.Clone(group.Items)
		group.PinnedAt = clonePointer(group.PinnedAt)
		group.ActivePresetID = clonePointer(group.ActivePresetID)
	}
	presets := map[int]int{}
	for i := range c.GroupPresets {
		preset := &c.GroupPresets[i]
		if _, exists := presets[preset.ID]; exists || preset.ID <= 0 {
			c.issue("blocker", "invalid_or_duplicate_group_preset", "group", preset.GroupID, preset.ID)
		}
		presets[preset.ID] = preset.GroupID
		preset.Items = slices.Clone(preset.Items)
		if !groups[preset.GroupID] {
			c.issue("blocker", "preset_group_missing", "group", preset.GroupID, preset.ID)
		}
		for _, item := range preset.Items {
			if !channels[item.ChannelID] {
				c.issue("blocker", "preset_channel_missing", "channel", item.ChannelID, preset.ID)
			}
		}
	}
	for _, group := range c.Groups {
		if group.ActivePresetID != nil && presets[*group.ActivePresetID] != group.ID {
			c.issue("blocker", "active_group_preset_missing", "group", group.ID, *group.ActivePresetID)
		}
	}
}
