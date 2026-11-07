package sitesync

import (
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestPickPreferredDetectedRouteType(t *testing.T) {
	tests := []struct {
		name      string
		modelName string
		values    []model.SiteModelRouteType
		expected  model.SiteModelRouteType
	}{
		{
			name:      "claude prefers anthropic when available",
			modelName: "claude-3-5-sonnet",
			values:    []model.SiteModelRouteType{model.SiteModelRouteTypeOpenAIChat, model.SiteModelRouteTypeAnthropic},
			expected:  model.SiteModelRouteTypeAnthropic,
		},
		{
			name:      "claude falls back to response before chat",
			modelName: "claude-3-5-sonnet",
			values:    []model.SiteModelRouteType{model.SiteModelRouteTypeOpenAIChat, model.SiteModelRouteTypeOpenAIResponse},
			expected:  model.SiteModelRouteTypeOpenAIResponse,
		},
		{
			name:      "claude falls back to chat before gemini when anthropic missing",
			modelName: "claude-3-5-sonnet",
			values:    []model.SiteModelRouteType{model.SiteModelRouteTypeGemini, model.SiteModelRouteTypeOpenAIChat},
			expected:  model.SiteModelRouteTypeOpenAIChat,
		},
		{
			name:      "gemini keeps native route when available",
			modelName: "gemini-2.0-flash",
			values:    []model.SiteModelRouteType{model.SiteModelRouteTypeOpenAIChat, model.SiteModelRouteTypeGemini},
			expected:  model.SiteModelRouteTypeGemini,
		},
		{
			name:      "gpt prefers response over chat",
			modelName: "gpt-4o-mini",
			values:    []model.SiteModelRouteType{model.SiteModelRouteTypeOpenAIChat, model.SiteModelRouteTypeOpenAIResponse},
			expected:  model.SiteModelRouteTypeOpenAIResponse,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if actual := pickPreferredDetectedRouteType(tt.modelName, tt.values); actual != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, actual)
			}
		})
	}
}

func TestBuildSiteModelRouteDetectionKeepsDeclaredChatForGPT5(t *testing.T) {
	detection, ok := buildSiteModelRouteDetection(
		"gpt-5.4",
		nil,
		[]string{"/v1/chat/completions"},
		"/api/pricing",
		map[string]struct{}{"gpt-5.4": {}},
	)
	if !ok {
		t.Fatalf("expected declared chat detection to be produced")
	}

	metadata, ok := model.ParseSiteModelRouteMetadata(detection.RouteRawPayload)
	if !ok {
		t.Fatalf("expected route metadata to parse")
	}
	if metadata.RouteType != model.SiteModelRouteTypeOpenAIChat {
		t.Fatalf("expected declared chat route type %q, got %q", model.SiteModelRouteTypeOpenAIChat, metadata.RouteType)
	}
	if len(metadata.SupportedEndpointTypes) != 1 || metadata.SupportedEndpointTypes[0] != "/v1/chat/completions" {
		t.Fatalf("expected upstream endpoint list to remain intact, got %#v", metadata.SupportedEndpointTypes)
	}
	if len(metadata.HeuristicEndpointTypes) != 0 {
		t.Fatalf("expected no heuristic endpoint list, got %#v", metadata.HeuristicEndpointTypes)
	}
	if len(metadata.NormalizedEndpointTypes) != 1 || metadata.NormalizedEndpointTypes[0] != string(model.SiteModelRouteTypeOpenAIChat) {
		t.Fatalf("expected normalized endpoint list to contain only chat, got %#v", metadata.NormalizedEndpointTypes)
	}
}

