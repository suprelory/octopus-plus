package model

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/outbound"
)

type SiteModelRouteType string

const (
	SiteModelRouteTypeOpenAIChat      SiteModelRouteType = "openai_chat"
	SiteModelRouteTypeOpenAIResponse  SiteModelRouteType = "openai_response"
	SiteModelRouteTypeAnthropic       SiteModelRouteType = "anthropic"
	SiteModelRouteTypeGemini          SiteModelRouteType = "gemini"
	SiteModelRouteTypeOpenAIEmbedding SiteModelRouteType = "openai_embedding"
	SiteModelRouteTypeUnknown         SiteModelRouteType = "unknown"
)

type SiteModelRouteSource string

const (
	SiteModelRouteSourceSyncInferred    SiteModelRouteSource = "sync_inferred"
	SiteModelRouteSourceManualOverride  SiteModelRouteSource = "manual_override"
	SiteModelRouteSourceRuntimeLearned  SiteModelRouteSource = "runtime_learned"
	SiteModelRouteSourceDefaultAssigned SiteModelRouteSource = "default_assigned"
)

// SiteRouteBaseURL overrides the projected channel base URL for a specific
// outbound route type. Some upstreams expose different protocols under
// different path prefixes (e.g. OpenAI responses at "<base>/v1" but Anthropic
// messages at "<base>/anthropic/v1"); a single site base URL cannot serve
// both, so each route type may carry its own full base URL here.
type SiteRouteBaseURL struct {
	RouteType SiteModelRouteType `json:"route_type"`
	BaseURL   string             `json:"base_url"`
}

// ResolveRouteBaseURL returns the per-route base URL override for routeType,
// trimmed of trailing slashes. The second return value reports whether a
// usable (non-empty) override exists.
func (s *Site) ResolveRouteBaseURL(routeType SiteModelRouteType) (string, bool) {
	if s == nil {
		return "", false
	}
	normalizedRouteType := NormalizeSiteModelRouteType(routeType)
	if !IsProjectedSiteModelRouteType(normalizedRouteType) {
		return "", false
	}
	for _, item := range s.RouteBaseURLs {
		itemRouteType := NormalizeSiteModelRouteType(item.RouteType)
		if !IsProjectedSiteModelRouteType(itemRouteType) || itemRouteType != normalizedRouteType {
			continue
		}
		trimmed := strings.TrimRight(strings.TrimSpace(item.BaseURL), "/")
		if trimmed == "" {
			return "", false
		}
		return trimmed, true
	}
	return "", false
}

// NormalizeSiteRouteBaseURLs trims values, drops entries with an empty base
// URL or route type, and keeps the first entry per route type.
func NormalizeSiteRouteBaseURLs(items []SiteRouteBaseURL) []SiteRouteBaseURL {
	if len(items) == 0 {
		return items
	}
	seen := make(map[SiteModelRouteType]struct{}, len(items))
	result := make([]SiteRouteBaseURL, 0, len(items))
	for _, item := range items {
		rawRouteType := strings.TrimSpace(string(item.RouteType))
		routeType := NormalizeSiteModelRouteType(item.RouteType)
		baseURL := strings.TrimRight(strings.TrimSpace(item.BaseURL), "/")
		if rawRouteType == "" || !IsProjectedSiteModelRouteType(routeType) || baseURL == "" {
			continue
		}
		if _, ok := seen[routeType]; ok {
			continue
		}
		seen[routeType] = struct{}{}
		result = append(result, SiteRouteBaseURL{RouteType: routeType, BaseURL: baseURL})
	}
	return result
}

