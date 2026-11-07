package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/helper"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/internal/transformer"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

type RoutingPreviewRequest struct {
	GroupID  int             `json:"group_id"`
	APIKeyID int             `json:"api_key_id"`
	Endpoint string          `json:"endpoint"`
	Headers  http.Header     `json:"headers"`
	Request  json.RawMessage `json:"request"`
}

type RoutingPreviewCandidate struct {
	Item              dbmodel.GroupItem                `json:"item"`
	ChannelName       string                           `json:"channel_name"`
	Order             int                              `json:"order"`
	Eligible          bool                             `json:"eligible"`
	Reason            string                           `json:"reason"`
	SelectionReason   string                           `json:"selection_reason,omitempty"`
	Strategy          string                           `json:"strategy,omitempty"`
	QualityRank       int                              `json:"quality_rank"`
	CapabilityStatus  string                           `json:"capability_status,omitempty"`
	CapabilityReasons []string                         `json:"capability_reasons,omitempty"`
	ConversionPath    []string                         `json:"conversion_path,omitempty"`
	HealthyKeys       int                              `json:"healthy_keys"`
	BlockedKeys       int                              `json:"blocked_keys"`
	RetryAt           *time.Time                       `json:"retry_at,omitempty"`
	Metrics           *dbmodel.ChannelSelectionMetrics `json:"metrics,omitempty"`
}

type RoutingPreview struct {
	GroupID    int                       `json:"group_id"`
	Model      string                    `json:"model"`
	Affinity   balancer.AffinityOptions  `json:"affinity"`
	Budget     *dbmodel.RoutingSummary   `json:"budget"`
	Candidates []RoutingPreviewCandidate `json:"candidates"`
	Warnings   []string                  `json:"warnings"`
}

