package ledger

import (
	"context"
	"time"
)

// MetricsData is the ledger's contribution to the metrics gauges: every count
// and timestamp the gauges need, read in one pass. The metrics package turns it
// into named gauges.
type MetricsData struct {
	// Runs counts terminal runs by (lens, scope, outcome); outcome is filed,
	// skipped or failed. Running and AwaitingAnalyst runs are not counted.
	Runs []RunCount
	// Latest holds, per (lens, scope), the latest run in a terminal state.
	Latest []LatestRun
	// Schedules holds, per (lens, scope, schedule id), the schedule's state.
	// Only runs that carry a schedule id contribute.
	Schedules []ScheduleState
	// Pending, Topics, Decisions, Subjects and OutboxFailed are counts by label.
	Pending      []LabelCount
	Topics       []LabelCount
	Decisions    []LabelCount
	Subjects     []LabelCount
	OutboxFailed []LabelCount
}

// RunCount is the number of terminal runs with one outcome.
type RunCount struct {
	Lens, Scope, Outcome string
	N                    int
}

// LatestRun is the latest terminal run of one (lens, scope).
type LatestRun struct {
	Lens, Scope     string
	State           string
	DurationSeconds float64
	// ItemsFiled is the items filed by the latest Filed run of the pair (0
	// when the pair has never filed).
	ItemsFiled int
}

// ScheduleState is what the alerts need to know about one schedule of one
// (lens, scope).
type ScheduleState struct {
	Lens, Scope, Schedule string
	// HasFiled reports whether the schedule has a Filed run with a usable
	// window; LastSuccess is its window end and WindowSeconds its window length.
	HasFiled      bool
	LastSuccess   time.Time
	WindowSeconds float64
	// ConsecutiveFailed is the schedule's Failed runs since its last Filed run.
	ConsecutiveFailed int
}

// LabelCount is a count under a lens and scope plus one extra label value
// (state, ruling or type; empty for the unlabelled counts).
type LabelCount struct {
	Lens, Scope, Label string
	N                  int
	// Oldest is the update time of the oldest topic counted (Pending only).
	Oldest time.Time
}

// topicScope is the scope of a topic's latest occurrence; "" when it has none.
const topicScope = `COALESCE((SELECT o.scope FROM occurrence o WHERE o.topic_id=t.topic_id ORDER BY o.id DESC LIMIT 1),'')`

// MetricsData reads every figure the metrics gauges need.
func (l *Ledger) MetricsData(ctx context.Context) (MetricsData, error) {
	var md MetricsData
	var err error
	if md.Runs, err = l.runCounts(ctx); err != nil {
		return md, err
	}
	if md.Latest, err = l.latestRuns(ctx); err != nil {
		return md, err
	}
	if md.Schedules, err = l.scheduleStates(ctx); err != nil {
		return md, err
	}
	if md.Pending, err = l.labelCounts(ctx, `SELECT t.lens, `+topicScope+`, '', COUNT(*), MIN(t.updated_at) FROM topic t WHERE t.state='Pending' GROUP BY 1,2,3`); err != nil {
		return md, err
	}
	if md.Topics, err = l.labelCounts(ctx, `SELECT t.lens, `+topicScope+`, t.state, COUNT(*), '' FROM topic t GROUP BY 1,2,3`); err != nil {
		return md, err
	}
	if md.Decisions, err = l.labelCounts(ctx, `SELECT t.lens, `+topicScope+`, d.ruling, COUNT(*), '' FROM decision d JOIN topic t ON t.topic_id=d.topic_id GROUP BY 1,2,3`); err != nil {
		return md, err
	}
	// A subject is counted once per (lens, scope, type) in which a topic
	// tracks it; a subject no topic tracks is counted under no lens or scope.
	if md.Subjects, err = l.labelCounts(ctx, `SELECT lens, scope, type, COUNT(*), '' FROM (
		SELECT DISTINCT COALESCE(t.lens,'') AS lens, COALESCE(`+topicScope+`,'') AS scope, s.type AS type, s.defined_in, s.key
		FROM subject s LEFT JOIN topic t ON t.defined_in=s.defined_in AND t.subject_key=s.key)
		GROUP BY 1,2,3`); err != nil {
		return md, err
	}
	if md.OutboxFailed, err = l.labelCounts(ctx, `SELECT t.lens, `+topicScope+`, '', COUNT(*), '' FROM decision d JOIN topic t ON t.topic_id=d.topic_id WHERE d.bead_state='failed' GROUP BY 1,2,3`); err != nil {
		return md, err
	}
	return md, nil
}

