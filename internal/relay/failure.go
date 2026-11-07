package relay

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	transformerModel "github.com/suprelory/octopus-plus/polywire/model"
)

// FailureClass is the relay's stable, transport-independent error taxonomy.
// The class is deliberately narrower than an HTTP status code: status codes
// describe a response, while this value controls retry and circuit behaviour.
type FailureClass string

const (
	FailureNone             FailureClass = "none"
	FailureRequest          FailureClass = "request"
	FailureConfiguration    FailureClass = "configuration"
	FailureAuthentication   FailureClass = "authentication"
	FailurePermission       FailureClass = "permission"
	FailureQuota            FailureClass = "quota"
	FailureRateLimit        FailureClass = "rate_limit"
	FailureModelUnsupported FailureClass = "model_unsupported"
	FailureTransient        FailureClass = "transient"
	FailureBudgetExceeded   FailureClass = "budget_exceeded"
	FailureClientCanceled   FailureClass = "client_canceled"
)

// FailureClassification contains the decision made from one upstream error.
// RetryAt is absolute so a retry remains correct after queueing or failover.
type FailureClassification struct {
	Class       FailureClass
	Scope       string // key, model or channel; empty for local/request failures
	Retryable   bool
	Record      bool
	Passthrough bool
	RetryAt     time.Time
	StatusCode  int
}

// classifiedRelayError preserves the source category across fmt.Errorf
// wrapping. It is used for local faults (request construction and overrides),
// where no upstream HTTP status exists to classify.
type classifiedRelayError struct {
	class FailureClass
	err   error
}

func (e *classifiedRelayError) Error() string {
	if e == nil || e.err == nil {
		return string(e.class)
	}
	return e.err.Error()
}

