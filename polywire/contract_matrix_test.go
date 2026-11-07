package polywire_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
	relaystream "github.com/suprelory/octopus-plus/polywire/streamio"
)

type contractEvent struct{ Type, Data string }
type protocolContract struct {
	Request, Response       json.RawMessage
	Stream                  []contractEvent
	InvalidJSON             string `json:"invalid_json"`
	AbruptPrefix            int    `json:"abrupt_prefix"`
	StreamUnsupportedReason string `json:"stream_unsupported_reason"`
	Semantics               map[string]struct {
		Events            []contractEvent
		Kind              model.StreamEventKind
		UnsupportedReason string `json:"unsupported_reason"`
	}
}

var inboundContracts = map[inbound.InboundType]outbound.OutboundType{
	inbound.InboundTypeOpenAIChat:      outbound.OutboundTypeOpenAIChat,
	inbound.InboundTypeOpenAIResponse:  outbound.OutboundTypeOpenAIResponse,
	inbound.InboundTypeAnthropic:       outbound.OutboundTypeAnthropic,
	inbound.InboundTypeOpenAIEmbedding: outbound.OutboundTypeOpenAIEmbedding,
}

func readProtocolContract(t *testing.T, typ outbound.OutboundType) protocolContract {
	t.Helper()
	descriptor, ok := outbound.Descriptor(typ)
	if !ok {
		t.Fatalf("unregistered protocol %d", typ)
	}
	body, err := os.ReadFile(filepath.Join("testdata", "contracts", descriptor.Name+".json"))
	if err != nil {
		t.Fatalf("new adapters require golden request, response and abnormal-stream fixtures: %v", err)
	}
	var fixture protocolContract
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Request) == 0 || len(fixture.Response) == 0 || fixture.InvalidJSON == "" {
		t.Fatalf("incomplete contract for %s", descriptor.Name)
	}
	if fixture.StreamUnsupportedReason == "" && (len(fixture.Stream) < 2 || fixture.AbruptPrefix <= 0 || fixture.AbruptPrefix >= len(fixture.Stream)) {
		t.Fatalf("missing stream/abnormal fixtures for %s", descriptor.Name)
	}
	return fixture
}

func TestProtocolContractRequestResponseMatrix(t *testing.T) {
	ctx := context.Background()
	for _, input := range inbound.Types() {
		clientType, ok := inboundContracts[input]
		if !ok {
			t.Fatalf("new inbound %d requires a contract mapping", input)
		}
		clientFixture := readProtocolContract(t, clientType)
		for _, output := range outbound.Types() {
			t.Run(fmt.Sprintf("%s_to_%s", clientType, output), func(t *testing.T) {
				client := inbound.Get(input)
				request, err := client.TransformRequest(ctx, clientFixture.Request)
				if err != nil {
					t.Fatal(err)
				}
				decision := outbound.PlanRequestForModel(request, "contract-model", output, false)
				if !outbound.SupportsRequestType(output, request.ResolveRequestType()) {
					if !decision.Rejected() {
						t.Fatal("unsupported operation accepted")
					}
					return
				}
				if decision.Rejected() {
					t.Fatal(decision.Summary())
				}
				provider := outbound.Get(output)
				wire, _, err := outbound.BuildRequest(ctx, provider, output, request, "https://example.invalid/v1", "contract-key")
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(wire.Body)
				wire.Body.Close()
				if err != nil || !json.Valid(body) || !bytes.Contains(body, []byte("hello")) {
					t.Fatalf("request semantic lost: %s, %v", body, err)
				}
				for mirror, mirrorType := range inboundContracts {
					if mirrorType != output {
						continue
					}
					roundtrip, err := inbound.Get(mirror).TransformRequest(ctx, body)
					if err != nil {
						t.Fatal(err)
					}
					if roundtrip.IsEmbeddingRequest() {
						if roundtrip.EmbeddingsPayload().Input.Single == nil || *roundtrip.EmbeddingsPayload().Input.Single != "hello" {
							t.Fatal("embedding input lost")
						}
					} else if messageText(roundtrip.ConversationMessages()[0]) != "hello" {
						t.Fatalf("message roundtrip = %+v", roundtrip.ConversationMessages())
					}
				}
				fixture := readProtocolContract(t, output)
				response, err := provider.TransformResponse(ctx, jsonHTTPResponse(fixture.Response))
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := client.TransformResponse(ctx, response)
				if err != nil {
					t.Fatal(err)
				}
				roundtrip, err := outbound.Get(clientType).TransformResponse(ctx, jsonHTTPResponse(encoded))
				if err != nil {
					t.Fatal(err)
				}
				if request.IsEmbeddingRequest() {
					if len(roundtrip.EmbeddingData) != 1 || roundtrip.Usage.TotalTokens != 3 {
						t.Fatalf("embedding response = %+v", roundtrip)
					}
				} else {
					assertContractResponse(t, roundtrip)
				}
			})
		}
	}
}

