package openai

import (
	"context"
	"encoding/json"

	"github.com/suprelory/octopus-plus/polywire/model"
)

type ChatInbound struct {
	streamAggregator model.StreamAggregator
	// storedResponse stores the non-stream response
	storedResponse *model.InternalLLMResponse
}

func (i *ChatInbound) TransformRequest(ctx context.Context, body []byte) (*model.InternalLLMRequest, error) {
	var request model.InternalLLMRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	if err := request.CaptureFieldPresence(body); err != nil {
		return nil, err
	}
	// O-H2: tag the origin so outbound transformers (raw passthrough,
	// alternation enforcement, schema conversion) can tell a Chat request
	// apart from a Responses request.
	request.RawAPIFormat = model.APIFormatOpenAIChatCompletion
	request.RequestType = model.RequestTypeChat
	if err := request.NormalizeOperation(); err != nil {
		return nil, err
	}
	if err := request.CaptureRequestRecovery(body, request); err != nil {
		return nil, err
	}
	return &request, nil
}

func (i *ChatInbound) TransformResponse(ctx context.Context, response *model.InternalLLMResponse) ([]byte, error) {
	// Store the response for later retrieval
	i.storedResponse = response

	body, err := json.Marshal(chatResponseForWire(response))
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (i *ChatInbound) TransformStream(ctx context.Context, stream *model.InternalLLMResponse) ([]byte, error) {
	return i.TransformStreamEvents(ctx, model.StreamEventsFromInternalResponse(stream))
}

func (i *ChatInbound) encodeStreamChunk(stream *model.InternalLLMResponse) ([]byte, error) {
	if stream.Object == "[DONE]" {
		return []byte("data: [DONE]\n\n"), nil
	}

	// Store the chunk for aggregation
	i.streamAggregator.Add(stream)
	stream = chatResponseForWire(stream)

	var body []byte
	var err error

	// Handle the case where choices are empty but we need them to be present as an empty array
	// This is to satisfy some clients (like Cherry Studio) that require choices field to be present
	if len(stream.Choices) == 0 && stream.Object == "chat.completion.chunk" {
		type Alias model.InternalLLMResponse
		aux := &struct {
			*Alias
			Choices []model.Choice `json:"choices"`
		}{
			Alias:   (*Alias)(stream),
			Choices: []model.Choice{},
		}
		body, err = json.Marshal(aux)
	} else {
		body, err = json.Marshal(stream)
	}

	if err != nil {
		return nil, err
	}
	return []byte("data: " + string(body) + "\n\n"), nil
}

func (i *ChatInbound) TransformStreamEvents(ctx context.Context, events []model.StreamEvent) ([]byte, error) {
	if err := model.ValidateNativeStreamEncoding(events, model.APIFormatOpenAIChatCompletion); err != nil {
		return nil, err
	}
	var result []byte
	var batch []model.StreamEvent
	flush := func() error {
		stream := model.InternalResponseFromStreamEvents(batch)
		batch = batch[:0]
		if stream == nil {
			return nil
		}
		encoded, err := i.encodeStreamChunk(stream)
		result = append(result, encoded...)
		return err
	}
	for _, event := range events {
		if event.Kind == model.StreamEventKindDone {
			if err := flush(); err != nil {
				return nil, err
			}
			result = append(result, []byte("data: [DONE]\n\n")...)
		} else {
			batch = append(batch, event)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return result, nil
}

// GetInternalResponse returns the complete internal response for logging, statistics, etc.
// For streaming: aggregates all stored stream chunks into a complete response
// For non-streaming: returns the stored response
func (i *ChatInbound) GetInternalResponse(ctx context.Context) (*model.InternalLLMResponse, error) {
	if i.storedResponse != nil {
		return i.storedResponse, nil
	}
	return i.streamAggregator.BuildAndReset(), nil
}