func (e *classifiedRelayError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func classifyLocalRelayError(class FailureClass, err error) error {
	if err == nil {
		return nil
	}
	return &classifiedRelayError{class: class, err: err}
}

func localRelayErrorClass(err error) (FailureClass, bool) {
	var classified *classifiedRelayError
	if !errors.As(err, &classified) || classified == nil {
		return FailureNone, false
	}
	return classified.class, true
}

// classifyRelayFailure turns a status/error pair into retry and breaker
// behaviour. Error envelopes are checked before status codes because providers
// commonly return quota or model errors with a generic 400/403/500 status.
func classifyRelayFailure(statusCode int, err error, retryAt time.Time) FailureClassification {
	return classifyRelayFailureWithContext(context.Background(), false, statusCode, err, retryAt)
}

func classifyRelayFailureContext(ctx context.Context, statusCode int, err error, retryAt time.Time) FailureClassification {
	if ctx == nil {
		return classifyRelayFailure(statusCode, err, retryAt)
	}
	return classifyRelayFailureWithContext(ctx, true, statusCode, err, retryAt)
}

func classifyRelayFailureWithContext(ctx context.Context, hasRequestContext bool, statusCode int, err error, retryAt time.Time) (result FailureClassification) {
	defer func() {
		if !result.Record {
			return
		}
		switch result.Class {
		case FailureAuthentication, FailurePermission:
			result.Scope = "key"
		case FailureModelUnsupported:
			result.Scope = "model"
		case FailureQuota, FailureRateLimit:
			code, typ, message := responseErrorFields(err)
			text := strings.ToLower(strings.Join([]string{code, typ, message, relayErrorMessage(err)}, " "))
			result.Scope = "channel"
			if !isBalanceExhaustedText(text) && (isKeyQuotaExhaustedText(text) || strings.Contains(text, "key_quota") || strings.Contains(text, "key_rate_limit") || strings.Contains(text, "api key quota") || strings.Contains(text, "per-key")) {
				result.Scope = "key"
			}
		default:
			result.Scope = "channel"
		}
	}()
	if isLocalRelayBudgetExceeded(ctx, err) {
		return FailureClassification{Class: FailureBudgetExceeded, StatusCode: statusCode, RetryAt: retryAt}
	}
	if isCostBudgetError(err) {
		return FailureClassification{Class: FailureRequest, StatusCode: http.StatusBadRequest}
	}
	if hasRequestContext && ctx.Err() != nil && isClientCancellation(ctx, err) {
		return FailureClassification{Class: FailureClientCanceled, StatusCode: statusCode, RetryAt: retryAt}
	}
	if localClass, ok := localRelayErrorClass(err); ok {
		result := FailureClassification{Class: localClass, StatusCode: statusCode, RetryAt: retryAt}
		if localClass == FailureTransient {
			result.Retryable = true
			result.Record = true
		}
		return result
	}

	code, typ, message := responseErrorFields(err)
	if statusCode <= 0 {
		var responseError *transformerModel.ResponseError
		if errors.As(err, &responseError) && responseError != nil && responseError.StatusCode > 0 {
			statusCode = responseError.StatusCode
		}
	}
	text := strings.ToLower(strings.Join([]string{code, typ, message, relayErrorMessage(err)}, " "))

	// A context error which is not a downstream cancellation (for example an
	// upstream deadline) is transient. The caller still handles client context
	// cancellation separately before reaching this function.
	if errors.Is(err, context.Canceled) && (!hasRequestContext || ctx.Err() != nil) {
		return FailureClassification{Class: FailureClientCanceled, StatusCode: statusCode, RetryAt: retryAt}
	}

	if isQuotaText(text) {
		return FailureClassification{
			Class: FailureQuota,
			// Exhausted funds cannot recover by retrying the same credential.
			// The executor can still rotate an exhausted key or switch channels.
			Retryable:   !isBalanceExhaustedText(text) && !isKeyQuotaExhaustedText(text),
			Record:      true,
			Passthrough: failurePassthroughStatus(statusCode),
			RetryAt:     retryAt,
			StatusCode:  statusCode,
		}
	}
	if isRateLimitText(text) || statusCode == http.StatusTooManyRequests || statusCode == 529 {
		return FailureClassification{
			Class:       FailureRateLimit,
			Retryable:   true,
			Record:      true,
			Passthrough: failurePassthroughStatus(statusCode),
			RetryAt:     retryAt,
			StatusCode:  statusCode,
		}
	}
	if isModelUnsupportedText(text) {
		return FailureClassification{
			Class:      FailureModelUnsupported,
			Record:     true,
			RetryAt:    retryAt,
			StatusCode: statusCode,
		}
	}
	if isAuthenticationText(text) {
		return FailureClassification{Class: FailureAuthentication, Record: true, RetryAt: retryAt, StatusCode: statusCode}
	}
	if isPermissionText(text) {
		return FailureClassification{Class: FailurePermission, Record: true, RetryAt: retryAt, StatusCode: statusCode}
	}
	if statusCode == http.StatusUnauthorized {
		return FailureClassification{Class: FailureAuthentication, Record: true, RetryAt: retryAt, StatusCode: statusCode}
	}
	if statusCode == http.StatusForbidden {
		return FailureClassification{Class: FailurePermission, Record: true, RetryAt: retryAt, StatusCode: statusCode}
	}
	if statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooEarly {
		return FailureClassification{Class: FailureTransient, Retryable: true, Record: true, RetryAt: retryAt, StatusCode: statusCode}
	}
	if statusCode == http.StatusNotFound {
		// A bare 404 is commonly an endpoint/route mismatch. Only suspend a
		// model when the provider explicitly identifies the model as missing.
		return FailureClassification{Class: FailureRequest, RetryAt: retryAt, StatusCode: statusCode}
	}
	if statusCode >= 400 && statusCode < 500 {
		return FailureClassification{Class: FailureRequest, RetryAt: retryAt, StatusCode: statusCode}
	}

	// Status 0 is the normal shape of a network/DNS/TLS error. A 5xx response
	// and malformed upstream response are also transient provider failures.
	if statusCode == 0 || statusCode >= 500 || err != nil {
		return FailureClassification{
			Class:       FailureTransient,
			Retryable:   true,
			Record:      true,
			Passthrough: statusCode == http.StatusServiceUnavailable || statusCode == http.StatusBadGateway || statusCode == http.StatusGatewayTimeout,
			RetryAt:     retryAt,
			StatusCode:  statusCode,
		}
	}
	if statusCode >= 200 && statusCode < 400 && err == nil {
		return FailureClassification{Class: FailureNone, StatusCode: statusCode, RetryAt: retryAt}
	}
	return FailureClassification{Class: FailureRequest, RetryAt: retryAt, StatusCode: statusCode}
}

func failurePassthroughStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusBadGateway ||
		statusCode == http.StatusServiceUnavailable ||
		statusCode == http.StatusGatewayTimeout ||
		statusCode == 529
}

func responseErrorFields(err error) (code, typ, message string) {
	var responseError *transformerModel.ResponseError
	if !errors.As(err, &responseError) || responseError == nil {
		return "", "", ""
	}
	return responseError.Detail.Code, responseError.Detail.Type, responseError.Detail.Message
}

