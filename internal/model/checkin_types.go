package model

import (
	"fmt"
	_ "time/tzdata"
)

type SitePlatform string

const (
	SitePlatformNewAPI      SitePlatform = "new-api"
	SitePlatformAnyRouter   SitePlatform = "anyrouter"
	SitePlatformAgentRouter SitePlatform = "agentrouter"
	SitePlatformOneAPI      SitePlatform = "one-api"
	SitePlatformOneHub      SitePlatform = "one-hub"
	SitePlatformDoneHub     SitePlatform = "done-hub"
	SitePlatformSub2API     SitePlatform = "sub2api"
	SitePlatformAPI         SitePlatform = "api"
)

type SiteExecutionStatus string

const (
	SiteExecutionStatusIdle    SiteExecutionStatus = "idle"
	SiteExecutionStatusSuccess SiteExecutionStatus = "success"
	SiteExecutionStatusPartial SiteExecutionStatus = "partial"
	SiteExecutionStatusFailed  SiteExecutionStatus = "failed"
	SiteExecutionStatusSkipped SiteExecutionStatus = "skipped"
	SiteExecutionStatusPending SiteExecutionStatus = "pending"
)

const (
	DefaultSiteCheckinTimezone    = "Asia/Shanghai"
	DefaultSiteCheckinWindowStart = "00:00"
	DefaultSiteCheckinWindowEnd   = "23:59"
)

func (p SitePlatform) Validate() error {
	switch p {
	case SitePlatformNewAPI, SitePlatformAnyRouter, SitePlatformAgentRouter, SitePlatformOneAPI, SitePlatformOneHub, SitePlatformDoneHub,
		SitePlatformSub2API, SitePlatformAPI:
		return nil
	default:
		return fmt.Errorf("unsupported site platform: %s", p)
	}
}
