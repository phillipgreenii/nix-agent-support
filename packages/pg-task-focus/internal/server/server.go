// Package server is the HTTP API of the pg-task-focus daemon: the routes of
// api/openapi.yaml, the browser-exposure defences, the RFC 9457 problems, the
// event stream, the health documents and /metrics. The handlers are hand
// written under the contract tests, and each is a thin translation between the
// wire shapes and the engine, which owns every rule.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/view"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/web"
)

// APIPrefix is the path prefix of every endpoint except the ops routes.
const APIPrefix = "/api/v1"

// SchemaVersion is the event schema version the daemon writes.
const SchemaVersion = 1

// Options configures New.
type Options struct {
	// Logger receives the access and mutation lines; it MUST NOT be nil.
	Logger *slog.Logger
	// Metrics is the catalog; it MUST NOT be nil.
	Metrics *obs.Metrics
	// TracerProvider makes the spans; nil means no spans.
	TracerProvider trace.TracerProvider
	// Clock is the time source of every read; nil means the system clock.
	Clock clock.Clock
	// Version is the build version shown by /healthz.
	Version string
	// Port is listen_port; Hosts and Origins derive from it and PublicURL.
	Port int
	// PublicURL is the configured browser URL, empty for none.
	PublicURL string
	// Started is the process start, for uptime.
	Started time.Time
	// HeartbeatInterval is how often /stream writes a comment line; zero
	// means 15 seconds (the longest the contract allows).
	HeartbeatInterval time.Duration
	// Alerts reads the alert scheduler for /healthz; nil reports none.
	Alerts func() AlertsReading
}

// AlertsReading is what the scheduler shows /healthz.
type AlertsReading struct {
	RunningCycle string
	NextReminder time.Time // zero when none
}

// ConfigState is the state of the configuration for /healthz.
type ConfigState struct {
	Valid           bool
	Digest          string
	LoadedAt        time.Time
	ReloadError     string
	RestartRequired []string
}

// ReplayInfo is what the startup replay did.
type ReplayInfo struct {
	Events   int
	Duration time.Duration
}

// Server is the HTTP API. Build it with New, serve Handler, and hand it the
// engine with Attach once replay has finished.
type Server struct {
	log     *slog.Logger
	metrics *obs.Metrics
	tracer  trace.Tracer
	clock   clock.Clock
	opts    Options

	hosts   map[string]bool
	origins map[string]bool

	eng      atomic.Pointer[engine.Engine]
	ready    atomic.Bool
	writable atomic.Bool
	failing  atomic.Value // string: the check that keeps readiness down

	mu       sync.Mutex
	replay   ReplayInfo
	recovery wire.HealthRecover
	cfgState ConfigState

	hub *hub
}

// New builds the server. Until Attach and MarkReady, every /api/v1 endpoint
// and /readyz answer 503 not_ready; /healthz and /metrics answer from the
// start.
func New(opts Options) *Server {
	s := &Server{log: opts.Logger, metrics: opts.Metrics, clock: opts.Clock, opts: opts}
	if s.clock == nil {
		s.clock = clock.Real()
	}
	tp := opts.TracerProvider
	if tp == nil {
		tp = noop.NewTracerProvider()
	}
	s.tracer = tp.Tracer(obs.ScopeName)
	s.hosts, s.origins = allowlists(opts.Port, opts.PublicURL)
	s.writable.Store(true)
	s.failing.Store("replay")
	s.hub = newHub(s)
	return s
}

// allowlists builds the Host and Origin allowlists: 127.0.0.1:<port>,
// localhost:<port>, and the host and origin of the public URL.
func allowlists(port int, publicURL string) (hosts, origins map[string]bool) {
	hosts, origins = map[string]bool{}, map[string]bool{}
	p := strconv.Itoa(port)
	for _, h := range []string{"127.0.0.1", "localhost"} {
		hosts[net.JoinHostPort(h, p)] = true
		origins["http://"+net.JoinHostPort(h, p)] = true
	}
	if u, err := url.Parse(publicURL); err == nil && u.Host != "" {
		hosts[strings.ToLower(u.Host)] = true
		origins[strings.ToLower(u.Scheme+"://"+u.Host)] = true
	}
	return hosts, origins
}

