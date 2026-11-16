package checkin

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

// Unverified JWT timestamps only schedule refreshes; they never authorize access.
func tokenTimes(c model.CheckinCredential) (expires, issued int64) {
	expires, issued = c.TokenExpiresAt, c.TokenRefreshedAt
	parts := strings.Split(model.NormalizeSiteAccessToken(c.AccessToken), ".")
	if len(parts) != 3 || len(parts[1]) > 16<<10 {
		return
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return
	}
	var claims struct {
		Exp json.Number `json:"exp"`
		Iat json.Number `json:"iat"`
	}
	if json.Unmarshal(body, &claims) != nil {
		return
	}
	if seconds, err := claims.Exp.Int64(); err == nil && seconds > 0 && seconds <= math.MaxInt64/1000 && (expires <= 0 || seconds*1000 < expires) {
		expires = seconds * 1000
	}
	if seconds, err := claims.Iat.Int64(); err == nil && seconds > 0 && seconds <= math.MaxInt64/1000 && seconds*1000 < expires {
		issued = max(issued, seconds*1000)
	}
	return
}

func refreshDue(c model.CheckinCredential, now time.Time) bool {
	if c.RefreshToken == "" {
		return false
	}
	expires, issued := tokenTimes(c)
	lead := (5 * time.Minute).Milliseconds()
	if issued > 0 && expires > issued {
		lead = min(lead, (expires-issued)/5)
	}
	return expires > 0 && now.UnixMilli() >= expires-lead
}

func usable(c model.CheckinCredential, now time.Time) bool {
	switch c.Type {
	case model.CheckinCredentialCookie:
		return c.Cookie != ""
	case model.CheckinCredentialAPIKey:
		return c.APIKey != ""
	case model.CheckinCredentialPassword:
		if c.AccessToken == "" && c.Cookie == "" {
			return false
		}
	default:
		if c.AccessToken == "" {
			return false
		}
	}
	expires, _ := tokenTimes(c)
	return expires == 0 || expires > now.UnixMilli()
}
