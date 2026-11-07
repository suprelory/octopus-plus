package gemini

import (
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/internal/dataurl"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func prepareGeminiRequest(request *model.InternalLLMRequest, effectiveModel string) (*model.InternalLLMRequest, model.AlternationReport) {
	if request == nil {
		return nil, model.AlternationReport{}
	}
	prepared := request.Clone()
	if modelName := strings.TrimSpace(effectiveModel); modelName != "" {
		prepared.Model = modelName
	}
	prepared.NormalizeMessages()
	messages, report := model.EnforceAlternationWithReport(prepared.ConversationMessages(), model.AlternationProviderGemini)
	prepared.SetConversationMessages(messages)
	return prepared, report
}

func (o *MessagesOutbound) DescribeRequestChanges(request *model.InternalLLMRequest, effectiveModel string) []model.RequestTransformationChange {
	if request == nil {
		return nil
	}
	prepared, alternation := prepareGeminiRequest(request, effectiveModel)
	changes := alternation.RequestChanges("Gemini")
	for messageIndex, message := range prepared.ConversationMessages() {
		changes = append(changes, describeGeminiMessageChanges(message, messageIndex, prepared)...)
	}
	return changes
}

// describeGeminiMessageChanges mirrors the content branches in
// convertLLMToGeminiRequest. It intentionally reports only losses that the
// generic planner cannot infer from the part type alone (for example, an
// image_url whose value is an external URL rather than a base64 data URL).
func describeGeminiMessageChanges(message model.Message, messageIndex int, request *model.InternalLLMRequest) []model.RequestTransformationChange {
	changes := make([]model.RequestTransformationChange, 0)
	role := strings.ToLower(strings.TrimSpace(message.Role))
	if role == "" {
		role = "user"
	} else if role == "model" {
		role = "assistant"
	}
	if role != "system" && role != "developer" && role != "user" && role != "assistant" && role != "tool" {
		return []model.RequestTransformationChange{geminiChange(
			fmt.Sprintf("messages[%d]", messageIndex),
			model.RequestTransformationDrop,
			fmt.Sprintf("Gemini drops messages with unsupported role %q", message.Role),
		)}
	}
	for partIndex, part := range message.Content.MultipleContent {
		field := fmt.Sprintf("messages[%d].content[%d]", messageIndex, partIndex)
		typ := strings.ToLower(strings.TrimSpace(part.Type))

		switch role {
		case "system", "developer", "assistant":
			// Gemini's systemInstruction and assistant conversion paths only
			// consume the scalar Content field. Structured parts are silently
			// ignored; report the supported-looking types here so the planner
			// does not claim they survive the wire conversion.
			switch typ {
			case "", "text", "image_url", "input_audio", "file", "document":
				changes = append(changes, geminiChange(field, model.RequestTransformationDrop,
					fmt.Sprintf("Gemini drops structured %s content in %s messages", typOrText(typ), role)))
			}
		case "user":
			switch typ {
			case "image_url":
				_, partChanges, _ := planGeminiImageURLConversion(part.ImageURL, field)
				changes = append(changes, partChanges...)
			case "input_audio":
				if part.Audio == nil {
					changes = append(changes, geminiChange(field, model.RequestTransformationDrop,
						"Gemini drops an input_audio content part with no audio source"))
				}
			case "file":
				_, partChanges, _ := planGeminiFileConversion(part.File, field)
				changes = append(changes, partChanges...)
			case "document":
				_, partChanges, _ := planGeminiDocumentConversion(part.Document, request, field)
				changes = append(changes, partChanges...)
			case "":
				changes = append(changes, geminiChange(field, model.RequestTransformationDrop,
					"Gemini drops an untyped content part"))
			}
		case "tool":
			// Tool messages are converted through functionResponse and their
			// structured content is flattened by the tool-result builder.
		}
	}
	return changes
}

func typOrText(typ string) string {
	if typ == "" {
		return "untyped"
	}
	return typ
}

func geminiChange(field string, action model.RequestTransformationAction, reason string) model.RequestTransformationChange {
	return model.RequestTransformationChange{Field: field, Action: action, Reason: reason}
}

// planGeminiImageURLConversion is the single source of truth for image_url
// handling in both the planner and the wire builder. Gemini generateContent
// accepts inlineData, not arbitrary OpenAI-style image URLs.
func planGeminiImageURLConversion(image *model.ImageURL, field string) (*model.GeminiPart, []model.RequestTransformationChange, string) {
	if image == nil || strings.TrimSpace(image.URL) == "" {
		return nil, []model.RequestTransformationChange{geminiChange(field, model.RequestTransformationDrop,
			"Gemini drops an image_url content part with no image source")}, "Gemini drops an image_url content part with no image source"
	}
	dataURL := dataurl.ParseDataURL(image.URL)
	if dataURL == nil || !dataURL.IsBase64 {
		return nil, []model.RequestTransformationChange{geminiChange(field, model.RequestTransformationDrop,
			"Gemini only represents image_url content as a base64 data URL and drops external or non-base64 URLs")}, "Gemini only represents image_url content as a base64 data URL and drops external or non-base64 URLs"
	}
	if strings.TrimSpace(dataURL.Data) == "" {
		return nil, []model.RequestTransformationChange{geminiChange(field, model.RequestTransformationDrop,
			"Gemini drops an image_url content part with an empty data URL payload")}, "Gemini drops an image_url content part with an empty data URL payload"
	}
	return &model.GeminiPart{InlineData: &model.GeminiBlob{
		MimeType: dataURL.MediaType,
		Data:     dataURL.Data,
	}}, nil, ""
}

