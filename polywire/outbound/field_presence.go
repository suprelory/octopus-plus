package outbound

import (
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

// Chat uses the canonical request's field names. Its explicit null/empty values
// can disappear from the JSON projection used by scalar rules, so inspect their
// original presence against the emitted wire as well. Unknown extensions are
// already classified by RequestRecoveryChanges and must not become known losses.
func reportChatEmptyFieldDrops(input conversionInput, reported LossReport) LossReport {
	if input.request.RawAPIFormat != model.APIFormatOpenAIChatCompletion {
		return nil
	}
	var losses LossReport
	for field := range input.request.EmptyFields {
		if input.request.FieldPresenceOf(field) == model.FieldAbsent || field == "n" {
			// Every supported target produces the one choice represented by n:null.
			continue
		}
		if recovery := input.request.Operation; recovery != nil && recovery.Recovery != nil {
			if _, unknown := recovery.Recovery.Fields[field]; unknown {
				continue
			}
		}
		if value, exists := input.source[field]; exists && !emptyFieldValue(value) {
			// A later edit replaced the original empty value; evaluate that value
			// through the ordinary conversion rules instead of stale presence.
			continue
		}
		target := chatEmptyFieldTarget(field, input.targetFormat)
		if wireFieldPresent(input.wire, target) {
			continue
		}
		alreadyReported := false
		for _, change := range reported {
			if change.Field == field || strings.HasPrefix(change.Field, field+".") {
				alreadyReported = true
				break
			}
		}
		if !alreadyReported {
			losses = append(losses, CapabilityLoss{
				Field: field, TargetField: target, Action: LossActionDrop,
				Condition: "explicit null or empty source field is absent from emitted wire",
				Reason:    fmt.Sprintf("%s omits the explicit null or empty value of %s", input.targetFormat, field),
			})
		}
	}
	return losses
}

func emptyFieldValue(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case string:
		return value == ""
	case []any:
		return len(value) == 0
	case map[string]any:
		return len(value) == 0
	default:
		return false
	}
}

func wireFieldPresent(wire map[string]any, path string) bool {
	parts := strings.Split(path, ".")
	for index, part := range parts {
		value, exists := wire[part]
		if !exists {
			return false
		}
		if index == len(parts)-1 {
			return true // An explicit JSON null is present.
		}
		wire, _ = value.(map[string]any)
	}
	return false
}

func chatEmptyFieldTarget(field string, target model.APIFormat) string {
	switch target {
	case model.APIFormatOpenAIResponse:
		switch field {
		case "max_tokens", "max_completion_tokens":
			return "max_output_tokens"
		case "response_format":
			return "text.format"
		case "verbosity":
			return "text.verbosity"
		case "reasoning_effort":
			return "reasoning.effort"
		}
	case model.APIFormatAnthropicMessage:
		switch field {
		case "max_completion_tokens":
			return "max_tokens"
		case "stop":
			return "stop_sequences"
		case "user":
			return "metadata.user_id"
		}
	case model.APIFormatGeminiContents:
		if name := map[string]string{
			"temperature": "temperature", "top_p": "topP", "top_k": "topK",
			"max_tokens": "maxOutputTokens", "max_completion_tokens": "maxOutputTokens",
			"stop": "stopSequences", "frequency_penalty": "frequencyPenalty",
			"presence_penalty": "presencePenalty", "seed": "seed",
			"response_format": "responseSchema", "logprobs": "responseLogprobs",
			"top_logprobs": "logprobs", "modalities": "responseModalities",
		}[field]; name != "" {
			return "generationConfig." + name
		}
	}
	return field
}
