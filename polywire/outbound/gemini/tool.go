package gemini

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/samber/lo"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func geminiFunctionCallID(functionCall *model.GeminiFunctionCall, index int, discriminator ...string) string {
	if functionCall != nil {
		if id := strings.TrimSpace(functionCall.ID); id != "" {
			return id
		}
		return anthropicSafeFallbackToolCallID(functionCall.Name, index, discriminator...)
	}
	return anthropicSafeFallbackToolCallID("", index, discriminator...)
}

func anthropicSafeFallbackToolCallID(functionName string, index int, discriminator ...string) string {
	raw := fmt.Sprintf("call_%s_%d", functionName, index)
	for _, value := range discriminator {
		if value != "" {
			joined := strings.Join(discriminator, "\x00")
			sum := sha256.Sum256([]byte(joined))
			raw += "_" + hex.EncodeToString(sum[:6])
			break
		}
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	id := b.String()
	if strings.Trim(id, "_-") == "" {
		id = fmt.Sprintf("call_%d", index)
	}
	if len(id) <= 64 {
		return id
	}
	sum := sha256.Sum256([]byte(raw))
	suffix := hex.EncodeToString(sum[:6])
	prefixLen := 64 - 1 - len(suffix)
	if prefixLen < len("call") {
		return "call_" + suffix
	}
	return strings.TrimRight(id[:prefixLen], "_-") + "_" + suffix
}

func convertLLMToolResultToGeminiContent(msg *model.Message, functionName string) *model.GeminiContent {
	content := &model.GeminiContent{
		Role: "user", // Function responses come from user role in Gemini
	}

	var responseData map[string]any
	if msg.Content.Content != nil {
		if parsed, ok := decodeGeminiToolResponse(*msg.Content.Content); ok {
			responseData = parsed
		}
	}

	if responseData == nil {
		responseData = map[string]any{"result": lo.FromPtrOr(msg.Content.Content, "")}
	}

	fp := &model.GeminiFunctionResponse{
		ID:       lo.FromPtr(msg.ToolCallID),
		Name:     functionName,
		Response: responseData,
	}

	content.Parts = []*model.GeminiPart{
		{FunctionResponse: fp},
	}

	return content
}

// resolveGeminiToolResponseName looks up the originating function name for a
// tool-result message. Gemini requires `functionResponse.name` to match the
// upstream `functionCall.name` byte-for-byte; falling back to the tool-call
// ID (as the previous implementation did) produces
// `INVALID_ARGUMENT: Function response name does not match any function call
// name`.
//
// Precedence:
//  1. `msg.ToolCallName` — populated by the inbound layer when available.
//  2. `toolCallNamesByID[msg.ToolCallID]` — name observed on a prior assistant
//     turn within the same request.
//  3. Empty string — caller is expected to downgrade the turn (degradedToolCalls
//     path) so Gemini still receives a well-formed message.
func resolveGeminiToolResponseName(msg *model.Message, toolCallNamesByID map[string]string) string {
	if msg == nil {
		return ""
	}
	if msg.ToolCallName != nil {
		if name := strings.TrimSpace(*msg.ToolCallName); name != "" {
			return name
		}
	}
	if msg.ToolCallID != nil {
		if name, ok := toolCallNamesByID[*msg.ToolCallID]; ok {
			return name
		}
	}
	return ""
}

func decodeGeminiToolResponse(raw string) (map[string]any, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !json.Valid([]byte(trimmed)) {
		return nil, false
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return nil, false
	}
	switch value := decoded.(type) {
	case map[string]any:
		return value, true
	default:
		return map[string]any{"result": value}, true
	}
}

func convertLLMToolResultToGeminiTextContent(msg *model.Message, toolName string) *model.GeminiContent {
	text := formatGeminiToolResultFallback(msg, toolName)
	if text == "" {
		text = "Tool result received."
	}
	return &model.GeminiContent{
		Role:  "user",
		Parts: []*model.GeminiPart{{Text: text}},
	}
}

func formatGeminiToolResultFallback(msg *model.Message, toolName string) string {
	label := strings.TrimSpace(toolName)
	if label == "" {
		if msg.ToolCallName != nil {
			label = strings.TrimSpace(*msg.ToolCallName)
		}
	}
	body := strings.TrimSpace(flattenGeminiToolResultContent(msg))
	switch {
	case label != "" && body != "":
		return fmt.Sprintf("Tool result %s: %s", label, body)
	case label != "":
		return fmt.Sprintf("Tool result %s received.", label)
	default:
		return body
	}
}

func flattenGeminiToolResultContent(msg *model.Message) string {
	if msg == nil {
		return ""
	}
	if msg.Content.Content != nil {
		return *msg.Content.Content
	}
	if len(msg.Content.MultipleContent) == 0 {
		return ""
	}
	texts := make([]string, 0, len(msg.Content.MultipleContent))
	for _, part := range msg.Content.MultipleContent {
		if part.Text != nil && *part.Text != "" {
			texts = append(texts, *part.Text)
		}
	}
	if len(texts) > 0 {
		return strings.Join(texts, "\n")
	}
	if body, err := json.Marshal(msg.Content.MultipleContent); err == nil {
		return string(body)
	}
	return ""
}
