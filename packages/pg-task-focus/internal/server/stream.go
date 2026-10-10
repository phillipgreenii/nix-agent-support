package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
)

// hub fans a change signal out to the open /stream connections. A signal is
// only "something may have changed": each connection reads the current version
// and store itself, so signals coalesce and a callback in the engine never
// blocks on a slow client.
type hub struct {
	s    *Server
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func newHub(s *Server) *hub { return &hub{s: s, subs: map[chan struct{}]struct{}{}} }

func (h *hub) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	h.s.metrics.SSEClients.Inc()
	return ch
}

func (h *hub) unsubscribe(ch chan struct{}) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
	h.s.metrics.SSEClients.Dec()
}

func (h *hub) kick() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default: // a signal is already pending
		}
	}
}

// streamState is the data of the "state" event: the new version, which a
// client compares only for inequality, and the store's state.
type streamState struct {
	Version wire.Version `json:"version"`
	Store   wire.Store   `json:"store"`
}

// getStream is GET /stream: server-sent events. It sends a "state" event
// carrying the version at once and on every change of the version (a mutation
// or a configuration reload), a "store" event on any change of the store's
// state (read-only mode begins), and a comment heartbeat at least every 15
// seconds. The server's ordinary write timeout would end it, so the
// per-connection write deadline is cleared.
func (s *Server) getStream(w http.ResponseWriter, r *http.Request) {
	e, ok := s.requireEngine(w, r)
	if !ok {
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	hb := s.opts.HeartbeatInterval
	if hb <= 0 || hb > 15*time.Second {
		hb = 15 * time.Second
	}
	sub := s.hub.subscribe()
	defer s.hub.unsubscribe(sub)

	var lastVersion wire.Version
	var lastStore string
	first := true
	send := func() bool {
		v := wire.FromVersion(e.Version())
		st := wire.FromStoreHealth(s.storeHealth(e))
		if !first && st.State != lastStore {
			if !s.writeEvent(w, rc, "store", st) {
				return false
			}
		}
		if first || v != lastVersion || st.State != lastStore {
			if !s.writeEvent(w, rc, "state", streamState{Version: v, Store: st}) {
				return false
			}
		}
		lastVersion, lastStore, first = v, st.State, false
		return true
	}
	if !send() {
		return
	}
	ticker := time.NewTicker(hb)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub:
			if !send() {
				return
			}
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if rc.Flush() != nil {
				return
			}
		}
	}
}

func (s *Server) writeEvent(w http.ResponseWriter, rc *http.ResponseController, name string, data any) bool {
	b, err := json.Marshal(data)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return false
	}
	if rc.Flush() != nil {
		return false
	}
	s.metrics.SSEEventsSent.Inc()
	return true
}
