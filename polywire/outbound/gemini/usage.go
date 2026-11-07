package gemini

import (
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func convertGeminiUsageMetadata(metadata *model.GeminiUsageMetadata) *model.Usage {
	if metadata == nil {
		return nil
	}
	usage := &model.Usage{
		PromptTokens:     int64(metadata.PromptTokenCount),
		CompletionTokens: int64(metadata.CandidatesTokenCount),
		TotalTokens:      int64(metadata.TotalTokenCount),
	}

	if metadata.CachedContentTokenCount > 0 {
		if usage.PromptTokensDetails == nil {
			usage.PromptTokensDetails = &model.PromptTokensDetails{}
		}
		usage.PromptTokensDetails.CachedTokens = int64(metadata.CachedContentTokenCount)
	}

	if metadata.ThoughtsTokenCount > 0 {
		if usage.CompletionTokensDetails == nil {
			usage.CompletionTokensDetails = &model.CompletionTokensDetails{}
		}
		usage.CompletionTokensDetails.ReasoningTokens = int64(metadata.ThoughtsTokenCount)
	}

	if metadata.ToolUsePromptTokenCount > 0 {
		usage.ToolUsePromptTokens = int64(metadata.ToolUsePromptTokenCount)
	}

	if len(metadata.PromptTokensDetails) > 0 {
		if usage.PromptTokensDetails == nil {
			usage.PromptTokensDetails = &model.PromptTokensDetails{}
		}
		applyGeminiModalityToPromptDetails(usage.PromptTokensDetails, metadata.PromptTokensDetails)
		usage.PromptModalityTokenDetails = toInternalModalityCounts(metadata.PromptTokensDetails)
	}

	if len(metadata.CandidatesTokensDetails) > 0 {
		if usage.CompletionTokensDetails == nil {
			usage.CompletionTokensDetails = &model.CompletionTokensDetails{}
		}
		applyGeminiModalityToCompletionDetails(usage.CompletionTokensDetails, metadata.CandidatesTokensDetails)
		usage.CompletionModalityTokenDetails = toInternalModalityCounts(metadata.CandidatesTokensDetails)
	}

	return usage
}

// applyGeminiModalityToPromptDetails folds per-modality Gemini breakdowns into
// the internal PromptTokensDetails (TEXT/IMAGE/VIDEO/AUDIO/DOCUMENT).
func applyGeminiModalityToPromptDetails(details *model.PromptTokensDetails, counts []model.GeminiModalityTokenCount) {
	if details == nil {
		return
	}
	for _, mt := range counts {
		switch strings.ToUpper(mt.Modality) {
		case "TEXT":
			details.TextTokens += int64(mt.TokenCount)
		case "IMAGE":
			details.ImageTokens += int64(mt.TokenCount)
		case "VIDEO":
			details.VideoTokens += int64(mt.TokenCount)
		case "AUDIO":
			details.AudioTokens += int64(mt.TokenCount)
		case "DOCUMENT":
			details.DocumentTokens += int64(mt.TokenCount)
		}
	}
}

// applyGeminiModalityToCompletionDetails folds per-modality Gemini breakdowns
// into the internal CompletionTokensDetails.
func applyGeminiModalityToCompletionDetails(details *model.CompletionTokensDetails, counts []model.GeminiModalityTokenCount) {
	if details == nil {
		return
	}
	for _, mt := range counts {
		switch strings.ToUpper(mt.Modality) {
		case "TEXT":
			details.TextTokens += int64(mt.TokenCount)
		case "IMAGE":
			details.ImageTokens += int64(mt.TokenCount)
		case "VIDEO":
			details.VideoTokens += int64(mt.TokenCount)
		case "AUDIO":
			details.AudioTokens += int64(mt.TokenCount)
		}
	}
}

func toInternalModalityCounts(counts []model.GeminiModalityTokenCount) []model.ModalityTokenCount {
	if len(counts) == 0 {
		return nil
	}
	result := make([]model.ModalityTokenCount, 0, len(counts))
	for _, mt := range counts {
		result = append(result, model.ModalityTokenCount{
			Modality:   mt.Modality,
			TokenCount: int64(mt.TokenCount),
		})
	}
	return result
}
