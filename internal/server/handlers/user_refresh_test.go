package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/auth"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
)

// JWT time/signature validation is tested in auth. Move the verified snapshot
// forward into its renewal window here to exercise the HTTP handler without a
// process-wide clock override or waiting 25 days.
func ageAuthenticatedSession(c *gin.Context) {
	claims, _ := middleware.AuthenticatedAdminClaims(c)
	now := time.Now().Truncate(time.Second)
	claims.AuthTime = now.Add(-25 * 24 * time.Hour).Unix()
	claims.IssuedAt = jwt.NewNumericDate(time.Unix(claims.AuthTime, 0))
	claims.NotBefore = claims.IssuedAt
	claims.ExpiresAt = jwt.NewNumericDate(now.Add(5 * 24 * time.Hour))
	claims.Renewable = true
}

func refreshRequest(token string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/refresh", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}

func decodeRefreshResponse(t *testing.T, response *httptest.ResponseRecorder) model.UserLoginResponse {
	t.Helper()
	var envelope struct {
		Data model.UserLoginResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Token == "" || envelope.Data.ExpireAt == "" {
		t.Fatal("missing renewed credentials")
	}
	return envelope.Data
}

func TestRefreshHTTPContract(t *testing.T) {
	token := setupLogStreamHandlerTest(t)
	router := gin.New()
	router.POST("/refresh", middleware.Auth(), middleware.RequireJSON(), refresh)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, refreshRequest(token))
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("fixed token refresh = %d %s", response.Code, response.Body.String())
	}
	fresh, _, err := auth.GenerateJWTToken(-1)
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, refreshRequest(fresh))
	if response.Code != http.StatusNoContent {
		t.Fatalf("early renewal status = %d", response.Code)
	}

	router = gin.New()
	router.POST("/refresh", middleware.Auth(), middleware.RequireJSON(), func(c *gin.Context) {
		ageAuthenticatedSession(c)
		refresh(c)
	})
	request := refreshRequest(fresh)
	request.Header.Del("Content-Type")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing JSON header = %d", response.Code)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, refreshRequest(fresh))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("renewal = %d %s", response.Code, response.Body.String())
	}
	renewed := decodeRefreshResponse(t, response)
	claims, err := auth.ParseJWTTokenForUser(renewed.Token, op.UserGet())
	if err != nil {
		t.Fatal(err)
	}
	original, err := auth.ParseJWTTokenForUser(fresh, op.UserGet())
	if err != nil {
		t.Fatal(err)
	}
	if claims.SessionID != original.SessionID || !claims.Renewable || claims.ExpiresAt.Sub(claims.IssuedAt.Time) != 30*24*time.Hour {
		t.Fatalf("wrong renewed claims: %+v", claims)
	}
	if expiry, err := time.Parse(time.RFC3339, renewed.ExpireAt); err != nil || !expiry.Equal(claims.ExpiresAt.Time) {
		t.Fatal("expiry response disagrees with JWT")
	}
}

func TestRefreshRejectsExpiryAfterMiddleware(t *testing.T) {
	token := setupLogStreamHandlerTest(t)
	router := gin.New()
	router.POST("/refresh", middleware.Auth(), func(c *gin.Context) {
		ageAuthenticatedSession(c)
		claims, _ := middleware.AuthenticatedAdminClaims(c)
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Second))
		refresh(c)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, refreshRequest(token))
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "auth.expired_token") {
		t.Fatalf("expired refresh = %d %s", response.Code, response.Body.String())
	}
}

func TestRefreshPasswordChangeCannotUpgradeOldSnapshot(t *testing.T) {
	token := setupLogStreamHandlerTest(t)
	oldUser := op.UserGet()
	authenticated := make(chan struct{})
	resume := make(chan struct{})
	router := gin.New()
	router.POST("/refresh", middleware.Auth(), func(c *gin.Context) {
		ageAuthenticatedSession(c)
		close(authenticated)
		<-resume
		refresh(c)
	})
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(response, refreshRequest(token))
	}()
	<-authenticated
	err := op.UserChangePassword("initial-password", "replacement-password")
	close(resume)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("refresh = %d", response.Code)
	}
	renewed := decodeRefreshResponse(t, response)
	if !auth.VerifyJWTTokenForUser(renewed.Token, oldUser) {
		t.Fatal("renewal did not use authenticated snapshot")
	}
	if auth.VerifyJWTToken(renewed.Token) {
		t.Fatal("old session survived concurrent password change")
	}
}
