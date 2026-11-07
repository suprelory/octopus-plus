package openai

import (
	"github.com/samber/lo"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func (i *ResponseInbound) handleReasoningContent(content *string) [][]byte {
	var events [][]byte

	events = append(events, i.ensureReasoningItemStarted()...)

	// Accumulate reasoning content
	i.accumulatedReasoning.WriteString(*content)

	events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
		Type:        "response.reasoning.delta",
		ItemID:      &i.currentItemID,
		OutputIndex: lo.ToPtr(i.outputIndex),
		Delta:       *content,
	}))

	// Emit reasoning_summary_text.delta
	events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
		Type:         "response.reasoning_summary_text.delta",
		ItemID:       &i.currentItemID,
		OutputIndex:  lo.ToPtr(i.outputIndex),
		SummaryIndex: lo.ToPtr(0),
		Delta:        *content,
	}))

	return events
}

func (i *ResponseInbound) ensureReasoningItemStarted() [][]byte {
	if i.hasReasoningItemStarted {
		return nil
	}

	var events [][]byte

	events = append(events, i.closeCurrentOutputItem()...)

	i.hasReasoningItemStarted = true
	i.currentItemID = generateItemID()

	item := &ResponsesItem{
		ID:      i.currentItemID,
		Type:    "reasoning",
		Status:  lo.ToPtr("in_progress"),
		Summary: []ResponsesReasoningSummary{},
	}

	events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: lo.ToPtr(i.outputIndex),
		Item:        item,
	}))

	events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
		Type:         "response.reasoning_summary_part.added",
		ItemID:       &i.currentItemID,
		OutputIndex:  lo.ToPtr(i.outputIndex),
		SummaryIndex: lo.ToPtr(0),
		Part:         &ResponsesContentPart{Type: "summary_text"},
	}))

	return events
}

func (i *ResponseInbound) handleTextContent(content *string) [][]byte {
	var events [][]byte

	// Close reasoning item if it was started
	if i.hasReasoningItemStarted {
		events = append(events, i.closeReasoningItem()...)
	}

	// Close refusal part if active — text becomes a new content part
	if i.hasRefusalPartStarted {
		events = append(events, i.closeCurrentContentPart()...)
	}

	// Start message output item if not started
	if !i.hasMessageItemStarted {
		i.hasMessageItemStarted = true
		i.currentItemID = generateItemID()

		events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
			Type:        "response.output_item.added",
			OutputIndex: lo.ToPtr(i.outputIndex),
			Item: &ResponsesItem{
				ID:      i.currentItemID,
				Type:    "message",
				Status:  lo.ToPtr("in_progress"),
				Role:    "assistant",
				Content: &ResponsesInput{Items: []ResponsesItem{}},
			},
		}))
	}

	// Start content part if not started
	if !i.hasContentPartStarted {
		i.hasContentPartStarted = true
		i.messageContentOrder = append(i.messageContentOrder, "output_text")

		events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
			Type:         "response.content_part.added",
			ItemID:       &i.currentItemID,
			OutputIndex:  lo.ToPtr(i.outputIndex),
			ContentIndex: lo.ToPtr(i.contentIndex),
			Part: &ResponsesContentPart{
				Type: "output_text",
				Text: lo.ToPtr(""),
			},
		}))
	}

	// Accumulate text content
	i.accumulatedText.WriteString(*content)

	// Emit output_text.delta
	events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
		Type:         "response.output_text.delta",
		ItemID:       &i.currentItemID,
		OutputIndex:  lo.ToPtr(i.outputIndex),
		ContentIndex: lo.ToPtr(i.contentIndex),
		Delta:        *content,
	}))

	return events
}

