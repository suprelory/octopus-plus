package outbound

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestPlanRequestForModelUsesEffectiveModelWithoutMutatingRequest(t *testing.T) {
	request := &model.InternalLLMRequest{
		RequestType:     model.RequestTypeChat,
		RawAPIFormat:    model.APIFormatOpenAIChatCompletion,
		Model:           "request-model",
		ReasoningEffort: "medium",
	}
	before := request.Clone()

	for _, test := range []struct {
		name         string
		model        string
		outboundType OutboundType
	}{
		{name: "gemini family", model: "gemini-3-pro", outboundType: OutboundTypeGemini},
		{name: "unsupported outbound", model: "legacy-model", outboundType: OutboundType(-1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			modelOverridden := request.Clone()
			modelOverridden.Model = test.model
			want := PlanRequestForModel(modelOverridden, modelOverridden.Model, test.outboundType, false)
			got := PlanRequestForModel(request, test.model, test.outboundType, false)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("parameterized planner differs from cloned request:\nwant=%#v\ngot=%#v", want, got)
			}
		})
	}
	if !reflect.DeepEqual(request, before) {
		t.Fatalf("parameterized planner mutated the canonical request: before=%#v after=%#v", before, request)
	}
}

func TestPlanRequestCoversRequestedSemantics(t *testing.T) {
	stream := true
	includeUsage := &model.StreamOptions{IncludeUsage: true}
	disableParallel := true
	req := &model.InternalLLMRequest{
		RequestType:     model.RequestTypeChat,
		RawAPIFormat:    model.APIFormatOpenAIChatCompletion,
		Model:           "claude-sonnet",
		Messages:        []model.Message{{Role: "user", Content: model.MessageContent{MultipleContent: []model.MessageContentPart{{Type: "input_audio", Audio: &model.Audio{Format: "wav", Data: "AA=="}}}}}},
		Tools:           []model.Tool{{Type: "function", Function: model.Function{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}},
		ToolChoice:      &model.ToolChoice{NamedToolChoice: &model.NamedToolChoice{Type: "function", DisableParallelToolUse: &disableParallel}},
		ReasoningEffort: "high",
		ResponseFormat:  &model.ResponseFormat{Type: "json_schema", Schema: &model.Schema{Type: "object"}},
		Stream:          &stream,
		StreamOptions:   includeUsage,
	}

	decision := PlanRequestForModel(req, req.Model, OutboundTypeAnthropic, false)
	if decision.Status != CapabilityDegraded {
		t.Fatalf("status = %s, want degraded: %+v", decision.Status, decision)
	}
	for _, feature := range []string{"tools", "tool_choice", "reasoning", "structured_output", "multimodal", "stream_usage"} {
		if !slices.Contains(decision.RequiredFeatures, feature) {
			t.Errorf("missing required feature %q in %v", feature, decision.RequiredFeatures)
		}
	}
	for _, field := range []string{"response_format", "messages[0].content[0]"} {
		if !slices.Contains(decision.DegradedFields, field) {
			t.Errorf("missing degraded field %q in %v", field, decision.DegradedFields)
		}
	}
}

func TestPlanRelayOperationUsesProtocolDescriptor(t *testing.T) {
	supported := PlanRelayOperation(OutboundTypeOpenAIResponse, RelayOperationResponsesCompact)
	if supported.Status != CapabilitySupported || supported.StaticQuality != QualityNative {
		t.Fatalf("supported operation decision = %#v", supported)
	}
	if supported.RequestType != model.RequestTypeResponses || supported.InboundFormat != model.APIFormatOpenAIResponse {
		t.Fatalf("supported operation request metadata = %#v", supported)
	}

	rejected := PlanRelayOperation(OutboundTypeAnthropic, RelayOperationImages)
	if !rejected.Rejected() || rejected.StaticQuality != QualityUnsupported {
		t.Fatalf("rejected operation decision = %#v", rejected)
	}
}

func TestPlanRequestRequiresSidecarsOnlyForNativeResponsesRecovery(t *testing.T) {
	req := &model.InternalLLMRequest{RequestType: model.RequestTypeResponses, RawAPIFormat: model.APIFormatOpenAIResponse, Model: "gpt-5", Messages: []model.Message{{Role: "user"}}}
	req.MarkOpenAIResponsesPassthroughRequired("tool:web_search")
	if decision := PlanRequestForModel(req, req.Model, OutboundTypeOpenAIResponse, false); !decision.Rejected() {
		t.Fatalf("missing native tool sidecar accepted: %+v", decision)
	}
	if decision := PlanRequestForModel(req, req.Model, OutboundTypeGemini, false); decision.Status != CapabilityDegraded || len(decision.Losses) != 1 || !decision.Losses[0].IsNativeSemanticLoss() {
		t.Fatalf("native tool fallback = %+v", decision)
	}

	decision := PlanRequestForModel(req, req.Model, OutboundTypeOpenAIResponse, true)
	if decision.Status != CapabilitySupported || !decision.Passthrough {
		t.Fatalf("native Responses passthrough decision = %#v, want supported passthrough", decision)
	}
}

func TestPlanRequestAllowsNativeResponsesRecoveryWithoutRawPassthrough(t *testing.T) {
	previousResponseID := "resp_previous"
	tests := []struct {
		name    string
		request *model.InternalLLMRequest
	}{
		{
			name: "exact replay",
			request: func() *model.InternalLLMRequest {
				req := &model.InternalLLMRequest{
					RequestType:   model.RequestTypeResponses,
					RawAPIFormat:  model.APIFormatOpenAIResponse,
					Model:         "gpt-5",
					RawInputItems: json.RawMessage(`[{"type":"computer_call_output","call_id":"call_1","output":"ok"}]`),
				}
				req.MarkOpenAIResponsesPassthroughRequired("input:computer_call_output")
				req.MarkOpenAIExactReplayRequest()
				return req
			}(),
		},
		{
			name: "upstream continuation",
			request: func() *model.InternalLLMRequest {
				req := &model.InternalLLMRequest{
					RequestType:        model.RequestTypeResponses,
					RawAPIFormat:       model.APIFormatOpenAIResponse,
					Model:              "gpt-5",
					PreviousResponseID: &previousResponseID,
					RawInputItems:      json.RawMessage(`[{"type":"computer_call_output","call_id":"call_1","output":"ok"}]`),
				}
				req.MarkOpenAIResponsesPassthroughRequired("input:computer_call_output")
				return req
			}(),
		},
		{
			name: "exact replay with native tool",
			request: func() *model.InternalLLMRequest {
				req := &model.InternalLLMRequest{
					RequestType:  model.RequestTypeResponses,
					RawAPIFormat: model.APIFormatOpenAIResponse,
					Model:        "gpt-5",
					Messages:     []model.Message{{Role: "user"}},
				}
				req.SetOpenAIResponsesOptions(model.OpenAIResponsesOptions{
					RawTools: json.RawMessage(`[{"type":"web_search","search_context_size":"high"}]`),
				})
				req.MarkOpenAIResponsesPassthroughRequired("tool:web_search")
				req.MarkOpenAIExactReplayRequest()
				return req
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := PlanRequestForModel(tt.request, tt.request.Model, OutboundTypeOpenAIResponse, false)
			if decision.Rejected() || decision.Passthrough {
				t.Fatalf("Responses recovery decision = %#v, want canonical non-rejected path", decision)
			}
			if decision := PlanRequestForModel(tt.request, tt.request.Model, OutboundTypeGemini, false); decision.Status != CapabilityDegraded || decision.Conversion.ReplayAvailable || decision.Conversion.ExactReplay {
				t.Fatalf("non-Responses fallback decision = %#v, want lossy without replay", decision)
			}
		})
	}
}

func TestPlanRequestRejectsResponsesRecoveryWithoutNativeSidecar(t *testing.T) {
	request := &model.InternalLLMRequest{
		RequestType:  model.RequestTypeResponses,
		RawAPIFormat: model.APIFormatOpenAIResponse,
		Model:        "gpt-5",
		Messages:     []model.Message{{Role: "user"}},
	}
	request.MarkOpenAIResponsesPassthroughRequired("tool:web_search")
	request.MarkOpenAIExactReplayRequest()

	if decision := PlanRequestForModel(request, request.Model, OutboundTypeOpenAIResponse, false); !decision.Rejected() {
		t.Fatalf("recovery without raw native tools must remain rejected: %#v", decision)
	}
}

func TestPlanRequestNativePassthroughIsLossless(t *testing.T) {
	req := &model.InternalLLMRequest{RequestType: model.RequestTypeChat, RawAPIFormat: model.APIFormatAnthropicMessage, Model: "claude", Messages: []model.Message{{Role: "user"}}, ResponseFormat: &model.ResponseFormat{Type: "json_schema"}}
	decision := PlanRequestForModel(req, req.Model, OutboundTypeAnthropic, true)
	if decision.Status != CapabilitySupported || decision.Lossiness != "none" || !decision.Passthrough {
		t.Fatalf("unexpected passthrough decision: %+v", decision)
	}
	if !slices.Contains(decision.ConversionPath, "raw_passthrough") {
		t.Fatalf("conversion path = %v", decision.ConversionPath)
	}
}

func TestPlanRequestDetectsLossyGeminiSchema(t *testing.T) {
	req := &model.InternalLLMRequest{
		RequestType:  model.RequestTypeResponses,
		RawAPIFormat: model.APIFormatOpenAIResponse,
		Model:        "gemini-2.5-pro",
		Messages:     []model.Message{{Role: "user"}},
		ResponseFormat: &model.ResponseFormat{
			Type:   "json_schema",
			Schema: &model.Schema{Type: "object", Ref: "#/$defs/item"},
		},
	}
	decision := PlanRequestForModel(req, req.Model, OutboundTypeGemini, false)
	if decision.Status != CapabilityDegraded || !slices.Contains(decision.DegradedFields, "response_format.schema") {
		t.Fatalf("unexpected schema decision: %+v", decision)
	}
	assertCapabilityLoss(t, decision, "response_format.schema", LossActionTranslate)
}

func TestPlanRequestReportsAdapterFieldLosses(t *testing.T) {
	topK := int64(40)
	fiveStops := &model.Stop{MultipleStop: []string{"a", "b", "c", "d", "e"}}
	fourStops := &model.Stop{MultipleStop: []string{"a", "b", "c", "d"}}
	singleStop := "done"

	tests := []struct {
		name         string
		outboundType OutboundType
		inbound      model.APIFormat
		topK         *int64
		stop         *model.Stop
		wantStatus   CapabilityStatus
		wantLosses   map[string]LossAction
	}{
		{
			name:         "OpenAI Chat drops top_k",
			outboundType: OutboundTypeOpenAIChat,
			inbound:      model.APIFormatOpenAIChatCompletion,
			topK:         &topK,
			wantStatus:   CapabilityDegraded,
			wantLosses:   map[string]LossAction{"top_k": LossActionDrop},
		},
		{
			name:         "OpenAI Responses drops top_k and stop",
			outboundType: OutboundTypeOpenAIResponse,
			inbound:      model.APIFormatOpenAIChatCompletion,
			topK:         &topK,
			stop:         &model.Stop{Stop: &singleStop},
			wantStatus:   CapabilityDegraded,
			wantLosses: map[string]LossAction{
				"top_k": LossActionDrop,
				"stop":  LossActionDrop,
			},
		},
		{
			name:         "Anthropic truncates excess stop sequences",
			outboundType: OutboundTypeAnthropic,
			inbound:      model.APIFormatOpenAIChatCompletion,
			stop:         fiveStops,
			wantStatus:   CapabilityDegraded,
			wantLosses:   map[string]LossAction{"stop": LossActionTruncate},
		},
		{
			name:         "Anthropic preserves stop sequences at limit",
			outboundType: OutboundTypeAnthropic,
			inbound:      model.APIFormatOpenAIChatCompletion,
			stop:         fourStops,
			wantStatus:   CapabilitySupported,
		},
		{
			name:         "Gemini carries top_k and stop sequences",
			outboundType: OutboundTypeGemini,
			inbound:      model.APIFormatOpenAIChatCompletion,
			topK:         &topK,
			stop:         fiveStops,
			wantStatus:   CapabilitySupported,
		},
		{
			name:         "OpenAI Chat preserves native stop",
			outboundType: OutboundTypeOpenAIChat,
			inbound:      model.APIFormatOpenAIChatCompletion,
			stop:         &model.Stop{Stop: &singleStop},
			wantStatus:   CapabilitySupported,
		},
		{
			name:         "OpenAI Chat drops Anthropic stop semantics",
			outboundType: OutboundTypeOpenAIChat,
			inbound:      model.APIFormatAnthropicMessage,
			stop:         &model.Stop{Stop: &singleStop},
			wantStatus:   CapabilityDegraded,
			wantLosses:   map[string]LossAction{"stop": LossActionDrop},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &model.InternalLLMRequest{
				RequestType:  model.RequestTypeChat,
				RawAPIFormat: test.inbound,
				TopK:         test.topK,
				Stop:         test.stop,
			}
			decision := PlanRequestForModel(request, request.Model, test.outboundType, false)
			if decision.Status != test.wantStatus {
				t.Fatalf("status = %s, want %s: %#v", decision.Status, test.wantStatus, decision)
			}
			for field, action := range test.wantLosses {
				assertCapabilityLoss(t, decision, field, action)
			}
		})
	}
}

