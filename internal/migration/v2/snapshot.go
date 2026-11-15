// Package v2 inspects legacy data before the independent check-in migration.
// It does not initialize a database or modify the supplied snapshot.
package v2

import (
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

// Snapshot contains configuration only. Request bodies and historical logs are
// unnecessary for preflight and are deliberately not loaded from a backup.
// Legacy types belong at this conversion boundary, not in the new runtime.
type Snapshot struct {
	Version             int
	ExportedAt          time.Time
	IncludeLogs         bool
	IncludeStats        bool
	Channels            []model.Channel
	ChannelKeys         []model.ChannelKey
	ProxyConfigurations []model.ProxyConfiguration
	Sites               []model.Site
	SiteAccounts        []model.SiteAccount
	SiteUserGroups      []model.SiteUserGroup
	SiteChannelBindings []model.SiteChannelBinding
	Groups              []model.Group
	GroupItems          []model.GroupItem
	Settings            []model.Setting
}

func (s *Snapshot) fields() map[string]any {
	return map[string]any{
		"version": &s.Version, "exported_at": &s.ExportedAt,
		"include_logs": &s.IncludeLogs, "include_stats": &s.IncludeStats,
		"channels": &s.Channels, "channel_keys": &s.ChannelKeys,
		"proxy_configurations": &s.ProxyConfigurations,
		"sites":                &s.Sites, "site_accounts": &s.SiteAccounts,
		"site_user_groups": &s.SiteUserGroups, "site_channel_bindings": &s.SiteChannelBindings,
		"groups": &s.Groups, "group_items": &s.GroupItems, "settings": &s.Settings,
	}
}