// handleRefusalContent mirrors handleTextContent but emits refusal-family
// stream events (response.content_part.added with Part.Type="refusal",
// response.refusal.delta). Refusal is a distinct content part: if a text
// part was open it is closed first so the two parts land at separate
// content_index values.
func (i *ResponseInbound) handleRefusalContent(content string) [][]byte {
	var events [][]byte

	// Close reasoning item if it was started
	if i.hasReasoningItemStarted {
		events = append(events, i.closeReasoningItem()...)
	}

	// Close text part if active — refusal becomes a new content part
	if i.hasContentPartStarted {
		events = append(events, i.closeCurrentContentPart()...)
	}

	// Start message output item if not started
	if !i.hasMessageItemStarted {
		i.hasMessageItemStarted = true
		i.currentItemID = generateItemID()

		events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
			Type:        "response.output_item.added",
			OutputIndex: lo.ToPtr(i.outputIndex),
			Item: &ResponsesItem{
				ID:      i.currentItemID,
				Type:    "message",
				Status:  lo.ToPtr("in_progress"),
				Role:    "assistant",
				Content: &ResponsesInput{Items: []ResponsesItem{}},
			},
		}))
	}

	// Start refusal content part if not started
	if !i.hasRefusalPartStarted {
		i.hasRefusalPartStarted = true
		i.messageContentOrder = append(i.messageContentOrder, "refusal")

		events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
			Type:         "response.content_part.added",
			ItemID:       &i.currentItemID,
			OutputIndex:  lo.ToPtr(i.outputIndex),
			ContentIndex: lo.ToPtr(i.contentIndex),
			Part: &ResponsesContentPart{
				Type: "refusal",
				Text: lo.ToPtr(""),
			},
		}))
	}

	// Accumulate refusal content
	i.accumulatedRefusal.WriteString(content)

	// Emit refusal.delta
	events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
		Type:         "response.refusal.delta",
		ItemID:       &i.currentItemID,
		OutputIndex:  lo.ToPtr(i.outputIndex),
		ContentIndex: lo.ToPtr(i.contentIndex),
		Delta:        content,
	}))

	return events
}

func (i *ResponseInbound) handleToolCalls(toolCalls []model.ToolCall) [][]byte {
	var events [][]byte

	// Close message item if it was started
	if i.hasMessageItemStarted {
		events = append(events, i.closeMessageItem()...)
	}

	// Close reasoning item if it was started
	if i.hasReasoningItemStarted {
		events = append(events, i.closeReasoningItem()...)
	}

	for _, tc := range toolCalls {
		toolCallIndex := tc.Index

		// Initialize tool call tracking if needed
		if _, ok := i.toolCalls[toolCallIndex]; !ok {
			events = append(events, i.closeCurrentContentPart()...)
			events = append(events, i.closeCurrentOutputItem()...)

			i.toolCalls[toolCallIndex] = &model.ToolCall{
				Index: toolCallIndex,
				ID:    tc.ID,
				Type:  tc.Type,
				Function: model.FunctionCall{
					Name:      tc.Function.Name,
					Namespace: tc.Function.Namespace,
					Arguments: "",
				},
			}

			itemID := tc.ID
			if itemID == "" {
				itemID = generateItemID()
			}

			item := &ResponsesItem{
				ID:        itemID,
				Type:      "function_call",
				Status:    lo.ToPtr("in_progress"),
				CallID:    tc.ID,
				Name:      tc.Function.Name,
				Namespace: tc.Function.Namespace,
			}

			events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
				Type:        "response.output_item.added",
				OutputIndex: lo.ToPtr(i.outputIndex),
				Item:        item,
			}))

			i.toolCallItemStarted[toolCallIndex] = true
			i.toolCallOutputIndex[toolCallIndex] = i.outputIndex
			i.currentItemID = itemID
			i.outputIndex++
		}

		// Accumulate arguments
		if tc.ID != "" {
			i.toolCalls[toolCallIndex].ID = tc.ID
		}
		if tc.Function.Name != "" {
			i.toolCalls[toolCallIndex].Function.Name = tc.Function.Name
		}
		if tc.Function.Namespace != "" {
			i.toolCalls[toolCallIndex].Function.Namespace = tc.Function.Namespace
		}
		i.toolCalls[toolCallIndex].Function.Arguments += tc.Function.Arguments

		// Emit function_call_arguments.delta
		if tc.Function.Arguments != "" {
			itemID := i.toolCalls[toolCallIndex].ID
			if itemID == "" {
				itemID = i.currentItemID
			}

			events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
				Type:         "response.function_call_arguments.delta",
				ItemID:       &itemID,
				OutputIndex:  lo.ToPtr(i.toolCallOutputIndex[toolCallIndex]),
				ContentIndex: lo.ToPtr(0),
				Delta:        tc.Function.Arguments,
			}))
		}
	}

	return events
}

