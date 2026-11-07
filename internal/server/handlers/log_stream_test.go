package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/auth"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
)

func setupLogStreamHandlerTest(t *testing.T) string {
	t.Helper()
	setupSiteHandlerTestDB(t)
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(op.AdminUsernameEnv, "admin")
	t.Setenv(op.AdminPasswordEnv, "initial-password")
	if err := op.UserInit(); err != nil {
		t.Fatal(err)
	}
	token, _, err := auth.GenerateJWTToken(15)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestLogStreamClosesOnPasswordChange(t *testing.T) {
	jwt := setupLogStreamHandlerTest(t)
	router := gin.New()
	router.GET("/token", middleware.Auth(), getStreamToken)
	router.GET("/stream", streamLog)
	server := httptest.NewServer(router)
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/token", nil)
	request.Header.Set("Authorization", "Bearer "+jwt)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	err = json.NewDecoder(response.Body).Decode(&envelope)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || envelope.Data.Token == "" {
		t.Fatalf("stream token response: status=%d err=%v", response.StatusCode, err)
	}
	stream, err := client.Get(server.URL + "/stream?token=" + envelope.Data.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", stream.StatusCode)
	}
	duplicate, err := client.Get(server.URL + "/stream?token=" + envelope.Data.Token)
	if err != nil {
		t.Fatal(err)
	}
	duplicate.Body.Close()
	if duplicate.StatusCode != http.StatusUnauthorized {
		t.Fatalf("consumed token status = %d", duplicate.StatusCode)
	}
	if err := op.UserChangePassword("initial-password", "replacement-password"); err != nil {
		t.Fatal(err)
	}
	if body, err := io.ReadAll(stream.Body); err != nil || len(body) != 0 {
		t.Fatalf("revoked SSE did not finish cleanly: bytes=%d err=%v", len(body), err)
	}
}

func TestLogStreamTokenRejectsStaleAuthenticatedRequest(t *testing.T) {
	jwt := setupLogStreamHandlerTest(t)
	authenticated := make(chan struct{})
	resume := make(chan struct{})
	router := gin.New()
	router.GET("/token", middleware.Auth(), func(c *gin.Context) {
		close(authenticated)
		<-resume
		getStreamToken(c)
	})
	request := httptest.NewRequest(http.MethodGet, "/token", nil)
	request.Header.Set("Authorization", "Bearer "+jwt)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(response, request)
	}()
	<-authenticated
	err := op.UserChangePassword("initial-password", "replacement-password")
	close(resume)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("stale request issued a token: status=%d", response.Code)
	}
}

func TestLogStreamClosesAtAuthorizationDeadline(t *testing.T) {
	setupLogStreamHandlerTest(t)
	router := gin.New()
	router.GET("/stream", streamLog)
	server := httptest.NewServer(router)
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	token, err := op.RelayLogStreamTokenCreateForSession(op.UserGet(), time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL + "/stream?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", response.StatusCode)
	}
	if body, err := io.ReadAll(response.Body); err != nil || len(body) != 0 {
		t.Fatalf("expired SSE did not finish: bytes=%d err=%v", len(body), err)
	}
}
