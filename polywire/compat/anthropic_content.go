package compat

import (
	"encoding/json"

	"github.com/suprelory/octopus-plus/polywire/model"
	anthropic "github.com/suprelory/octopus-plus/polywire/protocol/anthropic"
)

// AnthropicCitationsToModel retains the native payload alongside the fields
// shared by providers. Location zeroes and nullable titles survive replay.
func AnthropicCitationsToModel(citations []anthropic.Citation) []model.Citation {
	if citations == nil {
		return nil
	}
	result := make([]model.Citation, 0, len(citations))
	for _, citation := range citations {
		raw, _ := json.Marshal(citation)
		uri := citation.URL
		if uri == "" {
			uri = citation.Source
		}
		result = append(result, model.Citation{
			Provider: "anthropic", Raw: raw, Type: citation.Type,
			CitedText: citation.CitedText, URI: uri, Title: citation.Title,
			DocumentIndex: citation.DocumentIndex, DocumentTitle: citation.DocumentTitle,
			StartCharIndex: citation.StartCharIndex, EndCharIndex: citation.EndCharIndex,
			StartPageNumber: citation.StartPageNumber, EndPageNumber: citation.EndPageNumber,
			StartBlockIndex: citation.StartBlockIndex, EndBlockIndex: citation.EndBlockIndex,
			SearchResultIndex: citation.SearchResultIndex, EncryptedIndex: citation.EncryptedIndex,
		})
	}
	return result
}

func AnthropicCitationsFromModel(citations []model.Citation) []anthropic.Citation {
	var result []anthropic.Citation
	for _, citation := range citations {
		// Other providers' offsets and URLs are not valid Anthropic locations.
		if citation.Provider != "" && citation.Provider != "anthropic" || citation.Type == "" {
			continue
		}
		converted := anthropic.Citation{
			Type: citation.Type, CitedText: citation.CitedText,
			URL: citation.URI, Title: citation.Title,
			DocumentIndex: citation.DocumentIndex, DocumentTitle: citation.DocumentTitle,
			StartCharIndex: citation.StartCharIndex, EndCharIndex: citation.EndCharIndex,
			StartPageNumber: citation.StartPageNumber, EndPageNumber: citation.EndPageNumber,
			StartBlockIndex: citation.StartBlockIndex, EndBlockIndex: citation.EndBlockIndex,
			SearchResultIndex: citation.SearchResultIndex, EncryptedIndex: citation.EncryptedIndex,
		}
		if citation.Provider == "anthropic" {
			converted.Raw = append(json.RawMessage(nil), citation.Raw...)
		}
		if citation.Type == "search_result_location" {
			converted.Source = citation.URI
			converted.URL = ""
		}
		result = append(result, converted)
	}
	return result
}

func AnthropicServerToolUseToModel(block anthropic.MessageContentBlock) *model.ServerToolUseBlock {
	use := &model.ServerToolUseBlock{
		ID: block.ID, BlockType: block.Type, ServerName: block.ServerName,
		Input:  append(json.RawMessage(nil), block.Input...),
		Caller: append(json.RawMessage(nil), block.Caller...),
	}
	if block.Name != nil {
		use.Name = *block.Name
	}
	return use
}

func AnthropicServerToolUseFromModel(use *model.ServerToolUseBlock) anthropic.MessageContentBlock {
	kind := use.BlockType
	if kind == "" {
		kind = "server_tool_use"
	}
	input := use.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	return anthropic.MessageContentBlock{
		Type: kind, ID: use.ID, Name: &use.Name, ServerName: use.ServerName,
		Input:  append(json.RawMessage(nil), input...),
		Caller: append(json.RawMessage(nil), use.Caller...),
	}
}

func AnthropicServerToolResultToModel(block anthropic.MessageContentBlock) *model.ServerToolResultBlock {
	result := &model.ServerToolResultBlock{
		BlockType: block.Type,
		Content:   append(json.RawMessage(nil), block.RawContent...),
	}
	if block.ToolUseID != nil {
		result.ToolUseID = *block.ToolUseID
	}
	if block.IsError != nil {
		isError := *block.IsError
		result.IsError = &isError
	}
	if len(result.Content) == 0 && block.Content != nil {
		result.Content, _ = json.Marshal(block.Content)
	}
	return result
}

func AnthropicServerToolResultFromModel(result *model.ServerToolResultBlock) anthropic.MessageContentBlock {
	kind := result.BlockType
	if kind == "" {
		kind = "web_search_tool_result"
	}
	return anthropic.MessageContentBlock{
		Type: kind, ToolUseID: &result.ToolUseID, IsError: result.IsError,
		RawContent: append(json.RawMessage(nil), result.Content...),
	}
}
