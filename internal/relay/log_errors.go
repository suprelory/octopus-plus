package relay

import (
	"fmt"
	"time"

	"github.com/suprelory/octopus-plus/internal/utils/log"
)

// Provider messages can echo prompts, credentials or entire request bodies.
// Console diagnostics contain only our taxonomy/type, never provider text or
// provider-supplied error codes. Full exchanges use the opt-in capture store.
func relayErrorDiagnostic(err error) string {
	if err == nil {
		return "none"
	}
	return fmt.Sprintf("%s (%T)", classifyRelayFailure(0, err, time.Time{}).Class, err)
}

func (ra *relayAttempt) logUpstreamHTTPError(status int) {
	requestID := ""
	if ra.metrics != nil && ra.metrics.capture != nil {
		requestID = ra.metrics.capture.id
	}
	log.Warnw("relay.upstream_error",
		"request_id", requestID,
		"channel_id", ra.channel.ID,
		"status", status,
		"error_class", string(classifyRelayFailure(status, ra.upstreamError, time.Time{}).Class),
	)
}