func TestPlanRequestReportsAnthropicThinkingRepairs(t *testing.T) {
	topK := int64(40)
	topP := 0.8
	temperature := 0.4
	request := &model.InternalLLMRequest{
		RequestType:     model.RequestTypeChat,
		RawAPIFormat:    model.APIFormatOpenAIChatCompletion,
		ReasoningEffort: "high",
		TopK:            &topK,
		TopP:            &topP,
		Temperature:     &temperature,
	}

	decision := PlanRequestForModel(request, request.Model, OutboundTypeAnthropic, false)
	if decision.Status != CapabilityDegraded {
		t.Fatalf("status = %s, want degraded: %#v", decision.Status, decision)
	}
	assertCapabilityLoss(t, decision, "top_k", LossActionDrop)
	assertCapabilityLoss(t, decision, "top_p", LossActionDrop)
	assertCapabilityLoss(t, decision, "temperature", LossActionRepair)
}

func TestPlanRequestReportsResponsesBuilderDrops(t *testing.T) {
	frequencyPenalty := 0.2
	presencePenalty := 0.3
	logprobs := true
	seed := int64(42)
	user := "user-1"
	request := &model.InternalLLMRequest{
		RequestType:      model.RequestTypeChat,
		RawAPIFormat:     model.APIFormatOpenAIChatCompletion,
		FrequencyPenalty: &frequencyPenalty,
		PresencePenalty:  &presencePenalty,
		Logprobs:         &logprobs,
		Seed:             &seed,
		LogitBias:        map[string]int64{"42": 10},
		User:             &user,
		Prediction:       json.RawMessage(`{"type":"content","content":"known"}`),
		WebSearchOptions: json.RawMessage(`{"search_context_size":"low"}`),
		Metadata:         map[string]string{"trace": "request-1"},
		Audio: &struct {
			Format string `json:"format,omitempty"`
			Voice  string `json:"voice,omitempty"`
		}{Format: "wav", Voice: "alloy"},
	}
	commonLosses := []string{
		"audio",
		"frequency_penalty",
		"logit_bias",
		"logprobs",
		"prediction",
		"presence_penalty",
		"seed",
		"user",
		"web_search_options",
	}

	openAIDecision := PlanRequestForModel(request, request.Model, OutboundTypeOpenAIResponse, false)
	for _, field := range commonLosses {
		assertCapabilityLoss(t, openAIDecision, field, LossActionDrop)
	}
	if slices.Contains(openAIDecision.DegradedFields, "metadata") {
		t.Fatalf("OpenAI Responses should preserve metadata: %#v", openAIDecision)
	}

	unsupportedDecision := PlanRequestForModel(request, request.Model, OutboundType(-1), false)
	if !unsupportedDecision.Rejected() {
		t.Fatalf("legacy unsupported outbound type must be rejected: %#v", unsupportedDecision)
	}
}

