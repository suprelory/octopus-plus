package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestRelayTerminalCompletesWithoutTransportEOF(t *testing.T) {
	for _, protocol := range []struct {
		name   string
		in     inbound.InboundType
		out    outbound.OutboundType
		format model.APIFormat
		frames []string
	}{
		{"chat", inbound.InboundTypeOpenAIChat, outbound.OutboundTypeOpenAIChat, model.APIFormatOpenAIChatCompletion, []string{
			`{"id":"chat_1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[{"index":1,"delta":{"content":"second"}}]}`,
			`{"choices":[{"index":1,"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
			`[DONE]`,
		}},
		{"responses", inbound.InboundTypeOpenAIResponse, outbound.OutboundTypeOpenAIResponse, model.APIFormatOpenAIResponse, []string{
			`{"type":"response.created","response":{"id":"resp_1","model":"gpt-4o","status":"in_progress","output":[]}}`,
			`{"type":"response.output_text.delta","delta":"hello"}`,
			`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-4o","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
		}},
		{"anthropic", inbound.InboundTypeAnthropic, outbound.OutboundTypeAnthropic, model.APIFormatAnthropicMessage, []string{
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"gpt-4o","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		}},
	} {
		for _, passthrough := range []bool{false, true} {
			if protocol.out == outbound.OutboundTypeOpenAIChat && passthrough {
				continue
			}
			t.Run(fmt.Sprintf("%s/passthrough=%t", protocol.name, passthrough), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				upstreamClosed := make(chan struct{})
				wire := "data: " + strings.Join(protocol.frames, "\n\ndata: ") + "\n\n"
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(upstreamClosed)
					w.Header().Set("Content-Type", "text/event-stream")
					for _, frame := range protocol.frames {
						if _, err := fmt.Fprintf(w, "data: %s\n\n", frame); err != nil {
							return
						}
						w.(http.Flusher).Flush()
					}
					<-r.Context().Done()
				}))
				defer upstream.Close()
				request, _ := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
				response, err := upstream.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				ra, recorder := newEmptyStreamTestAttempt(t, protocol.in, protocol.format, protocol.out)
				ra.channel = &dbmodel.Channel{Type: protocol.out}
				if passthrough {
					cfg := ra.outAdapter.(model.PassthroughCapable).PassthroughConfig()
					err = ra.handleStreamResponsePassthroughV2(ctx, response, cfg)
				} else {
					err = ra.handleStreamResponseV2(ctx, response)
				}
				if err != nil || ctx.Err() != nil {
					t.Fatalf("terminal stream waited for transport: err=%v ctx=%v", err, ctx.Err())
				}
				select {
				case <-upstreamClosed:
				case <-ctx.Done():
					t.Fatal("upstream body remained open after terminal")
				}
				if ra.metrics.InternalResponse == nil || ra.metrics.Stats.InputToken != 3 || ra.metrics.Stats.OutputToken != 2 {
					t.Fatalf("terminal usage not collected exactly once: %+v", ra.metrics.Stats)
				}
				if passthrough && recorder.Body.String() != wire {
					t.Fatal("passthrough lost native frames")
				}
				if protocol.name == "chat" && !strings.Contains(recorder.Body.String(), "second") {
					t.Fatal("first choice finish dropped the later choice")
				}
				if ra.streamDiagnostics.CleanEOF || ra.streamDiagnostics.FinishCause != model.StreamFinishCauseExplicitTerminal {
					t.Fatalf("incorrect completion diagnostics: %+v", ra.streamDiagnostics)
				}
			})
		}
	}
}

func TestRelayPassthroughDoesNotCompleteTerminalPreview(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	prefix := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.completed\"}\n"
	output := &notifyStreamWriter{header: http.Header{}}
	output.onWrite = func(p []byte) {
		if strings.Contains(string(p), "response.completed") {
			cancel() // No blank line: cancellation must not promote the preview.
		}
	}
	go func() { _, _ = io.WriteString(writer, prefix) }()
	ra, _ := newOpenAIResponsesPassthroughAttempt(output)
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}
	err := ra.handleStreamResponsePassthroughV2(ctx, response, ra.outAdapter.(model.PassthroughCapable).PassthroughConfig())
	if err == nil || ra.metrics.InternalResponse != nil {
		t.Fatalf("unfinished terminal was treated as successful: %v", err)
	}
}
