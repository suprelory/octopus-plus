package anthropic

import (
	"context"

	"github.com/suprelory/octopus-plus/polywire/model"
)

// TransformStream retains the chunk interface using the canonical event encoder.
func (i *MessagesInbound) TransformStream(ctx context.Context, stream *model.InternalLLMResponse) ([]byte, error) {
	return i.TransformStreamEvents(ctx, model.StreamEventsFromInternalResponse(stream))
}
