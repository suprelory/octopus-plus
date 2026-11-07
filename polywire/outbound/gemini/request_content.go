package gemini

import (
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

// geminiInlineDataMaxBytes is the decoded-size ceiling Gemini enforces
// on inline_data payloads. Gemini documents the limit as ~20 MB per the
// File API guidance; exceeding it returns 400 "request payload size
// exceeds the limit". Callers that need larger payloads must upload via
// the Files API and send a FileData reference instead.
// Ref: https://ai.google.dev/gemini-api/docs/document-processing
//
// Declared as a var (not const) so tests can shrink the threshold
// without allocating tens of megabytes of base64 fixture data.
var geminiInlineDataMaxBytes = 20 * 1024 * 1024

// convertDocumentToGeminiPart maps an Anthropic-style document block onto a
// GeminiPart. PDF / image payloads become inline_data; text documents fall
// back to a Text part prefixed with the title/context so the model still
// has the metadata. URL-sourced documents degrade to a text hint since
// Gemini does not fetch URLs inline.
//
// Base64 payloads larger than ~20MB are rejected upstream; G-M10 widens
// this path to:
//  1. TransformerMetadata["gemini_files_api_uri"] (per-request override) —
//     emit a FileData reference instead of inline_data;
//  2. a generic mediaType-keyed override
//     TransformerMetadata["gemini_files_api_uri:<media_type>"] for callers
//     that pre-uploaded a specific asset;
//  3. otherwise drop the block with a warning so operators can see the
//     payload is too big for inline transport.
func (o *MessagesOutbound) convertDocumentToGeminiPart(doc *model.DocumentSource, req *model.InternalLLMRequest) *model.GeminiPart {
	part, _, warning := planGeminiDocumentConversion(doc, req, "")
	if warning != "" {
		o.config.Log().Warnf("%s", warning)
	}
	return part
}

// lookupGeminiFilesAPIURI looks up a pre-uploaded Files API URI that should
// substitute for an oversized inline document. Priority:
//
//  1. TransformerMetadata["gemini_files_api_uri:<media_type>"] — per-mime
//     override for callers that pre-uploaded a specific asset.
//  2. TransformerMetadata["gemini_files_api_uri"] — generic fallback for
//     the common single-document case.
func lookupGeminiFilesAPIURI(req *model.InternalLLMRequest, mediaType string) string {
	if req == nil {
		return ""
	}
	if mediaType != "" {
		if uri := req.TransformerMetadataValue(model.TransformerMetadataGeminiFilesAPIURI + ":" + mediaType); uri != "" {
			return uri
		}
	}
	return req.TransformerMetadataValue(model.TransformerMetadataGeminiFilesAPIURI)
}

// buildDocumentTextHint joins title / context / body into a single
// whitespace-separated block. Used as a fallback when Gemini (or any other
// non-Anthropic provider) cannot embed a native document.
func buildDocumentTextHint(doc *model.DocumentSource, body string) string {
	parts := make([]string, 0, 3)
	if doc.Title != "" {
		parts = append(parts, "Title: "+doc.Title)
	}
	if doc.Context != "" {
		parts = append(parts, "Context: "+doc.Context)
	}
	if body != "" {
		parts = append(parts, body)
	}
	return strings.Join(parts, "\n\n")
}

func audioTypeToMimeType(format string) string {
	switch format {
	case "wav":
		return "audio/wav"
	case "mp3":
		return "audio/mp3"
	case "aiff":
		return "audio/aiff"
	case "aac":
		return "audio/aac"
	case "ogg":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	default:
		return "audio/wav"
	}
}
