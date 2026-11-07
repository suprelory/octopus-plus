package relay

import (
	"fmt"
	"strings"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/internal/transformer"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

// evaluateCapabilityPolicy applies the relay-wide degradation policy to a
// planner decision. Hard capability rejections are always rejected. Known
// degradation is rejected only when strict mode is enabled, except for the
// explicit fallback for unknown top-level fields and native semantics. The returned
// code preserves the existing protocol error semantics for each case.
func evaluateCapabilityPolicy(decision outbound.CapabilityDecision, policy capabilityDegradationPolicy) (reject bool, code string) {
	if decision.Rejected() {
		return true, CodeRelayModelNotSupported
	}
	if decision.Status == outbound.CapabilityDegraded && policy == capabilityPolicyStrict {
		if onlyAvailabilityFallbackLosses(decision) {
			return false, ""
		}
		return true, CodeRelayCapabilityRejected
	}
	return false, ""
}

func onlyAvailabilityFallbackLosses(decision outbound.CapabilityDecision) bool {
	if len(decision.Losses) == 0 {
		return false
	}
	for _, loss := range decision.Losses {
		if !loss.IsAvailabilityFallbackLoss() {
			return false
		}
	}
	return true
}

// shouldRejectCapability is the boolean form used by callers that only need
// to decide whether an attempt may proceed.
func shouldRejectCapability(decision outbound.CapabilityDecision, policy capabilityDegradationPolicy) bool {
	reject, _ := evaluateCapabilityPolicy(decision, policy)
	return reject
}

func capabilityTrace(decision outbound.CapabilityDecision, policy capabilityDegradationPolicy, adapterType string) balancer.CapabilityTrace {
	losses := make([]dbmodel.CapabilityLoss, 0, len(decision.Losses))
	for _, loss := range decision.Losses {
		losses = append(losses, dbmodel.CapabilityLoss{
			Field:       loss.Field,
			TargetField: loss.TargetField,
			Action:      string(loss.Action),
			Condition:   loss.Condition,
			Reason:      loss.Reason,
		})
	}
	return balancer.CapabilityTrace{
		AdapterType:      adapterType,
		Status:           string(decision.Status),
		Policy:           string(policy),
		ConversionPath:   decision.ConversionPath,
		Conversion:       &decision.Conversion,
		RequiredFeatures: decision.RequiredFeatures,
		DegradedFields:   decision.DegradedFields,
		Losses:           losses,
		Lossiness:        decision.Lossiness,
		Reasons:          decision.Reasons,
	}
}

func capabilityRejectionMessage(decision outbound.CapabilityDecision, adapterType string) string {
	details := make([]string, 0, 3)
	if len(decision.DegradedFields) > 0 {
		details = append(details, "fields="+strings.Join(decision.DegradedFields, ","))
	}
	target := string(decision.OutboundFormat)
	if target == "" {
		target = adapterType
	}
	if target != "" {
		details = append(details, "target="+target)
	}
	if len(decision.ConversionPath) > 0 {
		details = append(details, "path="+strings.Join(decision.ConversionPath, " -> "))
	}
	if len(details) == 0 {
		return decision.Summary()
	}
	return fmt.Sprintf("%s [%s]", decision.Summary(), strings.Join(details, "; "))
}

func resolveFinalAttemptResult(
	sawSupportedCapability bool,
	lastErr error,
	lastResult attemptResult,
	capabilityErr error,
	capabilityResult attemptResult,
) (attemptResult, error) {
	if !sawSupportedCapability && capabilityErr != nil {
		return capabilityResult, capabilityErr
	}
	return lastResult, lastErr
}

func preferCapabilityRejection(
	currentErr error,
	currentResult attemptResult,
	candidateErr error,
	candidateResult attemptResult,
) (attemptResult, error) {
	if candidateErr == nil {
		return currentResult, currentErr
	}
	if currentErr == nil || capabilityRejectionPriority(candidateResult) > capabilityRejectionPriority(currentResult) {
		return candidateResult, candidateErr
	}
	return currentResult, currentErr
}

func capabilityRejectionPriority(result attemptResult) int {
	if result.ProtocolError == nil {
		return 0
	}
	switch result.ProtocolError.Detail.Code {
	case CodeRelayModelNotSupported:
		return 2
	case CodeRelayCapabilityRejected:
		return 1
	default:
		return 0
	}
}

type capabilityDegradationPolicy string

const (
	capabilityPolicyAllow  capabilityDegradationPolicy = "allow"
	capabilityPolicyWarn   capabilityDegradationPolicy = "warn"
	capabilityPolicyStrict capabilityDegradationPolicy = "strict"
)

func getCapabilityDegradationPolicy() capabilityDegradationPolicy {
	value, err := op.SettingGetString(dbmodel.SettingKeyCapabilityDegradationPolicy)
	if err != nil {
		return capabilityPolicyWarn
	}
	policy := capabilityDegradationPolicy(strings.ToLower(strings.TrimSpace(value)))
	switch policy {
	case capabilityPolicyAllow, capabilityPolicyWarn, capabilityPolicyStrict:
		return policy
	default:
		return capabilityPolicyWarn
	}
}

func logRelayCapability(channel *dbmodel.Channel, modelName string, decision outbound.CapabilityDecision, policy capabilityDegradationPolicy) {
	transformer.RecordCapabilityDecision(decision)
	if channel == nil {
		return
	}
	log.Debugw("relay.capability_decision",
		"channel_id", channel.ID,
		"channel", channel.Name,
		"model", modelName,
		"status", decision.Status,
		"conversion_path", decision.ConversionPath,
		"conversion", decision.Conversion,
		"required_features", decision.RequiredFeatures,
		"degraded_fields", decision.DegradedFields,
		"losses", decision.Losses,
		"lossiness", decision.Lossiness,
		"static_quality", decision.StaticQuality,
		"reasons", decision.Reasons,
	)
	if decision.Status == outbound.CapabilityDegraded {
		if policy == capabilityPolicyWarn {
			log.Warnw("relay.capability_degraded", "channel_id", channel.ID, "channel", channel.Name, "model", modelName, "fields", decision.DegradedFields, "losses", decision.Losses, "reasons", decision.Reasons)
		} else if policy == capabilityPolicyAllow {
			log.Debugw("relay.capability_degraded_allowed", "channel_id", channel.ID, "channel", channel.Name, "model", modelName, "fields", decision.DegradedFields)
		}
	}
}