func (i *ResponseInbound) closeReasoningItem() [][]byte {
	if !i.hasReasoningItemStarted {
		return nil
	}

	var events [][]byte
	i.hasReasoningItemStarted = false
	fullReasoning := i.accumulatedReasoning.String()

	// Emit reasoning_summary_text.done
	events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
		Type:         "response.reasoning_summary_text.done",
		ItemID:       &i.currentItemID,
		OutputIndex:  lo.ToPtr(i.outputIndex),
		SummaryIndex: lo.ToPtr(0),
		Text:         fullReasoning,
	}))

	// Emit reasoning_summary_part.done
	events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
		Type:         "response.reasoning_summary_part.done",
		ItemID:       &i.currentItemID,
		OutputIndex:  lo.ToPtr(i.outputIndex),
		SummaryIndex: lo.ToPtr(0),
		Part:         &ResponsesContentPart{Type: "summary_text", Text: &fullReasoning},
	}))

	events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
		Type:        "response.reasoning.done",
		ItemID:      &i.currentItemID,
		OutputIndex: lo.ToPtr(i.outputIndex),
		Text:        fullReasoning,
	}))

	item := ResponsesItem{
		ID:   i.currentItemID,
		Type: "reasoning",
		Summary: []ResponsesReasoningSummary{{
			Type: "summary_text",
			Text: fullReasoning,
		}},
	}

	if len(i.reasoningBlockSignatures) > 0 {
		sig := i.reasoningBlockSignatures[0]
		item.EncryptedContent = &sig
	}

	events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
		Type:        "response.output_item.done",
		OutputIndex: lo.ToPtr(i.outputIndex),
		Item:        &item,
	}))

	i.completedOutputItems = append(i.completedOutputItems, item)
	i.outputIndex++
	i.accumulatedReasoning.Reset()
	i.reasoningBlockSignatures = nil

	return events
}

func (i *ResponseInbound) closeMessageItem() [][]byte {
	if !i.hasMessageItemStarted {
		return nil
	}

	var events [][]byte
	i.hasMessageItemStarted = false

	// Close whichever content part (text or refusal) is still open
	events = append(events, i.closeCurrentContentPart()...)

	fullText := i.accumulatedText.String()
	fullRefusal := i.accumulatedRefusal.String()

	contentItems := make([]ResponsesItem, 0, 2)
	for contentIndex, t := range i.messageContentOrder {
		switch t {
		case "output_text":
			if fullText != "" {
				text := fullText
				contentItems = append(contentItems, ResponsesItem{
					Type:        "output_text",
					Text:        &text,
					Annotations: i.annotationsAt(contentIndex),
				})
			}
		case "refusal":
			if fullRefusal != "" {
				refusal := fullRefusal
				contentItems = append(contentItems, ResponsesItem{
					Type:    "refusal",
					Refusal: &refusal,
				})
			}
		}
	}

	// Preserve legacy shape: a message with no accumulated text still
	// produces a single empty output_text item so downstream clients never
	// see a zero-length content array.
	if len(contentItems) == 0 {
		contentItems = append(contentItems, ResponsesItem{
			Type:        "output_text",
			Text:        lo.ToPtr(fullText),
			Annotations: i.annotationsAt(0),
		})
	}

	item := ResponsesItem{
		ID:      i.currentItemID,
		Type:    "message",
		Status:  lo.ToPtr("completed"),
		Role:    "assistant",
		Content: &ResponsesInput{Items: contentItems},
	}

	events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
		Type:        "response.output_item.done",
		OutputIndex: lo.ToPtr(i.outputIndex),
		Item:        &item,
	}))

	i.completedOutputItems = append(i.completedOutputItems, item)
	i.outputIndex++
	i.contentIndex = 0
	i.accumulatedText.Reset()
	i.accumulatedRefusal.Reset()
	i.messageContentOrder = nil
	i.messageAnnotations = nil

	return events
}