func TestPlanRequestReportsGeminiTopLogprobsClamp(t *testing.T) {
	topLogprobs := int64(8)
	request := &model.InternalLLMRequest{
		RequestType:  model.RequestTypeChat,
		RawAPIFormat: model.APIFormatOpenAIChatCompletion,
		TopLogprobs:  &topLogprobs,
	}

	decision := PlanRequestForModel(request, request.Model, OutboundTypeGemini, false)
	assertCapabilityLoss(t, decision, "top_logprobs", LossActionTruncate)

	topLogprobs = 5
	decision = PlanRequestForModel(request, request.Model, OutboundTypeGemini, false)
	if slices.Contains(decision.DegradedFields, "top_logprobs") {
		t.Fatalf("in-range top_logprobs should not be degraded: %#v", decision)
	}
}

func TestPlanRequestReportsGeminiMultimodalWireDrops(t *testing.T) {
	request := &model.InternalLLMRequest{
		RequestType:  model.RequestTypeChat,
		RawAPIFormat: model.APIFormatOpenAIChatCompletion,
		Model:        "gemini-2.5-pro",
		Messages: []model.Message{{
			Role: "user",
			Content: model.MessageContent{MultipleContent: []model.MessageContentPart{
				{Type: "image_url", ImageURL: &model.ImageURL{URL: "https://example.com/image.png"}},
				{Type: "file", File: &model.File{FileID: "file-abc123"}},
			}},
		}},
	}

	decision := PlanRequestForModel(request, request.Model, OutboundTypeGemini, false)
	if decision.Status != CapabilityDegraded {
		t.Fatalf("status = %s, want degraded: %#v", decision.Status, decision)
	}
	assertCapabilityLoss(t, decision, "messages[0].content[0]", LossActionDrop)
	assertCapabilityLoss(t, decision, "messages[0].content[1]", LossActionDrop)
}

