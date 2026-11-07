package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/transformer"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

type countingRequestOutbound struct {
	model.Outbound
	builds int
}

func (o *countingRequestOutbound) TransformRequest(ctx context.Context, request *model.InternalLLMRequest, baseURL, key string) (*http.Request, error) {
	o.builds++
	return o.Outbound.TransformRequest(ctx, request, baseURL, key)
}

func TestRelayReusesPreparedRequestAcrossAttempts(t *testing.T) {
	text := "hello"
	request := &model.InternalLLMRequest{Model: "client", RawAPIFormat: model.APIFormatOpenAIChatCompletion,
		Messages: []model.Message{{Role: "user", Content: model.MessageContent{Content: &text}}}}
	planner := newRelayCapabilityPlanner(request, nil, false)
	channel := &dbmodel.Channel{ID: 1, Type: outbound.OutboundTypeOpenAIChat, PassthroughMode: dbmodel.ChannelPassthroughModeOff,
		BaseUrls: []dbmodel.BaseUrl{{URL: "https://first.example/v1"}}}
	decision := planner.plan(channel, transformer.Outbound(channel.Type), "upstream")
	prepared := planner.preparedFor(channel, "upstream", decision)
	if prepared == nil {
		t.Fatal("planner discarded the validated body")
	}
	adapter := &countingRequestOutbound{Outbound: transformer.Outbound(channel.Type)}
	for index := range 2 {
		attempt := &relayAttempt{relayRequest: &relayRequest{internalRequest: request.Clone()}, channel: channel, outAdapter: adapter,
			preparedRequest: prepared, usedKey: dbmodel.ChannelKey{ChannelKey: fmt.Sprintf("test-key-%d", index)}}
		attempt.internalRequest.Model = "upstream"
		wire, report, err := attempt.buildOutboundRequest(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(wire.Body)
		wire.Body.Close()
		if err != nil || !strings.Contains(string(body), `"model":"upstream"`) || !reflect.DeepEqual(report, decision.ConversionReport) ||
			wire.Header.Get("Authorization") != "Bearer "+attempt.usedKey.ChannelKey {
			t.Fatalf("attempt did not use its own envelope and validated payload: %s %v", body, err)
		}
		wire.Header.Set("X-Attempt", "first")
		channel.BaseUrls = []dbmodel.BaseUrl{{URL: "https://second.example/v1"}}
	}
	if adapter.builds != 0 {
		t.Fatalf("execution rebuilt the payload %d times", adapter.builds)
	}
	if planner.preparedFor(channel, "other-model", decision) != nil {
		t.Fatal("different model reused the prepared body")
	}
	if planner.preparedFor(channel, "", decision) != nil {
		t.Fatal("unspecified execution model reused a mapped payload")
	}
	override := `{"temperature":0}`
	channel.ParamOverride = &override
	if planner.preparedFor(channel, "upstream", decision) != nil {
		t.Fatal("changed override reused the previous cache entry")
	}
	if planner.preparedFor(channel, "upstream", outbound.CapabilityDecision{Passthrough: true}) != nil {
		t.Fatal("passthrough reused a canonical body")
	}
}

func TestRelayPreparedRequestRetainsStrictPolicy(t *testing.T) {
	topK := int64(5)
	request := &model.InternalLLMRequest{Model: "upstream", RequestType: model.RequestTypeChat, RawAPIFormat: model.APIFormatOpenAIChatCompletion, TopK: &topK}
	planner := newRelayCapabilityPlanner(request, nil, false)
	channel := &dbmodel.Channel{Type: outbound.OutboundTypeOpenAIChat, PassthroughMode: dbmodel.ChannelPassthroughModeOff,
		BaseUrls: []dbmodel.BaseUrl{{URL: "https://example.invalid/v1"}}}
	decision := planner.plan(channel, transformer.Outbound(channel.Type), request.Model)
	attempt := &relayAttempt{relayRequest: &relayRequest{internalRequest: request, capabilityPolicy: capabilityPolicyStrict},
		channel: channel, outAdapter: transformer.Outbound(channel.Type), capabilityDecision: decision,
		preparedRequest: planner.preparedFor(channel, request.Model, decision)}
	if attempt.preparedRequest == nil {
		t.Fatal("missing prepared request")
	}
	status, err := attempt.forwardViaHTTPStandard(context.Background())
	if status != http.StatusBadRequest || err == nil {
		t.Fatalf("cached conversion bypassed strict policy: %d %v", status, err)
	}
}

func TestRelayPreparedRequestCacheIsBounded(t *testing.T) {
	request := &model.InternalLLMRequest{Model: "client", RequestType: model.RequestTypeChat}
	planner := newRelayCapabilityPlanner(request, nil, false)
	channel := &dbmodel.Channel{Type: outbound.OutboundTypeOpenAIChat}
	for index := range maxRelayPreparedRequests + 2 {
		planner.plan(channel, transformer.Outbound(channel.Type), fmt.Sprintf("model-%d", index))
	}
	if len(planner.prepared) != maxRelayPreparedRequests || planner.preparedBytes > maxRelayPreparedBytes {
		t.Fatalf("unbounded cache: %d requests, %d bytes", len(planner.prepared), planner.preparedBytes)
	}
	decision := planner.plan(channel, transformer.Outbound(channel.Type), "model-over-limit")
	if decision.Rejected() || planner.preparedFor(channel, "model-over-limit", decision) != nil {
		t.Fatal("cache limit changed capability decisions")
	}
}

func TestRelayPreparedRequestCacheBoundsBytesAndFallsBack(t *testing.T) {
	text := strings.Repeat("x", maxRelayPreparedBytes/2)
	request := &model.InternalLLMRequest{Model: "client", RequestType: model.RequestTypeChat,
		Messages: []model.Message{{Role: "user", Content: model.MessageContent{Content: &text}}}}
	planner := newRelayCapabilityPlanner(request, nil, false)
	channel := &dbmodel.Channel{Type: outbound.OutboundTypeOpenAIChat, BaseUrls: []dbmodel.BaseUrl{{URL: "https://example.invalid/v1"}}}
	planner.plan(channel, transformer.Outbound(channel.Type), "first")
	decision := planner.plan(channel, transformer.Outbound(channel.Type), "second")
	if len(planner.prepared) != 1 || planner.preparedBytes > maxRelayPreparedBytes || decision.Rejected() {
		t.Fatalf("byte budget changed capability or retained too much: %d %d %+v", len(planner.prepared), planner.preparedBytes, decision)
	}
	request = request.Clone()
	request.Model = "second"
	adapter := &countingRequestOutbound{Outbound: transformer.Outbound(channel.Type)}
	attempt := &relayAttempt{relayRequest: &relayRequest{internalRequest: request}, channel: channel, outAdapter: adapter,
		preparedRequest: planner.preparedFor(channel, "second", decision)}
	wire, _, err := attempt.buildOutboundRequest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wire.Body.Close()
	if attempt.preparedRequest != nil || adapter.builds != 1 {
		t.Fatal("request outside the cache budget did not use ordinary construction")
	}
}
