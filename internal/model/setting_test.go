package model

import (
	"strings"
	"testing"
)

func TestProxyURLSetting(t *testing.T) {
	for _, test := range []struct {
		value string
		valid bool
	}{
		{"", true},
		{"http://proxy.example:8080", true},
		{"https://proxy.example:8443", true},
		{"socks://proxy.example:1080", true},
		{"socks5://proxy.example:1080", true},
		{"socks5h://proxy.example:1080", true},
		{"socks5h://alice:p%40ss@[::1]:1080", true},
		{"socks5h://", false},
		{"socks4://proxy.example:1080", false},
	} {
		setting := Setting{Key: SettingKeyProxyURL, Value: test.value}
		if err := setting.Validate(); (err == nil) != test.valid {
			t.Errorf("proxy URL %q: %v", test.value, err)
		}
	}
}

func TestCheckinNotificationSettings(t *testing.T) {
	for _, tc := range []struct {
		key   SettingKey
		value string
		valid bool
	}{
		{SettingKeyCheckinNotifyEnabled, "true", true}, {SettingKeyCheckinNotifyEnabled, "yes", false},
		{SettingKeyCheckinNotifySuccessEnabled, "true", true}, {SettingKeyCheckinNotifySuccessEnabled, "yes", false},
		{SettingKeyCheckinNotifyManualEnabled, "false", true}, {SettingKeyCheckinNotifyManualEnabled, "1", false},
		{SettingKeyNotificationChannels, `{}`, true},
		{SettingKeyNotificationChannels, `{"bark_url":"https://api.day.app/key"}`, true},
		{SettingKeyNotificationChannels, `{"templates":{"telegram":{"format":"markdown","body":"**{{site}}**"}}}`, true},
		{SettingKeyNotificationChannels, `{"templates":{"telegram":{"format":"html"}}}`, false},
		{SettingKeyNotificationChannels, `{"telegram_chat_id":"123"}`, false},
		{SettingKeyNotificationChannels, `{"webhook_url":"file:///tmp/test"}`, false},
		{SettingKeyCheckinNotifyCooldownSeconds, "0", true}, {SettingKeyCheckinNotifyCooldownSeconds, "604800", true},
		{SettingKeyCheckinNotifyCooldownSeconds, "-1", false}, {SettingKeyCheckinNotifyCooldownSeconds, "604801", false},
		{SettingKeyCheckinLowBalanceThreshold, "0.5", true}, {SettingKeyCheckinLowBalanceThreshold, "-1", false},
		{SettingKeyCheckinLowBalanceThreshold, "NaN", false}, {SettingKeyCheckinLowBalanceThreshold, "Inf", false},
		{SettingKeyCheckinNotifyWebhookURL, "", true}, {SettingKeyCheckinNotifyWebhookURL, " https://example.com/notify?key=secret ", true},
		{SettingKeyCheckinNotifyWebhookURL, "file:///tmp/notify", false}, {SettingKeyCheckinNotifyWebhookURL, "https://", false},
		{SettingKeyCheckinNotifyWebhookURL, "https://user:pass@example.com", false}, {SettingKeyCheckinNotifyWebhookURL, "https://example.com/#secret", false},
	} {
		setting := Setting{Key: tc.key, Value: tc.value}
		if err := setting.Validate(); (err == nil) != tc.valid {
			t.Errorf("%s = %q: %v", tc.key, tc.value, err)
		}
	}
}

func TestNotificationDefaultsPreserveExistingBehavior(t *testing.T) {
	defaults := make(map[SettingKey]string)
	for _, setting := range DefaultSettings() {
		defaults[setting.Key] = setting.Value
	}
	for _, key := range []SettingKey{SettingKeyCheckinNotifyEnabled, SettingKeyCheckinNotifySuccessEnabled, SettingKeyCheckinNotifyManualEnabled} {
		if defaults[key] != "false" {
			t.Fatalf("%s is not disabled by default", key)
		}
	}
	if value, exists := defaults[SettingKeyNotificationChannels]; !exists || value != "" {
		t.Fatal("new channel configuration must start unset to preserve the legacy webhook")
	}
}

