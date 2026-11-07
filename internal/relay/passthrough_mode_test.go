package relay

import (
	"encoding/json"
	"testing"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/transformer"
	transformerModel "github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestShouldUseHTTPPassthroughHonorsChannelMode(t *testing.T) {
	attempt := &relayAttempt{
		relayRequest: &relayRequest{
			rawBody: []byte(`{"model":"m"}`),
			internalRequest: &transformerModel.InternalLLMRequest{
				RawAPIFormat: transformerModel.APIFormatAnthropicMessage,
			},
		},
		channel:    &dbmodel.Channel{PassthroughMode: dbmodel.ChannelPassthroughModeAuto},
		outAdapter: transformer.Outbound(outbound.OutboundTypeAnthropic),
	}
	passthrough := attempt.outAdapter.(transformerModel.PassthroughCapable)
	if !attempt.shouldUseHTTPPassthrough(passthrough) {
		t.Fatal("auto mode must preserve same-format passthrough")
	}
	attempt.channel.PassthroughMode = dbmodel.ChannelPassthroughModeOff
	if attempt.shouldUseHTTPPassthrough(passthrough) {
		t.Fatal("off mode must force the transformer path")
	}
}

func TestParamOverrideForcesValidatedTransformerPath(t *testing.T) {
	override := `{"temperature":0.2}`
	channel := &dbmodel.Channel{
		Type:            outbound.OutboundTypeAnthropic,
		PassthroughMode: dbmodel.ChannelPassthroughModeAuto,
		ParamOverride:   &override,
	}
	request := &transformerModel.InternalLLMRequest{RawAPIFormat: transformerModel.APIFormatAnthropicMessage}
	adapter := transformer.Outbound(channel.Type)
	rawBody := []byte(`{"model":"m"}`)
	if planRelayPassthrough(request, rawBody, channel, adapter, false) {
		t.Fatal("param override must disable byte-stable passthrough")
	}
	decision := newRelayCapabilityPlanner(request, rawBody, false).plan(channel, adapter, "m")
	if decision.StaticQuality == outbound.QualityNative || !containsString(decision.RequiredFeatures, "param_override") || !containsString(decision.ConversionPath, "wire_override") {
		t.Fatalf("override capability decision = %#v", decision)
	}
}

func TestWSPassthroughPayloadAppliesParamOverrideAndPreservesEnvelope(t *testing.T) {
	override := `{"temperature":0.2}`
	request := &transformerModel.InternalLLMRequest{Model: "upstream-model"}
	attempt := &relayAttempt{
		relayRequest: &relayRequest{rawBody: []byte(`{"model":"client-model","input":"hello"}`), internalRequest: request},
		channel:      &dbmodel.Channel{ParamOverride: &override},
	}
	payload, err := attempt.buildWSPassthroughRequestPayload()
	if err != nil {
		t.Fatalf("build websocket payload: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode websocket payload: %v", err)
	}
	if decoded["type"] != "response.create" || decoded["stream"] != true || decoded["temperature"] != 0.2 {
		t.Fatalf("websocket payload = %#v", decoded)
	}
}

func TestShouldUseHTTPPassthroughKeepsResponsesNativeOnlySafety(t *testing.T) {
	request := &transformerModel.InternalLLMRequest{
		RequestType:  transformerModel.RequestTypeResponses,
		RawAPIFormat: transformerModel.APIFormatOpenAIResponse,
	}
	request.MarkOpenAIResponsesPassthroughRequired("tool:custom")
	attempt := &relayAttempt{
		relayRequest: &relayRequest{rawBody: []byte(`{"model":"m"}`), internalRequest: request},
		channel:      &dbmodel.Channel{PassthroughMode: dbmodel.ChannelPassthroughModeOff},
		outAdapter:   transformer.Outbound(outbound.OutboundTypeOpenAIResponse),
	}
	passthrough := attempt.outAdapter.(transformerModel.PassthroughCapable)
	if !attempt.shouldUseHTTPPassthrough(passthrough) {
		t.Fatal("native-only Responses requests must override optional passthrough configuration")
	}
}

