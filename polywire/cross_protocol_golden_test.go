package polywire_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	anthropicInbound "github.com/suprelory/octopus-plus/polywire/inbound/anthropic"
	openaiInbound "github.com/suprelory/octopus-plus/polywire/inbound/openai"
	"github.com/suprelory/octopus-plus/polywire/model"
	anthropicOutbound "github.com/suprelory/octopus-plus/polywire/outbound/anthropic"
	geminiOutbound "github.com/suprelory/octopus-plus/polywire/outbound/gemini"
	openaiOutbound "github.com/suprelory/octopus-plus/polywire/outbound/openai"
)

func TestCrossProtocolRequestGolden(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		inbound  model.Inbound
		outbound model.Outbound
		baseURL  string
	}{
		{
			name:     "openai_chat_to_anthropic",
			body:     `{"model":"client-model","messages":[{"role":"system","content":"Be concise."},{"role":"user","content":"Hello"}],"temperature":0.3,"max_tokens":64,"stream":false}`,
			inbound:  &openaiInbound.ChatInbound{},
			outbound: &anthropicOutbound.MessageOutbound{},
			baseURL:  "https://api.anthropic.com",
		},
		{
			name:     "openai_chat_to_openai_chat",
			body:     `{"model":"client-model","messages":[{"role":"system","content":"Be concise."},{"role":"user","content":"Hello"}],"temperature":0.3,"max_tokens":64,"stream":false}`,
			inbound:  &openaiInbound.ChatInbound{},
			outbound: &openaiOutbound.ChatOutbound{},
			baseURL:  "https://api.openai.com/v1",
		},
		{
			name:     "openai_chat_to_openai_responses",
			body:     `{"model":"client-model","messages":[{"role":"system","content":"Be concise."},{"role":"user","content":"Hello"}],"temperature":0.3,"max_tokens":64,"stream":false}`,
			inbound:  &openaiInbound.ChatInbound{},
			outbound: &openaiOutbound.ResponseOutbound{},
			baseURL:  "https://api.openai.com/v1",
		},
		{
			name:     "openai_chat_to_gemini",
			body:     `{"model":"client-model","messages":[{"role":"system","content":"Be concise."},{"role":"user","content":"Hello"}],"temperature":0.3,"max_tokens":64,"stream":false}`,
			inbound:  &openaiInbound.ChatInbound{},
			outbound: &geminiOutbound.MessagesOutbound{},
			baseURL:  "https://generativelanguage.googleapis.com/v1beta",
		},
		{
			name:     "anthropic_to_openai_chat",
			body:     `{"model":"client-model","max_tokens":64,"system":"Be concise.","messages":[{"role":"user","content":"Hello"}],"temperature":0.3,"stream":false}`,
			inbound:  &anthropicInbound.MessagesInbound{},
			outbound: &openaiOutbound.ChatOutbound{},
			baseURL:  "https://api.openai.com/v1",
		},
		{
			name:     "anthropic_to_openai_responses",
			body:     `{"model":"client-model","max_tokens":64,"system":"Be concise.","messages":[{"role":"user","content":"Hello"}],"temperature":0.3,"stream":false}`,
			inbound:  &anthropicInbound.MessagesInbound{},
			outbound: &openaiOutbound.ResponseOutbound{},
			baseURL:  "https://api.openai.com/v1",
		},
		{
			name:     "anthropic_to_anthropic",
			body:     `{"model":"client-model","max_tokens":64,"system":"Be concise.","messages":[{"role":"user","content":"Hello"}],"temperature":0.3,"stream":false}`,
			inbound:  &anthropicInbound.MessagesInbound{},
			outbound: &anthropicOutbound.MessageOutbound{},
			baseURL:  "https://api.anthropic.com",
		},
		{
			name:     "anthropic_to_gemini",
			body:     `{"model":"client-model","max_tokens":64,"system":"Be concise.","messages":[{"role":"user","content":"Hello"}],"temperature":0.3,"stream":false}`,
			inbound:  &anthropicInbound.MessagesInbound{},
			outbound: &geminiOutbound.MessagesOutbound{},
			baseURL:  "https://generativelanguage.googleapis.com/v1beta",
		},
		{
			name:     "openai_responses_to_gemini",
			body:     `{"model":"client-model","input":"Hello from Responses","instructions":"Be concise.","temperature":0.3,"max_output_tokens":64,"stream":false}`,
			inbound:  &openaiInbound.ResponseInbound{},
			outbound: &geminiOutbound.MessagesOutbound{},
			baseURL:  "https://generativelanguage.googleapis.com/v1beta",
		},
		{
			name:     "openai_responses_to_openai_chat",
			body:     `{"model":"client-model","input":"Hello from Responses","instructions":"Be concise.","temperature":0.3,"max_output_tokens":64,"stream":false}`,
			inbound:  &openaiInbound.ResponseInbound{},
			outbound: &openaiOutbound.ChatOutbound{},
			baseURL:  "https://api.openai.com/v1",
		},
		{
			name:     "openai_responses_to_openai_responses",
			body:     `{"model":"client-model","input":"Hello from Responses","instructions":"Be concise.","temperature":0.3,"max_output_tokens":64,"stream":false}`,
			inbound:  &openaiInbound.ResponseInbound{},
			outbound: &openaiOutbound.ResponseOutbound{},
			baseURL:  "https://api.openai.com/v1",
		},
		{
			name:     "openai_responses_to_anthropic",
			body:     `{"model":"client-model","input":"Hello from Responses","instructions":"Be concise.","temperature":0.3,"max_output_tokens":64,"stream":false}`,
			inbound:  &openaiInbound.ResponseInbound{},
			outbound: &anthropicOutbound.MessageOutbound{},
			baseURL:  "https://api.anthropic.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			request, err := tt.inbound.TransformRequest(ctx, []byte(tt.body))
			if err != nil {
				t.Fatalf("TransformRequest inbound: %v", err)
			}
			if err := request.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			request.Model = "upstream-model"
			httpRequest, err := tt.outbound.TransformRequest(ctx, request, tt.baseURL, "test-key")
			if err != nil {
				t.Fatalf("TransformRequest outbound: %v", err)
			}
			defer httpRequest.Body.Close()
			actual, err := io.ReadAll(httpRequest.Body)
			if err != nil {
				t.Fatalf("read outbound body: %v", err)
			}
			assertJSONGolden(t, tt.name+".golden.json", actual)
		})
	}
}

func assertJSONGolden(t *testing.T, name string, actual []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	actual = canonicalJSON(t, actual)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		actual = append(actual, '\n')
		if err := os.WriteFile(path, actual, 0o644); err != nil {
			t.Fatalf("update golden %s: %v", path, err)
		}
		return
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	expected = canonicalJSON(t, expected)
	if string(actual) != string(expected) {
		t.Fatalf("golden mismatch for %s\nactual:\n%s\nexpected:\n%s", name, actual, expected)
	}
}

func canonicalJSON(t *testing.T, data []byte) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("invalid JSON %q: %v", data, err)
	}
	result, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("canonicalize JSON: %v", err)
	}
	return result
}
