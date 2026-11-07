package relay

import (
	"context"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestNativePassthroughPreservesUnknownAndMCPFrames(t *testing.T) {
	ra, recorder := newEmptyStreamTestAttempt(t, inbound.InboundTypeOpenAIResponse, model.APIFormatOpenAIResponse, outbound.OutboundTypeOpenAIResponse)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r","model":"m","status":"in_progress"}}`, "",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"mcp_call","id":"mcp-1","name":"lookup","server_label":"server","arguments":"{}","future":null}}`, "",
		`data: {"type":"response.future_native","opaque":{"zero":0}}`, "",
		`data: {"type":"response.completed","response":{"id":"r","model":"m","status":"completed","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`, "", "",
	}, "\n")
	config := ra.outAdapter.(model.PassthroughCapable).PassthroughConfig()
	if err := ra.handleStreamResponsePassthroughV2(context.Background(), sseTestResponse(body), config); err != nil {
		t.Fatal(err)
	}
	if recorder.Body.String() != body {
		t.Fatalf("native frames changed: %s", recorder.Body.String())
	}
	response := ra.ensureStreamConverter().Response()
	if response == nil || response.Usage == nil || response.Usage.TotalTokens != 5 {
		t.Fatalf("native usage lost: %+v", response)
	}
	if ra.streamDiagnostics == nil || ra.streamDiagnostics.CompletionStatus != "completed" || ra.streamDiagnostics.ConversionLoss != nil {
		t.Fatalf("native passthrough reported a conversion loss: %+v", ra.streamDiagnostics)
	}
}
