package openai

import (
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

// Keep native content in the stored response, but only expose content that
// Chat Completions can represent. Server-side calls do not ask the client to
// execute a function and must not become tool_calls or empty native blocks.
func chatResponseForWire(response *model.InternalLLMResponse) *model.InternalLLMResponse {
	if response == nil {
		return nil
	}
	wire := *response
	wire.Choices = append([]model.Choice(nil), response.Choices...)
	for index := range wire.Choices {
		wire.Choices[index].Message = chatMessageForWire(wire.Choices[index].Message)
		wire.Choices[index].Delta = chatMessageForWire(wire.Choices[index].Delta)
		if len(wire.Choices[index].Citations) > 0 {
			message := wire.Choices[index].Delta
			if message == nil {
				message = wire.Choices[index].Message
			}
			if message != nil {
				cloned := *message
				cloned.Annotations = nil
				for _, citation := range wire.Choices[index].Citations {
					if raw, err := model.OpenAICitationForWire(citation, model.APIFormatOpenAIChatCompletion); err == nil {
						cloned.Annotations = append(cloned.Annotations, raw)
					}
				}
				if wire.Choices[index].Delta != nil {
					wire.Choices[index].Delta = &cloned
				} else {
					wire.Choices[index].Message = &cloned
				}
			}
		}
	}
	return &wire
}

func chatMessageForWire(message *model.Message) *model.Message {
	if message == nil {
		return nil
	}
	needsProjection := false
	for _, part := range message.Content.MultipleContent {
		if part.Type == "server_tool_use" || part.Type == "server_tool_result" || part.BlockIndex != nil || len(part.Citations) > 0 {
			needsProjection = true
			break
		}
	}
	if !needsProjection {
		return message
	}
	wire := *message
	var parts []model.MessageContentPart
	var text strings.Builder
	textOnly := true
	for _, part := range message.Content.MultipleContent {
		if part.Type == "server_tool_use" || part.Type == "server_tool_result" {
			continue
		}
		parts = append(parts, part)
		if part.Type == "text" {
			if part.Text != nil {
				text.WriteString(*part.Text)
			}
		} else {
			textOnly = false
		}
	}
	if textOnly {
		wire.Content = model.MessageContent{Content: message.Content.Content}
		if len(parts) > 0 {
			value := text.String()
			wire.Content.Content = &value
		}
	} else {
		wire.Content = model.MessageContent{MultipleContent: parts}
	}
	return &wire
}
