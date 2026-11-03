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
	"strings"
	"sync"
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
}

// New returns a Server. clients is the metric-label whitelist; hello builds the
// GET /v1/hello answer.
func New(o *obs.Obs, clients []string, hello func() any) *Server {
	s := &Server{obs: o, clients: clients, hello: hello, ops: map[string]operation{}}
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
		s.finish(w, "hello", client, "ok", time.Now(), 200, s.hello())
	case strings.HasPrefix(r.URL.Path, "/v1/") && r.Method == http.MethodPost:
		name := strings.TrimPrefix(r.URL.Path, "/v1/")
		s.mu.RLock()
		op, ok := s.ops[name]
		s.mu.RUnlock()
		if !ok {
			s.fail(w, "unknown", client, time.Now(), wire.Errf(wire.ErrNotFound, "no such operation: "+name))
			return
		}
		start := time.Now()
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			s.fail(w, name, client, start, wire.Errf(wire.ErrBadRequest, err.Error()))
			return
		}
		ans, werr := s.call(r.Context(), op, body)
		s.observe(op.kind, time.Since(start))
		if werr != nil {
			if werr.Name == wire.ErrQueryTimeout {
				s.obs.Reg.Add(obs.MQueryTimeouts, map[string]string{"op": opLabel(name)}, 1)
				s.obs.Event("query_timeout", "op", name, "client", client, "ms", fmt.Sprint(time.Since(start).Milliseconds()))
			}
			s.fail(w, name, client, start, werr)
			return
		}
		s.finish(w, name, client, "ok", start, 200, ans)
	default:
		s.fail(w, "unknown", client, time.Now(), wire.Errf(wire.ErrBadRequest, r.Method+" "+r.URL.Path+" is not an operation"))
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
	s.obs.Event("request", "op", op, "client", client, "outcome", outcome, "ms", fmt.Sprint(time.Since(start).Milliseconds()))
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
