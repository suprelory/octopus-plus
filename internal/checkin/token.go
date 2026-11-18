package checkin

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin/router"
	"github.com/suprelory/octopus-plus/internal/model"
)

func decodeTokenClaims(token string, claims any) bool {
	parts := strings.Split(model.NormalizeSiteAccessToken(token), ".")
	if len(parts) != 3 || len(parts[1]) > 16<<10 {
		return false
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	return err == nil && json.Unmarshal(body, claims) == nil
}

// Unverified JWT timestamps only schedule refreshes; they never authorize access.
func tokenTimes(c model.CheckinCredential) (expires, issued int64) {
	expires, issued = c.TokenExpiresAt, c.TokenRefreshedAt
	var claims struct {
		Exp json.Number `json:"exp"`
		Iat json.Number `json:"iat"`
	}
	if !decodeTokenClaims(c.AccessToken, &claims) {
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

// Only the profile response verifies the ID that may be persisted or used in a submission.
func profileIDHint(token string) *int {
	var claims struct{ ID, Sub json.RawMessage }
	if !decodeTokenClaims(token, &claims) {
		return nil
	}
	for _, raw := range []json.RawMessage{claims.ID, claims.Sub} {
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			continue
		}
		if id, err := n.Int64(); err == nil && id > 0 && id <= math.MaxInt {
			value := int(id)
			return &value
		}
	}
	return nil
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
		hasCookie := c.Cookie != ""
		if len(c.SessionCookies) > 0 {
			header := router.NewCookies(c.AuthBaseURL, "", c.SessionCookies).Header(c.AuthBaseURL + "/api/user/self")
			hasCookie = router.HasSession(header)
		}
		if c.AccessToken == "" && !hasCookie {
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
