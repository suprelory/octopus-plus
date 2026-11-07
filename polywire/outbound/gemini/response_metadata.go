package gemini

import (
	"github.com/suprelory/octopus-plus/polywire/model"
)

// convertGeminiGroundingToInternal maps Gemini's groundingMetadata response
// block onto the cross-provider GroundingInfo shape. Returns nil when the
// upstream payload is empty so the caller can skip assignment. G-H10.
func convertGeminiGroundingToInternal(md *model.GeminiGroundingMetadata) *model.GroundingInfo {
	if md == nil {
		return nil
	}
	info := &model.GroundingInfo{
		Provider: "gemini", Raw: md.Raw,
		SearchQueries: md.WebSearchQueries,
	}
	if md.SearchEntryPoint != nil {
		info.SearchEntryPointHTML = md.SearchEntryPoint.RenderedContent
	}
	if len(md.GroundingChunks) > 0 {
		sources := make([]model.GroundingSource, 0, len(md.GroundingChunks))
		for _, c := range md.GroundingChunks {
			if c == nil || c.Web == nil {
				// Skip unknown chunk shapes — only "web" is modelled.
				sources = append(sources, model.GroundingSource{})
				continue
			}
			sources = append(sources, model.GroundingSource{
				URI:     c.Web.URI,
				Title:   c.Web.Title,
				Snippet: c.Web.Snippet,
			})
		}
		info.Sources = sources
	}
	if len(md.GroundingSupports) > 0 {
		supports := make([]model.GroundingSupport, 0, len(md.GroundingSupports))
		for _, s := range md.GroundingSupports {
			if s == nil {
				continue
			}
			gs := model.GroundingSupport{
				SourceIndices:    s.GroundingChunkIndices,
				ConfidenceScores: s.ConfidenceScores,
			}
			if s.Segment != nil {
				gs.SegmentStartIndex = s.Segment.StartIndex
				gs.SegmentEndIndex = s.Segment.EndIndex
				gs.SegmentText = s.Segment.Text
			}
			supports = append(supports, gs)
		}
		info.Supports = supports
	}
	// If literally nothing was populated, treat as absent so the caller can
	// leave Choice.Grounding nil.
	if len(info.SearchQueries) == 0 && len(info.Sources) == 0 && len(info.Supports) == 0 && info.SearchEntryPointHTML == "" && len(info.Raw) == 0 {
		return nil
	}
	return info
}

// convertGeminiCitationsToInternal maps Gemini's citationMetadata response
// block onto the cross-provider []Citation shape. G-H10.
func convertGeminiCitationsToInternal(md *model.GeminiCitationMetadata) []model.Citation {
	if md == nil || len(md.CitationSources) == 0 {
		return nil
	}
	out := make([]model.Citation, 0, len(md.CitationSources))
	for _, src := range md.CitationSources {
		if src == nil {
			continue
		}
		out = append(out, model.Citation{
			Provider: "gemini", Format: model.APIFormatGeminiContents, Raw: src.Raw,
			StartIndex: src.StartIndex,
			EndIndex:   src.EndIndex,
			URI:        src.URI,
			Title:      src.Title,
			License:    src.License,
		})
	}
	return out
}

// convertGeminiURLContextToInternal maps Gemini's urlContextMetadata
// response block onto the cross-provider URLContextInfo shape. G-H10.
func convertGeminiURLContextToInternal(md *model.GeminiUrlContextMetadata) *model.URLContextInfo {
	if md == nil || len(md.URLMetadata) == 0 {
		return nil
	}
	entries := make([]model.URLContextEntry, 0, len(md.URLMetadata))
	for _, u := range md.URLMetadata {
		if u == nil {
			continue
		}
		url := u.RetrievedURL
		if url == "" {
			url = u.URL
		}
		entries = append(entries, model.URLContextEntry{
			URL:    url,
			Status: u.URLRetrievalStatus,
		})
	}
	if len(entries) == 0 {
		return nil
	}
	return &model.URLContextInfo{URLs: entries}
}

// convertGeminiSafetyRatings maps Gemini's per-category safety evaluation
// onto the cross-provider SafetyRating shape. Returns nil for empty input
// so the caller can skip assignment. G-M9.
func convertGeminiSafetyRatings(raw []*model.GeminiSafetyRating) []model.SafetyRating {
	if len(raw) == 0 {
		return nil
	}
	out := make([]model.SafetyRating, 0, len(raw))
	for _, r := range raw {
		if r == nil {
			continue
		}
		out = append(out, model.SafetyRating{
			Category:    r.Category,
			Probability: r.Probability,
			Blocked:     r.Blocked,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// convertGeminiFinishReason maps a Gemini finishReason to the canonical
// FinishReason wire string. Unlike the previous hardcoded switch, this
// preserves provider-specific reasons (BLOCKLIST / PROHIBITED_CONTENT / SPII /
// MALFORMED_FUNCTION_CALL / IMAGE_SAFETY / LANGUAGE / OTHER) instead of
// collapsing them to "stop".
func convertGeminiFinishReason(reason string) string {
	return model.FinishReasonFromGemini(reason).String()
}
