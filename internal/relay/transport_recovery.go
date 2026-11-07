package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/internal/relay/stream"
)

type upstreamRecovery uint8

const (
	upstreamRecoveryNone upstreamRecovery = iota
	upstreamRecoveryReconnect
	upstreamRecoveryHTTP
)

// Transports suggest recovery; the executor authorizes and counts the next send.
type upstreamRecoveryError struct {
	recovery upstreamRecovery
	err      error
}

func (e *upstreamRecoveryError) Error() string { return e.err.Error() }
func (e *upstreamRecoveryError) Unwrap() error { return e.err }

// Request errors such as rate limits do not invalidate the upstream connection.
func isUpstreamWSRequestError(err error) bool {
	var eventErr *wsUpstreamEventError
	return errors.As(err, &eventErr) && !isContinuationTransportFailure(err)
}

func (ra *relayAttempt) upstreamWSFailure(ctx context.Context, status int, err error, sendFailed bool) (int, error) {
	if timeoutErr := ra.firstTokenTimeoutIfNeeded(ctx, err); timeoutErr != nil {
		return 0, timeoutErr
	}
	if ra.requestContext().Err() != nil || errors.Is(err, stream.ErrDownstreamWrite) {
		return status, err
	}
	if !sendFailed && isUpstreamWSRequestError(err) {
		return status, err
	}
	if ra.responseCommitted() {
		wsUpstreamPool.RecordWSFailure(ra.channel.ID)
		return status, err
	}
	continuation := requiresUpstreamWSContinuation(ra.internalRequest)
	reconnect := (sendFailed && isUpstreamWSConnectionBroken(err)) || (continuation && shouldReconnectUpstreamWSBeforeReplay(err))
	if reconnect && ra.transportRecovery != upstreamRecoveryReconnect {
		return 0, &upstreamRecoveryError{recovery: upstreamRecoveryReconnect, err: err}
	}
	wsUpstreamPool.RecordWSFailure(ra.channel.ID)
	if continuation && (sendFailed || isContinuationTransportFailure(err)) {
		balancer.DeleteRoutingAffinity(ra.apiKeyID, ra.groupID, ra.requestModel)
		return http.StatusConflict, fmt.Errorf("upstream continuation transport unavailable; please restart the conversation: %w", err)
	}
	if sendFailed && !ra.internalRequest.HasOpenAIResponsesPassthrough() {
		return 0, &upstreamRecoveryError{recovery: upstreamRecoveryHTTP, err: err}
	}
	return status, err
}
