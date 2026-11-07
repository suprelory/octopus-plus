package auth

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/suprelory/octopus-plus/internal/conf"
	"github.com/suprelory/octopus-plus/internal/model"
)

func sessionTestUser(t *testing.T) model.User {
	t.Helper()
	jwtSecretOnce = sync.Once{}
	jwtSecretOnce.Do(func() { jwtSecretKey = []byte("session-unit-test-secret") })
	t.Cleanup(func() { jwtSecretOnce = sync.Once{}; jwtSecretKey = nil })
	return model.User{ID: 1, Username: "admin", Password: "synthetic-password-hash"}
}

func TestJWTLoginSessionClaims(t *testing.T) {
	user := sessionTestUser(t)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var previousID string
	for _, expiresMin := range []int{-1, 0, 60, MaxJWTExpiresMin * 2} {
		token, expiresAt, err := generateJWTTokenForUserAt(expiresMin, user, now)
		if err != nil {
			t.Fatal(err)
		}
		claims, err := parseJWTTokenForUserAt(token, user, now)
		if err != nil {
			t.Fatal(err)
		}
		parsedExpiry, parseErr := time.Parse(time.RFC3339, expiresAt)
		if claims.Version != 2 || claims.SessionID == "" || claims.SessionID == previousID ||
			claims.AuthTime != now.Unix() || claims.Renewable != (expiresMin == -1) ||
			claims.ExpiresAt.Sub(now) != jwtLifetime(expiresMin) || parseErr != nil || !parsedExpiry.Equal(claims.ExpiresAt.Time) {
			t.Fatalf("unexpected login claims for expire=%d: %+v", expiresMin, claims)
		}
		previousID = claims.SessionID
	}
}

func TestJWTRenewalWindowAndAbsoluteLimit(t *testing.T) {
	user := sessionTestUser(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	token, _, err := generateJWTTokenForUserAt(-1, user, start)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := parseJWTTokenForUserAt(token, user, start)
	if err != nil {
		t.Fatal(err)
	}
	atWindow := start.Add(23 * 24 * time.Hour)
	if renewed, _, err := renewJWTTokenForUserAt(initial, user, atWindow); renewed != "" || err != nil {
		t.Fatalf("exactly seven days must not renew: token=%t err=%v", renewed != "", err)
	}
	if renewed, _, err := renewJWTTokenForUserAt(initial, user, atWindow.Add(time.Second)); renewed == "" || err != nil {
		t.Fatalf("inside renewal window: token=%t err=%v", renewed != "", err)
	}
	for _, day := range []int{25, 50, 75} {
		now := start.Add(time.Duration(day) * 24 * time.Hour)
		claims, err := parseJWTTokenForUserAt(token, user, now)
		if err != nil {
			t.Fatal(err)
		}
		renewed, _, err := renewJWTTokenForUserAt(claims, user, now)
		if err != nil || renewed == "" {
			t.Fatalf("day %d: no renewal: %v", day, err)
		}
		claims, err = parseJWTTokenForUserAt(renewed, user, now)
		wantExpiry := now.Add(30 * 24 * time.Hour)
		if deadline := start.Add(JWTSessionLifetime); wantExpiry.After(deadline) {
			wantExpiry = deadline
		}
		if err != nil || claims.AuthTime != initial.AuthTime || claims.SessionID != initial.SessionID ||
			!claims.ExpiresAt.Equal(wantExpiry) || !claims.IssuedAt.Equal(now) {
			t.Fatalf("day %d: incorrect renewal: %+v %v", day, claims, err)
		}
		token = renewed
	}
	claims, err := parseJWTTokenForUserAt(token, user, start.Add(84*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if renewed, _, err := renewJWTTokenForUserAt(claims, user, start.Add(84*24*time.Hour)); renewed != "" || err != nil {
		t.Fatalf("already capped token must not renew: %v", err)
	}
	if _, err := parseJWTTokenForUserAt(token, user, start.Add(JWTSessionLifetime)); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("ordinary authentication accepted expired session: %v", err)
	}
	if _, _, err := renewJWTTokenForUserAt(claims, user, start.Add(JWTSessionLifetime)); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("renewal accepted expired session: %v", err)
	}
}

func TestJWTLegacyAndFixedTokensDoNotRenew(t *testing.T) {
	user := sessionTestUser(t)
	now := time.Now().Truncate(time.Second)
	legacy, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer: conf.APP_NAME, IssuedAt: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}).SignedString(userJWTSecret(user))
	if err != nil {
		t.Fatal(err)
	}
	fixed, _, err := generateJWTTokenForUserAt(60, user, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{legacy, fixed} {
		claims, err := parseJWTTokenForUserAt(token, user, now)
		if err != nil {
			t.Fatal(err)
		}
		if renewed, _, err := renewJWTTokenForUserAt(claims, user, now); renewed != "" || err != nil {
			t.Fatalf("non-renewable token changed: %v", err)
		}
		if _, err := parseJWTTokenForUserAt(token, user, now.Add(time.Hour)); !errors.Is(err, ErrExpiredToken) {
			t.Fatalf("fixed expiry was ignored: %v", err)
		}
	}
}

