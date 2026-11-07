package streamio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

type SSEEventObserver func(context.Context, string, []byte) error
type SourceEventObserver func(context.Context, SourceEvent) error

// IncrementalSSEObserver shares framing with SSESource. A stateless preview may
// release semantic precommit before a blank line; the canonical observer only
// sees the final envelope, including trailing IDs and additional data lines.
type IncrementalSSEObserver struct {
	decoder     *SSEDecoder
	observe     SourceEventObserver
	terminal    map[string]struct{}
	inspector   model.SourceEventInspector
	memo        SourceEvent
	preview     model.StreamEventPreview
	terminalHit bool
	finalized   bool
	legacy      bool
}

func NewIncrementalSSEObserver(maxEventSize int, terminal map[string]struct{}, observe SSEEventObserver) *IncrementalSSEObserver {
	var callback SourceEventObserver
	if observe != nil {
		callback = func(ctx context.Context, event SourceEvent) error { return observe(ctx, event.Type, event.Data) }
	}
	observer := NewIncrementalSourceEventObserver(maxEventSize, terminal, callback)
	observer.legacy = true
	return observer
}

func NewIncrementalSourceEventObserver(maxEventSize int, terminal map[string]struct{}, observe SourceEventObserver) *IncrementalSSEObserver {
	return &IncrementalSSEObserver{decoder: NewSSEDecoder(maxEventSize), terminal: terminal, observe: observe}
}

func (o *IncrementalSSEObserver) SetSourceInspector(inspector model.SourceEventInspector) {
	o.inspector = inspector
}

func (o *IncrementalSSEObserver) inspect(ctx context.Context, event SourceEvent) (SourceEvent, model.StreamEventPreview, error) {
	if o.memo.Decoded != nil && bytes.Equal(event.Data, o.memo.Data) {
		event.Decoded = o.memo.Decoded
	}
	if o.inspector != nil {
		return o.inspector.InspectSourceEvent(ctx, event)
	}
	preview := model.StreamEventPreview{EventType: strings.TrimSpace(event.Type)}
	if preview.EventType == "" && (o.legacy || len(o.terminal) > 0) {
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(event.Data, &envelope) == nil {
			preview.EventType = strings.TrimSpace(envelope.Type)
		}
	}
	return event, preview, nil
}

func (o *IncrementalSSEObserver) dispatch(ctx context.Context, event SourceEvent) error {
	event, preview, err := o.inspect(ctx, event)
	o.memo, o.preview = SourceEvent{}, model.StreamEventPreview{}
	if err != nil {
		return err
	}
	_, terminal := o.terminal[preview.EventType]
	if o.legacy {
		event.Type = preview.EventType
	}
	if o.observe != nil {
		if err := o.observe(ctx, event); err != nil {
			return err
		}
	}
	o.terminalHit = o.terminalHit || terminal || preview.Terminal
	return nil
}

func (o *IncrementalSSEObserver) Observe(ctx context.Context, chunk []byte) error {
	if o == nil || len(chunk) == 0 {
		return nil
	}
	if o.finalized {
		return fmt.Errorf("SSE observer already finalized")
	}
	if err := o.decoder.Feed(chunk, func(event SourceEvent) error { return o.dispatch(ctx, event) }); err != nil {
		return err
	}
	o.preview = model.StreamEventPreview{}
	if o.inspector != nil {
		if pending, ok := o.decoder.Pending(); ok {
			event, preview, err := o.inspect(ctx, pending)
			if err == nil {
				o.memo, o.preview = event, preview
			}
		}
	}
	return nil
}

func (o *IncrementalSSEObserver) Finalize(ctx context.Context) error {
	if o == nil {
		return nil
	}
	o.finalized = true
	return o.decoder.Finish(func(event SourceEvent) error { return o.dispatch(ctx, event) })
}

func (o *IncrementalSSEObserver) HasSemanticPreview() bool { return o != nil && o.preview.Semantic }

// Only complete frames accepted by the observer can terminate transport reads.
// Semantic previews release precommit without consuming an unfinished frame.
func (o *IncrementalSSEObserver) ReachedTerminal() bool {
	return o != nil && o.terminalHit
}