// Attach hands the server the open engine. It does not make the server ready.
func (s *Server) Attach(e *engine.Engine, replay ReplayInfo, rec wire.HealthRecover) {
	s.mu.Lock()
	s.replay, s.recovery = replay, rec
	s.mu.Unlock()
	s.eng.Store(e)
}

// MarkReady makes the API serve: replay has finished and the first store
// writability check has passed.
func (s *Server) MarkReady() {
	s.failing.Store("")
	s.ready.Store(true)
	s.metrics.Ready.Set(1)
	s.Notify()
}

// SetFailing names the check that keeps the server not ready.
func (s *Server) SetFailing(check string) { s.failing.Store(check) }

// SetWritable records the periodic writability probe's result.
func (s *Server) SetWritable(ok bool) { s.writable.Store(ok) }

// SetConfigState records the state of the configuration.
func (s *Server) SetConfigState(c ConfigState) {
	s.mu.Lock()
	s.cfgState = c
	s.mu.Unlock()
}

// Notify tells every open /stream that the version, or the store's state, may
// have changed. It never blocks and is safe to call from engine callbacks.
func (s *Server) Notify() { s.hub.kick() }

// now is the read instant.
func (s *Server) now() time.Time { return s.clock.Now() }

// engine returns the open engine, or nil before Attach.
func (s *Server) engine() *engine.Engine { return s.eng.Load() }

// storeHealth is the view of the engine's store health.
func (s *Server) storeHealth(e *engine.Engine) view.StoreHealth {
	h := e.Health()
	return view.StoreHealth{ReadOnly: h.ReadOnly, Reason: string(h.Reason), Since: h.Since}
}

// route is one registered endpoint.
type route struct {
	Method, Path string
	handler      http.HandlerFunc
	// ops marks the routes that sit outside APIPrefix and answer without
	// the engine; stream marks the one that is excluded from the duration
	// histogram.
	ops, stream bool
}

// Pattern is the ServeMux pattern of the route.
func (r route) Pattern() string { return r.Method + " " + r.Path }

// routes lists every endpoint. It is the one table the mux, the contract test
// and the metrics label (the route template) read.
func (s *Server) routes() []route {
	api := func(method, p string, h http.HandlerFunc) route {
		return route{Method: method, Path: APIPrefix + p, handler: h}
	}
	return []route{
		{Method: "GET", Path: "/{$}", handler: s.getIndex, ops: true},
		api("GET", "/state", s.getState),
		{Method: "GET", Path: APIPrefix + "/stream", handler: s.getStream, stream: true},
		api("POST", "/periods/change", s.postPeriodsChange),
		api("POST", "/profile/change", s.postProfileChange),
		api("POST", "/tasks/{id}/complete", s.postTaskComplete),
		api("POST", "/tasks/{id}/skip", s.postTaskSkip),
		api("POST", "/cycles/start", s.postCycleStart),
		api("POST", "/cycles/pause", s.postCycleVerb("pause")),
		api("POST", "/cycles/resume", s.postCycleVerb("resume")),
		api("POST", "/cycles/boost", s.postCycleBoost),
		api("POST", "/cycles/stop", s.postCycleVerb("stop")),
		api("POST", "/cycles/switch", s.postCycleSwitch),
		api("POST", "/cycles/annotate", s.postCycleAnnotate),
		api("POST", "/cycles/break", s.postCycleBreak),
		api("GET", "/events", s.getEvents),
		api("POST", "/events/{id}/correct", s.postCorrect),
		api("POST", "/events/{id}/retract", s.postRetractEvent),
		api("POST", "/batches/{id}/retract", s.postRetractBatch),
		api("GET", "/config", s.getConfig),
		api("GET", "/calendar", s.getCalendar),
		api("GET", "/attention", s.getAttention),
		{Method: "GET", Path: "/healthz", handler: s.getHealthz, ops: true},
		{Method: "GET", Path: "/readyz", handler: s.getReadyz, ops: true},
		{Method: "GET", Path: "/metrics", handler: s.getMetrics, ops: true},
	}
}

