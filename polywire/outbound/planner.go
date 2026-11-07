package outbound

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/config"
	"github.com/suprelory/octopus-plus/polywire/model"
)

type CapabilityStatus string

const (
	CapabilitySupported CapabilityStatus = "supported"
	CapabilityDegraded  CapabilityStatus = "degraded"
	CapabilityRejected  CapabilityStatus = "rejected"
)

type SemanticFeature string

const (
	FeatureTools            SemanticFeature = "tools"
	FeatureToolChoice       SemanticFeature = "tool_choice"
	FeatureReasoning        SemanticFeature = "reasoning"
	FeatureStructuredOutput SemanticFeature = "structured_output"
	FeatureMultimodal       SemanticFeature = "multimodal"
	FeatureStreamUsage      SemanticFeature = "stream_usage"
)

type CapabilityDecision struct {
	Status           CapabilityStatus
	RequestType      model.RequestType
	InboundFormat    model.APIFormat
	OutboundFormat   model.APIFormat
	ConversionPath   []string
	RequiredFeatures []string
	DegradedFields   []string
	Reasons          []string
	Losses           LossReport
	ConversionReport LossReport
	Conversion       model.RequestConversion
	Lossiness        string
	StaticQuality    ConversionQuality
	Passthrough      bool
}

func (d CapabilityDecision) Rejected() bool {
	return d.Status == CapabilityRejected
}

func (d CapabilityDecision) Summary() string {
	if len(d.Reasons) > 0 {
		return strings.Join(d.Reasons, "; ")
	}
	return string(d.Status)
}

// PlanRequestForModel evaluates protocol-level semantic compatibility before a
// key is selected or request bytes are sent upstream. The planner treats req as
// read-only; effectiveModel is used for provider-family checks whose result
// depends on the selected upstream model.
func PlanRequestForModel(req *model.InternalLLMRequest, effectiveModel string, outboundType OutboundType, passthrough bool) (decision CapabilityDecision) {
	return PlanRequestForModelWithConfig(req, effectiveModel, outboundType, passthrough, config.Config{})
}

// PlanRequestForModelWithConfig uses the supplied dependencies for the local wire
// build. It does not send the request or enforce a host degradation policy.
func PlanRequestForModelWithConfig(req *model.InternalLLMRequest, effectiveModel string, outboundType OutboundType, passthrough bool, cfg config.Config) (decision CapabilityDecision) {
	return planRequestForModel(req, effectiveModel, outboundType, passthrough, cfg, nil)
}

// PrepareRequestForModelWithConfig also retains the actual validated wire body
// for execution. Passthrough, rejected, or non-retargetable requests return no
// prepared body. The caller owns caching and must keep it scoped to this request.
func PrepareRequestForModelWithConfig(req *model.InternalLLMRequest, effectiveModel string, outboundType OutboundType, passthrough bool, cfg config.Config) (CapabilityDecision, *PreparedRequest) {
	var prepared *PreparedRequest
	decision := planRequestForModel(req, effectiveModel, outboundType, passthrough, cfg, &prepared)
	return decision, prepared
}

func planRequestForModel(req *model.InternalLLMRequest, effectiveModel string, outboundType OutboundType, passthrough bool, cfg config.Config, reusable **PreparedRequest) (decision CapabilityDecision) {
	decision = CapabilityDecision{Status: CapabilitySupported, Lossiness: "none"}
	defer func() {
		decision.Conversion = req.ConversionState(decision.OutboundFormat, decision.Passthrough, decision.Status == CapabilityDegraded || decision.Status == CapabilityRejected)
		if decision.Rejected() {
			decision.Conversion.ReplayAvailable = false
			decision.Conversion.ExactReplay = false
		}
	}()
	if req == nil {
		return rejectDecision(decision, "request is nil")
	}
	if err := req.ValidateOperationConsistency(); err != nil {
		return rejectDecision(decision, err.Error())
	}

	decision.RequestType = req.ResolveRequestType()
	decision.InboundFormat = req.RawAPIFormat
	capability, ok := Descriptor(outboundType)
	if !ok {
		return rejectDecision(decision, fmt.Sprintf("unsupported outbound type %d", outboundType))
	}
	decision.OutboundFormat = capability.APIFormat
	decision.StaticQuality = StaticConversionQuality(req.RawAPIFormat, outboundType, decision.RequestType)
	decision.Passthrough = passthrough && SupportsNativeFormat(outboundType, req.RawAPIFormat)
	decision.ConversionPath = conversionPath(req.RawAPIFormat, capability.APIFormat, decision.Passthrough)
	decision.RequiredFeatures = requestedFeatures(req)

	if !capability.Supports(decision.RequestType) {
		return rejectDecision(decision, fmt.Sprintf("channel does not support %s requests", decision.RequestType))
	}
	if err := req.ValidateNativeRecovery(capability.APIFormat, decision.Passthrough); err != nil {
		return rejectDecision(decision, err.Error())
	}
	if decision.Passthrough {
		return decision
	}

	// Adapters already own normalization copies. Only the effective model is
	// changed here, so another deep clone of the full request is unnecessary.
	prepared := *req
	if strings.TrimSpace(effectiveModel) != "" {
		prepared.Model = effectiveModel
	}
	adapter := New(outboundType, cfg)
	wire, report, err := BuildRequest(context.Background(), adapter, outboundType, &prepared, "https://conversion.invalid", "")
	if err != nil {
		return rejectDecision(decision, err.Error())
	}
	defer wire.Body.Close()
	decision = ApplyConversionReport(decision, report)
	if reusable != nil && !decision.Rejected() {
		*reusable = prepareWireRequest(wire, adapter, &prepared, report)
	}
	return decision
}

