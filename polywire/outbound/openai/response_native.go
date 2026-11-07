package openai

import (
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func responseProgressEvent(event ResponsesStreamEvent, base model.StreamEvent) model.StreamEvent {
	base.Kind = model.StreamEventKindMessageMetadata
	base.Metadata = &model.StreamMessageMetadata{}
	if event.Response != nil {
		base.Metadata.Created = event.Response.CreatedAt
		base.Metadata.ServiceTier = event.Response.ServiceTier
		base.Metadata.ProviderMetadata = event.Response.Metadata
		if event.Response.Status != nil {
			base.Metadata.Status = *event.Response.Status
		}
	}
	return base
}

func (o *ResponseOutbound) responsesNativeEvents(event ResponsesStreamEvent, base model.StreamEvent) []model.StreamEvent {
	var events []model.StreamEvent
	native := &model.StreamNativeEvent{Type: event.Type, OutputIndex: &event.OutputIndex, ContentIndex: event.ContentIndex, Arguments: event.Delta}
	if native.Arguments == "" {
		native.Arguments = event.Arguments
	}
	if item, ok := o.outputItems[event.OutputIndex]; ok && event.Item == nil && (strings.HasPrefix(event.Type, "response.mcp_") || strings.Contains(event.Type, "_call.")) {
		native.ID, native.CallID, native.Name, native.ServerLabel = item.ID, item.CallID, item.Name, item.ServerLabel
	}
	if event.ItemID != nil {
		native.ID = *event.ItemID
	}
	if event.Item != nil {
		native.Type, native.ID, native.CallID = event.Item.Type, event.Item.ID, event.Item.CallID
		native.Name, native.ServerLabel, native.Payload = event.Item.Name, event.Item.ServerLabel, event.Item.Raw
		native.Arguments = event.Item.Arguments
		if event.Item.Status != nil {
			native.Status = *event.Item.Status
		}
	}
	switch event.Type {
	case "response.created":
		start := base
		start.Kind, start.Native = model.StreamEventKindResponseStart, &model.StreamNativeEvent{Type: "response", ID: base.ID, Phase: "start"}
		events = append(events, start, responseProgressEvent(event, base))
	case "response.in_progress":
		events = append(events, responseProgressEvent(event, base))
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		stop := base
		stop.Kind, stop.Native = model.StreamEventKindResponseStop, &model.StreamNativeEvent{Type: "response", ID: base.ID, Phase: "stop"}
		stop.Terminal, stop.TerminalEvent = true, event.Type
		events = append(events, stop)
	case "response.output_item.added", "response.output_item.done":
		item := base
		item.Kind, native.Phase = model.StreamEventKindOutputItemStart, "start"
		if event.Type == "response.output_item.done" {
			item.Kind, native.Phase = model.StreamEventKindOutputItemStop, "stop"
		}
		item.Native = native
		events = append(events, item)
	}
	var kind model.StreamEventKind
	switch {
	case strings.HasPrefix(native.Type, "mcp_") || strings.HasPrefix(event.Type, "response.mcp_"):
		kind = model.StreamEventKindMCPCall
	case strings.HasPrefix(native.Type, "computer_") || strings.HasPrefix(event.Type, "response.computer_"):
		kind = model.StreamEventKindComputerUse
	case isNativeServerTool(native.Type) || strings.HasPrefix(event.Type, "response.web_search_call.") || strings.HasPrefix(event.Type, "response.file_search_call.") || strings.HasPrefix(event.Type, "response.code_interpreter_call.") || strings.HasPrefix(event.Type, "response.image_generation_call."):
		kind = model.StreamEventKindServerTool
	case event.Item != nil && native.Type != "message" && native.Type != "function_call" && native.Type != "reasoning":
		kind = model.StreamEventKindOpaque
	}
	if kind != "" {
		tool := base
		copyNative := *native
		copyNative.Required = true
		if copyNative.Phase == "" {
			copyNative.Phase = "delta"
			if strings.HasSuffix(event.Type, ".done") || strings.HasSuffix(event.Type, ".completed") || strings.HasSuffix(event.Type, ".failed") {
				copyNative.Phase = "stop"
			}
		}
		tool.Kind, tool.Native = kind, &copyNative
		if kind == model.StreamEventKindOpaque && event.Item != nil {
			tool.Opaque = event.Item.Raw
		}
		events = append(events, tool)
	}
	return events
}

func isNativeServerTool(kind string) bool {
	switch kind {
	case "web_search_call", "file_search_call", "code_interpreter_call", "image_generation_call", "shell_call", "local_shell_call", "apply_patch_call":
		return true
	}
	return false
}
