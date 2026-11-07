package outbound

import (
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func reportLoss(decision *CapabilityDecision, field string, action LossAction, reason string) {
	reportChange(decision, CapabilityLoss{Field: field, Action: action, Reason: reason})
}

func reportChange(decision *CapabilityDecision, change CapabilityLoss) {
	if decision == nil || strings.TrimSpace(change.Field) == "" || change.Action == "" || !change.IsLossy() {
		return
	}
	decision.DegradedFields = append(decision.DegradedFields, change.Field)
	decision.Reasons = append(decision.Reasons, change.Reason)
	decision.Losses = append(decision.Losses, change)
}

func evaluateInboundRepairs(req *model.InternalLLMRequest, decision *CapabilityDecision) {
	if req == nil || decision == nil {
		return
	}
	if from := req.TransformerMetadataValue(model.TransformerMetadataAnthropicMaxTokensRepairFrom); from != "" {
		reportLoss(decision, "max_tokens", LossActionRepair, fmt.Sprintf("Anthropic repaired max_tokens=%s to 1 before canonical conversion", from))
	}
}

func degrade(decision *CapabilityDecision, field, reason string) {
	reportLoss(decision, field, LossActionDrop, reason)
}