func TestJWTRejectsMalformedSessionClaims(t *testing.T) {
	user := sessionTestUser(t)
	now := time.Now().Truncate(time.Second)
	cases := []struct {
		name   string
		change func(jwt.MapClaims)
	}{
		{"unknown version", func(c jwt.MapClaims) { c["ver"] = 3 }},
		{"partial legacy", func(c jwt.MapClaims) { delete(c, "ver") }},
		{"missing renewable", func(c jwt.MapClaims) { delete(c, "renewable") }},
		{"null renewable", func(c jwt.MapClaims) { c["renewable"] = nil }},
		{"invalid renewable", func(c jwt.MapClaims) { c["renewable"] = "true" }},
		{"differently cased session", func(c jwt.MapClaims) { c["VER"] = c["ver"]; delete(c, "ver") }},
		{"differently cased expiry", func(c jwt.MapClaims) { c["EXP"] = c["exp"]; delete(c, "exp") }},
		{"quoted expiry", func(c jwt.MapClaims) { c["exp"] = "1793000000" }},
		{"empty sid", func(c jwt.MapClaims) { c["sid"] = "" }},
		{"invalid auth time", func(c jwt.MapClaims) { c["auth_time"] = 1.5 }},
		{"future authentication", func(c jwt.MapClaims) { c["auth_time"] = now.Unix() + 1 }},
		{"future issuance", func(c jwt.MapClaims) { c["iat"] = now.Unix() + 1; c["nbf"] = now.Unix() + 1 }},
		{"missing issued at", func(c jwt.MapClaims) { delete(c, "iat") }},
		{"missing not before", func(c jwt.MapClaims) { delete(c, "nbf") }},
		{"missing expiry", func(c jwt.MapClaims) { delete(c, "exp") }},
		{"overlong token", func(c jwt.MapClaims) { c["exp"] = now.Add(31 * 24 * time.Hour).Unix() }},
		{"overlong session", func(c jwt.MapClaims) { c["auth_time"] = now.Add(-89 * 24 * time.Hour).Unix() }},
		{"invalid ordering", func(c jwt.MapClaims) { c["nbf"] = now.Unix() - 1 }},
		{"expired wrong issuer", func(c jwt.MapClaims) {
			c["iss"] = "another-app"
			c["auth_time"] = now.Unix() - 120
			c["iat"] = now.Unix() - 120
			c["nbf"] = now.Unix() - 120
			c["exp"] = now.Unix() - 1
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := jwt.MapClaims{"iss": conf.APP_NAME, "iat": now.Unix(), "nbf": now.Unix(),
				"exp": now.Add(30 * 24 * time.Hour).Unix(), "ver": 2, "sid": "test-session",
				"auth_time": now.Unix(), "renewable": true}
			tc.change(claims)
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(userJWTSecret(user))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseJWTTokenForUserAt(token, user, now); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("expected invalid token, got %v", err)
			}
		})
	}
}

func TestJWTRenewalKeepsAuthenticatedPasswordSnapshot(t *testing.T) {
	oldUser := sessionTestUser(t)
	now := time.Now().Truncate(time.Second)
	token, _, err := generateJWTTokenForUserAt(-1, oldUser, now.Add(-25*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := parseJWTTokenForUserAt(token, oldUser, now)
	if err != nil {
		t.Fatal(err)
	}
	newUser := oldUser
	newUser.Password = "replacement-password-hash"
	renewed, _, err := renewJWTTokenForUserAt(claims, oldUser, now)
	if err != nil || renewed == "" {
		t.Fatalf("renewal: %v", err)
	}
	if _, err := parseJWTTokenForUserAt(renewed, oldUser, now); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{token, renewed} {
		if _, err := parseJWTTokenForUserAt(stale, newUser, now); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("old password produced a valid session after change: %v", err)
		}
	}
}
