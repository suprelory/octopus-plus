package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Status is optional: nil preserves the original single-request check-in.
type SiteCheckinStatusConfig struct {
	Method                string          `json:"method"`
	Path                  string          `json:"path"`
	Body                  string          `json:"body"`
	Headers               []CustomHeader  `json:"headers"`
	SkipPrecheck          bool            `json:"skip_precheck"`
	CheckedPath           string          `json:"checked_path"`
	CheckedValue          json.RawMessage `json:"checked_value"`
	UncheckedValue        json.RawMessage `json:"unchecked_value"`
	DatePath              string          `json:"date_path"`
	Code                  string          `json:"code"`
	InitialDelayMs        int             `json:"initial_delay_ms"`
	PollIntervalMs        int             `json:"poll_interval_ms"`
	MaxAttempts           int             `json:"max_attempts"`
	TimeoutSeconds        int             `json:"timeout_seconds"`
	ConfirmTimeoutMinutes int             `json:"confirm_timeout_minutes"`
	RewardSource          string          `json:"reward_source"`
}

// Retain submitted attempts until the next day, including their log ID, so
// manual status rechecks cannot submit or award them twice. An unsubmitted
// failure may be replaced by a new attempt while keeping its historical log.
// Neither raw responses nor credentials are stored here.
type SiteCheckinPending struct {
	LogID           int64               `json:"log_id,string"`
	Date            string              `json:"date"`
	Fingerprint     string              `json:"fingerprint"`
	StartedAt       time.Time           `json:"started_at"`
	Deadline        time.Time           `json:"deadline"`
	BeforeUnchecked bool                `json:"before_unchecked"`
	Submitted       bool                `json:"submitted"`
	Finished        bool                `json:"finished"`
	Status          SiteExecutionStatus `json:"status"`
	Reason          string              `json:"reason"`
}

func (s *Site) CheckinWorkflowFingerprint(account *SiteAccount) string {
	payload, _ := json.Marshal([]any{s.CheckinConfigFingerprint(), s.CheckinTimezone, account.Cookie, account.Username, account.ProxyMode, account.ProxyConfigID})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func (c *SiteCheckinStatusConfig) Normalize() {
	c.Method = strings.ToUpper(strings.TrimSpace(c.Method))
	if c.Method == "" {
		c.Method = "GET"
	}
	c.Path, c.Body, c.Code = strings.TrimSpace(c.Path), strings.TrimSpace(c.Body), strings.TrimSpace(c.Code)
	c.CheckedPath, c.DatePath = strings.TrimSpace(c.CheckedPath), strings.TrimSpace(c.DatePath)
	if len(c.CheckedValue) == 0 {
		c.CheckedValue = json.RawMessage("true")
	}
	if len(c.UncheckedValue) == 0 {
		c.UncheckedValue = json.RawMessage("false")
	}
	if c.PollIntervalMs == 0 {
		c.PollIntervalMs = 2000
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = 3
	}
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = 30
	}
	if c.ConfirmTimeoutMinutes == 0 {
		c.ConfirmTimeoutMinutes = 60
	}
	if c.RewardSource == "" {
		c.RewardSource = "submit"
	}
}

func (c *SiteCheckinStatusConfig) Validate() error {
	c.Normalize()
	// Reuse all request validation, including restricted headers and URL paths.
	request := &Site{Name: "status", Kind: SiteKindCheckin, Platform: SitePlatformAPI, BaseURL: "https://example.com", CheckinHTTPEnabled: true,
		CheckinHTTPMethod: c.Method, CheckinHTTPPath: c.Path, CheckinHTTPBody: c.Body, CheckinHTTPHeaders: c.Headers}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("checkin status request: %w", err)
	}
	if err := ValidateCheckinRewardExtractor(c.Code); err != nil {
		return fmt.Errorf("checkin status code: %w", err)
	}
	if c.Code == "" && c.CheckedPath == "" {
		return fmt.Errorf("checkin status requires a field path or JavaScript rule")
	}
	for _, path := range []string{c.CheckedPath, c.DatePath} {
		if len(path) > 512 || strings.ContainsAny(path, "\r\n\x00") {
			return fmt.Errorf("checkin status field path is invalid")
		}
	}
	for _, raw := range []json.RawMessage{c.CheckedValue, c.UncheckedValue} {
		var value any
		if len(raw) > 256 || json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("checkin status comparison must be a JSON scalar")
		}
		switch value.(type) {
		case bool, string, float64:
		default:
			return fmt.Errorf("checkin status comparison must be a boolean, string or number")
		}
	}
	var checked, unchecked any
	_ = json.Unmarshal(c.CheckedValue, &checked)
	_ = json.Unmarshal(c.UncheckedValue, &unchecked)
	a, _ := json.Marshal(checked)
	b, _ := json.Marshal(unchecked)
	if bytes.Equal(a, b) {
		return fmt.Errorf("checked and unchecked values must be different")
	}
	if c.InitialDelayMs < 0 || c.InitialDelayMs > 10000 || c.PollIntervalMs < 1 || c.PollIntervalMs > 10000 ||
		c.MaxAttempts < 1 || c.MaxAttempts > 5 || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 120 ||
		c.ConfirmTimeoutMinutes < 1 || c.ConfirmTimeoutMinutes > 1440 {
		return fmt.Errorf("checkin status polling limits are invalid")
	}
	if c.RewardSource != "submit" && c.RewardSource != "status" {
		return fmt.Errorf("checkin reward source must be submit or status")
	}
	return nil
}