func TestTurnstileSolverSettings(t *testing.T) {
	for _, tc := range []struct {
		key   SettingKey
		value string
		valid bool
	}{
		{SettingKeyTurnstileSolverEnabled, "true", true}, {SettingKeyTurnstileSolverEnabled, "yes", false},
		{SettingKeyTurnstileCloudflareSolverEnabled, "false", true}, {SettingKeyTurnstileCloudflareSolverEnabled, "1", false},
		{SettingKeyTurnstileSolverQEnabled, "true", true}, {SettingKeyTurnstileSolverQEnabled, "", false},
		{SettingKeyTurnstileCloudflareSolverAPIKey, "", true}, {SettingKeyTurnstileSolverQAPIKey, "sk-abc_123", true},
		{SettingKeyTurnstileSolverQAPIKey, "sk-abc\r\nX-Injected: 1", false}, {SettingKeyTurnstileCloudflareSolverAPIKey, "sk abc", false},
		{SettingKeyTurnstileCloudflareSolverAPIKey, strings.Repeat("a", 513), false},
	} {
		setting := Setting{Key: tc.key, Value: tc.value}
		if err := setting.Validate(); (err == nil) != tc.valid {
			t.Errorf("%s = %q: %v", tc.key, tc.value, err)
		}
	}
	trimmed := Setting{Key: SettingKeyTurnstileSolverQAPIKey, Value: "  sk-abc  "}
	if err := trimmed.Validate(); err != nil || trimmed.Value != "sk-abc" {
		t.Fatalf("key was not trimmed: %q, %v", trimmed.Value, err)
	}
	defaults := make(map[SettingKey]string)
	for _, setting := range DefaultSettings() {
		defaults[setting.Key] = setting.Value
	}
	for _, key := range []SettingKey{SettingKeyTurnstileSolverEnabled, SettingKeyTurnstileCloudflareSolverEnabled, SettingKeyTurnstileSolverQEnabled} {
		if defaults[key] != "false" {
			t.Fatalf("%s is not disabled by default", key)
		}
	}
}

func TestTrustedProxiesSetting(t *testing.T) {
	defaults := make(map[SettingKey]string)
	for _, setting := range DefaultSettings() {
		defaults[setting.Key] = setting.Value
	}
	if got := defaults[SettingKeyTrustedProxies]; got != "" {
		t.Fatalf("trusted proxies default = %q, want empty", got)
	}

	setting := Setting{Key: SettingKeyTrustedProxies, Value: "172.24.0.1\n10.0.0.0/24,172.24.0.1"}
	if err := setting.Validate(); err != nil {
		t.Fatalf("valid trusted proxies rejected: %v", err)
	}
	if setting.Value != "10.0.0.0/24,172.24.0.1" {
		t.Fatalf("normalized trusted proxies = %q", setting.Value)
	}

	invalid := Setting{Key: SettingKeyTrustedProxies, Value: "proxy.example.com"}
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected hostname trusted proxy to be rejected")
	}
}

func TestChannelAffinityDefaultSettings(t *testing.T) {
	defaults := make(map[SettingKey]string)
	for _, setting := range DefaultSettings() {
		defaults[setting.Key] = setting.Value
	}

	if got := defaults[SettingKeyChannelAffinityEnabled]; got != "true" {
		t.Fatalf("channel affinity enabled default = %q, want true", got)
	}
	if got := defaults[SettingKeyChannelAffinityTTLSeconds]; got != "3600" {
		t.Fatalf("channel affinity TTL default = %q, want 3600", got)
	}
}

func TestEmptyResponseDetectionSetting(t *testing.T) {
	defaults := make(map[SettingKey]string)
	for _, setting := range DefaultSettings() {
		defaults[setting.Key] = setting.Value
	}
	if got := defaults[SettingKeyEmptyResponseDetectionEnabled]; got != "true" {
		t.Fatalf("empty response detection default = %q, want true", got)
	}

	for _, value := range []string{"true", "false"} {
		setting := Setting{Key: SettingKeyEmptyResponseDetectionEnabled, Value: value}
		if err := setting.Validate(); err != nil {
			t.Fatalf("expected %q to be valid, got %v", value, err)
		}
	}
	invalid := Setting{Key: SettingKeyEmptyResponseDetectionEnabled, Value: "1"}
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected non-boolean value to be rejected")
	}
}

