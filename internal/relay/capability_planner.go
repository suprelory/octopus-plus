package relay

import (
	"strings"

	"github.com/suprelory/octopus-plus/internal/helper"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/transformer"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

// relayCapabilityCacheKey describes every decision input that can vary between
// candidates while one relay request is being processed. Channel identity is
// intentionally absent: the decision depends on the effective model, adapter
// type, passthrough result, and active override semantics, not the database ID.
type relayCapabilityCacheKey struct {
	effectiveModel string
	outboundType   outbound.OutboundType
	passthrough    bool
	overrideHash   string
}

// relayCapabilityPlanner is request-scoped. It is intentionally a regular map:
// ranking and candidate execution are sequential, and sharing this cache
// across requests would risk retaining request data and stale channel config.
type relayCapabilityPlanner struct {
	request          *model.InternalLLMRequest
	rawBody          []byte
	websocketIngress bool
	decisions        map[relayCapabilityCacheKey]outbound.CapabilityDecision
	prepared         map[relayCapabilityCacheKey]*outbound.PreparedRequest
	preparedBytes    int64
}

// Candidate ranking can visit many models. Bound retained converted bodies so
// large inputs do not multiply memory use by the number of candidates.
const (
	maxRelayPreparedRequests = 8
	maxRelayPreparedBytes    = 16 << 20
)

func newRelayCapabilityPlanner(request *model.InternalLLMRequest, rawBody []byte, websocketIngress bool) *relayCapabilityPlanner {
	return &relayCapabilityPlanner{
		request:          request,
		rawBody:          rawBody,
		websocketIngress: websocketIngress,
		decisions:        make(map[relayCapabilityCacheKey]outbound.CapabilityDecision),
		prepared:         make(map[relayCapabilityCacheKey]*outbound.PreparedRequest),
	}
}

func (p *relayCapabilityPlanner) effectiveModel(modelName string) string {
	if p == nil || p.request == nil {
		return modelName
	}
	if strings.TrimSpace(modelName) == "" {
		return p.request.Model
	}
	return modelName
}

func (p *relayCapabilityPlanner) plan(channel *dbmodel.Channel, adapter model.Outbound, modelName string) outbound.CapabilityDecision {
	if p == nil || p.request == nil {
		return transformer.PlanRequestForModel(nil, "", outbound.OutboundType(0), false)
	}

	effectiveModel := p.effectiveModel(modelName)
	if channel == nil {
		return transformer.PlanRequestForModel(p.request, effectiveModel, outbound.OutboundType(-1), false)
	}
	if adapter == nil {
		// A nil adapter is an invalid/temporary candidate. Do not cache the
		// non-passthrough fallback in case a later validation supplies a real
		// adapter for the same channel and model.
		return transformer.PlanRequestForModel(p.request, effectiveModel, channel.Type, false)
	}

	override := helper.InspectParamOverride(channel.ParamOverride)
	overrideConfigured := channelParamOverrideActive(channel)
	passthrough := planRelayPassthrough(p.request, p.rawBody, channel, adapter, p.websocketIngress, overrideConfigured)
	key := relayCapabilityCacheKey{
		effectiveModel: effectiveModel,
		outboundType:   channel.Type,
		passthrough:    passthrough,
		overrideHash:   override.Fingerprint,
	}
	if decision, ok := p.decisions[key]; ok {
		return decision
	}

	decision, prepared := transformer.PrepareRequestForModel(p.request, effectiveModel, channel.Type, passthrough)
	decorateParamOverrideDecision(&decision, override, overrideConfigured)
	p.decisions[key] = decision
	if prepared != nil && prepared.Size() > 0 && len(p.prepared) < maxRelayPreparedRequests && p.preparedBytes+prepared.Size() <= maxRelayPreparedBytes {
		p.prepared[key] = prepared
		p.preparedBytes += prepared.Size()
	}
	return decision
}

func (p *relayCapabilityPlanner) preparedFor(channel *dbmodel.Channel, modelName string, decision outbound.CapabilityDecision) *outbound.PreparedRequest {
	if p == nil || channel == nil || decision.Passthrough || decision.Rejected() {
		return nil
	}
	if p.effectiveModel(modelName) != modelName {
		return nil // The execution payload must use the exact planned model.
	}
	return p.prepared[relayCapabilityCacheKey{
		effectiveModel: p.effectiveModel(modelName),
		outboundType:   channel.Type,
		overrideHash:   helper.InspectParamOverride(channel.ParamOverride).Fingerprint,
	}]
}

func (p *relayCapabilityPlanner) rankChannel(channel *dbmodel.Channel, item dbmodel.GroupItem) int {
	if channel == nil || !channel.Enabled {
		return capabilityRankRejected
	}
	adapter := transformer.Outbound(channel.Type)
	if adapter == nil || p == nil || p.request == nil {
		return capabilityRankRejected
	}
	return capabilityRank(p.plan(channel, adapter, item.ModelName))
}

func channelParamOverrideConfigured(channel *dbmodel.Channel) bool {
	return channel != nil && channel.ParamOverride != nil && strings.TrimSpace(*channel.ParamOverride) != ""
}

// Valid empty objects and operation arrays preserve byte-stable passthrough.
// Invalid configuration must reach the helper for validation.
func channelParamOverrideActive(channel *dbmodel.Channel) bool {
	if !channelParamOverrideConfigured(channel) {
		return false
	}
	inspection := helper.InspectParamOverride(channel.ParamOverride)
	return inspection.Active
}

func decorateParamOverrideDecision(decision *outbound.CapabilityDecision, inspection helper.ParamOverrideInspection, configured bool) {
	if decision == nil || !configured || !inspection.Valid || !inspection.Active {
		return
	}
	if !containsString(decision.RequiredFeatures, "param_override") {
		decision.RequiredFeatures = append(decision.RequiredFeatures, "param_override")
	}
	if !containsString(decision.ConversionPath, "wire_override") {
		decision.ConversionPath = append(decision.ConversionPath, "wire_override")
	}
	// Once bytes are patched after the protocol builder, native byte stability
	// can no longer be claimed even when ingress and egress formats match.
	if decision.StaticQuality == outbound.QualityNative {
		decision.StaticQuality = outbound.QualityConditional
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func planRelayCapability(req *relayRequest, channel *dbmodel.Channel, adapter model.Outbound, modelName string) outbound.CapabilityDecision {
	if req == nil || req.internalRequest == nil {
		return transformer.PlanRequestForModel(nil, "", outbound.OutboundType(0), false)
	}
	if req.capabilityPlanner == nil {
		// Direct/unit callers may construct relayRequest themselves. Lazily
		// attach the same request-scoped cache used by the main handlers.
		req.capabilityPlanner = newRelayCapabilityPlanner(req.internalRequest, req.rawBody, req.c == nil)
	}
	return req.capabilityPlanner.plan(channel, adapter, modelName)
}

func planRelayPassthrough(request *model.InternalLLMRequest, rawBody []byte, channel *dbmodel.Channel, adapter model.Outbound, websocketIngress bool, overrideConfigured ...bool) bool {
	if request == nil || channel == nil || adapter == nil || len(rawBody) == 0 {
		return false
	}
	if channelParamOverrideActive(channel) || (len(overrideConfigured) > 0 && overrideConfigured[0]) {
		return false
	}
	capable, ok := adapter.(model.PassthroughCapable)
	if !ok || !capable.CanPassthrough(request.RawAPIFormat) {
		return false
	}

	passthrough := channel.AllowsPassthrough()
	if request.HasOpenAIResponsesPassthrough() && !websocketIngress {
		passthrough = true
	}
	if request.RawAPIFormat != model.APIFormatOpenAIResponse {
		return passthrough
	}
	if request.IsOpenAIExactReplayRequest() {
		return false
	}
	if websocketIngress {
		return channel.Type == outbound.OutboundTypeOpenAIResponse &&
			shouldEnableResponsesWS(channel) && effectiveResponsesWSMode(channel) == responsesWSModePassthrough
	}
	if requiresUpstreamWSContinuation(request) {
		return false
	}
	return passthrough
}

const (
	capabilityRankNative = iota
	capabilityRankCompatible
	capabilityRankDegraded
	capabilityRankUnknownFieldFallback
	capabilityRankNativeSemanticFallback
	capabilityRankRejected
)

func capabilityRank(decision outbound.CapabilityDecision) int {
	if decision.Rejected() {
		return capabilityRankRejected
	}
	// Keep native semantics and unknown fields ahead of availability fallbacks,
	// even if a preserving channel needs other repairs. Balancer preferences
	// stay within each quality tier.
	fallbackRank := 0
	for _, loss := range decision.Losses {
		if loss.IsNativeSemanticLoss() {
			return capabilityRankNativeSemanticFallback
		}
		if loss.IsUnknownTopLevelFieldDrop() {
			fallbackRank = capabilityRankUnknownFieldFallback
		}
	}
	if fallbackRank != 0 {
		return fallbackRank
	}
	if decision.Status == outbound.CapabilityDegraded {
		return capabilityRankDegraded
	}
	if decision.StaticQuality == outbound.QualityNative {
		return capabilityRankNative
	}
	return capabilityRankCompatible
}
