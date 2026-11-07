package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func (o *MessagesOutbound) TransformRequest(ctx context.Context, request *model.InternalLLMRequest, baseUrl, key string) (*http.Request, error) {
	if err := request.ValidateOperationConsistency(); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, fmt.Errorf("request is nil")
	}
	request, _ = prepareGeminiRequest(request, request.Model)

	// Convert internal request to Gemini format
	geminiReq := o.convertLLMToGeminiRequest(request)

	body, err := model.MarshalRequestWithRecovery(request, model.APIFormatGeminiContents, geminiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal gemini request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if err := o.RetargetRequest(req, model.RequestTargetFrom(request), baseUrl, key); err != nil {
		req.Body.Close()
		return nil, err
	}
	return req, nil
}

func (*MessagesOutbound) RetargetRequest(req *http.Request, target model.RequestTarget, baseUrl, key string) error {
	// Build URL
	parsedUrl, err := url.Parse(strings.TrimSuffix(baseUrl, "/"))
	if err != nil {
		return fmt.Errorf("failed to parse base url: %w", err)
	}

	// G-H5: When the channel BaseURL omits the API version segment
	// (`https://generativelanguage.googleapis.com`), the downstream request
	// would land on `/models/...` which 404s. Fall back to `/v1beta` when
	// no version prefix is configured; leave explicit `/v1` or `/v1beta`
	// paths alone.
	if !pathHasGeminiVersion(parsedUrl.Path) {
		parsedUrl.Path = strings.TrimRight(parsedUrl.Path, "/") + "/v1beta"
	}

	// Determine if streaming
	isStream := target.Streaming
	method := "generateContent"
	if isStream {
		method = "streamGenerateContent"
	}

	// Build path: /models/{model}:{method}
	modelName := target.Model
	if !strings.Contains(modelName, "/") {
		modelName = "models/" + modelName
	}
	parsedUrl.Path = fmt.Sprintf("%s/%s:%s", parsedUrl.Path, modelName, method)

	// G-H6: Carry the API key in `x-goog-api-key` — the query-string form
	// still works but leaks the secret into proxy access logs and is
	// discouraged by Google's current docs.
	if isStream {
		q := parsedUrl.Query()
		q.Set("alt", "sse")
		parsedUrl.RawQuery = q.Encode()
	}

	req.URL = parsedUrl
	if key != "" {
		req.Header.Set("x-goog-api-key", key)
	} else {
		req.Header.Del("x-goog-api-key")
	}

	return nil
}

// Helper functions

// pathHasGeminiVersion reports whether the configured base-URL path already
// contains a Gemini API version segment (`/v1`, `/v1beta`, etc.). Used by
// G-H5 to decide whether to prepend `/v1beta` as a fallback when channels
// were provisioned with a bare hostname. Matching on a leading `/v` prefix
// covers the versions Google documents (v1, v1beta, v1beta2, v1alpha) and
// will survive future bumps without churn.
func pathHasGeminiVersion(p string) bool {
	segment := strings.Trim(p, "/")
	if segment == "" {
		return false
	}
	first := segment
	if idx := strings.Index(segment, "/"); idx >= 0 {
		first = segment[:idx]
	}
	if len(first) < 2 || first[0] != 'v' {
		return false
	}
	// Must be `v<digit>...`; `/viewer` etc. should not count.
	return first[1] >= '0' && first[1] <= '9'
}

// canonicalGeminiModality normalises a client-supplied modality keyword into
// the Gemini wire shape. Gemini accepts TEXT / IMAGE / AUDIO (upper case);
// unknown values return the empty string so the caller drops them rather
// than letting a 400 surface at request time.
func canonicalGeminiModality(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "text":
		return "TEXT"
	case "image":
		return "IMAGE"
	case "audio":
		return "AUDIO"
	default:
		return ""
	}
}

// SupportsResponseModality reports whether a client output modality can be
// represented by Gemini's responseModalities field.
func SupportsResponseModality(m string) bool {
	return canonicalGeminiModality(m) != ""
}