func TestPlanRequestReportsAnthropicAndGeminiBuilderDrops(t *testing.T) {
	frequencyPenalty := 0.2
	presencePenalty := 0.3
	logprobs := true
	seed := int64(42)
	topLogprobs := int64(3)
	user := "user-1"
	request := &model.InternalLLMRequest{
		RequestType:      model.RequestTypeChat,
		RawAPIFormat:     model.APIFormatOpenAIChatCompletion,
		FrequencyPenalty: &frequencyPenalty,
		PresencePenalty:  &presencePenalty,
		Logprobs:         &logprobs,
		TopLogprobs:      &topLogprobs,
		Seed:             &seed,
		LogitBias:        map[string]int64{"42": 10},
		User:             &user,
		Prediction:       json.RawMessage(`{"type":"content","content":"known"}`),
		WebSearchOptions: json.RawMessage(`{"search_context_size":"low"}`),
		Metadata:         map[string]string{"trace": "request-1"},
		Audio: &struct {
			Format string `json:"format,omitempty"`
			Voice  string `json:"voice,omitempty"`
		}{Format: "wav", Voice: "alloy"},
	}

	anthropicDecision := PlanRequestForModel(request, request.Model, OutboundTypeAnthropic, false)
	for _, field := range []string{
		"audio",
		"frequency_penalty",
		"logit_bias",
		"logprobs",
		"metadata",
		"prediction",
		"presence_penalty",
		"seed",
		"top_logprobs",
		"web_search_options",
	} {
		assertCapabilityLoss(t, anthropicDecision, field, LossActionDrop)
	}
	if slices.Contains(anthropicDecision.DegradedFields, "user") {
		t.Fatalf("Anthropic should translate user to metadata.user_id: %#v", anthropicDecision)
	}

	request.Metadata = map[string]string{"user_id": "anthropic-user"}
	anthropicDecision = PlanRequestForModel(request, request.Model, OutboundTypeAnthropic, false)
	if slices.Contains(anthropicDecision.DegradedFields, "metadata") {
		t.Fatalf("Anthropic should preserve metadata.user_id: %#v", anthropicDecision)
	}
	assertCapabilityLoss(t, anthropicDecision, "user", LossActionDrop)

	geminiDecision := PlanRequestForModel(request, request.Model, OutboundTypeGemini, false)
	for _, field := range []string{"logit_bias", "prediction", "user", "web_search_options"} {
		assertCapabilityLoss(t, geminiDecision, field, LossActionDrop)
	}
	for _, field := range []string{"frequency_penalty", "logprobs", "metadata", "presence_penalty", "seed", "top_logprobs"} {
		if slices.Contains(geminiDecision.DegradedFields, field) {
			t.Fatalf("Gemini should preserve %s: %#v", field, geminiDecision)
		}
	}
}

