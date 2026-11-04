// Package server is the recorder's socket listener: HTTP/1.1 with JSON bodies
// on a Unix socket (mode 0600). It owns the framing, the operation registry
// that later phases add operations to, the per-request metrics and the request
// log line; it knows nothing about what an operation does.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// Kind classifies an operation for the latency histograms.
type Kind string

// Operation kinds.
const (
	KindQuery Kind = "query"
	KindFeed  Kind = "feed"
	KindWait  Kind = "wait"
)

// Handler answers one operation. body is the raw request JSON (possibly
// empty). It returns the answer value, or a *wire.Error.
type Handler func(ctx context.Context, body []byte) (any, *wire.Error)

type operation struct {
	h    Handler
	kind Kind
}

// Server is the listener plus its operation registry.
type Server struct {
	obs     *obs.Obs
	clients []string
	hello   func() any

	mu  sync.RWMutex
	ops map[string]operation

	srv  *http.Server
	ln   net.Listener
	path string
	done chan struct{}

	// now is the clock the request timings use (time.Now unless a test sets it).
	now   func() time.Time
	reqID atomic.Uint64
	boot  string
}

// New returns a Server. clients is the metric-label whitelist; hello builds the
// GET /v1/hello answer.
func New(o *obs.Obs, clients []string, hello func() any) *Server {
	s := &Server{obs: o, clients: clients, hello: hello, ops: map[string]operation{}, now: time.Now}
	s.boot = strconv.FormatInt(time.Now().UnixNano()/1e6%0xffffff, 16)
	return s
}

// Register adds the operation name (the path segment after /v1/). Later phases
// call this to add their operations.
func (s *Server) Register(name string, kind Kind, h Handler) {
	s.mu.Lock()
	s.ops[name] = operation{h: h, kind: kind}
	s.mu.Unlock()
}

// Operations lists the registered operation names (hello is always served), the
// capabilities list hello reports.
func (s *Server) Operations() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []string{"hello"}
	for n := range s.ops {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Handler returns the http.Handler (used by Serve, and directly by tests).
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.route) }

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	client := sanitize(r.Header.Get(wire.ClientHeader))
	switch {
	case r.URL.Path == "/v1/hello" && r.Method == http.MethodGet:
		s.finish(w, "hello", client, "ok", s.now(), 200, s.hello())
	case strings.HasPrefix(r.URL.Path, "/v1/") && r.Method == http.MethodPost:
		name := strings.TrimPrefix(r.URL.Path, "/v1/")
		s.mu.RLock()
		op, ok := s.ops[name]
		s.mu.RUnlock()
		if !ok {
			s.fail(w, "unknown", client, s.now(), wire.Errf(wire.ErrNotFound, "no such operation: "+name))
			return
		}
		start := s.now()
		tr := &obs.ReqTrace{ID: s.boot + "-" + strconv.FormatUint(s.reqID.Add(1), 10)}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			s.fail(w, name, client, start, wire.Errf(wire.ErrBadRequest, err.Error()))
			return
		}
		ans, werr := s.call(obs.WithTrace(r.Context(), tr), op, body)
		s.observe(op.kind, s.now().Sub(start))
		if werr != nil {
			if werr.Name == wire.ErrQueryTimeout {
				s.obs.Reg.Add(obs.MQueryTimeouts, map[string]string{"op": opLabel(name)}, 1)
				s.logQueryTimeout(name, client, tr, body, s.now().Sub(start))
			}
			s.fail(w, name, client, start, werr)
			return
		}
		s.finish(w, name, client, "ok", start, 200, ans)
	default:
		s.fail(w, "unknown", client, s.now(), wire.Errf(wire.ErrBadRequest, r.Method+" "+r.URL.Path+" is not an operation"))
	}
}

func (s *Server) call(ctx context.Context, op operation, body []byte) (ans any, werr *wire.Error) {
	defer func() {
		if r := recover(); r != nil {
			werr = wire.Errf(wire.ErrInternal, fmt.Sprint("handler panic: ", r))
		}
	}()
	return op.h(ctx, body)
}

