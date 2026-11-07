package relay

import (
	"context"
	"errors"
	"fmt"
	"testing"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/relay/stream"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestStreamWhitespaceWaitsForMeaningfulContent(t *testing.T) {
	for _, protocol := range []struct {
		name   string
		in     inbound.InboundType
		out    outbound.OutboundType
		format model.APIFormat
		raw    bool
		prefix string
		delta  string
		suffix string
	}{
		{
			name: "responses", in: inbound.InboundTypeOpenAIResponse, out: outbound.OutboundTypeOpenAIResponse, format: model.APIFormatOpenAIResponse, raw: true,
			prefix: relayTestResponseStart("resp_whitespace"),
			delta:  `{"type":"response.output_text.delta","delta":%q}`,
			suffix: "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_whitespace\",\"model\":\"gpt-4o\",\"status\":\"completed\",\"output\":[]}}\n\n",
		},
		{
			name: "chat", in: inbound.InboundTypeOpenAIChat, out: outbound.OutboundTypeOpenAIChat, format: model.APIFormatOpenAIChatCompletion,
			delta:  `{"id":"chat_whitespace","model":"gpt-4o","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":%q}}]}`,
			suffix: "data: {\"id\":\"chat_whitespace\",\"model\":\"gpt-4o\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		},
		{
			name: "anthropic", in: inbound.InboundTypeAnthropic, out: outbound.OutboundTypeAnthropic, format: model.APIFormatAnthropicMessage, raw: true,
			prefix: "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_whitespace\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			delta:  `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`,
			suffix: "data: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n",
		},
	} {
		for _, passthrough := range []bool{false, true} {
			if passthrough && !protocol.raw {
				continue
			}
			for _, hasAnswer := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/passthrough=%t/answer=%t", protocol.name, passthrough, hasAnswer), func(t *testing.T) {
					ctx := context.Background()
					ra, recorder := newEmptyStreamTestAttempt(t, protocol.in, protocol.format, protocol.out)
					ra.channel = &dbmodel.Channel{Type: protocol.out}
					blankDelta := fmt.Sprintf(protocol.delta, " \n\t\u3000")
					// A complete JSON data line can release passthrough before its SSE
					// delimiter arrives. Its stateless preview must also reject whitespace.
					_, preview, err := ra.outAdapter.(model.SourceEventInspector).InspectSourceEvent(ctx, model.SourceEvent{Data: []byte(blankDelta)})
					if err != nil || preview.Semantic {
						t.Fatalf("whitespace preview could commit the response: %+v, err=%v", preview, err)
					}
					body := protocol.prefix + "data: " + blankDelta + "\n\n"
					if hasAnswer {
						body += "data: " + fmt.Sprintf(protocol.delta, "answer") + "\n\n"
					}
					body += protocol.suffix
					if passthrough {
						cfg := ra.outAdapter.(model.PassthroughCapable).PassthroughConfig()
						err = ra.handleStreamResponsePassthroughV2(ctx, sseTestResponse(body), cfg)
					} else {
						err = ra.handleStreamResponseV2(ctx, sseTestResponse(body))
					}
					if hasAnswer {
						if err != nil || !ra.responseCommitted() {
							t.Fatalf("valid answer after leading whitespace failed: %v", err)
						}
						if passthrough && recorder.Body.String() != body {
							t.Fatal("passthrough changed or discarded buffered whitespace")
						}
					} else if !errors.Is(err, stream.ErrEmptyUpstreamStream) || ra.responseCommitted() || recorder.Body.Len() != 0 {
						t.Fatalf("whitespace-only stream was not rejected before commit: err=%v body=%s", err, recorder.Body.String())
					}
				})
			}
		}
	}
}