func TestPlanRequestReportsGeminiThinkingChanges(t *testing.T) {
	zero := int64(0)
	request := &model.InternalLLMRequest{
		RequestType:     model.RequestTypeChat,
		RawAPIFormat:    model.APIFormatOpenAIChatCompletion,
		Model:           "gemini-2.5-pro",
		ReasoningBudget: &zero,
	}
	decision := PlanRequestForModel(request, request.Model, OutboundTypeGemini, false)
	assertCapabilityLoss(t, decision, "reasoning_budget", LossActionRepair)

	huge := int64(1 << 40)
	request.ReasoningBudget = &huge
	decision = PlanRequestForModel(request, request.Model, OutboundTypeGemini, false)
	assertCapabilityLoss(t, decision, "reasoning_budget", LossActionRepair)

	budget := int64(4096)
	request.Model = "gemini-3-pro"
	request.ReasoningBudget = &budget
	decision = PlanRequestForModel(request, request.Model, OutboundTypeGemini, false)
	assertCapabilityLoss(t, decision, "reasoning_budget", LossActionTranslate)

	request.ReasoningBudget = nil
	request.ReasoningEffort = "medium"
	decision = PlanRequestForModel(request, request.Model, OutboundTypeGemini, false)
	assertCapabilityLoss(t, decision, "reasoning_effort", LossActionRepair)

	request.Model = "gemini-2.5-flash-lite"
	request.ReasoningEffort = "high"
	decision = PlanRequestForModel(request, request.Model, OutboundTypeGemini, false)
	assertCapabilityLoss(t, decision, "reasoning", LossActionDrop)
}

