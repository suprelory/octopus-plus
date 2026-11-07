package gemini

import (
	"reflect"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/config"
	"github.com/suprelory/octopus-plus/polywire/internal/testlog"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestLogGeminiSignatureAuditExtract(t *testing.T) {
	recorded := &testlog.Logger{}
	adapter := New(config.Config{Logger: recorded})
	blocks := []model.ReasoningBlock{
		{Kind: model.ReasoningBlockKindThinking, Text: "reasoning", Signature: "sigA", Provider: "gemini"},
		{Kind: model.ReasoningBlockKindSignature, Signature: "sigB", Provider: "gemini"},
	}
	adapter.logGeminiSignatureAudit("extract", blocks)
	entries := recorded.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.Message != "transformer.reasoning.signature.passthrough" || entry.Level != "debug" {
		t.Fatalf("unexpected diagnostic: %+v", entry)
	}
	want := map[string]any{
		"provider": "gemini", "direction": "extract", "thinking_count": 1, "signature_count": 2,
	}
	if !reflect.DeepEqual(entry.Fields, want) {
		t.Fatalf("fields = %#v, want %#v", entry.Fields, want)
	}
}

func TestLogGeminiSignatureAuditNoopOnEmpty(t *testing.T) {
	recorded := &testlog.Logger{}
	adapter := New(config.Config{Logger: recorded})
	adapter.logGeminiSignatureAudit("extract", nil)
	adapter.logGeminiSignatureAudit("extract", []model.ReasoningBlock{
		{Kind: model.ReasoningBlockKindSignature, Signature: ""},
	})
	if count := len(recorded.Entries()); count != 0 {
		t.Fatalf("expected zero log entries, got %d", count)
	}
}