func (l *Ledger) labelCounts(ctx context.Context, q string) ([]LabelCount, error) {
	rows, err := l.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []LabelCount
	for rows.Next() {
		var c LabelCount
		var oldest string
		if err := rows.Scan(&c.Lens, &c.Scope, &c.Label, &c.N, &oldest); err != nil {
			return nil, err
		}
		c.Oldest = parseTime(oldest)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (l *Ledger) runCounts(ctx context.Context) ([]RunCount, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT lens, scope, LOWER(state), COUNT(*) FROM run
		WHERE state IN ('Filed','Skipped','Failed') GROUP BY 1,2,3 ORDER BY 1,2,3`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RunCount
	for rows.Next() {
		var c RunCount
		if err := rows.Scan(&c.Lens, &c.Scope, &c.Outcome, &c.N); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (l *Ledger) latestRuns(ctx context.Context) ([]LatestRun, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT lens, scope, state, created_at, ended_at, elapsed_minutes,
		COALESCE((SELECT f.items_filed FROM run f WHERE f.lens=r.lens AND f.scope=r.scope AND f.state='Filed' ORDER BY f.seq DESC LIMIT 1),0)
		FROM run r
		WHERE state IN ('Filed','Skipped','Failed')
		AND seq=(SELECT MAX(seq) FROM run x WHERE x.lens=r.lens AND x.scope=r.scope AND x.state IN ('Filed','Skipped','Failed'))
		ORDER BY lens, scope`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []LatestRun
	for rows.Next() {
		var r LatestRun
		var created, ended string
		var elapsed float64
		if err := rows.Scan(&r.Lens, &r.Scope, &r.State, &created, &ended, &elapsed, &r.ItemsFiled); err != nil {
			return nil, err
		}
		c, e := parseTime(created), parseTime(ended)
		if !c.IsZero() && !e.IsZero() && !e.Before(c) {
			r.DurationSeconds = e.Sub(c).Seconds()
		} else {
			r.DurationSeconds = elapsed * 60
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (l *Ledger) scheduleStates(ctx context.Context) ([]ScheduleState, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT lens, scope, schedule_id, state, window_from, window_to FROM run
		WHERE schedule_id<>'' AND state IN ('Filed','Failed') ORDER BY lens, scope, schedule_id, seq`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ScheduleState
	var cur *ScheduleState
	for rows.Next() {
		var lens, scope, sched, state, from, to string
		if err := rows.Scan(&lens, &scope, &sched, &state, &from, &to); err != nil {
			return nil, err
		}
		if cur == nil || cur.Lens != lens || cur.Scope != scope || cur.Schedule != sched {
			out = append(out, ScheduleState{Lens: lens, Scope: scope, Schedule: sched})
			cur = &out[len(out)-1]
		}
		if state == RunFailed {
			cur.ConsecutiveFailed++
			continue
		}
		// A Filed run resets the failure count.
		cur.ConsecutiveFailed = 0
		f, ferr := time.Parse(time.RFC3339Nano, from)
		e, eerr := time.Parse(time.RFC3339Nano, to)
		if ferr == nil && eerr == nil {
			cur.HasFiled, cur.LastSuccess, cur.WindowSeconds = true, e, e.Sub(f).Seconds()
		}
	}
	return out, rows.Err()
}