// ApplyConversionReport records evidence returned by the actual request build.
// Relay calls this again before submission and persists the resulting decision.
func ApplyConversionReport(decision CapabilityDecision, report LossReport) CapabilityDecision {
	decision.ConversionReport = report
	for _, change := range report {
		if !change.IsLossy() {
			continue
		}
		decision.DegradedFields = append(decision.DegradedFields, change.Field)
		decision.Reasons = append(decision.Reasons, change.Reason)
		decision.Losses = append(decision.Losses, change)
		if change.Action == LossActionReject {
			decision.Status = CapabilityRejected
		}
	}
	decision.DegradedFields = uniqueSorted(decision.DegradedFields)
	decision.Reasons = uniqueSorted(decision.Reasons)
	decision.Losses = uniqueLossReports(decision.Losses)
	if len(decision.DegradedFields) > 0 && decision.Status != CapabilityRejected {
		decision.Status = CapabilityDegraded
		decision.Lossiness = "known"
		decision.Conversion.Mode = model.ConversionLossyCanonical
		decision.Conversion.ReplayAvailable, decision.Conversion.ExactReplay = false, false
	}
	if decision.Status == CapabilityRejected {
		decision.Lossiness = "rejected"
	}
	return decision
}

// PlanRelayOperation evaluates auxiliary endpoints that are proxied without
// passing through InternalLLMRequest. These decisions still use the relay-wide
// capability policy and audit trace, while field-level planning remains the
// responsibility of PlanRequestForModel.
func PlanRelayOperation(outboundType OutboundType, operation string) CapabilityDecision {
	decision := CapabilityDecision{
		Status:        CapabilitySupported,
		Lossiness:     "none",
		StaticQuality: QualityNative,
	}
	operation = strings.TrimSpace(operation)
	if operation == "" {
		decision.StaticQuality = QualityUnsupported
		return rejectDecision(decision, "relay operation is empty")
	}
	decision.RequiredFeatures = []string{"relay_operation:" + operation}
	decision.RequestType, decision.InboundFormat = relayOperationRequest(operation)

	descriptor, ok := Descriptor(outboundType)
	if !ok {
		decision.StaticQuality = QualityUnsupported
		return rejectDecision(decision, fmt.Sprintf("unsupported outbound type %d", outboundType))
	}
	decision.OutboundFormat = descriptor.APIFormat
	decision.ConversionPath = []string{operation, descriptor.Name}
	if !descriptor.SupportsRelayOperation(operation) {
		decision.StaticQuality = QualityUnsupported
		return rejectDecision(decision, fmt.Sprintf("channel does not support relay operation %s", operation))
	}
	return decision
}

func relayOperationRequest(operation string) (model.RequestType, model.APIFormat) {
	switch operation {
	case RelayOperationImages:
		return model.RequestTypeImages, model.APIFormatOpenAIImageGeneration
	case RelayOperationResponsesCompact, RelayOperationResponsesWebSocket:
		return model.RequestTypeResponses, model.APIFormatOpenAIResponse
	default:
		return "", ""
	}
}

func rejectDecision(decision CapabilityDecision, reason string) CapabilityDecision {
	decision.Status = CapabilityRejected
	decision.Lossiness = "rejected"
	decision.Reasons = append(decision.Reasons, reason)
	return decision
}

func conversionPath(inboundFormat, outboundFormat model.APIFormat, passthrough bool) []string {
	inbound := string(inboundFormat)
	if inbound == "" {
		inbound = "unknown"
	}
	if passthrough {
		return []string{inbound, "raw_passthrough", string(outboundFormat)}
	}
	return []string{inbound, "canonical", string(outboundFormat)}
}

func requestedFeatures(req *model.InternalLLMRequest) []string {
	features := make([]string, 0, 6)
	if len(req.Tools) > 0 {
		features = append(features, string(FeatureTools))
	}
	if req.ToolChoice != nil {
		features = append(features, string(FeatureToolChoice))
	}
	options := req.GetOpenAIResponsesOptions()
	if req.ReasoningEffort != "" || req.ReasoningBudget != nil || req.AdaptiveThinking || req.EnableThinking != nil || req.Thinking != nil || options.ReasoningSummary != nil || options.ReasoningGenerateSummary != nil {
		features = append(features, string(FeatureReasoning))
	}
	if hasStructuredOutput(req.ResponseFormat) {
		features = append(features, string(FeatureStructuredOutput))
	}
	if hasMultimodalSemantics(req) {
		features = append(features, string(FeatureMultimodal))
	}
	if req.Stream != nil && *req.Stream && (req.StreamOptions == nil || req.StreamOptions.IncludeUsage || req.RawAPIFormat != model.APIFormatOpenAIChatCompletion) {
		features = append(features, string(FeatureStreamUsage))
	}
	return features
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func uniqueLossReports(reports LossReport) LossReport {
	if len(reports) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(reports))
	result := make(LossReport, 0, len(reports))
	for _, report := range reports {
		report.Field = strings.TrimSpace(report.Field)
		report.Reason = strings.TrimSpace(report.Reason)
		if report.Field == "" || report.Action == "" {
			continue
		}
		key := string(report.Action) + "\x00" + report.Field + "\x00" + report.Reason
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, report)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Field != result[j].Field {
			return result[i].Field < result[j].Field
		}
		if result[i].Action != result[j].Action {
			return result[i].Action < result[j].Action
		}
		return result[i].Reason < result[j].Reason
	})
	return result
}
