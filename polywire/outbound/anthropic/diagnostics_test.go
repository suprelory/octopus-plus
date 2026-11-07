package anthropic

import (
	"reflect"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/config"
	"github.com/suprelory/octopus-plus/polywire/internal/testlog"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestLogAnthropicSignatureAuditInject(t *testing.T) {
	recorded := &testlog.Logger{}
	adapter := New(config.Config{Logger: recorded})
	blocks := []model.ReasoningBlock{
		{Kind: model.ReasoningBlockKindThinking, Text: "hello", Signature: "sigA", Provider: "anthropic"},
		{Kind: model.ReasoningBlockKindRedacted, Data: "REDACTED", Provider: "anthropic"},
		{Kind: model.ReasoningBlockKindSignature, Signature: "sigB", Provider: "anthropic"},
	}
	adapter.logAnthropicSignatureAudit("inject", blocks)
	entries := recorded.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.Message != "transformer.reasoning.signature.passthrough" || entry.Level != "debug" {
		t.Fatalf("unexpected diagnostic: %+v", entry)
	}
	want := map[string]any{
		"provider": "anthropic", "direction": "inject",
		"thinking_count": 1, "redacted_count": 1, "signature_count": 3,
	}
	if !reflect.DeepEqual(entry.Fields, want) {
		t.Fatalf("fields = %#v, want %#v", entry.Fields, want)
	}
}

func TestLogAnthropicSignatureAuditEmpty(t *testing.T) {
	recorded := &testlog.Logger{}
	adapter := New(config.Config{Logger: recorded})
	adapter.logAnthropicSignatureAudit("extract", nil)
	adapter.logAnthropicSignatureAudit("extract", []model.ReasoningBlock{
		{Kind: model.ReasoningBlockKindSignature, Signature: ""},
	})
	if count := len(recorded.Entries()); count != 0 {
		t.Fatalf("expected zero log entries for empty/no-payload blocks, got %d", count)
	}
}
