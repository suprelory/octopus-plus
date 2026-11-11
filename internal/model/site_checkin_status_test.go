package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckinStatusConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*SiteCheckinStatusConfig)
	}{
		{"external URL", func(c *SiteCheckinStatusConfig) { c.Path = "https://other.example/status" }},
		{"traversal", func(c *SiteCheckinStatusConfig) { c.Path = "/api/../status" }},
		{"GET body", func(c *SiteCheckinStatusConfig) { c.Body = `{}` }},
		{"reserved header", func(c *SiteCheckinStatusConfig) {
			c.Headers = []CustomHeader{{HeaderKey: "Host", HeaderValue: "other.example"}}
		}},
		{"missing rule", func(c *SiteCheckinStatusConfig) { c.CheckedPath = "" }},
		{"null comparison", func(c *SiteCheckinStatusConfig) { c.CheckedValue = json.RawMessage(`null`) }},
		{"equal comparisons", func(c *SiteCheckinStatusConfig) { c.CheckedValue = json.RawMessage(`false`) }},
		{"oversized code", func(c *SiteCheckinStatusConfig) { c.Code = strings.Repeat("a", 16385) }},
		{"unbounded polling", func(c *SiteCheckinStatusConfig) { c.MaxAttempts = 99 }},
		{"unbounded deadline", func(c *SiteCheckinStatusConfig) { c.ConfirmTimeoutMinutes = 1441 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := &SiteCheckinStatusConfig{Path: "/status", CheckedPath: "data.done"}
			tc.change(config)
			if err := config.Validate(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	site := &Site{BaseURL: "https://example.com", CheckinHTTPEnabled: true, CheckinStatus: &SiteCheckinStatusConfig{Path: "/status?date={{checkin_date}}", CheckedPath: "data.done"}}
	if err := site.CheckinStatus.Validate(); err != nil {
		t.Fatal(err)
	}
	before := site.CheckinConfigFingerprint()
	site.CheckinStatus.CheckedPath = "data.checked_in"
	if before == site.CheckinConfigFingerprint() {
		t.Fatal("status rule changes must invalidate old capability evidence")
	}
}
