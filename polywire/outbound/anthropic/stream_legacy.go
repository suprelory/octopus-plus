package anthropic

import (
	"context"

	"github.com/suprelory/octopus-plus/polywire/model"
)

// TransformStream retains the chunk interface using the canonical event parser.
func (o *MessageOutbound) TransformStream(ctx context.Context, eventData []byte) (*model.InternalLLMResponse, error) {
	events, err := o.TransformSourceEvent(ctx, model.SourceEvent{Data: eventData})
	if err != nil {
		return nil, err
	}
	return model.InternalResponseFromStreamEvents(events), nil
}