// closeCurrentContentPart flushes whichever content part (output_text or
// refusal) is currently open. The accumulated text for the part is NOT
// reset here — closeMessageItem reads both accumulators to build the final
// output_item.done content array, then resets at message level.
func (i *ResponseInbound) closeCurrentContentPart() [][]byte {
	var events [][]byte

	switch {
	case i.hasContentPartStarted:
		i.hasContentPartStarted = false
		fullText := i.accumulatedText.String()

		events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
			Type:         "response.output_text.done",
			ItemID:       &i.currentItemID,
			OutputIndex:  lo.ToPtr(i.outputIndex),
			ContentIndex: lo.ToPtr(i.contentIndex),
			Text:         fullText,
		}))

		events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
			Type:         "response.content_part.done",
			ItemID:       &i.currentItemID,
			OutputIndex:  lo.ToPtr(i.outputIndex),
			ContentIndex: lo.ToPtr(i.contentIndex),
			Part: &ResponsesContentPart{
				Type:        "output_text",
				Text:        lo.ToPtr(fullText),
				Annotations: i.messageAnnotations[i.contentIndex],
			},
		}))

		i.contentIndex++

	case i.hasRefusalPartStarted:
		i.hasRefusalPartStarted = false
		fullRefusal := i.accumulatedRefusal.String()

		events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
			Type:         "response.refusal.done",
			ItemID:       &i.currentItemID,
			OutputIndex:  lo.ToPtr(i.outputIndex),
			ContentIndex: lo.ToPtr(i.contentIndex),
			Text:         fullRefusal,
		}))

		events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
			Type:         "response.content_part.done",
			ItemID:       &i.currentItemID,
			OutputIndex:  lo.ToPtr(i.outputIndex),
			ContentIndex: lo.ToPtr(i.contentIndex),
			Part: &ResponsesContentPart{
				Type: "refusal",
				Text: lo.ToPtr(fullRefusal),
			},
		}))

		i.contentIndex++
	}

	return events
}

func (i *ResponseInbound) annotationsAt(index int) *[]ResponsesAnnotation {
	annotations := i.messageAnnotations[index]
	if len(annotations) == 0 {
		return nil
	}
	return &annotations
}

func (i *ResponseInbound) closeCurrentOutputItem() [][]byte {
	var events [][]byte

	// Close message item if open
	if i.hasMessageItemStarted {
		events = append(events, i.closeMessageItem()...)
	}

	// Close reasoning item if open
	if i.hasReasoningItemStarted {
		events = append(events, i.closeReasoningItem()...)
	}

	// Close any open tool call items
	for idx, tc := range i.toolCalls {
		if i.toolCallItemStarted[idx] {
			itemID := tc.ID
			if itemID == "" {
				itemID = i.currentItemID
			}

			// Emit function_call_arguments.done
			toolCallOutputIdx := i.toolCallOutputIndex[idx]
			events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
				Type:        "response.function_call_arguments.done",
				ItemID:      &itemID,
				OutputIndex: &toolCallOutputIdx,
				CallID:      tc.ID,
				Name:        tc.Function.Name,
				Namespace:   tc.Function.Namespace,
				Arguments:   tc.Function.Arguments,
			}))

			// Emit output_item.done
			item := ResponsesItem{
				ID:        itemID,
				Type:      "function_call",
				Status:    lo.ToPtr("completed"),
				CallID:    tc.ID,
				Name:      tc.Function.Name,
				Namespace: tc.Function.Namespace,
				Arguments: tc.Function.Arguments,
			}

			events = append(events, i.enqueueEvent(&ResponsesStreamEvent{
				Type:        "response.output_item.done",
				OutputIndex: &toolCallOutputIdx,
				Item:        &item,
			}))

			i.completedOutputItems = append(i.completedOutputItems, item)
			i.toolCallItemStarted[idx] = false
		}
	}

	return events
}

// finalOutputItems returns the accumulated output items for response.completed,
// synthesizing an empty message shell when nothing was emitted. The Responses
// spec requires a non-empty output on terminal events.
func (i *ResponseInbound) finalOutputItems() []ResponsesItem {
	if len(i.completedOutputItems) > 0 {
		out := make([]ResponsesItem, len(i.completedOutputItems))
		copy(out, i.completedOutputItems)
		return out
	}
	emptyText := ""
	return []ResponsesItem{
		{
			ID:   generateItemID(),
			Type: "message",
			Role: "assistant",
			Content: &ResponsesInput{
				Items: []ResponsesItem{
					{Type: "output_text", Text: &emptyText},
				},
			},
			Status: lo.ToPtr("completed"),
		},
	}
}
