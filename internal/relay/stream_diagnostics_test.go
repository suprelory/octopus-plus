package relay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/internal/transformer"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

type diagnosticsErrorReader struct{}

func (diagnosticsErrorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestRelayStreamDiagnosticsDistinguishEOFAndTerminal(t *testing.T) {
	const delta = "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"
	for _, tt := range []struct {
		name     string
		tail     string
		broken   bool
		complete bool
		reason   bool
		events   int64
	}{
		{"explicit done", "data: [DONE]\n\n", false, true, false, 2},
		{"usage tail", "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"total_tokens\":3}}\n\ndata: [DONE]\n\n", false, true, true, 4},
		{"missing terminal", "", false, false, false, 1},
		{"transport interruption", "", true, false, false, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := &model.InternalLLMRequest{Model: "m", Stream: p1Bool(true)}
			ra := &relayAttempt{relayRequest: &relayRequest{ctx: context.Background(), internalRequest: request, inAdapter: transformer.Inbound(inbound.InboundTypeOpenAIChat), streamWriter: &notifyStreamWriter{header: http.Header{}}, metrics: NewRelayMetrics(1, "m", "chat", "", nil, request)}, outAdapter: transformer.Outbound(outbound.OutboundTypeOpenAIChat)}
			var reader io.Reader = strings.NewReader(delta + tt.tail)
			if tt.broken {
				reader = io.MultiReader(reader, diagnosticsErrorReader{})
			}
			err := ra.handleStreamResponseV2(context.Background(), &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(reader)})
			if (err == nil) != tt.complete {
				t.Fatalf("stream error = %v", err)
			}
			d := ra.streamDiagnostics
			// Explicit terminals now finish before transport EOF is consumed.
			wantEOF := !tt.broken && !tt.complete
			if d == nil || d.EventsReceived != tt.events || d.BytesReceived == 0 || d.LastSourceSequence != tt.events || d.SourceTransport != model.SourceTransportSSE || d.CleanEOF != wantEOF || d.TerminalEventSeen != tt.complete || d.FinishReasonSeen != tt.reason {
				t.Fatalf("diagnostics = %+v", d)
			}
			if tt.complete {
				if d.CompletionStatus != "completed" || d.FinishCause != model.StreamFinishCauseExplicitTerminal || d.LastEventType != "[DONE]" {
					t.Fatalf("completion = %+v", d)
				}
			} else {
				if d.CompletionStatus != "interrupted" {
					t.Fatalf("interruption = %+v", d)
				}
				if !tt.broken && !errors.Is(err, model.ErrStreamIncomplete) {
					t.Fatalf("unclassified EOF = %v", err)
				}
				failure := classifyRelayFailure(200, err, ra.retryAt)
				if !failure.Retryable || failure.Class != FailureTransient {
					t.Fatalf("interruption must remain retryable: %+v", failure)
				}
			}
		})
	}
}