func TestBuildSiteModelRouteDetectionGuessesRouteFromModelName(t *testing.T) {
	tests := []struct {
		name                   string
		modelName              string
		enableGroups           []string
		supportedEndpointTypes []string
		expected               model.SiteModelRouteType
	}{
		{
			name:                   "unmappable endpoint types fall back to embedding guess",
			modelName:              "vendor-embedding-x",
			supportedEndpointTypes: []string{"/vendor/embeddings"},
			expected:               model.SiteModelRouteTypeOpenAIEmbedding,
		},
		{
			name:                   "unmappable endpoint types fall back to anthropic guess",
			modelName:              "claude-3-5-sonnet",
			supportedEndpointTypes: []string{"/vendor/custom"},
			expected:               model.SiteModelRouteTypeAnthropic,
		},
		{
			name:                   "unmappable endpoint types fall back to gpt responses guess",
			modelName:              "gpt-4.1",
			supportedEndpointTypes: []string{"/vendor/custom"},
			expected:               model.SiteModelRouteTypeOpenAIResponse,
		},
		{
			name:         "gpt groups without endpoint types fall back to responses guess",
			modelName:    "gpt-5.4",
			enableGroups: []string{"default"},
			expected:     model.SiteModelRouteTypeOpenAIResponse,
		},
		{
			name:         "enable groups without endpoint types fall back to chat guess",
			modelName:    "vendor-chat-x",
			enableGroups: []string{"default"},
			expected:     model.SiteModelRouteTypeOpenAIChat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detection, ok := buildSiteModelRouteDetection(
				tt.modelName,
				tt.enableGroups,
				tt.supportedEndpointTypes,
				"/api/pricing",
				map[string]struct{}{tt.modelName: {}},
			)
			if !ok {
				t.Fatalf("expected guessed detection to be produced")
			}
			if detection.RouteType != tt.expected {
				t.Fatalf("expected guessed route type %q, got %q", tt.expected, detection.RouteType)
			}

			metadata, ok := model.ParseSiteModelRouteMetadata(detection.RouteRawPayload)
			if !ok {
				t.Fatalf("expected route metadata to parse")
			}
			if !metadata.RouteSupported {
				t.Fatalf("expected guessed metadata to mark route as supported")
			}
			if !metadata.RouteGuessed {
				t.Fatalf("expected guessed metadata to record name guess")
			}
			if metadata.RouteType != tt.expected {
				t.Fatalf("expected guessed metadata route type %q, got %q", tt.expected, metadata.RouteType)
			}
		})
	}
}

func TestMergeSiteModelRouteDetectionsPrefersDetectedOverGuessed(t *testing.T) {
	guessed, ok := buildSiteModelRouteDetection(
		"vendor-chat-x",
		[]string{"default"},
		nil,
		"/api/pricing",
		nil,
	)
	if !ok {
		t.Fatalf("expected guessed detection to be produced")
	}
	detected, ok := buildSiteModelRouteDetection(
		"vendor-chat-x",
		nil,
		[]string{"/v1/messages"},
		"/api/available_model",
		nil,
	)
	if !ok {
		t.Fatalf("expected explicit detection to be produced")
	}

	merged := mergeSiteModelRouteDetections(
		map[string]siteModelRouteDetection{"vendor-chat-x": guessed},
		map[string]siteModelRouteDetection{"vendor-chat-x": detected},
	)
	if merged["vendor-chat-x"].RouteType != model.SiteModelRouteTypeAnthropic {
		t.Fatalf("expected explicit detection to replace guessed detection, got %q", merged["vendor-chat-x"].RouteType)
	}

	merged = mergeSiteModelRouteDetections(
		map[string]siteModelRouteDetection{"vendor-chat-x": detected},
		map[string]siteModelRouteDetection{"vendor-chat-x": guessed},
	)
	if merged["vendor-chat-x"].RouteType != model.SiteModelRouteTypeAnthropic {
		t.Fatalf("expected guessed detection to not replace explicit detection, got %q", merged["vendor-chat-x"].RouteType)
	}
}

func TestBuildSiteModelRouteDetectionDoesNotTreatSimilarMarkersAsRemoved(t *testing.T) {
	tests := []struct {
		name                   string
		modelName              string
		supportedEndpointTypes []string
	}{
		{name: "spark model", modelName: "spark-chat", supportedEndpointTypes: []string{"spark"}},
		{name: "unrelated volcengine substring", modelName: "vendor-model", supportedEndpointTypes: []string{"notvolcengine"}},
		{name: "unrelated doubao substring", modelName: "notdoubao-model", supportedEndpointTypes: []string{"chat/completions"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detection, ok := buildSiteModelRouteDetection(
				tt.modelName,
				nil,
				tt.supportedEndpointTypes,
				"/api/pricing",
				map[string]struct{}{tt.modelName: {}},
			)
			if !ok {
				t.Fatal("expected supported route detection to be retained")
			}
			if detection.RouteType == model.SiteModelRouteTypeUnknown {
				t.Fatalf("similar marker must not become unsupported: %+v", detection)
			}
			metadata, parsed := model.ParseSiteModelRouteMetadata(detection.RouteRawPayload)
			if !parsed || !metadata.RouteSupported {
				t.Fatalf("expected supported metadata, got parsed=%t metadata=%+v", parsed, metadata)
			}
		})
	}
}
