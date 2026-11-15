package streamio

import "testing"

func TestNormalizeEventDataUsesEnvelopeType(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "empty", data: "", want: `{"type":"message_stop"}`},
		{name: "untyped object", data: `{}`, want: `{"type":"message_stop"}`},
		{name: "null type", data: `{"type":null}`, want: `{"type":"message_stop"}`},
		{name: "typed object", data: `{"type":"other"}`, want: `{"type":"other"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(NormalizeEventData("message_stop", []byte(tt.data)))
			if got != tt.want {
				t.Fatalf("NormalizeEventData() = %q, want %q", got, tt.want)
			}
		})
	}
}
