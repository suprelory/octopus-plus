package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func (a *exchangeCapture) timing(phase string, reused bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ended || len(a.timings) >= 32 {
		return
	}
	a.timings = append(a.timings, model.RelayTiming{Phase: phase, ElapsedMS: time.Since(a.origin).Milliseconds(), Reused: reused})
}

func (a *exchangeCapture) traceHTTP(req *http.Request) {
	if a == nil {
		return
	}
	a.timing("send_start", false)
	trace := &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { a.timing("dns_start", false) },
		DNSDone:           func(httptrace.DNSDoneInfo) { a.timing("dns_done", false) },
		ConnectStart:      func(string, string) { a.timing("connect_start", false) },
		ConnectDone:       func(string, string, error) { a.timing("connect_done", false) },
		TLSHandshakeStart: func() { a.timing("tls_start", false) },
		TLSHandshakeDone:  func(tls.ConnectionState, error) { a.timing("tls_done", false) },
		GotConn:           func(info httptrace.GotConnInfo) { a.timing("connection_ready", info.Reused) },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				a.timing("request_sent", false)
			}
		},
		GotFirstResponseByte: func() { a.timing("response_first_byte", false) },
	}
	*req = *req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
}

func (t *relayCapture) bindDownstream(a *exchangeCapture) {
	if t != nil {
		t.active.Store(a)
	}
}

func (t *relayCapture) delivered() {
	if t == nil {
		return
	}
	if a := t.active.Load(); a != nil && t.served.CompareAndSwap(nil, a) {
		a.timing("downstream_delivery", false)
	}
}

func (t *relayCapture) finishOutcome(success bool, err error) {
	if t == nil {
		return
	}
	if a := t.served.Load(); a != nil && a.info.CompletionStatus != "" {
		t.client.info.CompletionStatus = a.info.CompletionStatus
		t.client.info.FinishCause = a.info.FinishCause
	}
	if t.client.info.Transport == "ws" || t.client.info.CompletionStatus != "" {
		if success {
			t.client.info.CompletionStatus = "completed"
		} else {
			t.client.info.CompletionStatus = "interrupted"
			if errors.Is(err, context.Canceled) {
				t.client.info.CompletionStatus = "canceled"
			}
		}
	}
}
