package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/relay/stream"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func (ra *relayAttempt) handleWSStreamResponseV2(ctx context.Context, reader *wsUpstreamReader) error {
	defer ra.closeFirstTokenBudget()
	inspector, _ := ra.outAdapter.(model.SourceEventInspector)
	return ra.handleTransformedStream(ctx, stream.NewWSSource(reader, inspector), nil)
}

// handleTransformedStream shares semantic precommit and finalization across
// HTTP SSE and upstream WebSocket events.
func (ra *relayAttempt) handleTransformedStream(ctx context.Context, source stream.StreamSource, timeoutCloser io.Closer) error {
	ra.heartbeat.Hand()
	ra.ensureStreamConverter()

	semanticPayload := false
	precommit := func(_, _ []byte) bool { return semanticPayload }

	var firstTokenTimeout time.Duration
	if ra.firstTokenTimeOutSec > 0 && ra.firstTokenBudget == nil {
		firstTokenTimeout = time.Duration(ra.firstTokenTimeOutSec) * time.Second
	}
	processor := stream.NewStreamProcessor(ra.heartbeat.streamConfig(stream.StreamConfig{
		Source: source,
		TransformEvent: func(ctx context.Context, event stream.SourceEvent) ([]byte, error) {
			output, semantic, err := ra.transformSourceEvent(ctx, event)
			semanticPayload = semantic
			return output, err
		},
		Writer:             ra.getStreamWriter(),
		Context:            ctx,
		FirstTokenTimeout:  firstTokenTimeout,
		HeartbeatInterval:  streamHeartbeatInterval(),
		PrecommitPredicate: precommit,
		PrecommitMaxEvents: 8,
		PrecommitMaxBytes:  64 * 1024,
		AllowEmptyPayload:  ra.allowEmptyPayload(),
		OnCommit:           ra.commitResponse,
		ReachedTerminal:    ra.streamReachedTerminal,
		OnFirstToken: func() {
			ra.metrics.SetFirstTokenTime(time.Now())
			ra.stopFirstTokenTimer()
		},
		OnFinish: func(context.Context) error {
			return ra.finalizeStreamLifecycle(ctx, true)
		},
	}))
	return ra.runStreamProcessor(ctx, processor, timeoutCloser)
}

// runStreamProcessor preserves the distinction between response headers,
// heartbeat bytes and a committed semantic payload when deciding retry safety.
func (ra *relayAttempt) runStreamProcessor(ctx context.Context, processor *stream.StreamProcessor, timeoutCloser io.Closer) error {
	err := processor.Run()
	if err != nil && ra.streamConverter != nil && !ra.streamConverter.Completed() {
		cause := model.StreamFinishCauseSourceError
		if ctx.Err() != nil || errors.Is(err, stream.ErrDownstreamWrite) {
			cause = model.StreamFinishCauseClientCancellation
		}
		_, _ = ra.streamConverter.Finish(ctx, cause)
		log.Debugf("stream completion status=interrupted cause=%s: %v", cause, relayErrorDiagnostic(err))
	}
	if ra.streamConverter != nil {
		diagnostics := ra.streamConverter.Diagnostics()
		diagnostics.CleanEOF = processor.CleanEOF()
		if diagnostics.SourceTransport == "" {
			diagnostics.SourceTransport = processor.SourceTransport()
		}
		if err != nil && diagnostics.CompletionStatus == "completed" {
			diagnostics.CompletionStatus = "interrupted"
		}
		ra.streamDiagnostics = &diagnostics
	}
	if processor.PayloadWritten() {
		ra.commitResponse()
	}
	if err != nil && strings.Contains(err.Error(), "first token timeout") {
		if timeoutCloser != nil {
			_ = timeoutCloser.Close()
		}
		return ra.firstTokenTimeoutError()
	}
	if err != nil {
		if timeoutErr := ra.firstTokenTimeoutIfNeeded(ctx, err); timeoutErr != nil {
			return timeoutErr
		}
	}
	return err
}

