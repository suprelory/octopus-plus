package relay

import (
	"context"
	"crypto/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/capture"
)

type relayCaptureKey struct{}

type relayCapture struct {
	id       string
	started  time.Time
	limit    int64
	budget   *capture.Budget
	client   *exchangeCapture
	attempts []*exchangeCapture
	writer   *captureResponseWriter
	active   atomic.Pointer[exchangeCapture]
	served   atomic.Pointer[exchangeCapture]
}

type exchangeCapture struct {
	info              dbmodel.RelayExchange
	request, response *capture.Body
	secrets           []string
	mu                sync.Mutex
	origin            time.Time
	timings           []dbmodel.RelayTiming
	ended             bool
}

func newRelayCapture() *relayCapture {
	limit := int64(0)
	keep, _ := op.SettingGetBool(dbmodel.SettingKeyRelayLogKeepEnabled)
	enabled, _ := op.SettingGetBool(dbmodel.SettingKeyRelayLogContentEnabled)
	if keep && enabled {
		mb, err := op.SettingGetInt(dbmodel.SettingKeyRelayLogContentMaxMB)
		if err != nil || mb < 1 || mb > 64 {
			mb = 4
		}
		limit = int64(mb) << 20
	}
	now := time.Now()
	return &relayCapture{id: "req_" + rand.Text(), started: now, limit: limit, budget: capture.NewBudget(limit * 4), client: &exchangeCapture{origin: now, info: dbmodel.RelayExchange{Transport: "http"}}}
}

func startHTTPRelayCapture(c *gin.Context) *relayCapture {
	trace := newRelayCapture()
	trace.client.request = capture.NewBody(dbmodel.RelayMessage{Method: c.Request.Method, URL: capture.URL(c.Request.URL.String()), Headers: capture.Headers(c.Request.Header), ContentType: c.Request.Header.Get("Content-Type")}, trace.budget, trace.limit)
	trace.client.response = capture.NewBody(dbmodel.RelayMessage{}, trace.budget, trace.limit)
	writer := &captureResponseWriter{ResponseWriter: c.Writer, body: trace.client.response, trace: trace}
	trace.writer = writer
	c.Writer = writer
	c.Header("X-Octopus-Request-Id", trace.id)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), relayCaptureKey{}, trace))
	return trace
}

func captureFromContext(ctx context.Context) *relayCapture {
	if ctx == nil {
		return nil
	}
	trace, _ := ctx.Value(relayCaptureKey{}).(*relayCapture)
	return trace
}

func (t *relayCapture) recordClientRequest(body []byte) {
	if t == nil {
		return
	}
	t.client.request.Observe(body)
	t.client.request.Finish(true)
}

func (t *relayCapture) beginAttempt(channel *dbmodel.Channel, model, secret string) *exchangeCapture {
	if t == nil {
		return nil
	}
	a := &exchangeCapture{info: dbmodel.RelayExchange{AttemptID: strconv.Itoa(len(t.attempts) + 1), ChannelID: channel.ID, ChannelName: channel.Name, Model: model, Transport: "http"}, secrets: []string{secret}}
	a.origin = t.started
	a.timing("attempt_start", false)
	t.attempts = append(t.attempts, a)
	return a
}

func (t *relayCapture) upstreamRequest(a *exchangeCapture, req *http.Request) {
	if t == nil || a == nil {
		return
	}
	a.traceHTTP(req)
	a.request = capture.NewBody(dbmodel.RelayMessage{Method: req.Method, URL: capture.URL(req.URL.String()), Headers: capture.Headers(req.Header, a.secrets...), ContentType: req.Header.Get("Content-Type")}, t.budget, t.limit)
	if req.Body == nil {
		a.request.Finish(true)
	} else {
		req.Body = capture.Reader(req.Body, a.request, req.ContentLength)
	}
}

