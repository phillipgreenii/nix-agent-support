package server

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
)

// ---- reads ----

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	e, ok := s.requireEngine(w, r)
	if !ok {
		return
	}
	now := s.now()
	writeJSON(w, http.StatusOK, wire.FromState(e.State(now), now, e.Version()))
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	e, ok := s.requireEngine(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, wire.FromConfig(e.Snapshot().Config))
}

func (s *Server) getAttention(w http.ResponseWriter, r *http.Request) {
	e, ok := s.requireEngine(w, r)
	if !ok {
		return
	}
	now := s.now()
	snap := e.Snapshot()
	st := e.State(now)
	writeJSON(w, http.StatusOK, wire.BuildAttention(st, snap.Config, now, snap.Config.PublicURL()))
}

func (s *Server) getCalendar(w http.ResponseWriter, r *http.Request) {
	e, ok := s.requireEngine(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	from, okFrom := s.instantParam(w, r, q.Get("from"), "from")
	if !okFrom {
		return
	}
	to, okTo := s.instantParam(w, r, q.Get("to"), "to")
	if !okTo {
		return
	}
	if !to.After(from) {
		s.invalid(w, r, "The window is empty: to (%s) is not after from (%s).", q.Get("to"), q.Get("from"))
		return
	}
	now := s.now()
	cal := wire.BuildCalendar(e.Snapshot().Model.Cycles(), from, to, now)
	if name := q.Get("calendar"); name != "" && name != wire.CalendarID && name != wire.CalendarName {
		cal.Events = []wire.Segment{}
	}
	writeJSON(w, http.StatusOK, cal)
}

// instantParam parses a required RFC 3339 query parameter.
func (s *Server) instantParam(w http.ResponseWriter, r *http.Request, v, name string) (time.Time, bool) {
	if v == "" {
		s.invalid(w, r, "The query parameter %s is required (an RFC 3339 instant).", name)
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		s.invalid(w, r, "The query parameter %s=%q is not an RFC 3339 instant (for example 2026-10-07T13:30:00Z).", name, v)
		return time.Time{}, false
	}
	return t.UTC(), true
}

func (s *Server) getEvents(w http.ResponseWriter, r *http.Request) {
	e, ok := s.requireEngine(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	eq := projection.EventQuery{View: projection.ViewCorrected}
	switch v := q.Get("view"); v {
	case "", projection.ViewCorrected:
	case projection.ViewOriginal:
		eq.View = projection.ViewOriginal
	default:
		s.invalid(w, r, "The query parameter view=%q is not one of corrected or original.", v)
		return
	}
	if v := q.Get("from"); v != "" {
		t, okp := s.instantParam(w, r, v, "from")
		if !okp {
			return
		}
		eq.From = &t
	}
	if v := q.Get("to"); v != "" {
		t, okp := s.instantParam(w, r, v, "to")
		if !okp {
			return
		}
		eq.To = &t
	}
	known := event.Types()
	for _, raw := range q["type"] {
		for _, t := range strings.Split(raw, ",") {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if !slices.Contains(known, event.Type(t)) {
				s.invalid(w, r, "The query parameter type=%q is not an event type.", t)
				return
			}
			eq.Types = append(eq.Types, event.Type(t))
		}
	}
	out, err := wire.FromEvents(e.Snapshot().Model.Events(eq), eq.View)
	if err != nil {
		s.writeError(w, r, e, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- mutations ----

func (s *Server) postPeriodsChange(w http.ResponseWriter, r *http.Request) {
	var req wire.ChangePeriodsRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	if len(req.Changes) == 0 {
		s.invalid(w, r, "A period change needs at least one change (kind, start, tz).")
		return
	}
	s.respondMutation(w, r, req.ToCommand())
}

func (s *Server) postProfileChange(w http.ResponseWriter, r *http.Request) {
	var req wire.ChangeProfileRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	if req.Profile == "" {
		s.invalid(w, r, "A profile change needs the profile.")
		return
	}
	s.respondMutation(w, r, req.ToCommand())
}

func (s *Server) postTaskComplete(w http.ResponseWriter, r *http.Request) {
	var req wire.CompleteTaskRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	s.respondMutation(w, r, command.CompleteTask{
		ID: event.ID(req.ID), TaskID: event.TaskID(r.PathValue("id")), EffectiveAt: wire.EffectiveAt(req.EffectiveAt),
	})
}

func (s *Server) postTaskSkip(w http.ResponseWriter, r *http.Request) {
	var req wire.SkipTaskRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	s.respondMutation(w, r, command.SkipTask{
		ID: event.ID(req.ID), TaskID: event.TaskID(r.PathValue("id")), Reason: req.Reason, EffectiveAt: wire.EffectiveAt(req.EffectiveAt),
	})
}

func (s *Server) postCycleStart(w http.ResponseWriter, r *http.Request) {
	var req wire.StartCycleRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	s.respondMutation(w, r, command.StartCycle{
		ID: event.ID(req.ID), Type: req.Type, Minutes: req.Minutes, EffectiveAt: wire.EffectiveAt(req.EffectiveAt),
	})
}

// postCycleVerb is the handler of pause, resume and stop, which take the same
// body.
func (s *Server) postCycleVerb(verb string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req wire.CycleVerbRequest
		if !s.readJSON(w, r, &req) {
			return
		}
		id, cycle, at := event.ID(req.ID), event.CycleID(req.CycleID), wire.EffectiveAt(req.EffectiveAt)
		switch verb {
		case "pause":
			s.respondMutation(w, r, command.PauseCycle{ID: id, CycleID: cycle, EffectiveAt: at})
		case "resume":
			s.respondMutation(w, r, command.ResumeCycle{ID: id, CycleID: cycle, EffectiveAt: at})
		default:
			s.respondMutation(w, r, command.StopCycle{ID: id, CycleID: cycle, EffectiveAt: at})
		}
	}
}

func (s *Server) postCycleBoost(w http.ResponseWriter, r *http.Request) {
	var req wire.BoostCycleRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	s.respondMutation(w, r, command.BoostCycle{
		ID: event.ID(req.ID), CycleID: event.CycleID(req.CycleID), Minutes: req.Minutes, EffectiveAt: wire.EffectiveAt(req.EffectiveAt),
	})
}

func (s *Server) postCycleSwitch(w http.ResponseWriter, r *http.Request) {
	var req wire.SwitchCycleRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	if req.To == "" {
		s.invalid(w, r, "A switch needs `to`, the paused cycle that becomes the focus.")
		return
	}
	s.respondMutation(w, r, command.SwitchCycle{ID: event.ID(req.ID), To: event.CycleID(req.To), EffectiveAt: wire.EffectiveAt(req.EffectiveAt)})
}

func (s *Server) postCycleAnnotate(w http.ResponseWriter, r *http.Request) {
	var req wire.AnnotateCycleRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	s.respondMutation(w, r, command.AnnotateCycle{
		ID: event.ID(req.ID), CycleID: event.CycleID(req.CycleID), Note: req.Note, KV: wire.KVsToEvent(req.KV),
	})
}

func (s *Server) postCycleBreak(w http.ResponseWriter, r *http.Request) {
	var req wire.BreakCycleRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	if req.CycleID == "" || req.From == nil || req.To == nil {
		s.invalid(w, r, "A break needs cycle_id, from and to.")
		return
	}
	s.respondMutation(w, r, command.BackfillBreak{
		ID: event.ID(req.ID), CycleID: event.CycleID(req.CycleID), From: req.From.Time, To: req.To.Time,
	})
}

func (s *Server) postCorrect(w http.ResponseWriter, r *http.Request) {
	var req wire.CorrectRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	if len(req.Fields) == 0 {
		s.invalid(w, r, "A correction needs `fields`, the replacement values.")
		return
	}
	s.respondMutation(w, r, command.Correct{
		ID: event.ID(req.ID), Target: event.ID(r.PathValue("id")), Fields: req.Fields, Reason: req.Reason,
	})
}

func (s *Server) postRetractEvent(w http.ResponseWriter, r *http.Request) {
	var req wire.RetractRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	s.respondMutation(w, r, command.Retract{ID: event.ID(req.ID), Target: event.ID(r.PathValue("id")), Reason: req.Reason})
}

func (s *Server) postRetractBatch(w http.ResponseWriter, r *http.Request) {
	var req wire.RetractRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	s.respondMutation(w, r, command.Retract{ID: event.ID(req.ID), TargetBatch: event.ID(r.PathValue("id")), Reason: req.Reason})
}
