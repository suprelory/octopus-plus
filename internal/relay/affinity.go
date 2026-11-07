package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
)

type affinityHeadersKey struct{}

func resolveRequestAffinity(headers http.Header, body []byte) balancer.AffinityOptions {
	mode, err := op.SettingGetString(dbmodel.SettingKeyChannelAffinityMode)
	if err != nil || mode == "" {
		mode = "prefer"
	}
	if enabled, err := op.SettingGetBool(dbmodel.SettingKeyChannelAffinityEnabled); err == nil && !enabled {
		mode = "off"
	}
	option := balancer.AffinityOptions{Mode: mode, Source: "none"}
	if mode == "off" {
		return option
	}
	source, _ := op.SettingGetString(dbmodel.SettingKeyChannelAffinitySource)
	if source == "" {
		source = "auto"
	}
	if source == "api_key" {
		option.Source = source
		return option
	}
	header, _ := op.SettingGetString(dbmodel.SettingKeyChannelAffinityHeader)
	if header == "" {
		header = "X-Session-Id"
	}
	var payload struct {
		SessionID      string `json:"session_id"`
		PromptCacheKey string `json:"prompt_cache_key"`
		Metadata       struct {
			SessionID string `json:"session_id"`
		} `json:"metadata"`
	}
	_ = json.Unmarshal(body, &payload)
	if payload.SessionID == "" {
		payload.SessionID = payload.Metadata.SessionID
	}
	for _, candidate := range []struct{ source, value string }{
		{"header", headers.Get(header)}, {"session_id", payload.SessionID}, {"prompt_cache_key", payload.PromptCacheKey},
	} {
		value := strings.TrimSpace(candidate.value)
		if (source == "auto" || source == candidate.source) && value != "" && len(value) <= 4096 {
			digest := sha256.Sum256([]byte(candidate.source + "\x00" + value))
			option.Scope, option.Source = hex.EncodeToString(digest[:]), candidate.source
			return option
		}
	}
	// Auto preserves the legacy API-key scope when clients supply no session ID.
	if source == "auto" {
		option.Source = "api_key"
		return option
	}
	// An explicitly selected but missing identifier disables affinity.
	option.Mode = "off"
	return option
}

func affinityHeaders(ctx context.Context) http.Header {
	headers, _ := ctx.Value(affinityHeadersKey{}).(http.Header)
	return headers
}