func TestPlanRequestGeminiBudgetTakesPrecedenceOverEffort(t *testing.T) {
	budget := int64(4096)
	request := &model.InternalLLMRequest{
		RequestType:     model.RequestTypeResponses,
		RawAPIFormat:    model.APIFormatOpenAIResponse,
		Model:           "gemini-2.5-pro",
		ReasoningBudget: &budget,
		ReasoningEffort: "minimal",
	}

	decision := PlanRequestForModel(request, request.Model, OutboundTypeGemini, false)
	if slices.Contains(decision.DegradedFields, "reasoning_effort") {
		t.Fatalf("ignored effort was reported as degraded: %#v", decision)
	}
}

func TestPlanRequestReportsGeminiUnknownModalityDrop(t *testing.T) {
	request := &model.InternalLLMRequest{
		RequestType:  model.RequestTypeChat,
		RawAPIFormat: model.APIFormatOpenAIChatCompletion,
		Modalities:   []string{"text", "video"},
	}

	decision := PlanRequestForModel(request, request.Model, OutboundTypeGemini, false)
	assertCapabilityLoss(t, decision, "modalities", LossActionDrop)
}

func TestPlanRequestReportsAnthropicRepairs(t *testing.T) {
	zero := int64(0)
	request := &model.InternalLLMRequest{
		RequestType:  model.RequestTypeChat,
		RawAPIFormat: model.APIFormatOpenAIChatCompletion,
		MaxTokens:    &zero,
		ToolChoice: &model.ToolChoice{NamedToolChoice: &model.NamedToolChoice{
			Type: "function",
		}},
	}

	decision := PlanRequestForModel(request, request.Model, OutboundTypeAnthropic, false)
	assertCapabilityLoss(t, decision, "max_tokens", LossActionRepair)
	assertCapabilityLoss(t, decision, "tool_choice.name", LossActionRepair)
}