func (o *MessagesOutbound) convertLLMToGeminiRequest(request *model.InternalLLMRequest) *model.GeminiGenerateContentRequest {
	contents, systemInstruction := o.convertGeminiMessages(request)
	geminiReq := &model.GeminiGenerateContentRequest{
		Contents:          contents,
		SystemInstruction: systemInstruction,
	}

	geminiReq.GenerationConfig = o.convertGeminiGenerationConfig(request)
	applyGeminiSafetySettings(geminiReq, request)
	o.applyGeminiTools(geminiReq, request)
	applyGeminiToolChoice(geminiReq, request)
	applyGeminiMetadata(geminiReq, request)

	return geminiReq
}

func (o *MessagesOutbound) convertGeminiMessages(request *model.InternalLLMRequest) ([]*model.GeminiContent, *model.GeminiContent) {
	messages := request.ConversationMessages()
	contents := make([]*model.GeminiContent, 0, len(messages))
	var systemInstruction *model.GeminiContent
	degradedToolCalls := map[string]string{}
	toolCallNamesByID := map[string]string{}

	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role == "" {
			role = "user"
		} else if role == "model" {
			role = "assistant"
		}

		switch role {
		case "system", "developer":
			systemInstruction = appendGeminiSystemInstruction(systemInstruction, &msg)
		case "user":
			contents = append(contents, o.convertGeminiUserContent(&msg, request))
		case "assistant":
			contents = append(contents, o.convertGeminiAssistantContent(&msg, toolCallNamesByID))
		case "tool":
			contents = append(contents, convertGeminiToolContent(&msg, toolCallNamesByID, degradedToolCalls))
		}
	}

	return contents, systemInstruction
}

func appendGeminiSystemInstruction(current *model.GeminiContent, msg *model.Message) *model.GeminiContent {
	if current == nil {
		current = &model.GeminiContent{Parts: []*model.GeminiPart{}}
	}
	if msg.Content.Content != nil {
		current.Parts = append(current.Parts, &model.GeminiPart{Text: *msg.Content.Content})
	}
	return current
}

func (o *MessagesOutbound) convertGeminiUserContent(msg *model.Message, request *model.InternalLLMRequest) *model.GeminiContent {
	content := &model.GeminiContent{Role: "user", Parts: []*model.GeminiPart{}}
	if msg.Content.Content != nil {
		content.Parts = append(content.Parts, &model.GeminiPart{Text: *msg.Content.Content})
	}

	if msg.Content.MultipleContent == nil {
		return content
	}
	for _, part := range msg.Content.MultipleContent {
		switch strings.ToLower(strings.TrimSpace(part.Type)) {
		case "text":
			if part.Text != nil {
				content.Parts = append(content.Parts, &model.GeminiPart{Text: *part.Text})
			}
		case "image_url":
			partOut, _, warning := planGeminiImageURLConversion(part.ImageURL, "")
			if warning != "" {
				o.config.Log().Warnf("gemini: %s", warning)
			}
			if partOut != nil {
				content.Parts = append(content.Parts, partOut)
			}
		case "input_audio":
			if part.Audio != nil {
				content.Parts = append(content.Parts, &model.GeminiPart{
					InlineData: &model.GeminiBlob{
						MimeType: audioTypeToMimeType(part.Audio.Format),
						Data:     part.Audio.Data,
					},
				})
			} else {
				o.config.Log().Warnf("gemini: drops an input_audio content part with no audio source")
			}
		case "file":
			partOut, _, warning := planGeminiFileConversion(part.File, "")
			if warning != "" {
				o.config.Log().Warnf("gemini: %s", warning)
			}
			if partOut != nil {
				content.Parts = append(content.Parts, partOut)
			}
		case "document":
			if p := o.convertDocumentToGeminiPart(part.Document, request); p != nil {
				content.Parts = append(content.Parts, p)
			}
		case "server_tool_use", "server_tool_result":
			o.config.Log().Warnf("gemini: dropping unsupported %q block", part.Type)
		}
	}

	return content
}

