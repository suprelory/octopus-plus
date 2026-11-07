package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/transformer"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestParseChatRequestRejectsUnsupportedChoiceCount(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hello"}],"n":2}`))
	if _, _, _, err := parseRequest(inbound.InboundTypeOpenAIChat, ctx); err == nil {
		t.Fatal("unsupported choice count was accepted")
	}
	var response struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusBadRequest || !strings.Contains(response.Error.Message, "n must be 1") {
		t.Fatalf("wrong protocol error: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestChatFieldRecoveryPreservesRoutingAndStrictPolicy(t *testing.T) {
	for _, test := range []struct {
		name       string
		extra      string
		target     outbound.OutboundType
		wantReject bool
	}{
		{"native extension", `,"vendor_hint":true`, outbound.OutboundTypeOpenAIChat, false},
		{"extension availability fallback", `,"vendor_hint":true`, outbound.OutboundTypeGemini, false},
		{"known null loss", `,"top_k":null`, outbound.OutboundTypeOpenAIChat, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			body := `{"model":"m","messages":[{"role":"user","content":"hello"}]` + test.extra + `}`
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			_, req, _, err := parseRequest(inbound.InboundTypeOpenAIChat, ctx)
			if err != nil {
				t.Fatal(err)
			}
			decision := transformer.PlanRequestForModel(req, "m", test.target, false)
			if reject, _ := evaluateCapabilityPolicy(decision, capabilityPolicyStrict); reject != test.wantReject {
				t.Fatalf("strict policy misclassified the field loss: %+v", decision)
			}
			if test.target == outbound.OutboundTypeGemini {
				native := transformer.PlanRequestForModel(req, "m", outbound.OutboundTypeOpenAIChat, false)
				if decision.Status != outbound.CapabilityDegraded || capabilityRank(native) >= capabilityRank(decision) {
					t.Fatalf("routing did not prefer native preservation: native=%+v fallback=%+v", native, decision)
				}
			}
		})
	}
}
