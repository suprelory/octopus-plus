package anthropic

import (
	"context"
	"errors"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestAnthropicStreamCompletion(t *testing.T) {
	const messageStop = `{"type":"message_stop"}`
	tests := []struct {
		name         string
		chunks       []string
		wantReason   model.FinishReason
		wantSequence string
		incomplete   bool
	}{
		{
			name:       "message stop without stop reason",
			chunks:     []string{messageStop},
			wantReason: model.FinishReasonStop,
		},
		{
			name: "null stop reason",
			chunks: []string{
				`{"type":"message_delta","delta":{"stop_reason":null}}`,
				messageStop,
			},
			wantReason: model.FinishReasonStop,
		},
		{
			name: "tool call without stop reason",
			chunks: []string{
				`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"lookup","input":{}}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"x\"}"}}`,
				messageStop,
			},
			wantReason: model.FinishReasonToolCalls,
		},
		{
			name: "preserve max tokens",
			chunks: []string{
				`{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`,
				messageStop,
			},
			wantReason: model.FinishReasonLength,
		},
		{
			name: "preserve stop sequence",
			chunks: []string{
				`{"type":"message_delta","delta":{"stop_reason":"stop_sequence","stop_sequence":"END"}}`,
				messageStop,
			},
			wantReason:   model.FinishReasonStopSequence,
			wantSequence: "END",
		},
		{
			name:       "repeated terminal markers",
			chunks:     []string{messageStop, messageStop, `[DONE]`},
			wantReason: model.FinishReasonStop,
		},
		{
			name:       "EOF without terminal marker",
			incomplete: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outbound := &MessageOutbound{}
			finalizer := model.NewStreamFinalizer()
			chunks := append([]string{
				`{"type":"message_start","message":{"id":"msg_1","model":"claude","role":"assistant","usage":{"input_tokens":2,"output_tokens":0}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
				`{"type":"message_delta","usage":{"output_tokens":3}}`,
			}, tt.chunks...)
			var stops, done int
			for _, chunk := range chunks {
				events, err := outbound.TransformStreamEvent(context.Background(), []byte(chunk))
				if err != nil {
					t.Fatal(err)
				}
				events, err = finalizer.ProcessStreamEvents(events)
				if err != nil {
					t.Fatalf("ProcessStreamEvents(%s): %v", chunk, err)
				}
				for _, event := range events {
					if event.Kind == model.StreamEventKindMessageStop {
						stops++
					}
					if event.Kind == model.StreamEventKindDone {
						done++
					}
				}
			}
			result, err := finalizer.FinalizeStream()
			if tt.incomplete {
				if !errors.Is(err, model.ErrStreamIncomplete) {
					t.Fatalf("FinalizeStream() = %v, want incomplete stream", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if stops != 1 || done != 1 || len(result.TailEvents) != 0 {
				t.Fatalf("completion events: stops=%d done=%d tail=%+v", stops, done, result.TailEvents)
			}
			if result.FinishReasons[0] != tt.wantReason {
				t.Fatalf("finish reason = %q, want %q", result.FinishReasons[0], tt.wantReason)
			}
			if result.Usage == nil || result.Usage.PromptTokens != 2 || result.Usage.CompletionTokens != 3 {
				t.Fatalf("usage lost: %+v", result.Usage)
			}
			if result.Response == nil || len(result.Response.Choices) != 1 {
				t.Fatalf("unexpected aggregate: %+v", result.Response)
			}
			choice := result.Response.Choices[0]
			if tt.wantSequence != "" && (choice.StopSequence == nil || *choice.StopSequence != tt.wantSequence) {
				t.Fatalf("stop sequence = %v, want %q", choice.StopSequence, tt.wantSequence)
			}
			if tt.wantReason == model.FinishReasonToolCalls && (choice.Message == nil || len(choice.Message.ToolCalls) != 1 || choice.Message.ToolCalls[0].Function.Arguments != `{"q":"x"}`) {
				t.Fatalf("tool call lost: %+v", choice.Message)
			}
		})
	}
}
