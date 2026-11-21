package model

// The retired site runtime and its fixtures use the v1 records.
type Site = LegacySite
type SiteAccount = LegacySiteAccount
type SiteToken = LegacySiteToken
type SiteUserGroup = LegacySiteUserGroup
type SiteModel = LegacySiteModel
type SiteChannelBinding = LegacySiteChannelBinding

const (
	SiteGroupModelSyncStatusIdle       SiteGroupModelSyncStatus = "idle"
	SiteGroupModelSyncStatusSynced     SiteGroupModelSyncStatus = "synced"
	SiteGroupModelSyncStatusEmpty      SiteGroupModelSyncStatus = "empty"
	SiteGroupModelSyncStatusStale      SiteGroupModelSyncStatus = "stale"
	SiteGroupModelSyncStatusFailed     SiteGroupModelSyncStatus = "failed"
	SiteGroupModelSyncStatusUnresolved SiteGroupModelSyncStatus = "unresolved"
	SiteGroupModelSyncStatusMissingKey SiteGroupModelSyncStatus = "missing_key"
	SiteGroupModelSyncStatusRemoved    SiteGroupModelSyncStatus = "removed"
)

const (
	SiteDefaultGroupKey  = "default"
	SiteDefaultGroupName = "default"
)