func TestRelayFailoverBudgetSettings(t *testing.T) {
	defaults := make(map[SettingKey]string)
	for _, setting := range DefaultSettings() {
		defaults[setting.Key] = setting.Value
	}
	if got := defaults[SettingKeyRelayMaxChannelAttempts]; got != "4" {
		t.Fatalf("relay max channel attempts default = %q, want 4", got)
	}
	if got := defaults[SettingKeyRelayMaxTotalAttempts]; got != "12" {
		t.Fatalf("relay max total attempts default = %q, want 12", got)
	}
	if got := defaults[SettingKeyRelayFailoverTimeoutSeconds]; got != "300" {
		t.Fatalf("relay failover timeout default = %q, want 300", got)
	}

	tests := []struct {
		name    string
		setting Setting
		valid   bool
	}{
		{name: "channel minimum", setting: Setting{Key: SettingKeyRelayMaxChannelAttempts, Value: "1"}, valid: true},
		{name: "channel maximum", setting: Setting{Key: SettingKeyRelayMaxChannelAttempts, Value: "64"}, valid: true},
		{name: "channel above maximum", setting: Setting{Key: SettingKeyRelayMaxChannelAttempts, Value: "65"}, valid: false},
		{name: "total default", setting: Setting{Key: SettingKeyRelayMaxTotalAttempts, Value: "12"}, valid: true},
		{name: "total zero", setting: Setting{Key: SettingKeyRelayMaxTotalAttempts, Value: "0"}, valid: false},
		{name: "timeout default", setting: Setting{Key: SettingKeyRelayFailoverTimeoutSeconds, Value: "300"}, valid: true},
		{name: "timeout above maximum", setting: Setting{Key: SettingKeyRelayFailoverTimeoutSeconds, Value: "3601"}, valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.setting.Validate()
			if test.valid && err != nil {
				t.Fatalf("expected setting to be valid, got %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("expected setting to be rejected")
			}
		})
	}
}

func TestCapabilityDegradationPolicySetting(t *testing.T) {
	defaults := make(map[SettingKey]string)
	for _, setting := range DefaultSettings() {
		defaults[setting.Key] = setting.Value
	}
	if got := defaults[SettingKeyCapabilityDegradationPolicy]; got != "warn" {
		t.Fatalf("capability degradation policy default = %q, want warn", got)
	}
	for _, value := range []string{"allow", "warn", "strict"} {
		setting := Setting{Key: SettingKeyCapabilityDegradationPolicy, Value: value}
		if err := setting.Validate(); err != nil {
			t.Fatalf("expected %q to be valid, got %v", value, err)
		}
	}
	invalid := Setting{Key: SettingKeyCapabilityDegradationPolicy, Value: "reject"}
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected unknown capability degradation policy to be rejected")
	}
}

func TestChannelAffinitySettingValidation(t *testing.T) {
	tests := []struct {
		name    string
		setting Setting
		valid   bool
	}{
		{name: "enabled true", setting: Setting{Key: SettingKeyChannelAffinityEnabled, Value: "true"}, valid: true},
		{name: "enabled false", setting: Setting{Key: SettingKeyChannelAffinityEnabled, Value: "false"}, valid: true},
		{name: "enabled invalid", setting: Setting{Key: SettingKeyChannelAffinityEnabled, Value: "1"}, valid: false},
		{name: "ttl minimum", setting: Setting{Key: SettingKeyChannelAffinityTTLSeconds, Value: "1"}, valid: true},
		{name: "ttl default", setting: Setting{Key: SettingKeyChannelAffinityTTLSeconds, Value: "3600"}, valid: true},
		{name: "ttl zero", setting: Setting{Key: SettingKeyChannelAffinityTTLSeconds, Value: "0"}, valid: false},
		{name: "ttl negative", setting: Setting{Key: SettingKeyChannelAffinityTTLSeconds, Value: "-1"}, valid: false},
		{name: "ttl non-integer", setting: Setting{Key: SettingKeyChannelAffinityTTLSeconds, Value: "one hour"}, valid: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.setting.Validate()
			if test.valid && err != nil {
				t.Fatalf("expected setting to be valid, got %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("expected setting to be rejected")
			}
		})
	}
}