func TestPlanRelayPassthroughMatchesResponsesExecutionGuards(t *testing.T) {
	request := &transformerModel.InternalLLMRequest{
		RequestType:  transformerModel.RequestTypeResponses,
		RawAPIFormat: transformerModel.APIFormatOpenAIResponse,
	}
	request.SetOpenAIResponsesOptions(transformerModel.OpenAIResponsesOptions{
		RawTools: json.RawMessage(`[{"type":"custom","name":"shell"}]`),
	})
	request.MarkOpenAIResponsesPassthroughRequired("tool:custom")
	channel := &dbmodel.Channel{
		Type:            outbound.OutboundTypeOpenAIResponse,
		PassthroughMode: dbmodel.ChannelPassthroughModeOff,
	}
	adapter := transformer.Outbound(channel.Type)
	rawBody := []byte(`{"model":"m","tools":[{"type":"custom","name":"shell"}]}`)

	if !planRelayPassthrough(request, rawBody, channel, adapter, false) {
		t.Fatal("HTTP native-only Responses semantics must force passthrough during planning")
	}

	request.MarkOpenAIExactReplayRequest()
	if planRelayPassthrough(request, rawBody, channel, adapter, false) {
		t.Fatal("exact replay must use canonical recovery instead of raw passthrough")
	}
	if decision := transformer.PlanRequestForModel(request, request.Model, channel.Type, false); decision.Rejected() {
		t.Fatalf("exact replay must remain eligible for canonical recovery: %#v", decision)
	}
}

func TestPlanRelayPassthroughRejectsNativeOnlyWSTransform(t *testing.T) {
	request := &transformerModel.InternalLLMRequest{
		RequestType:  transformerModel.RequestTypeResponses,
		RawAPIFormat: transformerModel.APIFormatOpenAIResponse,
	}
	request.MarkOpenAIResponsesPassthroughRequired("tool:custom")
	channel := &dbmodel.Channel{
		Type:   outbound.OutboundTypeOpenAIResponse,
		WSMode: dbmodel.ChannelWSModeTransform,
	}
	adapter := transformer.Outbound(channel.Type)

	passthrough := planRelayPassthrough(request, []byte(`{"model":"m"}`), channel, adapter, true)
	if passthrough {
		t.Fatal("WS transform mode must not be planned as native passthrough")
	}
	decision := transformer.PlanRequestForModel(request, request.Model, channel.Type, passthrough)
	if !decision.Rejected() {
		t.Fatalf("native-only WS transform decision = %#v, want rejected", decision)
	}
}

func TestPlanRelayPassthroughKeepsWSContinuationOnPassthroughTransport(t *testing.T) {
	setupRelayTestDB(t)
	if err := op.SettingSetString(dbmodel.SettingKeyResponsesWSEnabled, "true"); err != nil {
		t.Fatal(err)
	}

	previousResponseID := "resp_previous"
	request := &transformerModel.InternalLLMRequest{
		RequestType:        transformerModel.RequestTypeResponses,
		RawAPIFormat:       transformerModel.APIFormatOpenAIResponse,
		PreviousResponseID: &previousResponseID,
	}
	request.SetOpenAIResponsesOptions(transformerModel.OpenAIResponsesOptions{
		PreviousResponseID: &previousResponseID,
		RawTools:           json.RawMessage(`[{"type":"custom","name":"shell"}]`),
	})
	request.MarkOpenAIResponsesPassthroughRequired("tool:custom")
	channel := &dbmodel.Channel{
		Type:   outbound.OutboundTypeOpenAIResponse,
		WSMode: dbmodel.ChannelWSModePassthrough,
	}
	adapter := transformer.Outbound(channel.Type)
	rawBody := []byte(`{"model":"m","previous_response_id":"resp_previous","tools":[{"type":"custom","name":"shell"}]}`)

	if !planRelayPassthrough(request, rawBody, channel, adapter, true) {
		t.Fatal("WS passthrough continuation must preserve the original response.create payload")
	}
	if planRelayPassthrough(request, rawBody, channel, adapter, false) {
		t.Fatal("HTTP ingress continuation must stay on the affinity-aware WS transform path")
	}
	if decision := transformer.PlanRequestForModel(request, request.Model, channel.Type, false); decision.Rejected() {
		t.Fatalf("HTTP continuation must remain eligible for upstream WS recovery: %#v", decision)
	}
}