func TestPlanRequestReportsAnthropicInboundMaxTokensRepair(t *testing.T) {
	one := int64(1)
	request := &model.InternalLLMRequest{
		RequestType:         model.RequestTypeChat,
		RawAPIFormat:        model.APIFormatAnthropicMessage,
		MaxTokens:           &one,
		TransformerMetadata: map[string]string{model.TransformerMetadataAnthropicMaxTokensRepairFrom: "0"},
	}

	decision := PlanRequestForModel(request, request.Model, OutboundTypeOpenAIChat, false)
	assertCapabilityLoss(t, decision, "max_tokens", LossActionRepair)
}

func TestPlanRequestReportsOpenAIChatDocumentTranslation(t *testing.T) {
	request := &model.InternalLLMRequest{
		RequestType:  model.RequestTypeChat,
		RawAPIFormat: model.APIFormatAnthropicMessage,
		Messages: []model.Message{{
			Role: "user",
			Content: model.MessageContent{MultipleContent: []model.MessageContentPart{{
				Type: "document",
				Document: &model.DocumentSource{
					Type:  "url",
					URL:   "https://example.com/report.pdf",
					Title: "Report",
				},
			}}},
		}},
	}

	decision := PlanRequestForModel(request, request.Model, OutboundTypeOpenAIChat, false)
	assertCapabilityLoss(t, decision, "messages[0].content[0]", LossActionTranslate)
}

func TestPlanRequestConsumesAdapterReportedChanges(t *testing.T) {
	request := &model.InternalLLMRequest{
		RequestType:  model.RequestTypeChat,
		RawAPIFormat: model.APIFormatAnthropicMessage,
		Model:        "gemini-2.5-pro",
		Messages: []model.Message{{
			Role: "user",
			Content: model.MessageContent{MultipleContent: []model.MessageContentPart{{
				Type:     "document",
				Document: &model.DocumentSource{Type: "url", URL: "https://example.com/report.pdf", Title: "Report"},
			}}},
		}},
	}

	geminiDecision := PlanRequestForModel(request, request.Model, OutboundTypeGemini, false)
	assertCapabilityLoss(t, geminiDecision, "messages[0].content[0]", LossActionTranslate)

	cacheParts := make([]model.MessageContentPart, 0, model.AnthropicMaxCacheBreakpoints+1)
	for i := 0; i < model.AnthropicMaxCacheBreakpoints+1; i++ {
		text := "cache-part"
		cacheParts = append(cacheParts, model.MessageContentPart{
			Type:         "text",
			Text:         &text,
			CacheControl: &model.CacheControl{Type: model.CacheControlTypeEphemeral},
		})
	}
	request = &model.InternalLLMRequest{
		RequestType:  model.RequestTypeChat,
		RawAPIFormat: model.APIFormatOpenAIChatCompletion,
		Model:        "claude-sonnet-4-5",
		Messages:     []model.Message{{Role: "user", Content: model.MessageContent{MultipleContent: cacheParts}}},
	}

	anthropicDecision := PlanRequestForModel(request, request.Model, OutboundTypeAnthropic, false)
	assertCapabilityLoss(t, anthropicDecision, "messages[0].content[4].cache_control", LossActionTruncate)
}

