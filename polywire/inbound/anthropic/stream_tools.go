package anthropic

import "github.com/suprelory/octopus-plus/polywire/model"

// Anthropic content blocks are emitted serially. Other providers may interleave
// tool argument deltas, so keep the active tool streaming and defer other content
// until its stop. Metadata and usage never have to wait for a content block.
type toolStreamOrder struct {
	active  *model.StreamEvent
	pending []model.StreamEvent
}

func (s *toolStreamOrder) push(events []model.StreamEvent) []model.StreamEvent {
	var ordered []model.StreamEvent
	for _, event := range events {
		switch event.Kind {
		case model.StreamEventKindError:
			// A failed stream must not flush pending tools as successful calls.
			s.active, s.pending = nil, nil
			return append(ordered, event)
		case model.StreamEventKindMessageStop, model.StreamEventKindDone:
			// Legacy callers can omit tool stops. Canonical callers normally arrive
			// here with every tool already closed by the stream finalizer.
			for s.active != nil || len(s.pending) > 0 {
				if s.active != nil {
					stop := *s.active
					stop.Kind = model.StreamEventKindToolCallStop
					stop.Delta, stop.ContentBlock = nil, nil
					stop.Synthesized = true
					ordered = append(ordered, stop)
					s.active = nil
				}
				s.drain(&ordered)
			}
			ordered = append(ordered, event)
		default:
			s.accept(event, &ordered)
			if s.active == nil {
				s.drain(&ordered)
			}
		}
	}
	return ordered
}

func (s *toolStreamOrder) accept(event model.StreamEvent, ordered *[]model.StreamEvent) {
	if s.active != nil && !s.belongsToActiveTool(event) && !toolStreamMetadata(event.Kind) {
		s.pending = append(s.pending, snapshotToolEvent(event))
		return
	}
	*ordered = append(*ordered, event)
	switch event.Kind {
	case model.StreamEventKindToolCallStart, model.StreamEventKindToolCallDelta:
		if s.active == nil && event.ToolCall != nil {
			active := snapshotToolEvent(event)
			s.active = &active
		}
	case model.StreamEventKindToolCallStop:
		s.active = nil
	}
}

func (s *toolStreamOrder) drain(ordered *[]model.StreamEvent) {
	for s.active == nil && len(s.pending) > 0 {
		pending := s.pending
		s.pending = nil
		for index, event := range pending {
			s.accept(event, ordered)
			if s.active == nil && len(s.pending) > 0 {
				// Earlier deferred content must start before later deltas. Restart
				// from that content instead of opening a tool from an ID-less delta.
				s.pending = append(s.pending, pending[index+1:]...)
				break
			}
		}
	}
}

func (s *toolStreamOrder) belongsToActiveTool(event model.StreamEvent) bool {
	if event.Index != s.active.Index {
		return false
	}
	switch event.Kind {
	case model.StreamEventKindToolCallStart, model.StreamEventKindToolCallDelta, model.StreamEventKindToolCallStop:
	default:
		return false
	}
	if event.ToolCall != nil {
		return event.ToolCall.Index == s.active.ToolCall.Index
	}
	// Compatibility callers can supply an anonymous stop for the active tool.
	return event.Kind == model.StreamEventKindToolCallStop &&
		(event.BlockIndex == nil || s.active.BlockIndex == nil || *event.BlockIndex == *s.active.BlockIndex)
}

func toolStreamMetadata(kind model.StreamEventKind) bool {
	switch kind {
	case model.StreamEventKindUsageDelta, model.StreamEventKindMessageMetadata,
		model.StreamEventKindMessageStart, model.StreamEventKindResponseStart,
		model.StreamEventKindResponseStop, model.StreamEventKindOutputItemStart,
		model.StreamEventKindOutputItemStop:
		return true
	default:
		return false
	}
}

func snapshotToolEvent(event model.StreamEvent) model.StreamEvent {
	if event.ToolCall != nil {
		call := *event.ToolCall
		event.ToolCall = &call
	}
	if event.Delta != nil {
		delta := *event.Delta
		event.Delta = &delta
	}
	if event.BlockIndex != nil {
		index := *event.BlockIndex
		event.BlockIndex = &index
	}
	return event
}