func (o *MessagesOutbound) convertGeminiAssistantContent(msg *model.Message, toolCallNamesByID map[string]string) *model.GeminiContent {
	content := &model.GeminiContent{Role: "model", Parts: []*model.GeminiPart{}}
	geminiBlocks := msg.ReasoningBlocksByProvider("gemini")
	content.Parts = append(content.Parts, buildGeminiThoughtParts(geminiBlocks)...)
	geminiSigByToolCallID := collectGeminiSignaturesByToolCallID(geminiBlocks)
	geminiSigs := collectGeminiLooseSignatures(geminiBlocks)
	geminiSigByName := collectGeminiSignaturesByName(geminiBlocks)
	sigIdx := 0

	if msg.Content.Content != nil && *msg.Content.Content != "" {
		content.Parts = append(content.Parts, &model.GeminiPart{Text: *msg.Content.Content})
	}
	for _, toolCall := range msg.ToolCalls {
		if toolCall.ID != "" && toolCall.Function.Name != "" {
			toolCallNamesByID[toolCall.ID] = toolCall.Function.Name
		}
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
			o.config.Log().Warnf("gemini: failed to unmarshal tool call arguments for %s: %v", toolCall.Function.Name, err)
		}
		part := &model.GeminiPart{FunctionCall: &model.GeminiFunctionCall{
			ID:   toolCall.ID,
			Name: toolCall.Function.Name,
			Args: args,
		}}

		sig := toolCall.GetGeminiExtensions().ThoughtSignature
		if strings.TrimSpace(sig) == "" {
			sig = ""
		}
		if sig == "" {
			if byID, ok := geminiSigByToolCallID[toolCall.ID]; ok && byID != "" {
				sig = byID
				delete(geminiSigByToolCallID, toolCall.ID)
			} else if named, ok := geminiSigByName[toolCall.Function.Name]; ok && named != "" {
				sig = named
				delete(geminiSigByName, toolCall.Function.Name)
			} else if fallbackSig, ok := nextGeminiSignature(geminiSigs, &sigIdx); ok {
				sig = fallbackSig
			}
		}
		if sig != "" {
			part.ThoughtSignature = sig
		}
		content.Parts = append(content.Parts, part)
	}

	if len(geminiBlocks) > 0 || sigIdx > 0 {
		o.config.Log().Debugw("transformer.reasoning.signature.passthrough",
			"provider", "gemini",
			"direction", "inject",
			"signature_count", sigIdx,
			"available_signatures", len(geminiSigs),
		)
	}
	return content
}

func convertGeminiToolContent(msg *model.Message, toolCallNamesByID, degradedToolCalls map[string]string) *model.GeminiContent {
	functionName := resolveGeminiToolResponseName(msg, toolCallNamesByID)
	content := convertLLMToolResultToGeminiContent(msg, functionName)
	if msg.ToolCallID != nil {
		if toolName, ok := degradedToolCalls[*msg.ToolCallID]; ok {
			content = convertLLMToolResultToGeminiTextContent(msg, toolName)
		}
	}
	return content
}

func (o *MessagesOutbound) convertGeminiGenerationConfig(request *model.InternalLLMRequest) *model.GeminiGenerationConfig {
	config := &model.GeminiGenerationConfig{}
	hasConfig := applyGeminiSamplingConfig(config, request)
	if applyGeminiSpeechConfig(config, request) {
		hasConfig = true
	}
	if o.applyGeminiThinkingConfig(config, request) {
		hasConfig = true
	}
	if o.applyGeminiResponseFormat(config, request) {
		hasConfig = true
	}
	if applyGeminiResponseModalities(config, request) {
		hasConfig = true
	}
	if !hasConfig {
		return nil
	}
	return config
}