// PreviewRouting is an admin-only simulation. It never creates an upstream
// request, claims a key/probe, updates affinity, or consumes a scheduler turn.
func PreviewRouting(ctx context.Context, input RoutingPreviewRequest) (*RoutingPreview, error) {
	if input.GroupID <= 0 || input.APIKeyID < 0 {
		return nil, fmt.Errorf("invalid group or API key ID")
	}
	if len(input.Request) > 1<<20 {
		return nil, fmt.Errorf("preview request exceeds 1 MiB")
	}
	groups, err := op.GroupList(ctx)
	if err != nil {
		return nil, err
	}
	var group dbmodel.Group
	for _, candidate := range groups {
		if candidate.ID == input.GroupID {
			group = candidate
			break
		}
	}
	if group.ID == 0 {
		return nil, fmt.Errorf("group not found")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(input.Request, &payload); err != nil || payload == nil {
		return nil, fmt.Errorf("request must be a JSON object")
	}
	requestModel := group.Name
	if model, ok := payload["model"]; ok {
		requestModel = ""
		if json.Unmarshal(model, &requestModel) != nil || strings.TrimSpace(requestModel) == "" {
			return nil, fmt.Errorf("model must be a nonempty string")
		}
	}
	if requestModel != group.Name {
		return nil, fmt.Errorf("request model must match the selected group")
	}
	payload["model"], _ = json.Marshal(requestModel)
	if input.Endpoint == "websocket" {
		payload["stream"] = json.RawMessage("true")
	}
	body, _ := json.Marshal(payload)
	headers := make(http.Header, len(input.Headers))
	for name, values := range input.Headers {
		for _, value := range values {
			headers.Add(name, value)
		}
	}
	option := resolveRequestAffinity(headers, body)
	result := &RoutingPreview{GroupID: group.ID, Model: requestModel, Affinity: option, Candidates: []RoutingPreviewCandidate{}, Warnings: []string{}}
	if raw, exists := payload["previous_response_id"]; exists {
		var previous string
		if json.Unmarshal(raw, &previous) != nil {
			return nil, fmt.Errorf("previous_response_id must be a string")
		}
		if strings.TrimSpace(previous) != "" {
			return nil, fmt.Errorf("preview accepts stateless requests only; previous_response_id uses separate recovery rules")
		}
	}
	if input.Endpoint == "compact" && len(payload["input"]) == 0 {
		return nil, fmt.Errorf("compact preview requires input")
	}
	operation := ""
	var planner *relayCapabilityPlanner
	var quality balancer.QualityRanker
	snapshot := newCandidateSnapshot(ctx, group)
	switch input.Endpoint {
	case "images":
		operation = outbound.RelayOperationImages
	case "compact":
		operation = outbound.RelayOperationResponsesCompact
	default:
		types := map[string]inbound.InboundType{"chat": inbound.InboundTypeOpenAIChat, "responses": inbound.InboundTypeOpenAIResponse, "messages": inbound.InboundTypeAnthropic, "embeddings": inbound.InboundTypeOpenAIEmbedding, "websocket": inbound.InboundTypeOpenAIResponse}
		typ, ok := types[input.Endpoint]
		if !ok {
			return nil, fmt.Errorf("unsupported preview endpoint")
		}
		request, err := transformer.Inbound(typ).TransformRequest(ctx, body)
		if err != nil {
			return nil, fmt.Errorf("invalid simulated request: %w", err)
		}
		if err := request.Validate(); err != nil {
			return nil, err
		}
		planner = newRelayCapabilityPlanner(request, body, input.Endpoint == "websocket")
		quality = func(item dbmodel.GroupItem) int {
			channel, _ := snapshot.Channel(item.ChannelID)
			return planner.rankChannel(channel, item)
		}
	}
	execution := newRelayExecution(group, true)
	if operation != "" {
		execution = newOperationExecution(group, input.Endpoint)
	}
	result.Budget = execution.routingSummary(false, nil)
	policy := getCapabilityDegradationPolicy()
	eligibleGroup := group
	eligibleGroup.Items = nil
	preExcluded := make(map[int]string)
	for _, item := range group.Items {
		channel, err := snapshot.Channel(item.ChannelID)
		if err != nil {
			preExcluded[item.ID] = "channel_not_found"
		} else if !channel.Enabled {
			preExcluded[item.ID] = "channel_disabled"
		} else if transformer.Outbound(channel.Type) == nil {
			preExcluded[item.ID] = "unsupported_channel_type"
		} else {
			eligibleGroup.Items = append(eligibleGroup.Items, item)
		}
		if item.Weight <= 0 && group.Mode == dbmodel.GroupModeWeighted {
			result.Warnings = append(result.Warnings, fmt.Sprintf("item %d has nonpositive weight; effective weight is 1", item.ID))
		}
		if strings.TrimSpace(item.ModelName) == "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("item %d has an empty upstream model", item.ID))
		}
	}
	it := balancer.NewPreviewIterator(eligibleGroup, input.APIKeyID, requestModel, quality, option)
	defer it.Close()
	seen := make(map[int]bool)
	for it.Next() {
		item := it.Item()
		seen[item.ID] = true
		channel, _ := snapshot.Channel(item.ChannelID)
		row := RoutingPreviewCandidate{Item: item, ChannelName: channel.Name, Order: it.Index() + 1, SelectionReason: it.SelectionReason(), Strategy: it.SelectionStrategy(), QualityRank: it.QualityRank(), Metrics: it.SelectionMetrics(), Reason: "eligible", Eligible: true}
		var decision outbound.CapabilityDecision
		if planner != nil {
			decision = planner.plan(channel, transformer.Outbound(channel.Type), item.ModelName)
		} else {
			decision = outbound.PlanRelayOperation(channel.Type, operation)
			decorateParamOverrideDecision(&decision, helper.InspectParamOverride(channel.ParamOverride), channelParamOverrideActive(channel))
		}
		row.CapabilityStatus, row.CapabilityReasons, row.ConversionPath = string(decision.Status), decision.Reasons, decision.ConversionPath
		for _, key := range channel.Keys {
			if !key.Enabled || key.ChannelKey == "" {
				continue
			}
			if balancer.CanAttempt(channel.ID, key.ID, item.ModelName) {
				row.HealthyKeys++
			} else {
				row.BlockedKeys++
			}
			if at, ok := balancer.RetryAt(channel.ID, key.ID, item.ModelName); ok && (row.RetryAt == nil || at.Before(*row.RetryAt)) {
				row.RetryAt = &at
			}
		}
		if reject, _ := evaluateCapabilityPolicy(decision, policy); reject {
			row.Reason, row.Eligible = "capability_rejected", false
		} else if inspection := helper.InspectParamOverride(channel.ParamOverride); channelParamOverrideConfigured(channel) && !inspection.Valid {
			row.Reason, row.Eligible = "invalid_param_override", false
		} else if row.HealthyKeys == 0 {
			row.Reason, row.Eligible = "no_available_key", false
		}
		result.Candidates = append(result.Candidates, row)
	}
	for _, item := range group.Items {
		if seen[item.ID] {
			continue
		}
		reason := preExcluded[item.ID]
		if reason == "" {
			reason = "affinity_filtered"
		}
		row := RoutingPreviewCandidate{Item: item, Reason: reason}
		if channel, err := snapshot.Channel(item.ChannelID); err == nil {
			row.ChannelName = channel.Name
		}
		result.Candidates = append(result.Candidates, row)
	}
	if len(group.Items) == 0 {
		result.Warnings = append(result.Warnings, "group has no candidates")
	}
	return result, nil
}
