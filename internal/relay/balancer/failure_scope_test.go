package balancer

import (
	"github.com/suprelory/octopus-plus/internal/model"
	"testing"
	"time"
)

func TestKeyFailureDoesNotPenalizeChannelHealth(t *testing.T) {
	store := newChannelHealthStore(4)
	store.record(1, "m", model.AttemptFailed, time.Second, "quota", time.Now().Add(time.Hour), "key")
	snapshot := store.snapshot(1, "m")
	if snapshot.FailureRate != 0 || snapshot.ConsecutiveFailure != 0 || !snapshot.CooldownUntil.IsZero() {
		t.Fatalf("key penalized channel: %+v", snapshot)
	}
	store.record(1, "m", model.AttemptFailed, time.Second, "quota", time.Time{}, "channel")
	if store.snapshot(1, "m").FailureRate == 0 {
		t.Fatal("shared quota must affect channel health")
	}
}

func TestScopedCircuitIsolation(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	RecordScopedFailureAt(1, 11, "m", FailureAuthentication, time.Time{}, "key")
	if CanAttempt(1, 11, "other") || !CanAttempt(1, 12, "m") {
		t.Fatal("credential scope was not isolated")
	}
	RecordScopedFailureAt(2, 21, "m", FailureModelUnsupported, time.Time{}, "model")
	if CanAttempt(2, 22, "m") || !CanAttempt(2, 22, "other") {
		t.Fatal("model scope was not isolated")
	}
	RecordScopedFailureAt(3, 31, "m", FailureQuota, time.Time{}, "channel")
	if CanAttempt(3, 32, "other") || !CanAttempt(4, 41, "m") {
		t.Fatal("shared quota scope was not isolated")
	}
}
