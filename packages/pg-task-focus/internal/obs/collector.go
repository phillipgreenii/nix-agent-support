package obs

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/view"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
)

// Source is what the projection collector reads at each scrape.
type Source interface {
	// Snapshot is the model, configuration and version, read together; ok is
	// false before the engine is open.
	Snapshot() (engine.Snapshot, bool)
	// State is the view of the snapshot at now.
	Now() time.Time
	// NextReminder is the instant the running cycle next alerts, if one runs.
	NextReminder() (time.Time, bool)
	// StoreHealth is the view of the store's health.
	StoreHealth() view.StoreHealth
}

// projectionCollector derives the restart-safe gauges from the projection at
// scrape time, so a restart changes nothing about them.
type projectionCollector struct {
	src Source

	tasks        *prometheus.Desc
	overdue      *prometheus.Desc
	resolutions  *prometheus.Desc
	resolutions3 *prometheus.Desc
	active       *prometheus.Desc
	overtime     *prometheus.Desc
	paused       *prometheus.Desc
	today        *prometheus.Desc
	attention    *prometheus.Desc
	nextReminder *prometheus.Desc
}

// RegisterProjection registers the gauges derived from the projection:
// tasks, tasks_overdue, task_resolutions, task_resolutions_30d, cycle_active,
// cycle_overtime_seconds, cycle_paused_seconds, cycle_seconds_in_active_day,
// attention_items and next_reminder_timestamp_seconds. They are live-state
// views: a corrected or retracted event rewrites the past, while Prometheus
// keeps what the gauge said at the time; the log is the authoritative history.
func (m *Metrics) RegisterProjection(src Source) {
	d := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc(Namespace+"_"+name, help, labels, nil)
	}
	m.Registry.MustRegister(&projectionCollector{
		src:          src,
		tasks:        d("tasks", "Tasks of the current periods by cadence and status.", "cadence", "status"),
		overdue:      d("tasks_overdue", "Open tasks of the current periods whose due time has passed, by cadence.", "cadence"),
		resolutions:  d("task_resolutions", "Live resolution events in the whole log, by cadence and outcome.", "cadence", "outcome"),
		resolutions3: d("task_resolutions_30d", "Live resolution events whose effective_at is in the last 30 days, the basis for a current miss rate.", "cadence", "outcome"),
		active:       d("cycle_active", "Cycles of a type that are running or paused (not stopped).", "type"),
		overtime:     d("cycle_overtime_seconds", "Seconds the running cycle of a type is past its planned end; 0 when none is.", "type"),
		paused:       d("cycle_paused_seconds", "Seconds the longest-paused cycle of a type has been paused; 0 when none is.", "type"),
		today: d("cycle_seconds_in_active_day",
			"Seconds of cycle running time by type, with every segment clipped to the civil date of the active day period in that period's zone.", "type"),
		attention:    d("attention_items", "Items of the attention feed, by type and severity.", "type", "severity"),
		nextReminder: d("next_reminder_timestamp_seconds", "Unix time the running cycle next alerts; 0 when none runs."),
	})
}

// Describe implements prometheus.Collector.
func (c *projectionCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.tasks, c.overdue, c.resolutions, c.resolutions3, c.active, c.overtime, c.paused, c.today, c.attention, c.nextReminder} {
		ch <- d
	}
}

var cadenceOfKind = map[projection.Kind]string{projection.Day: "daily", projection.Week: "weekly", projection.Sprint: "sprint"}