// planGeminiFileConversion is the single source of truth for OpenAI-style
// file parts. File IDs and arbitrary file URLs belong to OpenAI's file store
// and cannot be dereferenced by Gemini's generateContent endpoint.
func planGeminiFileConversion(file *model.File, field string) (*model.GeminiPart, []model.RequestTransformationChange, string) {
	if file == nil {
		return nil, []model.RequestTransformationChange{geminiChange(field, model.RequestTransformationDrop,
			"Gemini drops a file content part with no file source")}, "Gemini drops a file content part with no file source"
	}
	if strings.TrimSpace(file.FileData) == "" {
		return nil, []model.RequestTransformationChange{geminiChange(field, model.RequestTransformationDrop,
			"Gemini cannot dereference OpenAI file_id or file_url references and drops the file content part")}, "Gemini cannot dereference OpenAI file_id or file_url references and drops the file content part"
	}
	dataURL := dataurl.ParseDataURL(file.FileData)
	if dataURL == nil || !dataURL.IsBase64 {
		return nil, []model.RequestTransformationChange{geminiChange(field, model.RequestTransformationDrop,
			"Gemini only represents file content as a base64 data URL and drops non-base64 file data")}, "Gemini only represents file content as a base64 data URL and drops non-base64 file data"
	}
	if strings.TrimSpace(dataURL.Data) == "" {
		return nil, []model.RequestTransformationChange{geminiChange(field, model.RequestTransformationDrop,
			"Gemini drops a file content part with an empty data URL payload")}, "Gemini drops a file content part with an empty data URL payload"
	}
	return &model.GeminiPart{InlineData: &model.GeminiBlob{
		MimeType: dataURL.MediaType,
		Data:     dataURL.Data,
	}}, nil, ""
}

func planGeminiDocumentConversion(doc *model.DocumentSource, req *model.InternalLLMRequest, field string) (*model.GeminiPart, []model.RequestTransformationChange, string) {
	change := func(action model.RequestTransformationAction, reason string) []model.RequestTransformationChange {
		if strings.TrimSpace(field) == "" {
			return nil
		}
		return []model.RequestTransformationChange{{Field: field, Action: action, Reason: reason}}
	}
	metadataChanges := func() []model.RequestTransformationChange {
		if doc == nil || (doc.Title == "" && doc.Context == "" && doc.Citations == nil) {
			return nil
		}
		return change(model.RequestTransformationDrop, "Gemini document parts do not preserve document title, context, or citation controls")
	}
	if doc == nil {
		return nil, change(model.RequestTransformationDrop, "Gemini drops a document content part with no document source"), ""
	}

	switch doc.Type {
	case "base64":
		if doc.Data == "" {
			return nil, change(model.RequestTransformationDrop, "Gemini drops an empty base64 document source"), ""
		}
		mime := doc.MediaType
		if mime == "" {
			mime = "application/pdf"
		}
		decoded := (len(doc.Data) * 3) / 4
		if decoded > geminiInlineDataMaxBytes {
			if uri := lookupGeminiFilesAPIURI(req, mime); uri != "" {
				warning := fmt.Sprintf("gemini: inline document ~%d bytes exceeds %d; forwarding via fileData(%q)", decoded, geminiInlineDataMaxBytes, uri)
				return &model.GeminiPart{FileData: &model.GeminiFileData{MimeType: mime, FileURI: uri}}, metadataChanges(), warning
			}
			reason := fmt.Sprintf("Gemini drops an inline document of approximately %d decoded bytes because it exceeds the %d-byte limit and no Files API URI is configured", decoded, geminiInlineDataMaxBytes)
			warning := fmt.Sprintf("gemini: dropping inline document (~%d bytes, mime=%q) - exceeds %d-byte inline limit and no gemini_files_api_uri provided", decoded, mime, geminiInlineDataMaxBytes)
			return nil, change(model.RequestTransformationDrop, reason), warning
		}
		return &model.GeminiPart{InlineData: &model.GeminiBlob{MimeType: mime, Data: doc.Data}}, metadataChanges(), ""

	case "url":
		if doc.URL == "" {
			return nil, change(model.RequestTransformationDrop, "Gemini drops an empty document URL source"), ""
		}
		reason := "Gemini converts an arbitrary document URL to a text hint and cannot preserve native document or citation semantics"
		return &model.GeminiPart{Text: buildDocumentTextHint(doc, "document at "+doc.URL)}, change(model.RequestTransformationTranslate, reason), ""

	case "text":
		text := doc.Text
		if text == "" {
			text = doc.Data
		}
		if text == "" {
			return nil, change(model.RequestTransformationDrop, "Gemini drops an empty text document source"), ""
		}
		reason := "Gemini converts a structured text document to plain text and cannot preserve citation controls"
		return &model.GeminiPart{Text: buildDocumentTextHint(doc, text)}, change(model.RequestTransformationTranslate, reason), ""

	case "content":
		if len(doc.Content) == 0 {
			return nil, change(model.RequestTransformationDrop, "Gemini drops an empty opaque document content source"), ""
		}
		return nil, change(model.RequestTransformationDrop, "Gemini cannot represent an opaque Anthropic document content array"), ""

	default:
		return nil, change(model.RequestTransformationDrop, fmt.Sprintf("Gemini cannot represent document source type %q", doc.Type)), ""
	}
}