// ValidateSiteRouteBaseURLs rejects overrides whose route type is not a
// projectable outbound route or whose base URL is not a valid http/https URL.
// It mirrors the validation applied to Site.BaseURL so malformed overrides are
// surfaced to the caller instead of silently breaking projection.
func ValidateSiteRouteBaseURLs(items []SiteRouteBaseURL) error {
	for _, item := range items {
		if !IsProjectedSiteModelRouteType(item.RouteType) {
			return fmt.Errorf("route base url has unsupported route type: %s", item.RouteType)
		}
		parsed, err := url.Parse(item.BaseURL)
		if err != nil {
			return fmt.Errorf("route base url for %s is invalid: %w", item.RouteType, err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("route base url for %s must use http or https", item.RouteType)
		}
		if parsed.Host == "" {
			return fmt.Errorf("route base url for %s must have a host", item.RouteType)
		}
	}
	return nil
}

func NormalizeSiteModelRouteType(routeType SiteModelRouteType) SiteModelRouteType {
	normalized := SiteModelRouteType(strings.ToLower(strings.TrimSpace(string(routeType))))
	switch normalized {
	case SiteModelRouteTypeOpenAIChat,
		SiteModelRouteTypeOpenAIResponse,
		SiteModelRouteTypeAnthropic,
		SiteModelRouteTypeGemini,
		SiteModelRouteTypeOpenAIEmbedding,
		SiteModelRouteTypeUnknown:
		return normalized
	case "":
		return SiteModelRouteTypeOpenAIChat
	default:
		return SiteModelRouteTypeUnknown
	}
}

func IsProjectedSiteModelRouteType(routeType SiteModelRouteType) bool {
	switch routeType {
	case SiteModelRouteTypeOpenAIChat,
		SiteModelRouteTypeOpenAIResponse,
		SiteModelRouteTypeAnthropic,
		SiteModelRouteTypeGemini,
		SiteModelRouteTypeOpenAIEmbedding:
		return true
	default:
		return false
	}
}

func NormalizeSiteModelRouteSource(routeSource SiteModelRouteSource, manualOverride bool) SiteModelRouteSource {
	switch routeSource {
	case SiteModelRouteSourceSyncInferred,
		SiteModelRouteSourceManualOverride,
		SiteModelRouteSourceRuntimeLearned,
		SiteModelRouteSourceDefaultAssigned:
		return routeSource
	default:
		if manualOverride {
			return SiteModelRouteSourceManualOverride
		}
		return SiteModelRouteSourceSyncInferred
	}
}

func InferSiteModelRouteType(modelName string) SiteModelRouteType {
	lower := strings.ToLower(strings.TrimSpace(modelName))
	switch {
	case strings.HasPrefix(lower, "claude"):
		return SiteModelRouteTypeAnthropic
	case strings.HasPrefix(lower, "gemini"):
		return SiteModelRouteTypeGemini
	case strings.HasPrefix(lower, "gpt"):
		return SiteModelRouteTypeOpenAIResponse
	case strings.Contains(lower, "embedding"):
		return SiteModelRouteTypeOpenAIEmbedding
	default:
		return SiteModelRouteTypeOpenAIChat
	}
}

func SiteModelRouteTypeSuffix(routeType SiteModelRouteType) string {
	switch NormalizeSiteModelRouteType(routeType) {
	case SiteModelRouteTypeOpenAIResponse:
		return "openai-response"
	case SiteModelRouteTypeAnthropic:
		return "anthropic"
	case SiteModelRouteTypeGemini:
		return "gemini"
	case SiteModelRouteTypeOpenAIEmbedding:
		return "openai-embedding"
	default:
		return ""
	}
}

func CompactSiteModelRouteTypeName(routeType SiteModelRouteType) string {
	switch NormalizeSiteModelRouteType(routeType) {
	case SiteModelRouteTypeOpenAIChat:
		return "Chat"
	case SiteModelRouteTypeOpenAIResponse:
		return "Response"
	case SiteModelRouteTypeAnthropic:
		return "Anthropic"
	case SiteModelRouteTypeGemini:
		return "Gemini"
	case SiteModelRouteTypeOpenAIEmbedding:
		return "Embedding"
	case SiteModelRouteTypeUnknown:
		return "Unsupported"
	default:
		return "Chat"
	}
}

func ComposeSiteChannelBindingKey(groupKey string, routeType SiteModelRouteType, split bool) string {
	groupKey = NormalizeSiteGroupKey(groupKey)
	if !split {
		return groupKey
	}
	if suffix := SiteModelRouteTypeSuffix(routeType); suffix != "" {
		return groupKey + "::" + suffix
	}
	return groupKey
}

func ParseSiteChannelBindingKey(groupKey string) (string, SiteModelRouteType) {
	baseKey, suffix, found := strings.Cut(NormalizeSiteGroupKey(groupKey), "::")
	if !found {
		return baseKey, SiteModelRouteTypeOpenAIChat
	}
	switch suffix {
	case "openai-response":
		return baseKey, SiteModelRouteTypeOpenAIResponse
	case "anthropic":
		return baseKey, SiteModelRouteTypeAnthropic
	case "gemini":
		return baseKey, SiteModelRouteTypeGemini
	case "openai-embedding":
		return baseKey, SiteModelRouteTypeOpenAIEmbedding
	default:
		return baseKey, SiteModelRouteTypeUnknown
	}
}

func ShouldSplitSiteChannelRoutes(platform SitePlatform) bool {
	switch platform {
	case SitePlatformAPI:
		return false
	default:
		return true
	}
}

func (t SiteModelRouteType) ToOutboundType() outbound.OutboundType {
	switch NormalizeSiteModelRouteType(t) {
	case SiteModelRouteTypeOpenAIResponse:
		return outbound.OutboundTypeOpenAIResponse
	case SiteModelRouteTypeAnthropic:
		return outbound.OutboundTypeAnthropic
	case SiteModelRouteTypeGemini:
		return outbound.OutboundTypeGemini
	case SiteModelRouteTypeOpenAIEmbedding:
		return outbound.OutboundTypeOpenAIEmbedding
	case SiteModelRouteTypeUnknown:
		return -1
	default:
		return outbound.OutboundTypeOpenAIChat
	}
}

func SiteModelRouteTypeFromOutboundType(t outbound.OutboundType) SiteModelRouteType {
	switch t {
	case outbound.OutboundTypeOpenAIResponse:
		return SiteModelRouteTypeOpenAIResponse
	case outbound.OutboundTypeAnthropic:
		return SiteModelRouteTypeAnthropic
	case outbound.OutboundTypeGemini:
		return SiteModelRouteTypeGemini
	case outbound.OutboundTypeOpenAIEmbedding:
		return SiteModelRouteTypeOpenAIEmbedding
	case outbound.OutboundTypeOpenAIChat:
		return SiteModelRouteTypeOpenAIChat
	default:
		return SiteModelRouteTypeUnknown
	}
}

func (s *Site) ResolveDefaultRouteType() SiteModelRouteType {
	if s.DefaultRouteType != "" {
		return NormalizeSiteModelRouteType(s.DefaultRouteType)
	}
	return SiteModelRouteTypeOpenAIChat
}
