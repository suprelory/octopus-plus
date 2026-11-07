package balancer

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestAttemptSnapshotsStreamDiagnostics(t *testing.T) {
	it := &Iterator{}
	span := &AttemptSpan{iter: it, startTime: time.Now()}
	d := &model.StreamDiagnostics{EventsReceived: 3, CompletionStatus: "interrupted", CleanEOF: true}
	span.SetStreamDiagnostics(d)
	span.End(model.AttemptFailed, 200, "missing terminal")
	d.EventsReceived = 100
	wire, err := json.Marshal(it.Attempts())
	if err != nil {
		t.Fatal(err)
	}
	var attempts []model.ChannelAttempt
	if err := json.Unmarshal(wire, &attempts); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Stream == nil || attempts[0].Stream.EventsReceived != 3 || !attempts[0].Stream.CleanEOF || attempts[0].Msg != "missing terminal" {
		t.Fatalf("persisted diagnostics = %s", wire)
	}
}