func applyGeminiSamplingConfig(config *model.GeminiGenerationConfig, request *model.InternalLLMRequest) bool {
	hasConfig := false
	if request.MaxTokens != nil {
		config.MaxOutputTokens = int(*request.MaxTokens)
		hasConfig = true
	} else if request.MaxCompletionTokens != nil {
		config.MaxOutputTokens = int(*request.MaxCompletionTokens)
		hasConfig = true
	}
	if request.Temperature != nil {
		config.Temperature = request.Temperature
		hasConfig = true
	}
	if request.TopP != nil {
		config.TopP = request.TopP
		hasConfig = true
	}
	if request.TopK != nil {
		topK := int(*request.TopK)
		config.TopK = &topK
		hasConfig = true
	}
	if request.PresencePenalty != nil {
		config.PresencePenalty = request.PresencePenalty
		hasConfig = true
	}
	if request.FrequencyPenalty != nil {
		config.FrequencyPenalty = request.FrequencyPenalty
		hasConfig = true
	}
	if request.Seed != nil {
		config.Seed = request.Seed
		hasConfig = true
	}
	if request.Logprobs != nil {
		enabled := *request.Logprobs
		config.ResponseLogprobs = &enabled
		hasConfig = true
	}
	if request.TopLogprobs != nil {
		n := int(*request.TopLogprobs)
		if n > 5 {
			n = 5
		}
		if n < 0 {
			n = 0
		}
		config.Logprobs = &n
		hasConfig = true
	}
	if mediaResolution := request.TransformerMetadataValue(model.TransformerMetadataGeminiMediaResolution); mediaResolution != "" {
		config.MediaResolution = mediaResolution
		hasConfig = true
	}
	if request.Stop != nil && request.Stop.MultipleStop != nil {
		config.StopSequences = request.Stop.MultipleStop
		hasConfig = true
	} else if request.Stop != nil && request.Stop.Stop != nil {
		config.StopSequences = []string{*request.Stop.Stop}
		hasConfig = true
	}
	if raw := request.TransformerMetadataValue(model.TransformerMetadataGeminiCandidateCount); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 1 {
			config.CandidateCount = n
			hasConfig = true
		}
	}
	return hasConfig
}

func applyGeminiSpeechConfig(config *model.GeminiGenerationConfig, request *model.InternalLLMRequest) bool {
	geminiExt := request.GetGeminiExtensions()
	if len(geminiExt.SpeechConfig) > 0 {
		config.SpeechConfig = geminiExt.SpeechConfig
		return true
	}
	if request.Audio == nil || strings.TrimSpace(request.Audio.Voice) == "" {
		return false
	}
	voice := strings.TrimSpace(request.Audio.Voice)
	synth, err := json.Marshal(map[string]any{
		"voiceConfig": map[string]any{
			"prebuiltVoiceConfig": map[string]any{"voiceName": voice},
		},
	})
	if err != nil {
		return false
	}
	config.SpeechConfig = synth
	return true
}

func (o *MessagesOutbound) applyGeminiThinkingConfig(config *model.GeminiGenerationConfig, request *model.InternalLLMRequest) bool {
	if request.ReasoningEffort == "" && request.ReasoningBudget == nil && !request.AdaptiveThinking {
		return false
	}
	decision := o.resolveThinkingConfig(request.Model, request.ReasoningBudget, request.ReasoningEffort, request.AdaptiveThinking)
	if !decision.Supported {
		return false
	}
	thinkingConfig := &model.GeminiThinkingConfig{IncludeThoughts: decision.IncludeThoughts}
	if decision.UseLevel {
		if decision.Level != "" {
			thinkingConfig.ThinkingLevel = decision.Level
		}
	} else {
		budget := decision.Budget
		thinkingConfig.ThinkingBudget = &budget
	}
	config.ThinkingConfig = thinkingConfig
	return true
}

func (o *MessagesOutbound) applyGeminiResponseFormat(config *model.GeminiGenerationConfig, request *model.InternalLLMRequest) bool {
	if request.ResponseFormat == nil {
		return false
	}
	switch request.ResponseFormat.Type {
	case "json_object":
		config.ResponseMimeType = "application/json"
	case "json_schema":
		config.ResponseMimeType = "application/json"
		schema, err := request.ResponseFormat.ToGeminiSchema()
		if err != nil {
			o.config.Log().Warnf("gemini: response schema conversion: %v", err)
		}
		config.ResponseSchema = schema
	case "text":
		config.ResponseMimeType = "text/plain"
	default:
		return false
	}
	return true
}

func applyGeminiResponseModalities(config *model.GeminiGenerationConfig, request *model.InternalLLMRequest) bool {
	if len(request.Modalities) == 0 {
		return false
	}
	convertedModalities := make([]string, 0, len(request.Modalities))
	for _, modality := range request.Modalities {
		if wire := canonicalGeminiModality(modality); wire != "" {
			convertedModalities = append(convertedModalities, wire)
		}
	}
	if len(convertedModalities) == 0 {
		return false
	}
	config.ResponseModalities = convertedModalities
	return true
}

