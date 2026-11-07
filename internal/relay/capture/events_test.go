package capture

import (
	"github.com/suprelory/octopus-plus/internal/model"
	"strings"
	"testing"
)

func TestSSEEventsAcrossEveryReadBoundary(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		first := "event: delta" + newline + "data: 你好" + newline + "data: second" + newline + newline
		second := ": heartbeat" + newline + newline
		last := "data: incomplete"
		raw := first + second + last
		for split := 1; split <= len(raw); split++ {
			body := NewBody(model.RelayMessage{ContentType: "text/event-stream"}, nil, 4096)
			for i := 0; i < len(raw); i += split {
				body.Observe([]byte(raw[i:min(i+split, len(raw))]))
			}
			body.Finish(false)
			m := body.Snapshot()
			if len(m.Events) != 3 {
				t.Fatalf("newline=%q split=%d: %+v", newline, split, m.Events)
			}
			for i, want := range []string{first, second, last} {
				e := m.Events[i]
				if raw[e.Offset:e.Offset+e.Bytes] != want || e.Complete != (i < 2) {
					t.Fatalf("bad event: %+v", e)
				}
			}
			if m.State != "partial" {
				t.Fatal(m.State)
			}
		}
	}
}

func TestEventIndexBoundedIndependentlyOfBody(t *testing.T) {
	body := NewBody(model.RelayMessage{ContentType: "text/event-stream"}, nil, 1<<20)
	body.Observe([]byte(strings.Repeat("data: x\n\n", 1000)))
	body.Finish(true)
	m := body.Snapshot()
	if len(m.Events) != maxEvents || !m.EventsTruncated || m.State != "captured" {
		t.Fatalf("%+v", m)
	}
	body = NewBody(model.RelayMessage{}, nil, 5)
	body.ObserveFrame([]byte("123"), false)
	body.ObserveFrame([]byte{255, 254, 253}, true)
	body.Finish(true)
	m = body.Snapshot()
	if len(m.Events) != 2 || m.Events[1].Offset != 3 || m.Events[1].Bytes != 2 || m.Events[1].Complete || m.State != "truncated" {
		t.Fatalf("%+v", m)
	}
}