func (s *Server) observe(k Kind, d time.Duration) {
	switch k {
	case KindQuery:
		s.obs.Reg.Observe(obs.MQueryDuration, d.Seconds())
	case KindFeed:
		s.obs.Reg.Observe(obs.MFeedDuration, d.Seconds())
	case KindWait:
		s.obs.Reg.Observe(obs.MWaitDuration, d.Seconds())
	}
}

func (s *Server) finish(w http.ResponseWriter, op, client, outcome string, start time.Time, status int, v any) {
	s.count(op, client, outcome)
	s.obs.Event("request", "op", op, "client", client, "outcome", outcome, "ms", fmt.Sprint(s.now().Sub(start).Milliseconds()))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, op, client string, start time.Time, e *wire.Error) {
	s.finish(w, op, client, e.Name, start, wire.HTTPStatus(e.Name), e)
}

func (s *Server) count(op, client, outcome string) {
	s.obs.Reg.Add(obs.MRequestsTotal, map[string]string{
		"client": obs.ClientLabel(client, s.clients), "op": opLabel(op), "outcome": outcome,
	}, 1)
}

// opLabel keeps the op label bounded: an unregistered name is "unknown".
func opLabel(op string) string {
	if len(op) > 32 {
		return "unknown"
	}
	return op
}

// sanitize bounds a caller-supplied client name for the log line.
func sanitize(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// Listen binds the Unix socket at path with mode 0600, replacing a stale
// socket but refusing when a live daemon answers on it.
func (s *Server) Listen(path string) error {
	// The socket may sit in a directory nothing else has created yet (the
	// state home).
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("socket directory: %w", err)
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("%s exists and is not a socket", path)
		}
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close()
			return fmt.Errorf("another recorder is already listening on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	s.ln, s.path = ln, path
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.obs.Log.Error("listener_error", "err", err.Error())
		}
	}()
	return nil
}

// Close stops the listener and removes the socket.
func (s *Server) Close() {
	if s.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	// A waiting feed would hold a graceful shutdown for up to a minute: after a
	// short grace the connections are closed, which is how a client sees a
	// restart (it reconnects with the same cursor).
	if err := s.srv.Shutdown(ctx); err != nil {
		_ = s.srv.Close()
	}
	<-s.done
	_ = os.Remove(s.path)
}

// logQueryTimeout logs the one query_timeout event of a timed-out request: what
// was asked (the kind and the key arguments, never a result), the request id,
// and where the time went. exec_ms is the time the request's statements and
// transactions ran in SQLite; wait_ms is the rest of the elapsed time, which is
// the wait for a read slot plus the handler's own work between statements.
func (s *Server) logQueryTimeout(op, client string, tr *obs.ReqTrace, body []byte, elapsed time.Duration) {
	exec := tr.Exec()
	wait := max(elapsed-exec, 0)
	kind, args := describeBody(op, body)
	s.obs.Event(obs.EvQueryTimeout, "op", op, "client", client,
		"ms", strconv.FormatInt(elapsed.Milliseconds(), 10),
		"request_id", tr.ID, "query_kind", kind, "args", args,
		"wait_ms", strconv.FormatInt(wait.Milliseconds(), 10),
		"exec_ms", strconv.FormatInt(exec.Milliseconds(), 10))
}

const (
	maxArgValue = 120
	maxArgsLen  = 512
)

// describeBody names the query (its operation, refined by the body's own "op"
// or "from" when it has one) and renders its key arguments as sorted key=value
// pairs: strings as they are, other values as compact JSON, each cut short. A
// key that names a credential is left out. It reads the request only.
func describeBody(op string, body []byte) (kind, args string) {
	kind = op
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return kind, ""
	}
	for _, k := range []string{"op", "from"} {
		var v string
		if raw, ok := m[k]; ok && json.Unmarshal(raw, &v) == nil && v != "" {
			kind += "/" + truncate(v, 40)
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if !secretKey(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		val := string(m[k])
		var str string
		if json.Unmarshal(m[k], &str) == nil {
			val = str
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k + "=" + truncate(val, maxArgValue))
		if b.Len() >= maxArgsLen {
			break
		}
	}
	return kind, truncate(b.String(), maxArgsLen)
}

func secretKey(k string) bool {
	k = strings.ToLower(k)
	for _, w := range []string{"token", "secret", "password", "passwd", "credential", "auth", "key"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
