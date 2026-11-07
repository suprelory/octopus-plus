package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	transformerModel "github.com/suprelory/octopus-plus/polywire/model"
)

func TestParseRetryAtSupportsDeltaSecondsAndHTTPDate(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAtAt("7", now); !got.Equal(now.Add(7 * time.Second)) {
		t.Fatalf("delta retry-at = %v, want %v", got, now.Add(7*time.Second))
	}
	date := now.Add(23 * time.Second).Format(http.TimeFormat)
	if got := parseRetryAtAt(date, now); !got.Equal(now.Add(23 * time.Second)) {
		t.Fatalf("http-date retry-at = %v, want %v", got, now.Add(23*time.Second))
	}
	if got := parseRetryAtAt("invalid", now); !got.IsZero() {
		t.Fatalf("invalid retry-at = %v, want zero", got)
	}
}

func TestRetryAfterHeaderValueRoundsUp(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, int(123*time.Millisecond), time.UTC)
	got := retryAfterHeaderValue(now.Add(1500*time.Millisecond), now)
	if got != "2" {
		t.Fatalf("retry-after header = %q, want 2", got)
	}
}

func TestWSRetryDeadlineAndErrorEvent(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	retryAt := parseWSRetryDeadline(now, json.RawMessage(`1.5`), nil)
	if !retryAt.Equal(now.Add(1500 * time.Millisecond)) {
		t.Fatalf("ws retry deadline = %v, want %v", retryAt, now.Add(1500*time.Millisecond))
	}
	event := buildWSErrorEvent(http.StatusTooManyRequests, CodeRelayRateLimit, "rate limited", retryAt, now)
	if got := event["retry_after"]; got != 2 {
		t.Fatalf("ws retry_after = %#v, want 2", got)
	}
}

func TestReplaceRequiredJSONModelUsesMappedModel(t *testing.T) {
	payload, err := replaceRequiredJSONModel([]byte(`{"model":"alias","input":"hello"}`), "mapped")
	if err != nil {
		t.Fatalf("replaceRequiredJSONModel() error = %v", err)
	}
	if got, err := requiredJSONModel(payload); err != nil || got != "mapped" {
		t.Fatalf("requiredJSONModel() = %q, %v; want mapped", got, err)
	}
}

func TestFailureClassificationUsesErrorEnvelopeBeforeStatus(t *testing.T) {
	err := &structError{message: "insufficient_quota", code: "insufficient_quota"}
	responseErr := fmt.Errorf("provider: %w", err)
	classification := classifyRelayFailure(http.StatusBadRequest, responseErr, time.Time{})
	if classification.Class != FailureQuota || !classification.Retryable || !classification.Record {
		t.Fatalf("quota classification = %#v", classification)
	}
}

func TestFailureClassificationDistinguishesStandaloneAndUpstreamCancellation(t *testing.T) {
	standalone := classifyRelayFailure(0, context.Canceled, time.Time{})
	if standalone.Class != FailureClientCanceled {
		t.Fatalf("standalone cancellation classification = %#v", standalone)
	}

	upstream := classifyRelayFailureContext(context.Background(), 0, context.Canceled, time.Time{})
	if upstream.Class != FailureTransient || !upstream.Retryable || !upstream.Record {
		t.Fatalf("upstream cancellation classification = %#v", upstream)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	client := classifyRelayFailureContext(canceledCtx, 0, context.Canceled, time.Time{})
	if client.Class != FailureClientCanceled {
		t.Fatalf("client cancellation classification = %#v", client)
	}
}

type structError struct {
	message string
	code    string
}

func (e *structError) Error() string { return e.message + " code=" + e.code }

func TestFailureClassificationDoesNotRetryConfigurationErrors(t *testing.T) {
	err := classifyLocalRelayError(FailureConfiguration, fmt.Errorf("bad override"))
	classification := classifyRelayFailure(0, err, time.Time{})
	if classification.Class != FailureConfiguration || classification.Retryable || classification.Record {
		t.Fatalf("configuration classification = %#v", classification)
	}
	responseErr := protocolErrorForAttempt(attemptResult{
		StatusCode: http.StatusInternalServerError,
		Failure:    classification,
	}, err)
	if responseErr == nil || responseErr.StatusCode != http.StatusInternalServerError || responseErr.Detail.Code != CodeRelayConfiguration {
		t.Fatalf("configuration protocol error = %#v", responseErr)
	}
}

func TestFailureClassificationDoesNotTreatBareNotFoundAsModelFailure(t *testing.T) {
	bare := classifyRelayFailure(http.StatusNotFound, fmt.Errorf("upstream route not found"), time.Time{})
	if bare.Class != FailureRequest || bare.Record {
		t.Fatalf("bare 404 classification = %#v, want request without breaker record", bare)
	}
	modelRoute := classifyRelayFailure(http.StatusNotFound, fmt.Errorf("models endpoint route not found"), time.Time{})
	if modelRoute.Class != FailureRequest || modelRoute.Record {
		t.Fatalf("model route 404 classification = %#v, want request without breaker record", modelRoute)
	}

	modelErr := &transformerModel.ResponseError{
		StatusCode: http.StatusNotFound,
		Detail: transformerModel.ErrorDetail{
			Code:    "model_not_found",
			Type:    "invalid_request_error",
			Message: "The model does not exist",
		},
	}
	classified := classifyRelayFailure(http.StatusNotFound, modelErr, time.Time{})
	if classified.Class != FailureModelUnsupported || !classified.Record || classified.Retryable {
		t.Fatalf("model 404 classification = %#v", classified)
	}
}

func TestFailureClassificationOnlyPassthroughsRateLimitStatuses(t *testing.T) {
	badRequest := classifyRelayFailure(http.StatusBadRequest, fmt.Errorf("rate_limit"), time.Time{})
	if badRequest.Class != FailureRateLimit || badRequest.Passthrough {
		t.Fatalf("400 rate-limit classification = %#v, want non-passthrough", badRequest)
	}
	tooMany := classifyRelayFailure(http.StatusTooManyRequests, fmt.Errorf("rate limited"), time.Time{})
	if !tooMany.Passthrough {
		t.Fatalf("429 rate-limit classification = %#v, want passthrough", tooMany)
	}
}
