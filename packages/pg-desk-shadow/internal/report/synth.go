package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
	"github.com/phillipgreenii/pg-desk-shadow/internal/scratch"
)

// Synthetic is the synthetic scratch directory the self-test and the tests
// build; every id, slug and title in it is made up.
type Synthetic struct {
	Dir    string
	Slug   string
	IDs    []string
	Titles []string
	T0     time.Time
}

const synthSlug = "acme/api"

func synthID(n int) string { return fmt.Sprintf("%s#%d", synthSlug, n) }

// SynthOptions tweaks the synthetic scenario (tests inject variants).
type SynthOptions struct {
	// AllHashChanged makes every live sweep row say content_hash_changed.
	AllHashChanged bool
	// NoRunRecord omits the run-record copy.
	NoRunRecord bool
	// Phase tags the manifest and the rows ("A" by default).
	Phase string
	// Seeded sets the manifest flag.
	Seeded bool
}

func w(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var b []byte
	for _, l := range lines {
		b = append(b, l...)
		b = append(b, '\n')
	}
	return os.WriteFile(path, b, 0o600)
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Synthesize writes the scenario under dir.
func Synthesize(dir string, o SynthOptions) (Synthetic, error) {
	if o.Phase == "" {
		o.Phase = "A"
	}
	t0 := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC) // a Monday
	l := scratch.Layout{Root: dir}
	if err := l.MkdirAll(); err != nil {
		return Synthetic{}, err
	}
	s := Synthetic{Dir: dir, Slug: synthSlug, T0: t0}
	for _, n := range []int{101, 102, 103, 104, 105, 106, 107, 108, 109, 110} {
		s.IDs = append(s.IDs, synthID(n))
	}
	s.Titles = []string{"Add retry to client", "Fix flaky timeout test", "Rename worker pool"}

	m := scratch.Manifest{
		Phase: o.Phase, CreatedAt: schema.Format(t0), Seeded: o.Seeded, BDMode: "passthrough", Queries: []string{"mine", "team"},
		Consumer: "shadow-compare", MaxPerPoll: 50, SweepMaxAge: "8760h", ReconcileAge: "30m", Builds: map[string]string{"pg-desk": "pg-desk version 0.0.0-synth", "pg-connector": "pg-connector version 0.0.0-synth"},
	}
	if err := l.Save(m); err != nil {
		return s, err
	}
	if err := os.WriteFile(l.HMACKey(), []byte("0123456789abcdef0123456789abcdef\n"), 0o600); err != nil {
		return s, err
	}
	seedIDs := []string{}
	for _, n := range []int{101, 102, 103, 104, 105, 107, 108, 109} {
		seedIDs = append(seedIDs, synthID(n))
	}
	if err := os.WriteFile(l.SeedFile(), []byte(js(map[string]any{"ids": seedIDs})+"\n"), 0o600); err != nil {
		return s, err
	}

	at := func(h, mi, sec int) time.Time { return time.Date(2026, 1, 5, h, mi, sec, 0, time.UTC) }
	type item struct {
		id     string
		t      time.Time
		kinds  []string
		origin string
		fields []string
	}
	extra := map[time.Time][]item{}
	add := func(it item) {
		slot := it.t.Truncate(time.Minute)
		extra[slot] = append(extra[slot], it)
	}
	add(item{synthID(101), at(10, 5, 30), []string{"head_changed"}, "pg-connector", []string{"head_sha"}})
	add(item{synthID(106), at(10, 50, 20), []string{"reconcile"}, "pg-connector", nil})
	add(item{synthID(107), at(11, 30, 10), []string{"head_changed"}, "pg-connector", []string{"head_sha"}})
	add(item{synthID(108), at(10, 10, 5), []string{"reconcile"}, "local-reconcile", nil})
	add(item{synthID(105), at(11, 20, 5), []string{"reconcile"}, "sweep", []string{"mergeable"}})

	var rows []string
	rows = append(rows, js(schema.Row{Schema: schema.Version, Kind: schema.KindRunStart, Phase: o.Phase, At: schema.Format(t0)}))
	zero, _ := 0, 0
	_ = zero
	gapFrom, gapTo := at(10, 28, 0), at(10, 35, 0)
	rows = append(rows, js(schema.Row{Schema: schema.Version, Kind: schema.KindGap, Phase: o.Phase, Reason: schema.GapSleep, GapFrom: schema.Format(gapFrom), GapTo: schema.Format(gapTo), At: schema.Format(gapTo)}))
	n := 0
	for slot := t0.Add(time.Minute); slot.Before(at(12, 0, 0)); slot = slot.Add(time.Minute) {
		if !slot.Before(gapFrom) && slot.Before(gapTo) {
			continue
		}
		n++
		ts := slot.Add(time.Second)
		row := schema.Row{
			Schema: schema.Version, Kind: schema.KindTick, Phase: o.Phase, Slot: schema.Format(slot), TickStart: schema.Format(ts),
			TickEnd: schema.Format(ts.Add(12 * time.Second)), DurationMS: 12000, ExitCode: schema.ExitPtr(0), Status: schema.StatusOK,
			Warmup: n <= 2, Hydrations: 0, GraphQLCostSum: 3, Builds: map[string]string{"pg-desk": "pg-desk version 0.0.0-synth"},
			Budget: &schema.Budget{Remaining: 3800 - n, ResetAt: schema.Format(at(12, 30, 0)), Source: "live", Floor: 2000, Known: true},
		}
		for _, it := range extra[slot] {
			row.Hydrations++
			row.Items = append(row.Items, schema.Item{EntityID: it.id, Kinds: it.kinds, Seq: int64(n), Origin: it.origin, At: schema.Format(it.t), Fields: it.fields, UpdatedAt: schema.Format(it.t.Add(-90 * time.Second))})
		}
		if slot.Equal(at(10, 15, 0)) {
			row.Status = schema.StatusSkippedBudget
			row.SkippedBudget = true
			row.ExitCode = nil
			row.DurationMS = 0
		}
		rows = append(rows, js(row))
	}
	if err := w(l.TicksFile(), rows); err != nil {
		return s, err
	}

	// Live router events (note the fourth row has no event_type: an old row).
	ev := func(id string, enq time.Time, kind string) string {
		return js(map[string]any{
			"time": schema.Format(enq.Add(20 * time.Minute)), "level": "info", "msg": "dispatch result", "kind": "dispatch", "role": "desk-pr",
			"event_type": kind, "bead": id, "change": kind + ":" + id, "enqueued_at": schema.Format(enq), "started_at": schema.Format(enq.Add(18 * time.Minute)), "duration_ms": 15000,
		})
	}
	var events []string
	events = append(
		events,
		ev(synthID(101), at(9, 1, 0), "pr.changed"), // inside warm-up: excluded
		ev(synthID(101), at(10, 5, 0), "pr.changed"),
		ev(synthID(102), at(10, 20, 0), "pr.changed"), // unexplained miss
		ev(synthID(103), at(10, 30, 0), "pr.changed"), // collector-down
		ev(synthID(104), at(10, 40, 0), "pr.changed"), // live-only
		ev(synthID(105), at(11, 0, 0), "pr.changed"),  // blind-spot
		ev(synthID(106), at(10, 50, 0), "pr.changed"), // first observation, matched
		ev(synthID(108), at(10, 10, 0), "pr.changed"), // only a local-reconcile item: not SHADOW-DETECTED
		js(map[string]any{"time": schema.Format(at(10, 1, 0)), "kind": "dispatch", "role": "desk-pr", "bead": synthID(109), "change": "pr.changed:" + synthID(109)}),
	)
	// Heartbeat dispatches keep the router "up".
	for m := 0; m < 180; m += 3 {
		events = append(events, js(map[string]any{"time": schema.Format(t0.Add(time.Duration(m) * time.Minute)), "kind": "dispatch", "event_type": "desk.heartbeat", "role": "desk-heartbeat", "enqueued_at": schema.Format(t0.Add(time.Duration(m) * time.Minute))}))
	}
	events = append(events, js(map[string]any{"_shadow": "reset", "reason": "inode-changed", "at": schema.Format(t0)}))
	if err := w(filepath.Join(l.LiveDir(), "router-events.jsonl"), events); err != nil {
		return s, err
	}

	// Queue rows carry the local offset.
	est := time.FixedZone("EST", -5*3600)
	q := func(op, id string, t time.Time, reason string) string {
		return js(map[string]any{"op": op, "eventId": "pr.changed:" + id, "at": t.In(est).Format(time.RFC3339Nano), "reason": reason, "payload": map[string]any{"id": id, "title": s.Titles[0]}})
	}
	queue := []string{
		q("enqueue", synthID(104), at(10, 39, 0), ""),
		q("evict", synthID(104), at(10, 39, 30), "reemit"),
		q("enqueue", synthID(104), at(10, 40, 0), ""),
		q("enqueue", synthID(101), at(10, 5, 0), ""),
	}
	if err := w(filepath.Join(l.LiveDir(), "router-queue.jsonl"), queue); err != nil {
		return s, err
	}

	if !o.NoRunRecord {
		rr := func(id string, end time.Time, hash, anchor bool, cause string) string {
			return js(map[string]any{"ts": end.Format(time.RFC3339), "entity_type": "pr", "entity_id": id, "repo": synthSlug, "pr": 1, "path": "full", "change": "sweep", "content_hash_changed": hash, "anchor_written": anchor, "anchor_cause": cause, "outcome": "ok", "duration_ms": 20000})
		}
		var runs []string
		for i := 0; i < 40; i++ {
			runs = append(runs, rr(synthID(120+i%10), at(9, 30, 0).Add(time.Duration(i)*time.Minute), o.AllHashChanged || i%20 != 0, false, ""))
		}
		runs = append(
			runs,
			rr(synthID(101), at(10, 6, 0), true, true, "pr-content-change"),
			rr(synthID(109), at(11, 10, 0), true, true, "conflict-flip"),
			rr(synthID(110), at(11, 12, 0), true, true, "created"),
		)
		if err := w(filepath.Join(l.LiveDir(), "run-record.log"), runs); err != nil {
			return s, err
		}
	}
	return s, nil
}