// getStreamWriter returns the appropriate stream writer for the current request.
func (ra *relayAttempt) getStreamWriter() StreamWriter {
	if ra.streamWriter != nil {
		return ra.streamWriter
	}
	return ra.c.Writer
}

// handleStreamResponseV2 validates the HTTP envelope before processing events.
func (ra *relayAttempt) handleStreamResponseV2(ctx context.Context, response *http.Response) error {
	defer ra.closeFirstTokenBudget()
	if ct := response.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "text/event-stream") {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 16*1024))
		return fmt.Errorf("upstream returned non-SSE content-type %q for stream request: %s", ct, string(body))
	}
	return ra.handleTransformedStream(ctx, stream.NewSSESource(response.Body, maxSSEEventSize), response.Body)
}

// handleStreamResponsePassthroughV2 uses StreamProcessor for unified passthrough handling.
// Works with any PassthroughCapable transformer (Anthropic, OpenAI Responses, etc.).
func (ra *relayAttempt) handleStreamResponsePassthroughV2(ctx context.Context, response *http.Response, cfg model.PassthroughConfig) error {
	defer ra.closeFirstTokenBudget()

	// Content-Type validation
	if ct := response.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "text/event-stream") {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 16*1024))
		return fmt.Errorf("upstream returned non-SSE content-type %q for stream request: %s", ct, string(body))
	}

	// Hand off early heartbeat
	ra.heartbeat.Hand()

	// Determine first token timeout
	var firstTokenTimeout time.Duration
	if ra.firstTokenTimeOutSec > 0 && ra.firstTokenBudget == nil {
		firstTokenTimeout = time.Duration(ra.firstTokenTimeOutSec) * time.Second
	}

	semanticPayload := false
	ra.ensureStreamConverter()
	observer := stream.NewIncrementalSourceEventObserver(maxSSEEventSize, cfg.TerminalEvents, func(ctx context.Context, event stream.SourceEvent) error {
		if len(event.Data) == 0 && event.Type == "" {
			return nil
		}
		events, err := ra.ensureStreamConverter().Push(ctx, event)
		if err != nil {
			ra.captureStreamError(err)
			return err
		}
		if len(events) == 0 {
			return nil
		}
		if model.HasSemanticStreamEvents(events) {
			semanticPayload = true
		}
		// Passthrough already preserves every native frame. The converter owns
		// aggregation; encoding a discarded projection can only introduce losses.
		return nil
	})
	if inspector, ok := ra.outAdapter.(model.SourceEventInspector); ok {
		observer.SetSourceInspector(inspector)
	}
	precommit := func(_, _ []byte) bool { return semanticPayload || observer.HasSemanticPreview() }
	precommitMaxEvents := maxSSEEventSize/(32*1024) + 8
	precommitMaxBytes := maxSSEEventSize
	const sseFramingAllowance = 64 * 1024
	if precommitMaxBytes <= int(^uint(0)>>1)-sseFramingAllowance {
		precommitMaxBytes += sseFramingAllowance
	}

	// Create StreamProcessor
	processor := stream.NewStreamProcessor(ra.heartbeat.streamConfig(stream.StreamConfig{
		Source:             stream.NewRawSource(response.Body, 32*1024),
		Transform:          nil, // Passthrough: no transformation
		Observer:           observer,
		Writer:             ra.getStreamWriter(),
		Context:            ctx,
		FirstTokenTimeout:  firstTokenTimeout,
		HeartbeatInterval:  streamHeartbeatInterval(),
		PrecommitPredicate: precommit,
		PrecommitMaxEvents: precommitMaxEvents,
		PrecommitMaxBytes:  precommitMaxBytes,
		AllowEmptyPayload:  ra.allowEmptyPayload(),
		OnCommit:           ra.commitResponse,
		ReachedTerminal:    ra.streamReachedTerminal,
		OnFirstToken: func() {
			ra.metrics.SetFirstTokenTime(time.Now())
			ra.stopFirstTokenTimer()
		},
		OnFinish: func(ctx context.Context) error {
			if err := ra.finalizeStreamLifecycle(ctx, false); err != nil {
				return err
			}
			log.Debugf("passthrough stream end")
			return nil
		},
	}))

	return ra.runStreamProcessor(ctx, processor, response.Body)
}