// Routes lists the "METHOD /path" of every registered endpoint, for the
// contract test.
func (s *Server) Routes() []string {
	var out []string
	for _, r := range s.routes() {
		// "/{$}" is the mux's spelling of exactly "/".
		out = append(out, strings.Replace(r.Pattern(), "/{$}", "/", 1))
	}
	return out
}

// Handler is the whole HTTP surface: the defences, then tracing, logging and
// metrics around the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	paths := map[string]bool{}
	streams := map[string]bool{}
	for _, r := range s.routes() {
		mux.HandleFunc(r.Pattern(), r.handler)
		paths[r.Path] = true
		if r.stream {
			streams[r.Pattern()] = true
		}
	}
	// A path with no method pattern for the verb used is a 405, and anything
	// else a 404, both as problems and not as the mux's plain text.
	for p := range paths {
		mux.HandleFunc(p, s.methodNotAllowed)
	}
	mux.HandleFunc("/", s.notFound)
	return s.instrument(s.defend(mux), streams)
}

// ---- the request context ----

type ctxKey int

const (
	keyRequest ctxKey = iota
)

// reqInfo is what the middleware knows about a request, for handlers and logs.
type reqInfo struct {
	id      string
	client  string
	traceID string
	fields  []slog.Attr
	reason  string
}

func info(ctx context.Context) *reqInfo {
	if v, ok := ctx.Value(keyRequest).(*reqInfo); ok {
		return v
	}
	return &reqInfo{}
}

// addFields adds log fields to the access line of the request.
func (ri *reqInfo) addFields(a ...slog.Attr) { ri.fields = append(ri.fields, a...) }

// statusWriter records the status a handler wrote and passes Flush and the
// controller's access to the underlying writer through.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// instrument is the middleware that traces, logs and counts every request. It
// runs outside the defences, so a refused request is counted and traced too.
func (s *Server) instrument(next http.Handler, streams map[string]bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx := obs.Propagator.Extract(r.Context(), propagationCarrier(r.Header))
		ctx, span := s.tracer.Start(ctx, "HTTP "+r.Method, trace.WithSpanKind(trace.SpanKindServer))
		ri := &reqInfo{id: newRequestID(), client: obs.NormalizeClient(r.Header.Get("X-Client")), traceID: span.SpanContext().TraceID().String()}
		ctx = context.WithValue(ctx, keyRequest, ri)
		w.Header().Set("X-Request-Id", ri.id)
		if tr := obs.TraceResponse(span.SpanContext()); tr != "" {
			w.Header().Set("traceresponse", tr)
		} else {
			// No tracer is recording (no collector): the daemon still
			// generates the ids, so a bug report can name the request.
			ri.traceID = newTraceID()
			w.Header().Set("traceresponse", "00-"+ri.traceID+"-"+newSpanID()+"-00")
		}
		sw := &statusWriter{ResponseWriter: w}
		r = r.WithContext(ctx)
		defer func() {
			// A panic in a handler is a defect: answer 500 as a problem,
			// log it, and keep the process serving.
			if p := recover(); p != nil {
				s.log.ErrorContext(ctx, "handler panic", "request_id", ri.id, "route", routeLabel(r), "panic", sanitizePanic(p))
				if sw.status == 0 {
					s.writeProblem(sw, r, wire.Simple(wire.ReasonInternal, "The request failed because of a defect in the daemon; report the trace_id.", r.URL.Path, ri.traceID))
				}
			}
			route := routeLabel(r)
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			elapsed := time.Since(start)
			span.SetName("HTTP " + r.Method + " " + route)
			obs.Attrs(
				span,
				attribute.String("http.request.method", r.Method), attribute.String("http.route", route),
				attribute.Int("http.response.status_code", status), attribute.String("client", ri.client),
				attribute.String("request_id", ri.id),
			)
			if ri.reason != "" {
				obs.Attrs(span, attribute.String("reason", ri.reason))
			}
			span.End()
			s.metrics.HTTPRequests.WithLabelValues(route, r.Method, strconv.Itoa(status), ri.client).Inc()
			if !streams[r.Method+" "+routeKey(r)] {
				s.metrics.HTTPDuration.WithLabelValues(route).Observe(elapsed.Seconds())
			}
			attrs := []any{
				"request_id", ri.id, "route", route, "method", r.Method, "status", status,
				"duration_ms", float64(elapsed.Microseconds()) / 1000, "client", ri.client,
			}
			if ri.reason != "" {
				attrs = append(attrs, "reason", ri.reason)
			}
			for _, a := range ri.fields {
				attrs = append(attrs, a)
			}
			level := slog.LevelInfo
			if strings.HasPrefix(route, "/healthz") || route == "/readyz" || route == "/metrics" || streams[r.Method+" "+routeKey(r)] {
				level = slog.LevelDebug
			}
			if status >= 500 {
				level = slog.LevelWarn
			}
			s.log.Log(ctx, level, "request", attrs...)
		}()
		next.ServeHTTP(sw, r)
	})
}