// Collect implements prometheus.Collector.
func (c *projectionCollector) Collect(ch chan<- prometheus.Metric) {
	snap, ok := c.src.Snapshot()
	if !ok {
		return
	}
	now := c.src.Now()
	st := view.Build(snap.Model, snap.Config, now)
	st.Store = c.src.StoreHealth()
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}

	// tasks and tasks_overdue: the tasks of the current periods.
	counts := map[[2]string]float64{}
	overdue := map[string]float64{}
	for _, t := range st.Tasks {
		cad := cadenceOfKind[t.Kind]
		counts[[2]string{cad, string(t.Task.Status)}]++
		if t.Overdue {
			overdue[cad]++
		}
	}
	for _, cad := range Cadences {
		for _, s := range TaskStatuses {
			gauge(c.tasks, counts[[2]string{cad, s}], cad, s)
		}
		gauge(c.overdue, overdue[cad], cad)
	}

	// task_resolutions: the live resolution events of the whole log.
	all, recent := map[[2]string]float64{}, map[[2]string]float64{}
	cutoff := now.Add(-30 * 24 * time.Hour)
	types := []event.Type{event.TypeTaskCompleted, event.TypeTaskSkipped, event.TypeTaskMissed, event.TypeTaskWithdrawn}
	for _, v := range snap.Model.Events(projection.EventQuery{Types: types}) {
		if v.Retracted {
			continue
		}
		id, outcome := resolutionOf(v.Corrected)
		if outcome == "" {
			continue
		}
		cad := cadenceOfTaskID(id)
		if cad == "" {
			continue
		}
		key := [2]string{cad, outcome}
		all[key]++
		if !v.Corrected.EffectiveAt.Time().Before(cutoff) {
			recent[key]++
		}
	}
	for _, cad := range Cadences {
		for _, o := range Outcomes {
			gauge(c.resolutions, all[[2]string{cad, o}], cad, o)
			gauge(c.resolutions3, recent[[2]string{cad, o}], cad, o)
		}
	}

	// cycle gauges, by the configured cycle types (bounded by the config).
	active, over, paused, inDay := map[string]float64{}, map[string]float64{}, map[string]float64{}, map[string]float64{}
	dayStart, dayEnd, haveDay := activeDay(snap.Model, now)
	for _, cy := range snap.Model.Cycles() {
		if cy.Status != projection.Stopped {
			active[cy.Type]++
		}
		if cy.Status == projection.Running {
			if rem := cy.Remaining(now); rem < 0 {
				over[cy.Type] = max(over[cy.Type], (-rem).Seconds())
			}
		}
		if cy.Status == projection.Paused && len(cy.Segments) > 0 {
			if end := cy.Segments[len(cy.Segments)-1].End; end != nil {
				paused[cy.Type] = max(paused[cy.Type], now.Sub(*end).Seconds())
			}
		}
		if haveDay {
			for _, s := range cy.Segments {
				end := now
				if s.End != nil {
					end = *s.End
				}
				inDay[cy.Type] += overlap(s.Start, end, dayStart, dayEnd).Seconds()
			}
		}
	}
	for _, t := range snap.Config.CycleTypes() {
		gauge(c.active, active[t.ID], t.ID)
		gauge(c.overtime, over[t.ID], t.ID)
		gauge(c.paused, paused[t.ID], t.ID)
		gauge(c.today, inDay[t.ID], t.ID)
	}

	// attention_items by type and severity, over the stable set of pairs.
	att := wire.BuildAttention(st, snap.Config, now, "")
	items := map[[2]string]float64{}
	for _, it := range att.Items {
		items[[2]string{it.Type, it.Severity}]++
	}
	for _, pair := range [][2]string{
		{"task", wire.SeverityMedium},
		{"task", wire.SeverityHigh},
		{"cycle", wire.SeverityLow},
		{"cycle", wire.SeverityMedium},
		{"cycle", wire.SeverityHigh},
		{"store", wire.SeverityHigh},
	} {
		gauge(c.attention, items[pair], pair[0], pair[1])
	}

	next := 0.0
	if t, ok := c.src.NextReminder(); ok {
		next = float64(t.UnixNano()) / 1e9
	}
	gauge(c.nextReminder, next)
}

// resolutionOf names the task and the outcome a resolution event records.
func resolutionOf(e event.Event) (event.TaskID, string) {
	switch p := e.Payload.(type) {
	case event.TaskCompleted:
		return p.TaskID, "completed"
	case event.TaskSkipped:
		return p.TaskID, "skipped"
	case event.TaskMissed:
		return p.TaskID, "missed"
	case event.TaskWithdrawn:
		return p.TaskID, "withdrawn"
	}
	return "", ""
}

// cadenceOfTaskID reads the cadence from a task id's prefix: day, week or
// sprint.
func cadenceOfTaskID(id event.TaskID) string {
	s := string(id)
	for prefix, cad := range map[string]string{"day:": "daily", "week:": "weekly", "sprint:": "sprint"} {
		if len(s) > len(prefix) && s[:len(prefix)] == prefix {
			return cad
		}
	}
	return ""
}

// activeDay is the civil date of the active day period, in that period's
// zone, as an interval of instants; false while no day period exists.
func activeDay(m *projection.Model, now time.Time) (time.Time, time.Time, bool) {
	p, ok := m.Period(projection.Day)
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	loc := p.TZ.Location()
	today := civil.Date{}
	t := now.In(loc)
	today = civil.Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}
	start := time.Date(today.Year, today.Month, today.Day, 0, 0, 0, 0, loc)
	end := time.Date(today.Year, today.Month, today.Day+1, 0, 0, 0, 0, loc)
	return start.UTC(), end.UTC(), true
}

// overlap is the length of [a1,a2) within [b1,b2).
func overlap(a1, a2, b1, b2 time.Time) time.Duration {
	lo, hi := a1, a2
	if b1.After(lo) {
		lo = b1
	}
	if b2.Before(hi) {
		hi = b2
	}
	if !hi.After(lo) {
		return 0
	}
	return hi.Sub(lo)
}
