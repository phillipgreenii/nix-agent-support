package core

import (
	"context"
	"math"
	"os"
	"sort"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/api/status"
	"github.com/phillipgreenii/pg-router/internal/activity"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/levels"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// composeStatusReply builds the cli.status-reply body as the published
// status.Reply. Lock ordering (Task 3.8 Binding decisions, Step 8):
// DepthByType/UnmatchedBindings are already lock-free reads of eventqueue's own
// published depthCell (Task 3.2), so no q.mu is ever taken here —
// Registry.List() (its own mutex) and s.activityRing.Read (the ring's own mutex)
// each run independently, with no lock held across either call.
//
// tick may be nil (the boot window, before the drive loop's first
// PublishTick) — every tick-derived field is simply omitted from the reply
// rather than guessed at, and nothing here panics on that nil.
//
// deliveries stays the legacy shape but is always empty: no producer of a
// per-(event,handler) delivery record keyed by a dispatch tracking id exists.
func (s *Service) composeStatusReply(ctx context.Context, since uint64) status.Reply {
	regs := s.reg.List()
	tick := s.CurrentTick()

	legacySources, legacyHandlers := 0, 0
	if tick != nil {
		legacySources = tick.Config.ActiveQueries
		legacyHandlers = tick.Config.ActiveRoles
	}

	// inFlightSources is the per-source "processing now" signal (DEC-OBS-2): a
	// set, since every call site only asks "is THIS source in it". It is nil
	// unless Options.SourceInFlight was set, which reads as nothing in flight.
	var inFlightSources map[string]bool
	if s.sourceInFlight != nil {
		names := s.sourceInFlight.InFlightSources()
		inFlightSources = make(map[string]bool, len(names))
		for _, n := range names {
			inFlightSources[n] = true
		}
	}

	reply := status.Reply{
		SchemaVersion: status.SchemaVersion,
		Deliveries:    []status.Delivery{},
		Queues:        statusQueues(s.q.DepthByType()),
		Config:        status.Config{Sources: legacySources, Handlers: legacyHandlers},
		// dispatch.busy/dispatch.total: the real-time fan-out concurrency pair.
		// busy is Queue.SessionsInFlight() (len(custody)), which may exceed 1
		// under the bounded fan-out; total is the active listener count.
		Dispatch: status.Dispatch{Busy: s.q.SessionsInFlight(), Total: s.q.ListenerCount()},
		QueueLog: statusQueueLog(s.q),
		Core: status.CoreInfo{
			State:      s.State().String(),
			PID:        os.Getpid(),
			StartedAt:  s.startedAt.UTC(),
			ConfigPath: s.configPath,
		},
		Registry:  s.statusRegistrationsWithRequires(regs),
		Listeners: statusListeners(s.declaredRoles, s.excludedRoles, s.listenerCounts, regs, s.q.InFlightListeners(), s.readRoleProgress()),
		Levels:    s.statusLevels(),
		// pausedRoles: every role a level requirement blocks right now, with the
		// failing clauses.
		PausedRoles: s.statusPausedRoles(),
		AsOf:        time.Now().UTC(),
		Sources:     statusSources(nil, s.excludedSources, s.sourceIntervalsMs, inFlightSources, s.sourceDescriptions),
		Activity:    []status.ActivityEntry{},
	}
	reply.UnmatchedBindings = s.q.UnmatchedBindings(s.declaredTypesSorted())
	if s.activityRing != nil {
		buf := make([]activity.Entry, activityReadWindow)
		n, dropped := s.activityRing.Read(since, buf)
		reply.Activity = statusActivity(buf[:n])
		reply.ActivityDropped = dropped
	}
	if tick != nil {
		reply.Core.Version = tick.Version
		reply.Mode = tick.RunMode
		reply.ResolvedConfig = statusResolvedConfig(tick.Config)
		reply.Sources = statusSources(tick.Sources, s.excludedSources, s.sourceIntervalsMs, inFlightSources, s.sourceDescriptions)
		reply.LastTickAt = tick.LastTickAt.UTC()
		reply.SnapshotAt = tick.SnapshotAt.UTC()
		if tick.Config.PollInterval != nil {
			reply.TickIntervalMs = tick.Config.PollInterval.Milliseconds()
		}
	}
	reply.Busy = statusBusy(reply.Listeners, reply.Sources)
	// counters are read back verbatim from the metrics mechanism (never a second,
	// independently-incremented counter). A nil MetricsReader omits the key; a
	// Snapshot error also omits it rather than failing the whole status call:
	// status is the TUI's every-tick poll dependency, so it must never fail
	// outright over a metrics-snapshot hiccup.
	if s.metricsReader != nil {
		if rm, err := s.metricsReader.Snapshot(ctx); err == nil {
			reply.Counters = statusCounters(rm)
		}
	}
	return reply
}

// statusQueues renders DepthByType's map as the legacy `queues` array, sorted by
// type for a deterministic reply.
func statusQueues(depth map[string]int) []status.Queue {
	types := make([]string, 0, len(depth))
	for t := range depth {
		types = append(types, t)
	}
	sort.Strings(types)
	out := make([]status.Queue, 0, len(types))
	for _, t := range types {
		out = append(out, status.Queue{Type: t, Depth: depth[t]})
	}
	return out
}

// statusRegistrations renders Registry.List() entries as the `registry` array.
func statusRegistrations(regs []Registration) []status.Registration {
	out := make([]status.Registration, 0, len(regs))
	for _, r := range regs {
		out = append(out, status.Registration{
			ID:    r.ID,
			Kind:  string(r.Kind),
			State: r.State.String(),
			Self:  string(r.Self),
		})
	}
	return out
}

// statusListeners renders `listeners`: the per-role view over the FULL declared
// role set (Options.DeclaredRoles, pre-selector), so a selector-excluded role
// still renders. excludedRoles is Options.ExcludedRoles: captured before the
// selector flips Role.Enabled, so `excluded` is computed from this independent
// signal. counts is Options.ListenerCounts; a role absent from it reads as
// delivered=0/declined=0.
//
// `backoff` is always null: nothing here holds a live reference to any
// orchestrator.roleListener, and the shipped production listener never accrues a
// running backoff streak.
//
// regs is the registry's live participant list, joined onto each declared role
// by Registration.ID == role name, so `selfReportState` reports the handler's
// own self-status (healthy / degraded / unavailable), or the empty string for a
// role that has never self-reported, and `unavailableReason` carries why core
// marked the role unavailable.
//
// The output is sorted by Role.Name so the Listeners pane renders in a stable
// order regardless of the config's own role-declaration order.
//
// inFlight is s.q.InFlightListeners(): eventqueue.Listener.ID() is the role
// name, the same key this map is keyed by.
func statusListeners(declared []roles.Role, excludedRoles []string, counts map[string]*ListenerCounts, regs []Registration, inFlight map[string]string, progress map[string]metrics.RoleProgress) []status.Listener {
	excluded := make(map[string]bool, len(excludedRoles))
	for _, n := range excludedRoles {
		excluded[n] = true
	}
	selfState := make(map[string]string, len(regs))
	unavailableReason := make(map[string]string, len(regs))
	for _, r := range regs {
		// The participant's SELF-status, not its lifecycle state; empty for a
		// participant that has never self-reported.
		selfState[r.ID] = string(r.Self)
		unavailableReason[r.ID] = r.UnavailableReason
	}
	out := make([]status.Listener, 0, len(declared))
	for _, r := range declared {
		binds := make([]string, len(r.Binds))
		copy(binds, r.Binds)
		var delivered, declined, lastDeliveredAtMs, handlerFailures int64
		declinedByReason := map[string]int64{}
		if c := counts[r.Name]; c != nil {
			delivered = c.Delivered.Load()
			declined = c.Declined.Load()
			declinedByReason = c.DeclinedByReasonSnapshot()
			if nanos := c.LastDeliveredAtNanos.Load(); nanos != 0 {
				lastDeliveredAtMs = nanos / int64(time.Millisecond)
			}
			handlerFailures = c.HandlerFailures.Load()
		}
		inFlightType, inFlightNow := inFlight[r.Name]
		out = append(out, status.Listener{
			Role:     r.Name,
			Binds:    binds,
			Enabled:  r.Enabled,
			Excluded: excluded[r.Name],
			// true exactly while this role has an outstanding offer.
			InFlight:          inFlightNow,
			InFlightEventType: inFlightType,
			// busy: metrics.RoleBusy, the one definition the pg_router_busy gauge
			// uses too; never a decline reason.
			Busy: metrics.RoleBusy(inFlightNow, progress[r.Name]),
			// description is ALWAYS present, even "".
			Description: r.Description,
			// declined stays the flat total; declinedByReason is a second,
			// additive breakdown of the SAME tally, empty (never missing) for a
			// role with no declines.
			Delivered:         delivered,
			Declined:          declined,
			DeclinedByReason:  declinedByReason,
			LastDeliveredAtMs: lastDeliveredAtMs,
			// handlerFailures counts genuine business-logic rejections, NOT a
			// decline, since the item was accepted.
			HandlerFailures:   handlerFailures,
			SelfReportState:   selfState[r.Name],
			UnavailableReason: unavailableReason[r.Name],
			// progress: the state the role-progress gauges read, nil for a role
			// with no progress series (a disabled role).
			Progress: progressObject(progress, r.Name),
			Backoff:  nil,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Role < out[j].Role })
	return out
}

// statusBusy is the status reply's top-level `busy` object: the names of the
// roles and of the sources that are busy right now, each sorted, read off the
// already-rendered rows' own busy flags so the list and the rows cannot
// disagree. Both lists are always present, empty when nothing is busy.
func statusBusy(listeners []status.Listener, sources []status.Source) status.Busy {
	out := status.Busy{Roles: []string{}, Sources: []string{}}
	for _, l := range listeners {
		if l.Busy {
			out.Roles = append(out.Roles, l.Role)
		}
	}
	for _, src := range sources {
		if src.Busy {
			out.Sources = append(out.Sources, src.Name)
		}
	}
	sort.Strings(out.Roles)
	sort.Strings(out.Sources)
	return out
}

// readRoleProgress reads the per-role progress state for one status reply; nil
// when no read is wired.
func (s *Service) readRoleProgress() map[string]metrics.RoleProgress {
	if s.roleProgress == nil {
		return nil
	}
	return s.roleProgress()
}

// progressObject is one listener's `progress` value, or nil (JSON null) when
// role has no progress series.
func progressObject(progress map[string]metrics.RoleProgress, role string) *status.Progress {
	p, ok := progress[role]
	if !ok {
		return nil
	}
	return &status.Progress{AgeSeconds: p.AgeSeconds, Unsuccessful: p.Unsuccessful, Busy: p.Busy}
}

// statusSources renders `sources` over the FULL configured source set: the
// active sources of THIS pass's TickSnapshot UNION excludedSources
// (Options.ExcludedSources, the selector-excluded names, for which nothing is
// left to report beyond the bare name), merged into ONE name-sorted list.
//
// type/mode are always "pull": no push query type exists in this codebase (see
// SourceReport.Type's doc). `enabled` is always true for a source: a query
// carries no config-level enable flag; the only way a configured source is
// inactive this run is selector exclusion, `excluded` alone. `lastTick` is the
// source's real last-fire time regardless of which pass fired it
// (SourceReport.LastTick's doc); `failure` stays per-pass.
//
// inFlight is a set of source names currently between an OnSourceFetchStart and
// its matching OnSourceFetchEnd; nil reads as "nothing in flight".
func statusSources(active []SourceReport, excludedSources []string, intervalsMs map[string]int64, inFlight map[string]bool, descriptions map[string]string) []status.Source {
	out := make([]status.Source, 0, len(active)+len(excludedSources))
	for _, sr := range active {
		row := status.Source{
			Name:               sr.Name,
			Type:               "pull",
			Enabled:            true,
			Mode:               "pull",
			ExpectedIntervalMs: intervalsMs[sr.Name],
			InFlight:           inFlight[sr.Name],
			// busy: a source is busy exactly while a fetch of it is in flight (a
			// [[query]] is a pull source; there is no separate query entity).
			Busy:        inFlight[sr.Name],
			Description: descriptions[sr.Name],
		}
		if !sr.LastTick.IsZero() {
			row.LastTick = sr.LastTick.UTC()
		}
		if sr.Failure != nil {
			row.Failure = &status.Failure{Count: sr.Failure.Count, NextEligible: sr.Failure.NextEligible.UTC()}
		}
		if c := sr.Calendar; c != nil {
			cal := &status.Calendar{Schedule: c.Schedule, OnMissed: c.OnMissed, Next: c.Next}
			if c.Last != nil {
				cal.Last = &status.CalendarLast{Slot: c.Last.Slot, Outcome: c.Last.Outcome, Reason: c.Last.Reason}
			}
			row.Calendar = cal
		}
		out = append(out, row)
	}
	for _, name := range excludedSources {
		// An excluded source cannot be mid-fetch (its query never runs this run),
		// so inFlight and busy are always false, never a lookup.
		out = append(out, status.Source{
			Name:               name,
			Type:               "pull",
			Enabled:            true,
			Excluded:           true,
			Mode:               "pull",
			ExpectedIntervalMs: intervalsMs[name],
			Description:        descriptions[name],
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// statusCounters folds the metric catalog's three delivery-side counters into the
// status reply's `counters` shape, keyed by event type, reusing mon.go's own
// monReadDataPoints flattening.
func statusCounters(rm metricdata.ResourceMetrics) *status.Counters {
	out := &status.Counters{
		UnconsumedExpired:   map[string]int64{},
		UnknownTypeRejected: map[string]int64{},
		Deduped:             map[string]int64{},
	}
	target := map[string]map[string]int64{
		metrics.MetricUnconsumedExpired:   out.UnconsumedExpired,
		metrics.MetricUnknownTypeRejected: out.UnknownTypeRejected,
		metrics.MetricDeduped:             out.Deduped,
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			perType, ok := target[m.Name]
			if !ok {
				continue
			}
			for _, v := range monReadDataPoints(m) {
				if t, ok := v.Labels["type"].(string); ok {
					perType[t] = int64(v.Value)
				}
			}
		}
	}
	return out
}

// statusActivity renders Ring.Read's output as the `activity` array, oldest first.
func statusActivity(entries []activity.Entry) []status.ActivityEntry {
	out := make([]status.ActivityEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, status.ActivityEntry{
			Seq:       e.Seq,
			StartedAt: e.StartedAt.UTC(),
			Type:      e.Type,
			Outcome:   e.Outcome,
			// participant is "" for an entry no single participant settled.
			Participant: e.Participant,
		})
	}
	return out
}

// statusResolvedConfig renders a TickSnapshot's ResolvedConfig as the
// `resolvedConfig` object; `pollIntervalMs` is omitted when PollInterval is nil
// (drain-and-exit mode has no polling cadence to report).
//
// perParticipant is ALWAYS present: an honestly-empty object, since no per-kind
// producer exists. Do NOT invent a per-kind shape here.
func statusResolvedConfig(cfg ResolvedConfig) *status.ResolvedConfig {
	out := &status.ResolvedConfig{
		RepoRoot:       cfg.RepoRoot,
		BeadsPrefix:    cfg.BeadsPrefix,
		ActiveRoles:    cfg.ActiveRoles,
		ActiveQueries:  cfg.ActiveQueries,
		PerParticipant: map[string]any{},
	}
	if cfg.PollInterval != nil {
		out.PollIntervalMs = int(cfg.PollInterval.Milliseconds())
	}
	return out
}

// statusQueueLog composes the status reply's `queueLog` object: the log's size and
// compaction count plus the size-limit enforcement state — the single most
// severe state, whether polled emitters are halted, and the admission refusals
// so far by reason always; the hard limit, the soft threshold and the percent
// only when a hard limit is configured. bytes is 0 when the queue's store cannot
// report a size.
func statusQueueLog(q *eventqueue.Queue) status.QueueLog {
	ls := q.LimitStatus()
	out := status.QueueLog{
		Bytes:          ls.Bytes,
		Compactions:    q.Compactions(),
		State:          ls.State,
		EmittersHalted: ls.EmittersHalted,
		Detail:         ls.Detail,
	}
	out.Rejected.LogFull = ls.RejectedLogFull
	out.Rejected.LogUnwritable = ls.RejectedLogUnwritable
	if ls.HardBytes > 0 {
		out.LimitBytes = ls.HardBytes
		out.SoftBytes = ls.SoftBytes
		out.Percent = math.Round(ls.Percent()*10) / 10
	}
	out.LastCompaction = lastCompactionStatus(q)
	return out
}

// statusLevels is the status reply's per-resource levels array (the same entries
// as level-list).
func (s *Service) statusLevels() []status.Level {
	if s.levels == nil {
		return []status.Level{}
	}
	return LevelEntries(s.levels)
}

// statusPausedRoles is the status reply's pausedRoles array: one entry per role
// the level requirements block right now, with its failing clauses, each carrying
// the level, its source and owner, when the resource entered that level and the
// reading behind it. Empty with no level projection wired.
func (s *Service) statusPausedRoles() []status.PausedRole {
	out := []status.PausedRole{}
	if s.levels == nil {
		return out
	}
	paused := s.q.PausedRoles()
	if len(paused) == 0 {
		return out
	}
	details := map[string]LevelDetail{}
	for _, d := range s.levels.Details() {
		details[d.Resource] = d
	}
	for _, p := range paused {
		failing := make([]status.FailingClause, 0, len(p.Result.Failing))
		for _, f := range p.Result.Failing {
			e := status.FailingClause{Clause: f.ClauseText, Resource: f.Resource, Kind: string(f.Kind), Level: f.Level}
			if len(f.Allowed) > 0 {
				e.Allowed = f.Allowed
			}
			if f.Kind != levels.KindUnknown {
				e.Source = f.Source
				e.Owner = f.Owner
				if d, ok := details[f.Resource]; ok {
					if !d.Since.IsZero() {
						e.Since = d.Since.UTC()
					}
					if d.Reading != nil {
						e.Reading = &status.LevelReading{Value: d.Reading.Value, Unit: d.Reading.Unit}
					}
				}
			}
			failing = append(failing, e)
		}
		out = append(out, status.PausedRole{Role: p.Role, Failing: failing})
	}
	return out
}

// statusRegistrationsWithRequires is statusRegistrations plus, on each entry
// whose id has registered requires clauses, a `requires` array: one object per
// original clause with its text, resource, allowed levels, the current verdict
// ("pass" or "fail") and the level the clause was judged against.
func (s *Service) statusRegistrationsWithRequires(regs []Registration) []status.Registration {
	out := statusRegistrations(regs)
	if s.q == nil {
		return out
	}
	for i, r := range regs {
		req := s.q.RequirementOf(r.ID)
		if len(req.Clauses) == 0 {
			continue
		}
		res := s.q.Explain(r.ID)
		snap := s.q.Levels()
		failing := map[int]levels.Entry{}
		for _, f := range res.Failing {
			failing[f.ClauseIndex] = f
		}
		clauses := make([]status.RequiresClause, 0, len(req.Clauses))
		for _, c := range req.Clauses {
			e := status.RequiresClause{Clause: c.Text, Resource: c.Resource, Allowed: append([]string{}, c.Allowed...)}
			if f, bad := failing[c.Index]; bad {
				e.Verdict = "fail"
				e.Kind = string(f.Kind)
				e.Level = f.Level
			} else {
				e.Verdict = "pass"
				if lv, ok := snap.Levels[c.Resource]; ok {
					e.Level = lv.Value
				}
			}
			clauses = append(clauses, e)
		}
		out[i].Requires = clauses
	}
	return out
}