// routeKey is the path template of the matched pattern, or the empty string.
func routeKey(r *http.Request) string {
	_, p, _ := strings.Cut(r.Pattern, " ")
	return p
}

// routeLabel is the metrics label: the route template, or "other" for a path
// no route matches, so a probe cannot make labels.
func routeLabel(r *http.Request) string {
	p := routeKey(r)
	switch p {
	case "", "/":
		return "other"
	case "/{$}":
		return "/"
	}
	return p
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func newTraceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func newSpanID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func sanitizePanic(p any) string {
	if e, ok := p.(error); ok {
		return e.Error()
	}
	if s, ok := p.(string); ok {
		return s
	}
	return "panic"
}

type propagationCarrier http.Header

func (c propagationCarrier) Get(k string) string { return http.Header(c).Get(k) }
func (c propagationCarrier) Set(k, v string)     { http.Header(c).Set(k, v) }
func (c propagationCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// ---- the defences ----

// defend enforces the browser-exposure rules on every route, /healthz,
// /metrics and /stream included: the Host allowlist (the DNS-rebinding
// defence), the Origin allowlist (an absent Origin is permitted), and, for a
// mutation, Content-Type application/json even when the body is {}. It sends
// no CORS header.
func (s *Server) defend(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ri := info(r.Context())
		if !s.hosts[strings.ToLower(r.Host)] {
			s.refuse(w, r, wire.ReasonForbiddenHost, "The Host header is not one this service answers to; use 127.0.0.1:"+strconv.Itoa(s.opts.Port)+" or the configured public URL.")
			return
		}
		if o, present := r.Header["Origin"]; present && (len(o) != 1 || !s.origins[strings.ToLower(o[0])]) {
			s.refuse(w, r, wire.ReasonForbiddenOrigin, "The Origin header is not allowed; browsers may call this API only from the configured public URL.")
			return
		}
		if r.Method == http.MethodPost && !isJSON(r.Header.Get("Content-Type")) {
			s.refuse(w, r, wire.ReasonUnsupportedMediaType, "A mutation MUST send Content-Type: application/json, even when its body is {}.")
			return
		}
		_ = ri
		next.ServeHTTP(w, r)
	})
}

func isJSON(ct string) bool {
	mt, _, _ := strings.Cut(ct, ";")
	return strings.EqualFold(strings.TrimSpace(mt), "application/json")
}

func (s *Server) refuse(w http.ResponseWriter, r *http.Request, reason command.Reason, detail string) {
	s.countRefusal(reason)
	s.writeProblem(w, r, wire.Simple(reason, detail, r.URL.Path, info(r.Context()).traceID))
}

func (s *Server) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	s.refuse(w, r, wire.ReasonMethodNotAllowed, "This path does not take "+r.Method+".")
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.refuse(w, r, wire.ReasonNotFound, "No such path.")
}

// getIndex serves the placeholder page of the web UI: static, with no script,
// and a content security policy that would refuse one.
func (s *Server) getIndex(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(web.Index())
}

// getMetrics serves the Prometheus text exposition.
func (s *Server) getMetrics(w http.ResponseWriter, r *http.Request) {
	promhttp.HandlerFor(s.metrics.Registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}
