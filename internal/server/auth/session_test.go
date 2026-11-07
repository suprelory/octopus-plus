package auth

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestPasswordChangeRevokesSessions(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "auth.db"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(op.AdminUsernameEnv, "admin")
	t.Setenv(op.AdminPasswordEnv, "initial-password")
	if err := op.UserInit(); err != nil {
		t.Fatal(err)
	}
	verified, err := op.UserVerifyAndGet("admin", "initial-password")
	if err != nil {
		t.Fatal(err)
	}
	oldToken, _, err := GenerateJWTTokenForUser(-1, verified)
	if err != nil || !VerifyJWTToken(oldToken) {
		t.Fatalf("initial token: %v", err)
	}
	if err := op.UserChangePassword("wrong-password", "replacement-password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	if !VerifyJWTToken(oldToken) {
		t.Fatal("failed password change revoked session")
	}
	if err := op.UserChangePassword("initial-password", "replacement-password"); err != nil {
		t.Fatal(err)
	}
	if VerifyJWTToken(oldToken) {
		t.Fatal("old token survived password change")
	}
	// A login that verified the old password before the change must not obtain
	// a token for the new password state even if signing happens afterward.
	racingToken, _, err := GenerateJWTTokenForUser(-1, verified)
	if err != nil {
		t.Fatal(err)
	}
	if VerifyJWTToken(racingToken) {
		t.Fatal("concurrent stale login survived password change")
	}
	newToken, _, err := GenerateJWTToken(-1)
	if err != nil || !VerifyJWTToken(newToken) {
		t.Fatalf("new token: %v", err)
	}
	if err := op.UserInit(); err != nil {
		t.Fatal(err)
	}
	jwtSecretOnce = sync.Once{}
	jwtSecretKey = nil
	if VerifyJWTToken(oldToken) || !VerifyJWTToken(newToken) {
		t.Fatal("session revocation did not survive reloading persisted state")
	}
	if err := op.UserChangeUsername("renamed"); err != nil {
		t.Fatal(err)
	}
	if !VerifyJWTToken(newToken) {
		t.Fatal("username change unexpectedly revoked session")
	}
}
