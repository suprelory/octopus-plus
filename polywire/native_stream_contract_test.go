package polywire_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
	relaystream "github.com/suprelory/octopus-plus/polywire/streamio"
)

func TestNativeResponsesLifecycleAndSourceReplay(t *testing.T) {
	ctx := context.Background()
	frames := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"r","model":"m","created_at":123,"status":"in_progress","metadata":{"trace":"keep"}}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":2,"item":{"type":"mcp_call","id":"mcp-1","name":"lookup","server_label":"server","arguments":"","future":{"zero":0}}}`,
		`{"type":"response.mcp_call_arguments.delta","sequence_number":2,"output_index":2,"item_id":"mcp-1","delta":"{}"}`,
		`{"type":"response.mcp_call.completed","sequence_number":3,"output_index":2,"item_id":"mcp-1"}`,
		`{"type":"response.output_item.done","sequence_number":4,"output_index":2,"item":{"type":"mcp_call","id":"mcp-1","name":"lookup","server_label":"server","arguments":"{}","output":"found","error":null}}`,
		`{"type":"response.output_item.added","sequence_number":5,"output_index":3,"item":{"type":"computer_call","id":"computer-1","call_id":"call-1","action":{"type":"click","x":0,"y":1},"pending_safety_checks":[{"id":"check-1","code":"verify"}]}}`,
		`{"type":"response.output_item.done","sequence_number":6,"output_index":4,"item":{"type":"web_search_call","id":"search-1","action":{"type":"search","query":"source"}}}`,
		`{"type":"response.future_native","sequence_number":7,"future":{"opaque":null}}`,
		`{"type":"response.completed","sequence_number":8,"response":{"id":"r","model":"m","status":"completed","output":[{"type":"computer_call","id":"computer-1","action":{"type":"click","x":0},"pending_safety_checks":[{"id":"check-1"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
	}
	policy, _ := outbound.TerminalPolicy(outbound.OutboundTypeOpenAIResponse)
	converter := model.NewStreamConverter(outbound.Get(outbound.OutboundTypeOpenAIResponse), policy)
	var replay model.StreamReplay
	kinds := make(map[model.StreamEventKind]bool)
	for index, frame := range frames {
		source := model.SourceEvent{Data: []byte(frame), ID: fmt.Sprint(index), Sequence: int64(index + 1), Transport: model.SourceTransportSSE}
		events, err := converter.Push(ctx, source)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			kinds[event.Kind] = true
			if event.Provenance == nil || event.Importance == "" || event.Provenance.Format != model.APIFormatOpenAIResponse || event.Provenance.Sequence != source.Sequence || !bytes.Equal(event.Provenance.RawPayload, source.Data) {
				t.Fatalf("lost source provenance: %+v", event)
			}
			if event.Provenance.ProviderSequence == nil || *event.Provenance.ProviderSequence != int64(index) {
				t.Fatalf("provider sequence lost: %+v", event.Provenance)
			}
			if event.Kind == model.StreamEventKindMCPCall && event.Native.Phase == "delta" && event.Native.ServerLabel != "server" {
				t.Fatalf("MCP identity lost: %+v", event.Native)
			}
		}
		forwarded, err := replay.Push(events, model.APIFormatOpenAIResponse)
		if err != nil || len(forwarded) != 1 || !reflect.DeepEqual(forwarded[0], source) {
			t.Fatalf("native replay = %+v, %v", forwarded, err)
		}
		// Every canonical event owns an immutable source snapshot.
		source.Data[0] = '!'
		if events[0].Provenance.RawPayload[0] != '{' {
			t.Fatal("source buffer was retained without ownership")
		}
	}
	for _, kind := range []model.StreamEventKind{model.StreamEventKindResponseStart, model.StreamEventKindResponseStop, model.StreamEventKindOutputItemStart, model.StreamEventKindOutputItemStop, model.StreamEventKindMCPCall, model.StreamEventKindComputerUse, model.StreamEventKindServerTool, model.StreamEventKindOpaque} {
		if !kinds[kind] {
			t.Fatalf("missing native event %s", kind)
		}
	}
	if events, err := converter.Push(ctx, model.SourceEvent{Data: []byte(frames[len(frames)-1]), Sequence: 10}); err != nil || len(events) != 0 {
		t.Fatalf("duplicate terminal = %+v, %v", events, err)
	}
	if tail, err := converter.Finish(ctx, model.StreamFinishCauseCleanEOF); err != nil || len(tail) != 0 {
		t.Fatalf("finish = %+v, %v", tail, err)
	}
	response := converter.Response()
	if response.Created != 123 || response.Usage.TotalTokens != 5 || len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 0 {
		t.Fatalf("native events were flattened: %+v", response)
	}
	if !bytes.Contains(response.RawResponsesOutputItems, []byte(`"pending_safety_checks"`)) || !bytes.Contains(response.RawResponsesOutputItems, []byte(`"action"`)) {
		t.Fatalf("native output recovery lost: %s", response.RawResponsesOutputItems)
	}
}

func TestProtocolContractSourceProvenanceAndTerminalReplay(t *testing.T) {
	ctx := context.Background()
	for _, typ := range outbound.Types() {
		fixture := readProtocolContract(t, typ)
		if fixture.StreamUnsupportedReason != "" {
			continue
		}
		t.Run(typ.String(), func(t *testing.T) {
			policy, _ := outbound.TerminalPolicy(typ)
			converter := model.NewStreamConverter(outbound.Get(typ), policy)
			var replay model.StreamReplay
			for index, frame := range fixture.Stream {
				source := model.SourceEvent{Type: frame.Type, Data: []byte(frame.Data), ID: "id", Sequence: int64(index + 1), Transport: model.SourceTransportSSE}
				events, err := converter.Push(ctx, source)
				if err != nil {
					t.Fatal(err)
				}
				if len(events) == 0 {
					t.Fatal("provider source disappeared")
				}
				for _, event := range events {
					if event.Provenance == nil || event.Importance == "" || !bytes.Equal(event.Provenance.RawPayload, source.Data) {
						t.Fatalf("missing provenance: %+v", event)
					}
				}
				forwarded, err := replay.Push(events, events[0].Provenance.Format)
				if err != nil || len(forwarded) != 1 || !reflect.DeepEqual(forwarded[0], source) {
					t.Fatalf("source changed during replay: %+v, %v", forwarded, err)
				}
			}
		})
	}
}

func TestUnknownNativeEventsAndEncodingLoss(t *testing.T) {
	ctx := context.Background()
	for _, typ := range []outbound.OutboundType{outbound.OutboundTypeOpenAIChat, outbound.OutboundTypeOpenAIResponse, outbound.OutboundTypeAnthropic, outbound.OutboundTypeGemini} {
		source := model.SourceEvent{Type: "provider.future", Data: []byte(`{"type":"provider.future","future":{"zero":0,"null":null}}`), Sequence: 7, Transport: model.SourceTransportSSE}
		events, err := outbound.Get(typ).TransformSourceEvent(ctx, source)
		if err != nil || len(events) != 1 || events[0].Kind != model.StreamEventKindOpaque {
			t.Fatalf("%s dropped unknown event: %+v, %v", typ, events, err)
		}
		for _, target := range []inbound.InboundType{inbound.InboundTypeOpenAIChat, inbound.InboundTypeOpenAIResponse, inbound.InboundTypeAnthropic} {
			wire, err := inbound.Get(target).TransformStreamEvents(ctx, events)
			var loss *model.StreamConversionLoss
			if !errors.As(err, &loss) || len(wire) != 0 || loss.SourceSequence != 7 || loss.EventType != source.Type {
				t.Fatalf("missing native loss report: %s %v", wire, err)
			}
		}
		var replay model.StreamReplay
		if _, err := replay.Push(events, model.APIFormat("wrong/protocol")); err == nil {
			t.Fatal("cross-protocol raw replay accepted")
		}
		if forwarded, err := replay.Push(events, events[0].Provenance.Format); err != nil || len(forwarded) != 1 || !bytes.Equal(forwarded[0].Data, source.Data) {
			t.Fatalf("native replay: %+v %v", forwarded, err)
		}
	}
}

func TestOpenAIStreamCitationWireRoundTrip(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		input  inbound.InboundType
		output outbound.OutboundType
		data   string
	}{
		{"chat_url", inbound.InboundTypeOpenAIChat, outbound.OutboundTypeOpenAIChat, `{"choices":[{"index":0,"delta":{"annotations":[{"type":"url_citation","url_citation":{"start_index":0,"end_index":4,"url":"https://example.invalid","title":"source","future":null}}]}}]}`},
		{"responses_file", inbound.InboundTypeOpenAIResponse, outbound.OutboundTypeOpenAIResponse, `{"type":"response.output_text.annotation.added","output_index":0,"content_index":0,"annotation_index":0,"annotation":{"type":"container_file_citation","file_id":"file-1","container_id":"container-1","filename":"source.txt","index":0,"future":null}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			events, err := outbound.Get(test.output).TransformSourceEvent(ctx, model.SourceEvent{Data: []byte(test.data)})
			if err != nil {
				t.Fatal(err)
			}
			var want *model.Citation
			for _, event := range events {
				if event.Delta != nil && event.Delta.Citation != nil {
					want = event.Delta.Citation
				}
			}
			if want == nil {
				t.Fatal("citation not parsed")
			}
			wire, err := inbound.Get(test.input).TransformStreamEvents(ctx, events)
			if err != nil {
				t.Fatal(err)
			}
			source := relaystream.NewSSESource(io.NopCloser(bytes.NewReader(wire)), 0)
			defer source.Close()
			decoder := outbound.Get(test.output)
			var got *model.Citation
			for {
				frame, err := source.ReadSourceEvent(ctx)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := decoder.TransformSourceEvent(ctx, frame)
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range decoded {
					if event.Delta != nil && event.Delta.Citation != nil {
						got = event.Delta.Citation
					}
				}
			}
			if got == nil || !bytes.Equal(got.Raw, want.Raw) || got.Format != want.Format {
				t.Fatalf("citation lost native fields: %+v -> %+v\n%s", want, got, wire)
			}
		})
	}
}

