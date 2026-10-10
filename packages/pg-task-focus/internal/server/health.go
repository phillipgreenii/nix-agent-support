package server

import (
	"net/http"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
)

// health builds the document of /healthz and /readyz. It reads only what is
// safe before replay: with no engine yet it reports "starting".
func (s *Server) health() wire.Health {
	now := s.now()
	h := wire.Health{
		Status: "starting", Ready: s.ready.Load(), Version: s.opts.Version, SchemaVersion: SchemaVersion,
		UptimeSeconds: now.Sub(s.opts.Started).Seconds(),
		Store:         wire.HealthStore{State: wire.StoreOK, Writable: s.writable.Load()},
		Config:        wire.HealthConfig{RestartRequired: []string{}},
		Alerts:        wire.HealthAlerts{},
	}
	if h.UptimeSeconds < 0 {
		h.UptimeSeconds = 0
	}
	if check, _ := s.failing.Load().(string); check != "" && !h.Ready {
		h.FailingCheck = check
	}
	s.mu.Lock()
	h.Replay = wire.HealthReplay{Events: s.replay.Events, DurationSeconds: s.replay.Duration.Seconds()}
	h.Recovery = s.recovery
	cs := s.cfgState
	s.mu.Unlock()
	h.Config = wire.HealthConfig{
		Valid: cs.Valid, Digest: cs.Digest, ReloadError: cs.ReloadError, RestartRequired: append([]string{}, cs.RestartRequired...),
	}
	if !cs.LoadedAt.IsZero() {
		t := event.At(cs.LoadedAt)
		h.Config.LoadedAt = &t
	}
	e := s.engine()
	if e == nil {
		return h
	}
	h.Store.SizeBytes = e.StoreSize()
	store := wire.FromStoreHealth(s.storeHealth(e))
	h.Store.State, h.Store.Reason, h.Store.Since = store.State, store.Reason, store.Since
	snap := e.Snapshot()
	if log := snap.Model.Log(); len(log) > 0 {
		t := log[len(log)-1].At
		h.LastAppend = &t
	}
	if s.opts.Alerts != nil {
		a := s.opts.Alerts()
		h.Alerts.RunningCycle = a.RunningCycle
		if !a.NextReminder.IsZero() {
			t := event.At(a.NextReminder)
			h.Alerts.NextReminder = &t
		}
	}
	if h.Ready {
		h.Status = "ok"
		if store.State == wire.StoreReadOnly || !h.Store.Writable || !h.Config.Valid {
			h.Status = "degraded"
		}
	}
	return h
}

// getHealthz is liveness: it answers from process start, replay included.
func (s *Server) getHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.health())
}

// getReadyz is readiness: 503 not_ready, with the health document, until
// replay has finished and the first store writability check has passed. After
// that a failing check does not gate it: the daemon is then read-only or
// degraded, and reads keep working.
func (s *Server) getReadyz(w http.ResponseWriter, r *http.Request) {
	h := s.health()
	if !h.Ready {
		detail := "The service is not ready."
		if h.FailingCheck != "" {
			detail += " Failing check: " + h.FailingCheck + "."
		}
		p := wire.Simple(command.ReasonNotReady, detail, r.URL.Path, info(r.Context()).traceID)
		p.Health = &h
		s.writeProblem(w, r, p)
		return
	}
	writeJSON(w, http.StatusOK, h)
}