func (t *relayCapture) upstreamResponse(a *exchangeCapture, response *http.Response) {
	if t == nil || a == nil || response == nil {
		return
	}
	status := response.StatusCode
	a.timing("response_headers", false)
	a.response = capture.NewBody(dbmodel.RelayMessage{StatusCode: &status, Headers: capture.Headers(response.Header, a.secrets...), ContentType: response.Header.Get("Content-Type")}, t.budget, t.limit)
	for _, name := range []string{"X-Request-Id", "Request-Id", "X-Amzn-Requestid", "X-Goog-Request-Id"} {
		if value := response.Header.Get(name); value != "" {
			a.info.UpstreamRequestID = capture.Text(value, a.secrets...)
			break
		}
	}
	if response.Body == nil {
		a.response.Finish(true)
	} else {
		response.Body = capture.Reader(response.Body, a.response, response.ContentLength)
	}
}

func (a *exchangeCapture) snapshot() dbmodel.RelayExchange {
	a.request.Finish(false)
	a.response.Finish(false)
	result := a.info
	a.mu.Lock()
	if !a.ended {
		a.timings = append(a.timings, dbmodel.RelayTiming{Phase: "finished", ElapsedMS: time.Since(a.origin).Milliseconds()})
		a.ended = true
	}
	result.Timings = append([]dbmodel.RelayTiming(nil), a.timings...)
	a.mu.Unlock()
	result.Request, result.Response = a.request.Snapshot(), a.response.Snapshot()
	return result
}

func (t *relayCapture) snapshot(ctx context.Context) *dbmodel.RelayTrace {
	if t == nil {
		return nil
	}
	if t.writer != nil {
		t.writer.finish(ctx == nil || ctx.Err() == nil)
	}
	result := &dbmodel.RelayTrace{ID: t.id, Client: t.client.snapshot(), Attempts: make([]dbmodel.RelayExchange, 0, len(t.attempts))}
	if served := t.served.Load(); served != nil {
		result.ServingAttemptID = served.info.AttemptID
	}
	if t.writer != nil && t.writer.status != nil {
		result.Client.Response.StatusCode = t.writer.status
		result.Client.Response.Headers = t.writer.headers
		result.Client.Response.ContentType = http.Header(t.writer.headers).Get("Content-Type")
	}
	for _, attempt := range t.attempts {
		result.Attempts = append(result.Attempts, attempt.snapshot())
	}
	return result
}

// Embed gin.ResponseWriter to retain Hijacker, CloseNotifier, Pusher and the
// framework's status accounting. Only bytes accepted by Write are observed.
type captureResponseWriter struct {
	gin.ResponseWriter
	mu        sync.Mutex
	body      *capture.Body
	status    *int
	headers   map[string][]string
	failed    bool
	trace     *relayCapture
	firstByte bool
}

func (w *captureResponseWriter) head() {
	if w.status == nil && w.ResponseWriter.Written() {
		status := w.ResponseWriter.Status()
		w.status, w.headers = &status, capture.Headers(w.ResponseWriter.Header())
		if strings.HasPrefix(strings.ToLower(w.ResponseWriter.Header().Get("Content-Type")), "text/event-stream") {
			w.body.EnableSSE()
		}
	}
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.ResponseWriter.Write(p)
	w.head()
	w.body.Observe(p[:n])
	if n > 0 && w.trace != nil {
		w.trace.delivered()
		if !w.firstByte {
			w.trace.client.timing("downstream_first_byte", false)
			w.firstByte = true
		}
	}
	if err != nil || n != len(p) {
		w.failed = true
	}
	return n, err
}

func (w *captureResponseWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *captureResponseWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ResponseWriter.WriteHeader(code)
	w.head()
}
func (w *captureResponseWriter) WriteHeaderNow() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ResponseWriter.WriteHeaderNow()
	w.head()
}
func (w *captureResponseWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ResponseWriter.Flush()
	w.head()
}
func (w *captureResponseWriter) finish(complete bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.head()
	w.body.Finish(complete && !w.failed && w.status != nil)
}
