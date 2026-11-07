package auth

import (
	"errors"
	"net/http"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

// ValidateAPIKey is shared by HTTP requests and individual WebSocket turns.
// The upgrade only checks permissions; each response.create consumes its RPM.
// Billable requests must also call ReserveAPIKeyCost and release after settlement.
func ValidateAPIKey(key model.APIKey, checkRPM bool) (*apperror.Error, int) {
	if !key.Enabled {
		return apperror.New(apperror.CodeAuthAPIKeyDisabled, "API key is disabled").WithStatus(http.StatusUnauthorized), 0
	}
	if key.ExpireAt > 0 && key.ExpireAt <= time.Now().Unix() {
		return apperror.New(apperror.CodeAuthAPIKeyExpired, "API key has expired").WithStatus(http.StatusUnauthorized), 0
	}
	stats := op.StatsAPIKeyGet(key.ID)
	if key.MaxCost > 0 && key.MaxCost <= stats.OutputCost+stats.InputCost {
		return apperror.New(apperror.CodeAuthAPIKeyCostExceeded, "API key has reached the max cost").WithStatus(http.StatusUnauthorized), 0
	}
	if checkRPM && key.MaxRPM > 0 {
		if allowed, retryAfter := op.RateLimitCheck(key.ID, key.MaxRPM); !allowed {
			return apperror.New(apperror.CodeAuthAPIKeyRateLimited, "API key has exceeded the rate limit").WithStatus(http.StatusTooManyRequests), retryAfter
		}
	}
	return nil, 0
}

func ReserveAPIKeyCost(key model.APIKey) (*op.APIKeyCostReservation, *apperror.Error) {
	reservation, err := op.APIKeyReserveCost(key.ID)
	if err == nil {
		return reservation, nil
	}
	if errors.Is(err, op.ErrAPIKeyCostInFlight) {
		return nil, apperror.New(apperror.CodeAuthAPIKeyRateLimited, err.Error()).WithStatus(http.StatusTooManyRequests)
	}
	if errors.Is(err, op.ErrAPIKeyCostExceeded) {
		return nil, apperror.New(apperror.CodeAuthAPIKeyCostExceeded, err.Error()).WithStatus(http.StatusUnauthorized)
	}
	return nil, apperror.New(apperror.CodeAuthInvalidToken, "API key is unavailable").WithStatus(http.StatusUnauthorized)
}