func applyGeminiSafetySettings(geminiReq *model.GeminiGenerateContentRequest, request *model.InternalLLMRequest) {
	safetyJSON := request.TransformerMetadataValue(model.TransformerMetadataGeminiSafetySettings)
	if safetyJSON == "" {
		return
	}
	var safetySettings []*model.GeminiSafetySetting
	if err := json.Unmarshal([]byte(safetyJSON), &safetySettings); err == nil {
		geminiReq.SafetySettings = safetySettings
	}
}

func (o *MessagesOutbound) applyGeminiTools(geminiReq *model.GeminiGenerateContentRequest, request *model.InternalLLMRequest) {
	if len(request.Tools) == 0 {
		return
	}
	functionDeclarations := make([]*model.GeminiFunctionDeclaration, 0, len(request.Tools))
	serverTools := make([]*model.GeminiTool, 0, len(request.Tools))
	for _, tool := range request.Tools {
		switch tool.Type {
		case "function", "":
			params, err := geminiSchemaParameters(tool.Function.Parameters)
			if err != nil {
				o.config.Log().Warnf("gemini: tool schema conversion for %s: %v", tool.Function.Name, err)
			}
			functionDeclarations = append(functionDeclarations, &model.GeminiFunctionDeclaration{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  params,
			})
		case "server_search":
			serverTools = append(serverTools, &model.GeminiTool{GoogleSearch: &model.GeminiGoogleSearch{}})
		case "code_execution":
			serverTools = append(serverTools, &model.GeminiTool{CodeExecution: &model.GeminiCodeExecution{}})
		case "url_context":
			serverTools = append(serverTools, &model.GeminiTool{UrlContext: &model.GeminiUrlContext{}})
		default:
			o.config.Log().Warnf("gemini: dropping unsupported tool type %q", tool.Type)
		}
	}

	tools := make([]*model.GeminiTool, 0, len(serverTools)+1)
	if len(functionDeclarations) > 0 {
		tools = append(tools, &model.GeminiTool{FunctionDeclarations: functionDeclarations})
	}
	tools = append(tools, serverTools...)
	if len(functionDeclarations) > 0 && len(serverTools) > 0 {
		o.config.Log().Warnf("gemini: server tools and functionDeclarations declared together; provider may reject the request")
	}
	if len(tools) > 0 {
		geminiReq.Tools = tools
	}
}

func applyGeminiToolChoice(geminiReq *model.GeminiGenerateContentRequest, request *model.InternalLLMRequest) {
	choice := request.ToolChoiceForTarget(model.APIFormatGeminiContents)
	if choice == nil {
		return
	}
	mode := "AUTO"
	var allowed []string
	if choice.ToolChoice != nil {
		switch strings.ToLower(*choice.ToolChoice) {
		case "auto":
			mode = "AUTO"
		case "required", "any":
			mode = "ANY"
		case "none":
			mode = "NONE"
		}
	} else if named := choice.NamedToolChoice; named != nil {
		switch strings.ToLower(named.Type) {
		case "auto":
			mode = "AUTO"
		case "any", "required":
			mode = "ANY"
		case "none":
			mode = "NONE"
		case "function", "tool":
			mode = "ANY"
			if name := named.ResolvedFunctionName(); name != "" {
				allowed = []string{name}
			}
		}
	}
	geminiReq.ToolConfig = &model.GeminiToolConfig{FunctionCallingConfig: &model.GeminiFunctionCallingConfig{
		Mode:                 mode,
		AllowedFunctionNames: allowed,
	}}
}

func applyGeminiMetadata(geminiReq *model.GeminiGenerateContentRequest, request *model.InternalLLMRequest) {
	geminiExt := request.GetGeminiExtensions()
	if geminiExt.CachedContentRef != nil {
		if ref := strings.TrimSpace(*geminiExt.CachedContentRef); ref != "" {
			geminiReq.CachedContent = ref
		}
	}
	if len(request.Metadata) == 0 {
		return
	}
	labels := make(map[string]string, len(request.Metadata))
	for k, v := range request.Metadata {
		labels[k] = v
	}
	geminiReq.Labels = labels
}
