// Package checkin owns independent check-in sessions and execution. It never
// resolves authentication through subscription sites, accounts or caches.
package checkin

import (
	"context"
	"errors"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

var (
	ErrSessionChanged       = errors.New("checkin credential identity changed")
	ErrSessionUnavailable   = errors.New("checkin session storage is unavailable")
	ErrSessionExpired       = errors.New("checkin session expired; update login credentials")
	ErrSessionSave          = errors.New("checkin session was issued but could not be saved")
	ErrSessionUncertain     = errors.New("checkin session exchange outcome is unknown; reconcile or replace credentials")
	ErrSessionBackoff       = errors.New("checkin session is cooling down")
	ErrLoginRequiresCheckin = errors.New("AgentRouter login must be performed by the checkin submission workflow")
	ErrUnsupportedSession   = errors.New("credential does not support this session operation")
)

type ExchangeFailure string

const (
	ExchangeRetryable    ExchangeFailure = "retryable" // Definitely not consumed, e.g. HTTP 429 or failure before dispatch.
	ExchangeRejected     ExchangeFailure = "rejected"
	ExchangeVerification ExchangeFailure = "verification_required"
	ExchangeUncertain    ExchangeFailure = "uncertain" // Request might have consumed the token; never resend it automatically.
)

// ExchangeError deliberately carries no raw response, URL, headers or tokens.
type ExchangeError struct {
	Failure             ExchangeFailure
	AllowAccessFallback bool
}

func (e *ExchangeError) Error() string {
	switch e.Failure {
	case ExchangeRetryable:
		return "checkin authentication is temporarily unavailable"
	case ExchangeRejected:
		return "checkin authentication was rejected; check login credentials and network configuration"
	case ExchangeVerification:
		return ErrVerificationRequired.Error()
	default:
		return ErrSessionUncertain.Error()
	}
}

func (e *ExchangeError) Is(target error) bool {
	return e.Failure == ExchangeVerification && target == ErrVerificationRequired
}

// Authenticator performs exactly one HTTP exchange. Coordination and saving
// belong to Sessions; an adapter must never retry a login or refresh itself.
type Authenticator interface {
	Refresh(context.Context, model.CheckinCredential) (op.CheckinTokenPair, error)
	Login(context.Context, model.CheckinCredential) (op.CheckinLoginSession, error)
}

type SessionStore interface {
	GetCredential(context.Context, int) (*model.CheckinCredential, error)
	ClaimRefresh(context.Context, int, int64, time.Duration) (*op.CheckinSessionClaim, error)
	ClaimLogin(context.Context, int, int64, time.Duration) (*op.CheckinSessionClaim, error)
	BeginSessionExchange(context.Context, op.CheckinSessionClaim) error
	CommitRefresh(context.Context, op.CheckinSessionClaim, op.CheckinTokenPair) (*model.CheckinCredential, error)
	CommitLogin(context.Context, op.CheckinSessionClaim, op.CheckinLoginSession) (*model.CheckinCredential, error)
	ReleaseRefresh(context.Context, op.CheckinSessionClaim) error
	RetrySessionExchange(context.Context, op.CheckinSessionClaim, time.Time) error
}
