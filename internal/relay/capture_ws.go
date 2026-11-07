package relay

import (
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/relay/capture"
)

func (t *relayCapture) startWSClient(raw []byte) {
	t.client.origin = t.started
	t.client.info.Transport = "ws"
	t.client.request = capture.NewBody(model.RelayMessage{ContentType: "application/json", Representation: "ws_messages"}, t.budget, t.limit)
	t.client.response = capture.NewBody(model.RelayMessage{ContentType: "application/json", Representation: "ws_messages"}, t.budget, t.limit)
	if len(raw) > 0 {
		t.client.request.ObserveFrame(raw, false)
	}
	t.client.request.Finish(true)
}

func (ra *relayAttempt) startWSCapture() {
	if ra.capture == nil {
		return
	}
	t := ra.metrics.capture
	ra.capture.timing("send_start", false)
	ra.capture.info.Transport = "ws"
	ra.capture.request = capture.NewBody(model.RelayMessage{ContentType: "application/json", Representation: "ws_messages"}, t.budget, t.limit)
	ra.capture.response = capture.NewBody(model.RelayMessage{ContentType: "application/json", Representation: "ws_messages"}, t.budget, t.limit)
}

func (ra *relayAttempt) observeWSRequest(p []byte) {
	if ra.capture == nil {
		return
	}
	ra.capture.request.ObserveFrame(p, false)
	ra.capture.timing("request_sent", false)
	ra.capture.request.Finish(true)
}

func (ra *relayAttempt) observeWSResponse(p []byte, binary bool) {
	if ra.capture != nil {
		ra.capture.response.ObserveFrame(p, binary)
	}
}
