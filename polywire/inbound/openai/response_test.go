package openai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/samber/lo"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestConvertToInternalRequestPreservesRawInputItems(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: ResponsesInput{Items: []ResponsesItem{
			{Type: "input_text", Text: stringPtr("hello")},
		}},
	}

	internalReq, err := convertToInternalRequest(req)
	if err != nil {
		t.Fatalf("convertToInternalRequest failed: %v", err)
	}
	if len(internalReq.RawInputItems) == 0 {
		t.Fatalf("expected raw input items to be preserved")
	}

	var items []map[string]any
	if err := json.Unmarshal(internalReq.RawInputItems, &items); err != nil {
		t.Fatalf("unmarshal raw input items failed: %v", err)
	}
	if len(items) != 1 || items[0]["type"] != "input_text" {
		t.Fatalf("expected original raw input items to be kept, got %#v", items)
	}
	if internalReq.TransformOptions.ArrayInputs == nil || !*internalReq.TransformOptions.ArrayInputs {
		t.Fatalf("expected array input flag to stay true")
	}
}

func TestResponseInboundPreservesNativeRecoverySidecars(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5",
		"input":[{"type":"apply_patch_call_output","call_id":"call_1","output":"ok","native_meta":{"keep":true}}],
		"tools":[{"type":"apply_patch","name":"patch","native_config":{"keep":true}}]
	}`)

	request, err := (&ResponseInbound{}).TransformRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("transform native Responses request: %v", err)
	}
	if !request.HasOpenAIResponsesPassthrough() {
		t.Fatal("native Responses request must retain its passthrough requirement")
	}
	if !strings.Contains(string(request.OpenAIRawInputItems()), "native_meta") {
		t.Fatalf("raw native input fields were lost: %s", request.OpenAIRawInputItems())
	}
	if rawTools := request.GetOpenAIResponsesOptions().RawTools; !strings.Contains(string(rawTools), "native_config") {
		t.Fatalf("raw native tool fields were lost: %s", rawTools)
	}
}

func TestConvertToInternalRequestMarksPassthroughForUnsupportedToolType(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: ResponsesInput{Text: stringPtr("hello")},
		Tools: []ResponsesTool{{
			Type: "apply_patch",
		}},
	}

	internalReq, err := convertToInternalRequest(req)
	if err != nil {
		t.Fatalf("convertToInternalRequest failed: %v", err)
	}
	if !internalReq.HasOpenAIResponsesPassthrough() {
		t.Fatalf("expected unsupported responses tool to require passthrough")
	}
	if ext := internalReq.GetOpenAIExtensions(); !ext.ResponsesPassthroughRequired || ext.ResponsesPassthroughReason != "tool:apply_patch" {
		t.Fatalf("expected OpenAI extension passthrough view, got %#v", ext)
	}
}

func TestConvertToInternalRequestMarksPassthroughForUnsupportedInputItem(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: ResponsesInput{Items: []ResponsesItem{{
			Type:   "apply_patch_call_output",
			CallID: "apc_123",
		}}},
	}

	internalReq, err := convertToInternalRequest(req)
	if err != nil {
		t.Fatalf("convertToInternalRequest failed: %v", err)
	}
	if !internalReq.HasOpenAIResponsesPassthrough() {
		t.Fatalf("expected unsupported responses input item to require passthrough")
	}
	if ext := internalReq.GetOpenAIExtensions(); !ext.ResponsesPassthroughRequired || ext.ResponsesPassthroughReason != "input:apply_patch_call_output" {
		t.Fatalf("expected OpenAI extension passthrough view, got %#v", ext)
	}
}

func TestConvertToInternalRequestDoesNotMarkPassthroughForSupportedFileAndAudioInputs(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: ResponsesInput{Items: []ResponsesItem{
			{
				Type: "message",
				Role: "user",
				Content: &ResponsesInput{Items: []ResponsesItem{
					{Type: "input_file", FileID: stringPtr("file_123")},
					{Type: "input_audio", InputAudio: &ResponsesInputAudio{Format: "wav", Data: "AAA="}},
				}},
			},
		}},
	}

	internalReq, err := convertToInternalRequest(req)
	if err != nil {
		t.Fatalf("convertToInternalRequest failed: %v", err)
	}
	if internalReq.HasOpenAIResponsesPassthrough() {
		t.Fatalf("expected supported file/audio inputs to stay normalized without passthrough")
	}
	if len(internalReq.Messages) != 1 || len(internalReq.Messages[0].Content.MultipleContent) != 2 {
		t.Fatalf("expected supported file/audio inputs to normalize into message content, got %#v", internalReq.Messages)
	}
	if internalReq.Messages[0].Content.MultipleContent[0].Type != "file" {
		t.Fatalf("expected file content part, got %#v", internalReq.Messages[0].Content.MultipleContent[0])
	}
	if internalReq.Messages[0].Content.MultipleContent[1].Type != "input_audio" {
		t.Fatalf("expected input_audio content part, got %#v", internalReq.Messages[0].Content.MultipleContent[1])
	}
}

func TestConvertToInternalRequestNormalizesTopLevelInputFile(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: ResponsesInput{Items: []ResponsesItem{{
			Type:     "input_file",
			FileID:   stringPtr("file_456"),
			Filename: stringPtr("notes.txt"),
		}}},
	}

	internalReq, err := convertToInternalRequest(req)
	if err != nil {
		t.Fatalf("convertToInternalRequest failed: %v", err)
	}
	if internalReq.HasOpenAIResponsesPassthrough() {
		t.Fatalf("expected top-level input_file to stay normalized without passthrough")
	}
	if len(internalReq.Messages) != 1 {
		t.Fatalf("expected one normalized message, got %#v", internalReq.Messages)
	}
	if internalReq.Messages[0].Role != "user" {
		t.Fatalf("expected top-level input_file to default to user role, got %#v", internalReq.Messages[0].Role)
	}
	if len(internalReq.Messages[0].Content.MultipleContent) != 1 || internalReq.Messages[0].Content.MultipleContent[0].Type != "file" {
		t.Fatalf("expected top-level input_file to become file content, got %#v", internalReq.Messages[0].Content)
	}
	if internalReq.Messages[0].Content.MultipleContent[0].File == nil || internalReq.Messages[0].Content.MultipleContent[0].File.FileID != "file_456" {
		t.Fatalf("expected normalized file reference to preserve file_id, got %#v", internalReq.Messages[0].Content.MultipleContent[0].File)
	}
}

func TestConvertInputToMessagesMergesConsecutiveFunctionCalls(t *testing.T) {
	messages, err := convertInputToMessages(&ResponsesInput{Items: []ResponsesItem{
		{Type: "function_call", CallID: "call_1", Name: "lookup", Namespace: "tools", Arguments: `{"q":"one"}`},
		{Type: "function_call", CallID: "call_2", Name: "weather", Arguments: `{"city":"two"}`},
		{Type: "function_call_output", CallID: "call_1", Output: &ResponsesInput{Text: lo.ToPtr("one")}},
		{Type: "function_call_output", CallID: "call_2", Output: &ResponsesInput{Text: lo.ToPtr("two")}},
	}})
	if err != nil {
		t.Fatalf("convertInputToMessages: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("message count = %d, want assistant plus two tool results: %+v", len(messages), messages)
	}
	if messages[0].Role != "assistant" || len(messages[0].ToolCalls) != 2 {
		t.Fatalf("consecutive calls were not merged: %+v", messages[0])
	}
	if messages[0].ToolCalls[0].Function.Namespace != "tools" {
		t.Fatalf("namespace lost during conversion: %+v", messages[0].ToolCalls[0])
	}
	if messages[1].Role != "tool" || messages[2].Role != "tool" {
		t.Fatalf("tool results lost ordering: %+v", messages)
	}
}

func TestConvertItemToMessageUsesReasoningTextContent(t *testing.T) {
	msg, err := convertItemToMessage(&ResponsesItem{
		Type:    "reasoning",
		Summary: []ResponsesReasoningSummary{{Type: "summary_text", Text: "summary"}},
		Content: &ResponsesInput{Items: []ResponsesItem{{
			Type: "reasoning_text",
			Text: lo.ToPtr("full reasoning"),
		}}},
	})
	if err != nil {
		t.Fatalf("convertItemToMessage: %v", err)
	}
	if msg == nil || msg.ReasoningContent == nil || *msg.ReasoningContent != "full reasoning" {
		t.Fatalf("reasoning content = %+v, want reasoning_text", msg)
	}
}

func TestConvertItemToMessageTagsEncryptedContentAsOpenAI(t *testing.T) {
	signature := "encrypted"
	msg, err := convertItemToMessage(&ResponsesItem{
		Type:             "reasoning",
		EncryptedContent: &signature,
	})
	if err != nil {
		t.Fatalf("convertItemToMessage() error = %v", err)
	}
	if msg == nil || msg.ReasoningSignatureSource == nil || !msg.ReasoningSignatureSource.ValidFor(model.SignatureProviderOpenAI) {
		t.Fatalf("missing OpenAI signature provenance: %+v", msg)
	}
}

func TestConvertToResponsesAPIResponseFiltersSignatureProvenance(t *testing.T) {
	tests := []struct {
		name      string
		message   *model.Message
		expected  string
		wantEmpty bool
	}{
		{
			name: "openai",
			message: func() *model.Message {
				msg := &model.Message{ReasoningContent: lo.ToPtr("summary")}
				msg.SetOpaqueReasoningSignature(model.OpaqueSignature{Provider: model.SignatureProviderOpenAI, Kind: model.OpaqueSignatureKindOpenAIReasoning, Value: "openai-signature"})
				return msg
			}(),
			expected: "openai-signature",
		},
		{
			name: "foreign",
			message: func() *model.Message {
				msg := &model.Message{ReasoningContent: lo.ToPtr("summary")}
				msg.SetOpaqueReasoningSignature(model.OpaqueSignature{Provider: model.SignatureProviderAnthropic, Kind: model.OpaqueSignatureKindAnthropicThinking, Value: "anthropic-signature"})
				return msg
			}(),
			wantEmpty: true,
		},
		{
			name: "legacy",
			message: &model.Message{
				ReasoningContent:   lo.ToPtr("summary"),
				ReasoningSignature: lo.ToPtr("legacy-signature"),
			},
			expected: "legacy-signature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := convertToResponsesAPIResponse(&model.InternalLLMResponse{Choices: []model.Choice{{Message: tt.message}}})
			if len(response.Output) == 0 || response.Output[0].Type != "reasoning" {
				t.Fatalf("reasoning output missing: %+v", response.Output)
			}
			got := response.Output[0].EncryptedContent
			if tt.wantEmpty {
				if got != nil {
					t.Fatalf("foreign encrypted_content = %q, want nil", *got)
				}
				return
			}
			if got == nil || *got != tt.expected {
				t.Fatalf("encrypted_content = %v, want %q", got, tt.expected)
			}
		})
	}
}

func TestConvertToResponsesAPIResponseKeepsMultipleSignaturesOpaque(t *testing.T) {
	first := model.ReasoningBlock{Kind: model.ReasoningBlockKindSignature, Index: 0}
	first.SetOpaqueSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderOpenAI,
		Kind:     model.OpaqueSignatureKindOpenAIReasoning,
		Value:    "signature-one",
	})
	second := model.ReasoningBlock{Kind: model.ReasoningBlockKindSignature, Index: 1}
	second.SetOpaqueSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderOpenAI,
		Kind:     model.OpaqueSignatureKindOpenAIReasoning,
		Value:    "signature-two",
	})
	response := convertToResponsesAPIResponse(&model.InternalLLMResponse{Choices: []model.Choice{{Message: &model.Message{
		ReasoningBlocks: []model.ReasoningBlock{first, second},
	}}}})

	if len(response.Output) != 2 || response.Output[0].EncryptedContent == nil || response.Output[1].EncryptedContent == nil {
		t.Fatalf("expected two reasoning items: %+v", response.Output)
	}
	if *response.Output[0].EncryptedContent != "signature-one" || *response.Output[1].EncryptedContent != "signature-two" {
		t.Fatalf("opaque signatures were altered: %+v", response.Output)
	}
}

func TestConvertToResponsesAPIResponseKeepsReasoningAssociationsAndDuplicates(t *testing.T) {
	first := model.ReasoningBlock{Kind: model.ReasoningBlockKindThinking, Index: 0, Text: "summary-one"}
	first.SetOpaqueSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderOpenAI,
		Kind:     model.OpaqueSignatureKindOpenAIReasoning,
		Value:    "repeated-signature",
	})
	second := model.ReasoningBlock{Kind: model.ReasoningBlockKindThinking, Index: 1, Text: "summary-two"}
	second.SetOpaqueSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderOpenAI,
		Kind:     model.OpaqueSignatureKindOpenAIReasoning,
		Value:    "repeated-signature",
	})
	response := convertToResponsesAPIResponse(&model.InternalLLMResponse{Choices: []model.Choice{{Message: &model.Message{
		ReasoningBlocks: []model.ReasoningBlock{first, second},
	}}}})

	if len(response.Output) != 2 {
		t.Fatalf("reasoning items = %d, want 2: %+v", len(response.Output), response.Output)
	}
	for index, summary := range []string{"summary-one", "summary-two"} {
		item := response.Output[index]
		if len(item.Summary) != 1 || item.Summary[0].Text != summary {
			t.Fatalf("item %d summary association lost: %+v", index, item)
		}
		if item.EncryptedContent == nil || *item.EncryptedContent != "repeated-signature" {
			t.Fatalf("item %d duplicate opaque signature lost: %+v", index, item)
		}
	}
}

func TestConvertReasoningInputPreservesSummaryOnlyBlock(t *testing.T) {
	message, err := convertItemToMessage(&ResponsesItem{
		Type:    "reasoning",
		Summary: []ResponsesReasoningSummary{{Type: "summary_text", Text: "summary-only"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if message == nil || len(message.ReasoningBlocks) != 1 || message.ReasoningBlocks[0].Text != "summary-only" {
		t.Fatalf("summary-only reasoning block was lost: %+v", message)
	}
}

func TestConvertToolChoicePreservesMissingNameForCapabilityPlanning(t *testing.T) {
	typeFunction := "function"
	choice := convertToolChoiceToInternal(&ResponsesToolChoice{Type: &typeFunction})
	if choice == nil || choice.NamedToolChoice == nil {
		t.Fatalf("missing-name tool choice was discarded: %#v", choice)
	}
	if choice.NamedToolChoice.Type != typeFunction || choice.NamedToolChoice.ResolvedFunctionName() != "" {
		t.Fatalf("unexpected preserved tool choice: %#v", choice.NamedToolChoice)
	}
}

func stringPtr(value string) *string {
	return &value
}