func isQuotaText(text string) bool {
	return isBalanceExhaustedText(text) ||
		isKeyQuotaExhaustedText(text) ||
		strings.Contains(text, "insufficient_quota") ||
		strings.Contains(text, "quota exceeded") ||
		strings.Contains(text, "quota_exceeded") ||
		strings.Contains(text, "no available account") ||
		(strings.Contains(text, "billing") && strings.Contains(text, "hard limit")) ||
		strings.Contains(text, "exceeded your current quota")
}

func isBalanceExhaustedText(text string) bool {
	// new-api omits its error code in Claude responses, while sub2api also
	// reports balance failures under the generic billing_error type/code.
	return strings.Contains(text, "insufficient_user_quota") ||
		strings.Contains(text, "insufficient_balance") ||
		strings.Contains(text, "insufficient account balance") ||
		strings.Contains(text, "insufficient balance") ||
		strings.Contains(text, "wallet quota insufficient") ||
		strings.Contains(text, "用户额度不足") ||
		strings.Contains(text, "订阅额度不足") ||
		(strings.Contains(text, "预扣费额度失败") && strings.Contains(text, "用户剩余额度"))
}

func isKeyQuotaExhaustedText(text string) bool {
	// pre_consume_token_quota_failed alone is not sufficient: new-api uses
	// that code for reservation database failures as well as exhausted keys.
	return strings.Contains(text, "api_key_quota_exhausted") ||
		strings.Contains(text, "key_quota_exceeded") ||
		strings.Contains(text, "api key quota exhausted") ||
		strings.Contains(text, "api key quota exceeded") ||
		strings.Contains(text, "api key 额度已用完") ||
		strings.Contains(text, "token quota is not enough")
}

func isRateLimitText(text string) bool {
	return strings.Contains(text, "rate_limit_exceeded") ||
		strings.Contains(text, "rate_limit") ||
		strings.Contains(text, "rate limit") ||
		strings.Contains(text, "too many requests") ||
		strings.Contains(text, "resource exhausted") ||
		strings.Contains(text, "overloaded")
}

func isModelUnsupportedText(text string) bool {
	return strings.Contains(text, "model_not_found") ||
		strings.Contains(text, "model_not_supported") ||
		strings.Contains(text, "model_unsupported") ||
		strings.Contains(text, "invalid_model") ||
		strings.Contains(text, "model not found") ||
		strings.Contains(text, "model does not exist") ||
		strings.Contains(text, "model is not available") ||
		strings.Contains(text, "unsupported model") ||
		strings.Contains(text, "does not support model")
}

func isAuthenticationText(text string) bool {
	return strings.Contains(text, "invalid_api_key") ||
		strings.Contains(text, "invalid api key") ||
		strings.Contains(text, "api_key_invalid") ||
		strings.Contains(text, "authentication") ||
		strings.Contains(text, "unauthorized") ||
		strings.Contains(text, "api key is invalid")
}

func isPermissionText(text string) bool {
	return strings.Contains(text, "permission_denied") ||
		strings.Contains(text, "access_denied") ||
		strings.Contains(text, "forbidden") ||
		strings.Contains(text, "does not have permission") ||
		strings.Contains(text, "not authorized")
}

func failureCircuitKind(classification FailureClassification) balancer.FailureKind {
	switch classification.Class {
	case FailureRequest, FailureConfiguration, FailureBudgetExceeded, FailureClientCanceled, FailureNone:
		return balancer.FailureIgnored
	case FailureRateLimit:
		return balancer.FailureRateLimit
	case FailureQuota:
		return balancer.FailureQuota
	case FailureModelUnsupported:
		return balancer.FailureModelUnsupported
	case FailureAuthentication:
		return balancer.FailureAuthentication
	case FailurePermission:
		return balancer.FailurePermission
	default:
		return balancer.FailureTransient
	}
}

func recordFailureAndResolveRetryAt(channelID, keyID int, modelName string, classification FailureClassification, retryAt time.Time) time.Time {
	balancer.RecordScopedFailureAt(channelID, keyID, modelName, failureCircuitKind(classification), retryAt, classification.Scope)
	if deadline, ok := balancer.RetryAt(channelID, keyID, modelName); ok {
		return deadline
	}
	return retryAt
}

func mayRotateKey(result attemptResult, stateful bool) bool {
	return !stateful && !result.Success && !result.Written && !result.Canceled && !result.ResetConversation && result.Failure.Scope == "key"
}
