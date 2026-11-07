package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/transformer"
	transformerModel "github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestInvalidParamOverrideCannotBypassValidation(t *testing.T) {
	override := `{invalid`
	channel := &dbmodel.Channel{
		Type:            outbound.OutboundTypeOpenAIResponse,
		PassthroughMode: dbmodel.ChannelPassthroughModeAuto,
		ParamOverride:   &override,
	}
	body := `{"model":"upstream-model","input":"hello"}`
	internalRequest := &transformerModel.InternalLLMRequest{
		Model:        "upstream-model",
		RawAPIFormat: transformerModel.APIFormatOpenAIResponse,
	}
	for _, websocket := range []bool{false, true} {
		if planRelayPassthrough(internalRequest, []byte(body), channel, transformer.Outbound(channel.Type), websocket) {
			t.Fatalf("invalid configuration enabled passthrough: websocket=%t", websocket)
		}
	}
	attempt := &relayAttempt{
		relayRequest: &relayRequest{internalRequest: internalRequest},
		channel:      channel,
	}
	t.Run("http", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		err := attempt.applyParamOverride(request)
		if class, ok := localRelayErrorClass(err); !ok || class != FailureConfiguration {
			t.Fatalf("expected configuration error, got %v", err)
		}
	})
	t.Run("websocket", func(t *testing.T) {
		_, err := attempt.applyParamOverridePayload([]byte(body))
		if class, ok := localRelayErrorClass(err); !ok || class != FailureConfiguration {
			t.Fatalf("expected configuration error, got %v", err)
		}
	})
}

func TestApplyParamOverrideRejectsRemovingRequiredTopLevelModel(t *testing.T) {
	override := `[{"op":"replace","path":"","value":{"messages":[]}}]`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"upstream-model","messages":[]}`))
	attempt := &relayAttempt{
		relayRequest: &relayRequest{internalRequest: &transformerModel.InternalLLMRequest{Model: "upstream-model"}},
		channel:      &dbmodel.Channel{ParamOverride: &override},
	}

	err := attempt.applyParamOverride(request)
	if err == nil {
		t.Fatal("expected removing the required model to fail")
	}
	class, ok := localRelayErrorClass(err)
	if !ok || class != FailureConfiguration {
		t.Fatalf("error class = %q, classified = %t; want %q", class, ok, FailureConfiguration)
	}
}

func TestApplyParamOverrideAllowsBodyWithoutModelForURLModelProtocols(t *testing.T) {
	override := `[{"op":"replace","path":"","value":{"contents":[]}}]`
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-pro:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
	internalRequest := &transformerModel.InternalLLMRequest{Model: "gemini-pro"}
	attempt := &relayAttempt{
		relayRequest: &relayRequest{internalRequest: internalRequest},
		channel:      &dbmodel.Channel{ParamOverride: &override},
	}

	if err := attempt.applyParamOverride(request); err != nil {
		t.Fatalf("URL-model request override failed: %v", err)
	}
	if internalRequest.Model != "gemini-pro" {
		t.Fatalf("internal model = %q, want gemini-pro", internalRequest.Model)
	}
}