func TestCapabilityLossJSONFieldNames(t *testing.T) {
	payload, err := json.Marshal(CapabilityLoss{Field: "top_k", Action: LossActionDrop, Reason: "not preserved"})
	if err != nil {
		t.Fatalf("marshal capability loss: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode capability loss: %v", err)
	}
	for _, field := range []string{"field", "action", "reason"} {
		if _, ok := decoded[field]; !ok {
			t.Errorf("missing JSON field %q in %s", field, payload)
		}
	}
	for _, legacyField := range []string{"Field", "Action", "Reason"} {
		if _, ok := decoded[legacyField]; ok {
			t.Errorf("unexpected Go field name %q in %s", legacyField, payload)
		}
	}
}

func TestPlanRequestReportsKnownFieldLosses(t *testing.T) {
	enabled := true
	summary := "auto"
	tests := []struct {
		name         string
		outboundType OutboundType
		request      *model.InternalLLMRequest
		wantFields   []string
	}{
		{
			name:         "chat non-function tool and reasoning controls",
			outboundType: OutboundTypeOpenAIChat,
			request: &model.InternalLLMRequest{
				Tools:                    []model.Tool{{Type: "image_generation"}},
				ReasoningBudget:          int64Ptr(100),
				AdaptiveThinking:         true,
				EnableThinking:           &enabled,
				ReasoningGenerateSummary: &summary,
			},
			wantFields: []string{"tools[0].type", "reasoning_budget", "adaptive_thinking", "enable_thinking", "reasoning_summary"},
		},
		{
			name:         "responses output modality",
			outboundType: OutboundTypeOpenAIResponse,
			request:      &model.InternalLLMRequest{Modalities: []string{"audio"}},
			wantFields:   []string{"modalities"},
		},
		{
			name:         "anthropic standalone reasoning budget",
			outboundType: OutboundTypeAnthropic,
			request:      &model.InternalLLMRequest{ReasoningBudget: int64Ptr(100)},
			wantFields:   []string{"reasoning_budget"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.request.RequestType = model.RequestTypeChat
			test.request.RawAPIFormat = model.APIFormatOpenAIChatCompletion
			decision := PlanRequestForModel(test.request, test.request.Model, test.outboundType, false)
			if decision.Status != CapabilityDegraded {
				t.Fatalf("decision = %#v, want degraded", decision)
			}
			for _, field := range test.wantFields {
				if !slices.Contains(decision.DegradedFields, field) {
					t.Errorf("missing degraded field %q in %#v", field, decision)
				}
			}
		})
	}
}

func int64Ptr(value int64) *int64 { return &value }

func assertCapabilityLoss(t *testing.T, decision CapabilityDecision, field string, action LossAction) {
	t.Helper()
	if !slices.Contains(decision.DegradedFields, field) {
		t.Errorf("missing degraded field %q in %#v", field, decision)
		return
	}
	for _, loss := range decision.Losses {
		if loss.Field == field && loss.Action == action {
			return
		}
	}
	t.Errorf("missing %s loss for field %q in %#v", action, field, decision.Losses)
}