func TestProtocolContractStreamMatrix(t *testing.T) {
	ctx := context.Background()
	for _, provider := range outbound.Types() {
		fixture := readProtocolContract(t, provider)
		if fixture.StreamUnsupportedReason != "" {
			continue
		}
		for _, client := range inbound.Types() {
			if client == inbound.InboundTypeOpenAIEmbedding {
				continue
			}
			t.Run(fmt.Sprintf("%s_to_%d", provider, client), func(t *testing.T) {
				policy, _ := outbound.TerminalPolicy(provider)
				converter := model.NewStreamConverter(outbound.Get(provider), policy)
				encoder := inbound.Get(client)
				var wire bytes.Buffer
				done := 0
				events := append(append([]contractEvent(nil), fixture.Stream...), fixture.Stream[len(fixture.Stream)-1])
				for index, event := range events {
					canonical, err := converter.Push(ctx, model.SourceEvent{Type: event.Type, Data: []byte(event.Data), ID: fmt.Sprint(index), Sequence: int64(index + 1), Transport: model.SourceTransportSSE})
					if err != nil {
						t.Fatal(err)
					}
					for _, event := range canonical {
						if event.Kind == model.StreamEventKindDone {
							done++
						}
					}
					encoded, err := encoder.TransformStreamEvents(ctx, canonical)
					if err != nil {
						t.Fatal(err)
					}
					wire.Write(encoded)
				}
				tail, err := converter.Finish(ctx, model.StreamFinishCauseCleanEOF)
				if err != nil || len(tail) != 0 || done != 1 {
					t.Fatalf("completion count=%d tail=%+v err=%v", done, tail, err)
				}
				assertContractResponse(t, converter.Response())
				clientType := inboundContracts[client]
				clientPolicy, _ := outbound.TerminalPolicy(clientType)
				decoded := model.NewStreamConverter(outbound.Get(clientType), clientPolicy)
				source := relaystream.NewSSESource(io.NopCloser(bytes.NewReader(wire.Bytes())), 0)
				defer source.Close()
				for {
					event, err := source.ReadSourceEvent(ctx)
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if _, err := decoded.Push(ctx, model.SourceEvent{Type: event.Type, Data: event.Data, ID: event.ID, Sequence: event.Sequence, Transport: event.Transport}); err != nil {
						t.Fatalf("downstream contract: %v\n%s", err, &wire)
					}
				}
				if _, err := decoded.Finish(ctx, model.StreamFinishCauseCleanEOF); err != nil {
					t.Fatal(err)
				}
				assertContractResponse(t, decoded.Response())
			})
		}
	}
}

func TestProtocolContractAbnormalAndNativeSemantics(t *testing.T) {
	ctx := context.Background()
	for _, typ := range outbound.Types() {
		fixture := readProtocolContract(t, typ)
		t.Run(typ.String(), func(t *testing.T) {
			if _, err := outbound.Get(typ).TransformSourceEvent(ctx, model.SourceEvent{Data: []byte(fixture.InvalidJSON)}); err == nil {
				t.Fatal("invalid provider JSON accepted")
			}
			if fixture.StreamUnsupportedReason != "" {
				return
			}
			for _, cause := range []model.StreamFinishCause{model.StreamFinishCauseCleanEOF, model.StreamFinishCauseSourceError, model.StreamFinishCauseClientCancellation} {
				policy, _ := outbound.TerminalPolicy(typ)
				converter := model.NewStreamConverter(outbound.Get(typ), policy)
				for _, event := range fixture.Stream[:fixture.AbruptPrefix] {
					if _, err := converter.Push(ctx, model.SourceEvent{Type: event.Type, Data: []byte(event.Data)}); err != nil {
						t.Fatal(err)
					}
				}
				if tail, err := converter.Finish(ctx, cause); !errors.Is(err, model.ErrStreamIncomplete) || len(tail) != 0 {
					t.Fatalf("%s accepted abrupt stream: %+v, %v", cause, tail, err)
				}
			}
			for _, semantic := range []string{"text", "thinking", "signature", "tools", "citations", "audio"} {
				feature, exists := fixture.Semantics[semantic]
				if !exists {
					t.Fatalf("missing %s contract", semantic)
				}
				if feature.UnsupportedReason != "" {
					continue
				}
				adapter := outbound.Get(typ)
				found := false
				for _, event := range feature.Events {
					events, err := adapter.TransformSourceEvent(ctx, model.SourceEvent{Type: event.Type, Data: []byte(event.Data)})
					if err != nil {
						t.Fatal(err)
					}
					for _, canonical := range events {
						found = found || canonical.Kind == feature.Kind
					}
				}
				if !found {
					t.Fatalf("%s does not produce %s", semantic, feature.Kind)
				}
			}
		})
	}
}

func jsonHTTPResponse(body []byte) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(body))}
}
func messageText(message model.Message) string {
	var text strings.Builder
	if message.Content.Content != nil {
		return *message.Content.Content
	}
	for _, part := range message.Content.MultipleContent {
		if part.Text != nil {
			text.WriteString(*part.Text)
		}
	}
	return text.String()
}
func assertContractResponse(t *testing.T, response *model.InternalLLMResponse) {
	t.Helper()
	if response == nil || len(response.Choices) != 1 || response.Choices[0].Message == nil || messageText(*response.Choices[0].Message) != "world" || response.Usage == nil || response.Usage.TotalTokens != 5 {
		data, _ := json.Marshal(response)
		t.Fatalf("response semantics = %s", data)
	}
	if response.Choices[0].FinishReason == nil || *response.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish reason = %+v", response.Choices[0])
	}
}