func TestNativeReplayRetainsNonJSONTerminalAndRejectsConflicts(t *testing.T) {
	ctx := context.Background()
	source := model.SourceEvent{Type: "response.completed", Data: []byte("terminal without JSON"), Sequence: 1}
	events, err := outbound.Get(outbound.OutboundTypeOpenAIResponse).TransformSourceEvent(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(events); err != nil {
		t.Fatalf("non-JSON provenance broke canonical serialization: %v", err)
	}
	var replay model.StreamReplay
	if frames, err := replay.Push(events, model.APIFormatOpenAIResponse); err != nil || len(frames) != 1 || !bytes.Equal(frames[0].Data, source.Data) {
		t.Fatalf("replay = %+v, %v", frames, err)
	}
	source.Data = []byte("conflicting source")
	conflicting := model.WithStreamSource([]model.StreamEvent{{Kind: model.StreamEventKindDone}}, source, model.APIFormatOpenAIResponse, nil)
	if _, err := replay.Push(conflicting, model.APIFormatOpenAIResponse); err == nil {
		t.Fatal("conflicting sequence accepted")
	}
}

func TestNativeGeminiUnknownPartsAndAudioReferences(t *testing.T) {
	source := model.SourceEvent{Data: []byte(`{"candidates":[{"content":{"parts":[{"fileData":{"mimeType":"audio/wav","fileUri":"https://example.invalid/audio"}},{"future_part":{"zero":0}}]},"finishReason":"STOP"}]}`), Sequence: 1}
	policy, _ := outbound.TerminalPolicy(outbound.OutboundTypeGemini)
	converter := model.NewStreamConverter(outbound.Get(outbound.OutboundTypeGemini), policy)
	events, err := converter.Push(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	var audio, opaque bool
	for _, event := range events {
		if event.Kind == model.StreamEventKindImageDelta {
			t.Fatal("audio became an image")
		}
		audio = audio || event.Kind == model.StreamEventKindAudioDelta && event.Media.URI == "https://example.invalid/audio"
		opaque = opaque || event.Kind == model.StreamEventKindOpaque
	}
	if !audio || !opaque {
		t.Fatalf("native parts lost: %+v", events)
	}
}
