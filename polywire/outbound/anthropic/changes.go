package anthropic

import (
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/compat"
	"github.com/suprelory/octopus-plus/polywire/model"
	anthropicModel "github.com/suprelory/octopus-plus/polywire/protocol/anthropic"
)

func (o *MessageOutbound) prepareAnthropicRequest(request *model.InternalLLMRequest, effectiveModel string) (*model.InternalLLMRequest, *anthropicModel.MessageRequest, []model.RequestTransformationChange) {
	if request == nil {
		return nil, nil, nil
	}
	prepared := request.Clone()
	if modelName := strings.TrimSpace(effectiveModel); modelName != "" {
		prepared.Model = modelName
	}
	prepared.NormalizeMessages()
	messages, alternation := model.EnforceAlternationWithReport(prepared.ConversationMessages(), model.AlternationProviderAnthropic)
	prepared.SetConversationMessages(messages)
	messageCountBeforePatch := len(prepared.ConversationMessages())
	compat.PatchAnthropicRequest(prepared)

	wire := o.convertToAnthropicRequestUnpruned(prepared)
	changes := alternation.RequestChanges("Anthropic")
	if len(prepared.ConversationMessages()) > messageCountBeforePatch {
		changes = append(changes, model.RequestTransformationChange{
			Field:  "messages",
			Action: model.RequestTransformationRepair,
			Reason: "Anthropic inserts synthetic tool_result blocks for orphaned tool calls",
		})
	}
	for _, field := range pruneCacheBreakpoints(wire) {
		changes = append(changes, model.RequestTransformationChange{
			Field:  field,
			Action: model.RequestTransformationTruncate,
			Reason: fmt.Sprintf("Anthropic keeps only the first %d cache_control breakpoints", model.AnthropicMaxCacheBreakpoints),
		})
	}
	return prepared, wire, changes
}

func (o *MessageOutbound) DescribeRequestChanges(request *model.InternalLLMRequest, effectiveModel string) []model.RequestTransformationChange {
	if request == nil {
		return nil
	}
	_, _, changes := o.prepareAnthropicRequest(request, effectiveModel)
	return changes
}