// transformSourceEvent converts a complete provider event into inbound wire
// bytes while preserving envelope metadata at the adapter boundary.
func (ra *relayAttempt) transformSourceEvent(ctx context.Context, event model.SourceEvent) ([]byte, bool, error) {
	events, err := ra.ensureStreamConverter().Push(ctx, event)
	if err != nil {
		ra.captureStreamError(err)
		log.Warnf("failed to transform stream events: %v", relayErrorDiagnostic(err))
		return nil, false, err
	}
	if len(events) == 0 {
		return nil, false, nil
	}
	semanticPayload := model.HasSemanticStreamEvents(events)
	inStream, err := ra.inAdapter.TransformStreamEvents(ctx, events)
	if err != nil {
		ra.captureStreamError(err)
		log.Warnf("failed to transform inbound stream events: %v", relayErrorDiagnostic(err))
		return nil, false, err
	}
	return inStream, semanticPayload, nil
}

func (ra *relayAttempt) ensureStreamConverter() *model.CanonicalStreamConverter {
	if ra.streamConverter == nil {
		policy := model.DefaultStreamTerminalPolicy()
		if ra.channel != nil {
			policy, _ = outbound.TerminalPolicy(ra.channel.Type)
		}
		ra.streamConverter = model.NewStreamConverter(ra.outAdapter, policy)
	}
	return ra.streamConverter
}

func (ra *relayAttempt) streamReachedTerminal() bool {
	return ra.streamConverter != nil && ra.streamConverter.FinishCause() == model.StreamFinishCauseExplicitTerminal
}

func (ra *relayAttempt) captureStreamError(err error) {
	if ra.streamConverter != nil {
		ra.streamConverter.RecordConversionLoss(err)
	}
	var responseError *model.ResponseError
	if errors.As(err, &responseError) {
		ra.upstreamError = responseError
	}
}

func (ra *relayAttempt) finalizeStreamLifecycle(ctx context.Context, writeTail bool) error {
	converter := ra.ensureStreamConverter()
	tailEvents, err := converter.Finish(ctx, converter.FinishCause())
	if err != nil {
		ra.captureStreamError(err)
		log.Debugf("stream completion status=interrupted cause=%s: %v", converter.FinishCause(), relayErrorDiagnostic(err))
		return err
	}
	finalized := converter.Finalization()
	log.Debugf("stream completion status=completed cause=%s terminal_event=%s", finalized.FinishCause, finalized.TerminalEvent)

	if len(tailEvents) > 0 {
		tail, transformErr := ra.inAdapter.TransformStreamEvents(ctx, tailEvents)
		if transformErr != nil {
			ra.captureStreamError(transformErr)
			return transformErr
		}
		if writeTail && len(tail) > 0 {
			writer := ra.getStreamWriter()
			ra.commitResponse()
			if _, writeErr := writer.Write(tail); writeErr != nil {
				return fmt.Errorf("%w: %w", stream.ErrDownstreamWrite, writeErr)
			}
			writer.Flush()
		}
	}

	if finalized.Response != nil && ra.metrics != nil && ra.responseCollected.CompareAndSwap(false, true) {
		actualModel := strings.TrimSpace(finalized.Response.Model)
		if actualModel == "" && ra.internalRequest != nil {
			actualModel = strings.TrimSpace(ra.internalRequest.Model)
		}
		ra.metrics.SetInternalResponse(finalized.Response, actualModel)
	}
	return nil
}
