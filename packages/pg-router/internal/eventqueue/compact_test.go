package eventqueue

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for write-ahead-log compaction (bead pg2-8e0m6, compact.go). The central
// claim is EQUIVALENCE: Replay(compact(L)) rebuilds exactly the state Replay(L)
// does. stateOf dumps every projection a replay builds, so "equal" below means
// the retained events (in FIFO order, with their resolved instants and payload),
// the per-listener accepts and settlements, the active-gate projection, the
// per-type depth, and the "ever enqueued" type set.

// stateOf renders every replay-built projection of q as a canonical string.
func stateOf(q *Queue) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	type ent struct {
		ID, Type, SV, At, Exp string
		Payload               map[string]any
		Accepted, Settled     []string
	}
	keys := func(m map[string]bool) []string {
		out := []string{}
		for k, v := range m {
			if v {
				out = append(out, k)
			}
		}
		sort.Strings(out)
		return out
	}
	var order []ent
	for _, id := range q.order {
		e, ok := q.entries[id]
		if !ok {
			order = append(order, ent{ID: "TOMBSTONE:" + id})
			continue
		}
		order = append(order, ent{
			ID: e.evt.ID, Type: e.evt.Type, SV: e.evt.SchemaVersion,
			At: e.evt.At.UTC().Format(time.RFC3339Nano), Exp: e.evt.ExpiresAt.UTC().Format(time.RFC3339Nano),
			Payload: e.evt.Payload, Accepted: keys(e.accepted), Settled: keys(e.settled),
		})
	}
	var gates []string
	for _, g := range q.gates {
		gates = append(gates, fmt.Sprintf("%s|%s|%s|%s|%s", g.Type, g.Description, g.Owner,
			g.SetAt.UTC().Format(time.RFC3339Nano), g.ExpiresAt.UTC().Format(time.RFC3339Nano)))
	}
	sort.Strings(gates)
	cell := q.cell.Load()
	var seen []string
	for t := range cell.everSeen {
		seen = append(seen, t)
	}
	sort.Strings(seen)
	b, err := json.Marshal(map[string]any{
		"order": order, "entries": len(q.entries), "gates": gates, "depth": cell.depth, "seen": seen,
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func stateOfRecords(t *testing.T, recs []Record) string {
	t.Helper()
	q, err := New(&MemStore{recs: append([]Record(nil), recs...)})
	if err != nil {
		t.Fatal(err)
	}
	return stateOf(q)
}

func newFileQueue(t *testing.T, path string, opts ...Option) (*Queue, *FileStore) {
	t.Helper()
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	q, err := New(fs, opts...)
	if err != nil {
		_ = fs.Close()
		t.Fatal(err)
	}
	return q, fs
}

// replayState opens path afresh (as a restart would) and returns its state.
func replayState(t *testing.T, path string) string {
	t.Helper()
	q, fs := newFileQueue(t, path)
	defer func() { _ = fs.Close() }()
	return stateOf(q)
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// randomLog builds a random record sequence over small id/listener/gate pools,
// including everything a hostile log could hold: accepts and evicts of unknown
// ids, repeated accepts, re-enqueues of live and of evicted ids, gate sets that
// are overwritten, cleared and expired. An id always carries the same TYPE (the
// queue's own Enqueue never writes a second enqueue of a live id with a different
// type, so a log that did is not one the queue can produce).
func randomLog(r *rand.Rand, n int) []Record {
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	types := []string{"alpha", "beta", "gamma", "delta"}
	listeners := []string{"l0", "l1", "l2"}
	gates := []string{"G_ONE", "G_TWO", "G_THREE"}
	const idPool = 14
	idType := func(i int) string { return types[i%len(types)] }
	var out []Record
	for k := 0; k < n; k++ {
		at := base.Add(time.Duration(k) * time.Minute)
		switch roll := r.Intn(100); {
		case roll < 32:
			i := r.Intn(idPool)
			exp := at.Add(time.Duration(r.Intn(180)-30) * time.Minute)
			out = append(out, Record{
				Op: opEnqueue, EventID: fmt.Sprintf("e%d", i), Type: idType(i), SchemaVersion: "1",
				At: at, ExpiresAt: exp, EnqueuedAt: at.Add(time.Duration(r.Intn(5)) * time.Second),
				Payload: map[string]any{"n": float64(k)},
			})
		case roll < 58:
			out = append(out, Record{Op: opAccept, EventID: fmt.Sprintf("e%d", r.Intn(idPool)), ListenerID: listeners[r.Intn(len(listeners))]})
		case roll < 76:
			out = append(out, Record{Op: opEvict, EventID: fmt.Sprintf("e%d", r.Intn(idPool))})
		case roll < 86:
			g := Record{Op: opGateSet, GateType: gates[r.Intn(len(gates))], Description: fmt.Sprintf("d%d", k), Owner: "o", At: at}
			if r.Intn(2) == 0 {
				g.ExpiresAt = at.Add(time.Duration(r.Intn(600)-100) * time.Minute)
			}
			out = append(out, g)
		case roll < 93:
			out = append(out, Record{Op: opGateCleared, GateType: gates[r.Intn(len(gates))], At: at})
		case roll < 97:
			out = append(out, Record{Op: opGateExpired, GateType: gates[r.Intn(len(gates))], At: at})
		default:
			out = append(out, Record{Op: opSeen, Type: types[r.Intn(len(types))]})
		}
	}
	return out
}

// Replay(compact(L)) == Replay(L), over random logs — events, accepts, gates,
// depth, the ever-seen set — AND the two queues then behave identically: the same
// offers go to the same listeners, and a re-emit of every id (live or retired)
// is deduped or admitted the same way.
func TestCompactEquivalenceProperty(t *testing.T) {
	for seed := int64(0); seed < 400; seed++ {
		r := rand.New(rand.NewSource(seed))
		l := randomLog(r, 1+r.Intn(160))
		c := compactRecords(l)

		if got, want := stateOfRecords(t, c), stateOfRecords(t, l); got != want {
			t.Fatalf("seed %d: state(compact(L)) != state(L)\n got: %s\nwant: %s", seed, got, want)
		}
		if len(c) > len(l) {
			t.Fatalf("seed %d: compaction grew the log: %d -> %d records", seed, len(l), len(c))
		}
		// Idempotent: compacting a compacted log changes nothing.
		if cc := compactRecords(c); !reflect.DeepEqual(cc, c) {
			t.Fatalf("seed %d: compact is not idempotent", seed)
		}
		// The EnqueuedAt (which a replay does not surface in memory) survives: every
		// retained event's record is the LAST enqueue record the log held for its id.
		lastEnq := map[string]Record{}
		for _, rec := range l {
			if rec.Op == opEnqueue {
				lastEnq[rec.EventID] = rec
			}
		}
		for _, rec := range c {
			if rec.Op == opEnqueue && !reflect.DeepEqual(rec, lastEnq[rec.EventID]) {
				t.Fatalf("seed %d: retained enqueue record for %s drifted: %+v vs %+v", seed, rec.EventID, rec, lastEnq[rec.EventID])
			}
		}

		// Behavioral equivalence over both queues.
		clk := newClock()
		clk.t = time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
		build := func(recs []Record) (*Queue, []*fakeListener) {
			q, err := New(&MemStore{recs: append([]Record(nil), recs...)}, WithClock(clk.now))
			if err != nil {
				t.Fatal(err)
			}
			var ls []*fakeListener
			for _, id := range []string{"l0", "l1", "l2"} {
				fl := newListener(id, "alpha", "beta", "gamma", "delta")
				q.Register(fl)
				ls = append(ls, fl)
			}
			return q, ls
		}
		qa, la := build(l)
		qb, lb := build(c)
		for pass := 0; pass < 4; pass++ {
			if a, b := qa.Dispatch(), qb.Dispatch(); a != b {
				t.Fatalf("seed %d: dispatch pass %d accepted %d vs %d", seed, pass, a, b)
			}
		}
		for i := range la {
			if !equal(la[i].offered, lb[i].offered) || !equal(la[i].accepted, lb[i].accepted) {
				t.Fatalf("seed %d: listener %s saw different offers: %v/%v vs %v/%v", seed, la[i].id, la[i].offered, la[i].accepted, lb[i].offered, lb[i].accepted)
			}
		}
		if !reflect.DeepEqual(qa.ActiveGates(), qb.ActiveGates()) {
			t.Fatalf("seed %d: active gates differ: %v vs %v", seed, qa.ActiveGates(), qb.ActiveGates())
		}
		for i := 0; i < 14; i++ {
			probe := evtUntil(fmt.Sprintf("e%d", i), []string{"alpha", "beta", "gamma", "delta"}[i%4], clk.in(time.Hour))
			ra, ea := qa.Enqueue(probe)
			rb, eb := qb.Enqueue(probe)
			if ra != rb || (ea == nil) != (eb == nil) {
				t.Fatalf("seed %d: re-emit of e%d: %v/%v vs %v/%v", seed, i, ra, ea, rb, eb)
			}
		}
		if got, want := stateOf(qb), stateOf(qa); got != want {
			t.Fatalf("seed %d: state diverged after identical activity\n got: %s\nwant: %s", seed, got, want)
		}
	}
}

// The "ever enqueued" projection (Queue.UnmatchedBindings) is part of replay
// state: a type whose every event is gone must still count as seen after
// compaction, via an opSeen record.
func TestCompactKeepsEverSeenTypes(t *testing.T) {
	l := []Record{
		{Op: opEnqueue, EventID: "a", Type: "gone", At: time.Unix(1, 0).UTC(), ExpiresAt: time.Unix(2, 0).UTC()},
		{Op: opEvict, EventID: "a"},
		{Op: opEnqueue, EventID: "b", Type: "kept", At: time.Unix(3, 0).UTC(), ExpiresAt: time.Unix(4, 0).UTC()},
	}
	c := compactRecords(l)
	var hasSeen bool
	for _, r := range c {
		if r.Op == opSeen && r.Type == "gone" {
			hasSeen = true
		}
	}
	if !hasSeen {
		t.Fatalf("compacted log lost the ever-seen marker for an all-evicted type: %+v", c)
	}
	q, err := New(&MemStore{recs: c})
	if err != nil {
		t.Fatal(err)
	}
	if got := q.UnmatchedBindings([]string{"gone", "kept", "never"}); !equal(got, []string{"never"}) {
		t.Fatalf("UnmatchedBindings = %v, want [never]", got)
	}
}

// Gate cases through the real Queue API: an active leased gate, a lapsed-but-
// unswept one, a cleared one, an expired-and-swept one, and a re-set gate (last
// writer wins) all survive compaction exactly as a plain replay shows them.
func TestCompactGateCases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.jsonl")
	clk := newClock()
	q, fs := newFileQueue(t, path, WithClock(clk.now))

	if _, err := q.SetGate(GateRequest{Type: "ACTIVE_LEASED", Description: "x", Owner: "o", TTL: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.SetGate(GateRequest{Type: "PERMANENT"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.SetGate(GateRequest{Type: "CLEARED"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := q.ClearGate("CLEARED", "op"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.SetGate(GateRequest{Type: "RESET", Description: "first", Owner: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.SetGate(GateRequest{Type: "RESET", Description: "second", Owner: "b", TTL: 12 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.SetGate(GateRequest{Type: "SWEPT", TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.SetGate(GateRequest{Type: "LAPSED", TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Minute)
	// Expire sweeps every lapsed lease (SWEPT and LAPSED alike) into a gate_expired
	// record; re-set LAPSED afterwards with a lease that then lapses UNSWEPT.
	q.Expire()
	if _, err := q.SetGate(GateRequest{Type: "LAPSED", TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Minute) // LAPSED is now lapsed but not swept
	want := stateOf(q)
	_ = fs.Close()

	before := fileSize(t, path)
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := fs2.Compact()
	if err != nil {
		t.Fatal(err)
	}
	_ = fs2.Close()
	if st.BytesAfter >= before {
		t.Fatalf("compaction did not shrink a gate-heavy log: %d -> %d", before, st.BytesAfter)
	}
	q2, fs3 := newFileQueue(t, path, WithClock(clk.now))
	defer func() { _ = fs3.Close() }()
	if got := stateOf(q2); got != want {
		t.Fatalf("gate state changed across compaction\n got: %s\nwant: %s", got, want)
	}
	// And the semantic reading: ACTIVE_LEASED, PERMANENT and RESET in force; the
	// lapsed lease reads inactive; cleared/swept ones are gone.
	var active []string
	for _, g := range q2.ActiveGates() {
		active = append(active, g.Type)
	}
	if !equal(active, []string{"ACTIVE_LEASED", "PERMANENT", "RESET"}) {
		t.Fatalf("active gates = %v", active)
	}
	if g, _ := q2.Gate("RESET"); g.Description != "second" || g.Owner != "b" {
		t.Fatalf("re-set gate did not keep the last writer: %+v", g)
	}
	// Dropped history: no record for the cleared / swept gates remains.
	recs, _ := fs3.Replay()
	for _, r := range recs {
		if r.GateType == "CLEARED" || r.GateType == "SWEPT" {
			t.Fatalf("compacted log still carries history for %s: %+v", r.GateType, r)
		}
		if r.Op == opGateCleared || r.Op == opGateExpired {
			t.Fatalf("compacted log still carries a %s record", r.Op)
		}
	}
}

// A torn trailing line is handled as today (everything before it replays), and
// startup compaction additionally removes it: a record appended afterwards is
// not glued onto the torn bytes, so it survives a further restart (before
// compaction it was silently lost).
func TestCompactStartupDropsTornTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.jsonl")
	q1, fs1 := newFileQueue(t, path)
	mustEnqueue(t, q1, evtUntil("e1", "T", time.Now().Add(time.Hour)))
	mustEnqueue(t, q1, evtUntil("e2", "T", time.Now().Add(time.Hour)))
	_ = fs1.Close()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"op":"enqueue","eventId":"e3","typ`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	wantBefore := replayState(t, path)

	q2, fs2 := newFileQueue(t, path, WithCompaction(0, true))
	if got := stateOf(q2); got != wantBefore {
		t.Fatalf("startup compaction changed the replayed state\n got: %s\nwant: %s", got, wantBefore)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"e3"`) || !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("torn tail survived startup compaction: %q", raw)
	}
	mustEnqueue(t, q2, evtUntil("e4", "T", time.Now().Add(time.Hour)))
	_ = fs2.Close()
	q3, fs3 := newFileQueue(t, path)
	defer func() { _ = fs3.Close() }()
	if q3.DepthByType()["T"] != 3 {
		t.Fatalf("record appended after startup compaction was lost: %v", q3.DepthByType())
	}
}

// Without compaction a torn tail is still tolerated exactly as before (guard
// against the FileStore rewrite regressing it): see also crash_test.go.
func TestFileStoreReplayTornTailUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	if err := os.WriteFile(path, []byte(`{"op":"enqueue","eventId":"a","type":"T"}`+"\n"+`{"op":"enqueue","eventId":"b","ty`), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	recs, err := fs.Replay()
	if err != nil || len(recs) != 1 || recs[0].EventID != "a" {
		t.Fatalf("Replay = %+v, %v; want just the intact record", recs, err)
	}
}

func copyDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	ents, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		in, err := os.Open(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.Create(filepath.Join(dst, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(out, in); err != nil {
			t.Fatal(err)
		}
		_ = in.Close()
		_ = out.Close()
	}
	return dst
}

// seedLog drives a real queue over path to a log with a lot of dead weight and a
// little live state (retained events with accepts, gates).
func seedLog(t *testing.T, path string, clk *mockClock) {
	t.Helper()
	q, fs := newFileQueue(t, path, WithClock(clk.now), WithEarlyEviction())
	l := newListener("h", "T")
	q.Register(l)
	for i := 0; i < 40; i++ {
		mustEnqueue(t, q, evtUntil(fmt.Sprintf("t%d", i), "T", clk.in(time.Hour)))
		q.Dispatch() // accepted -> early-evicted: three records of dead weight each
	}
	for i := 0; i < 5; i++ { // retained: nobody binds U
		mustEnqueue(t, q, evtUntil(fmt.Sprintf("u%d", i), "U", clk.in(time.Hour)))
	}
	if _, err := q.SetGate(GateRequest{Type: "SYSTEM_PAUSE", Owner: "op"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.SetGate(GateRequest{Type: "SYSTEM_PAUSE", Owner: "op2", TTL: 3 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	_ = fs.Close()
}

// Crash injection at every compaction step. At each durable step the on-disk
// directory is snapshotted — exactly what a crash at that instant would leave —
// and a fresh process started over each snapshot must see a valid, equivalent
// state: the temp file is ignored/removed, and the log is either the complete old
// one or the complete new one.
func TestCompactCrashAtEveryStep(t *testing.T) {
	for _, withRacingAppend := range []bool{false, true} {
		t.Run(fmt.Sprintf("racingAppend=%v", withRacingAppend), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "queue.jsonl")
			clk := newClock()
			seedLog(t, path, clk)

			fs, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = fs.Close() }()
			snaps := map[string]string{}
			var appended bool
			fs.compactHook = func(stage string) {
				if stage == compactStageTempWritten && withRacingAppend && !appended {
					appended = true
					// A record landing while the compaction is mid-flight must end up in
					// the tail of the new log, after the compacted prefix.
					if err := fs.Append(Record{Op: opEnqueue, EventID: "raced", Type: "U", At: clk.t, ExpiresAt: clk.in(time.Hour), EnqueuedAt: clk.t}); err != nil {
						t.Error(err)
					}
				}
				snaps[stage] = copyDir(t, dir)
			}
			wantOld := replayState(t, copyPath(t, path))
			stats, err := fs.Compact()
			if err != nil {
				t.Fatal(err)
			}
			if stats.BytesAfter >= stats.BytesBefore {
				t.Fatalf("expected the dead-weight log to shrink: %d -> %d", stats.BytesBefore, stats.BytesAfter)
			}
			wantNew := replayState(t, path)
			if withRacingAppend {
				if !strings.Contains(wantNew, `"raced"`) {
					t.Fatalf("racing append lost by compaction: %s", wantNew)
				}
			} else if wantNew != wantOld {
				t.Fatalf("compaction changed state\n got: %s\nwant: %s", wantNew, wantOld)
			}

			for _, stage := range []string{compactStageTempWritten, compactStageTempSynced, compactStageRenamed, compactStageDirSynced} {
				snap, ok := snaps[stage]
				if !ok {
					t.Fatalf("hook never reported stage %q", stage)
				}
				// Compaction preserves state and every snapshot already holds the racing
				// record (it was appended before the first snapshot), so a restart at ANY
				// stage — old log before the rename, new log from it — must read wantNew.
				got := replayState(t, filepath.Join(snap, "queue.jsonl"))
				if got != wantNew {
					t.Fatalf("restart after crash at %q diverged\n got: %s\nwant: %s", stage, got, wantNew)
				}
				if _, err := os.Stat(compactTempPath(filepath.Join(snap, "queue.jsonl"))); !os.IsNotExist(err) {
					t.Fatalf("stage %s: leftover compaction temp file survived the restart (err=%v)", stage, err)
				}
			}
		})
	}
}

func copyPath(t *testing.T, path string) string {
	t.Helper()
	return filepath.Join(copyDir(t, filepath.Dir(path)), filepath.Base(path))
}

// A leftover temp file from a crashed compaction is ignored and removed on the
// next start, and never read as the log.
func TestCompactLeftoverTempIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.jsonl")
	seedLog(t, path, newClock())
	want := replayState(t, path)
	if err := os.WriteFile(compactTempPath(path), []byte(`{"op":"enqueue","eventId":"poison","type":"T"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	q, fs := newFileQueue(t, path, WithCompaction(0, true))
	defer func() { _ = fs.Close() }()
	if got := stateOf(q); got != want {
		t.Fatalf("leftover temp file leaked into the log\n got: %s\nwant: %s", got, want)
	}
	if _, err := os.Stat(compactTempPath(path)); !os.IsNotExist(err) {
		t.Fatalf("temp file not cleaned up: %v", err)
	}
}

// A failed compaction (the rename fails) leaves the open store fully usable —
// appends after the failure still succeed — and removes its temp file.
func TestCompactFailureLeavesStoreUsable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.jsonl")
	seedLog(t, path, newClock())
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	fs.compactHook = func(stage string) {
		if stage != compactStageTempSynced {
			return
		}
		// Swap the log path for a non-empty directory: rename(file, dir) fails.
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := os.MkdirAll(filepath.Join(path, "x"), 0o755); err != nil {
			t.Error(err)
		}
	}
	if _, err := fs.Compact(); err == nil {
		t.Fatal("expected the compaction to fail")
	}
	if err := fs.Append(Record{Op: opEnqueue, EventID: "b", Type: "T"}); err != nil {
		t.Fatalf("store unusable after failed compaction: %v", err)
	}
	if _, err := os.Stat(compactTempPath(path)); !os.IsNotExist(err) {
		t.Fatalf("failed compaction left its temp file: %v", err)
	}
}

// Appends racing runtime compactions lose nothing and reorder nothing: after
// producers, a dispatcher and a compaction loop all run flat out, the durable log
// replays to EXACTLY the state the live queue holds. Run with -race.
func TestCompactConcurrentAppendsLoseNothing(t *testing.T) {
	// One INFO line per compaction would drown the output of a loop that runs
	// hundreds of them.
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.jsonl")
	q, fs := newFileQueue(t, path, WithEarlyEviction(), WithCompaction(0, false))
	l := newListener("h", "T")
	q.Register(l)

	const producers, per = 4, 250
	var wg, aux sync.WaitGroup
	var stop atomic.Bool
	exp := time.Now().Add(time.Hour)
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				typ := "T" // bound: accepted then early-evicted (dead weight)
				if i%2 == 1 {
					typ = "U" // unbound: retained
				}
				if _, err := q.Enqueue(evtUntil(fmt.Sprintf("p%d-%d", p, i), typ, exp)); err != nil {
					t.Error(err)
					return
				}
			}
		}(p)
	}
	aux.Add(2)
	go func() {
		defer aux.Done()
		for !stop.Load() {
			q.Dispatch()
			q.Expire()
		}
	}()
	var compacted int
	go func() {
		defer aux.Done()
		for !stop.Load() {
			if _, err := q.CompactNow(); err != nil {
				t.Error(err)
				return
			}
			compacted++
		}
	}()
	wg.Wait()
	stop.Store(true)
	aux.Wait()
	for q.Dispatch() > 0 { // drain what the dispatcher had not reached
	}
	if compacted == 0 {
		t.Fatal("no compaction ran during the race")
	}
	want := stateOf(q)
	if got := q.DepthByType()["U"]; got != producers*per/2 {
		t.Fatalf("live queue holds %d U events, want %d", got, producers*per/2)
	}
	_ = fs.Close()
	if got := replayState(t, path); got != want {
		t.Fatalf("durable log diverged from the live queue after racing compactions\n got: %s\nwant: %s", got, want)
	}
}

// Runtime trigger: once the log outgrows compact threshold the Expire sweep
// starts a background compaction, the size drops, and state survives.
func TestCompactRuntimeThresholdTrigger(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.jsonl")
	clk := newClock()
	q, fs := newFileQueue(t, path, WithClock(clk.now), WithEarlyEviction(), WithCompaction(8*1024, false))
	l := newListener("h", "T")
	q.Register(l)
	for i := 0; i < 200; i++ {
		mustEnqueue(t, q, evtUntil(fmt.Sprintf("t%d", i), "T", clk.in(time.Hour)))
		q.Dispatch()
	}
	mustEnqueue(t, q, evtUntil("keep", "U", clk.in(time.Hour)))
	grown := q.LogSize()
	if grown < 8*1024 {
		t.Fatalf("test premise: log only %d bytes", grown)
	}
	q.Expire() // triggers the background compaction
	q.waitCompaction()
	if q.Compactions() != 1 {
		t.Fatalf("Compactions = %d, want 1", q.Compactions())
	}
	if after := q.LogSize(); after >= grown/4 {
		t.Fatalf("log did not shrink enough: %d -> %d", grown, after)
	}
	if q.LogSize() != fileSize(t, path) {
		t.Fatalf("size gauge %d != file size %d", q.LogSize(), fileSize(t, path))
	}
	want := stateOf(q)
	_ = fs.Close()
	if got := replayState(t, path); got != want {
		t.Fatalf("state changed across the runtime compaction\n got: %s\nwant: %s", got, want)
	}
}

// Under the threshold nothing runs; and a live set that itself exceeds the
// threshold does not make every sweep compact again (the trigger rises).
func TestCompactThresholdDoesNotThrash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.jsonl")
	clk := newClock()
	q, fs := newFileQueue(t, path, WithClock(clk.now), WithCompaction(1024, false))
	defer func() { _ = fs.Close() }()
	mustEnqueue(t, q, evtUntil("a", "U", clk.in(time.Hour)))
	q.Expire()
	q.waitCompaction()
	if q.Compactions() != 0 {
		t.Fatalf("compacted below the threshold: %d", q.Compactions())
	}
	for i := 0; i < 60; i++ { // all live: nothing to drop, but past the threshold
		mustEnqueue(t, q, evtUntil(fmt.Sprintf("live%d", i), "U", clk.in(time.Hour)))
	}
	for i := 0; i < 10; i++ {
		q.Expire()
		q.waitCompaction()
	}
	if got := q.Compactions(); got != 1 {
		t.Fatalf("Compactions = %d across 10 sweeps over an all-live log, want exactly 1", got)
	}
}

// Without WithCompaction the queue never touches its log: existing behavior.
func TestCompactOffByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	q, fs := newFileQueue(t, path, WithEarlyEviction())
	defer func() { _ = fs.Close() }()
	l := newListener("h", "T")
	q.Register(l)
	for i := 0; i < 50; i++ {
		mustEnqueue(t, q, evtUntil(fmt.Sprintf("t%d", i), "T", time.Now().Add(time.Hour)))
		q.Dispatch()
		q.Expire()
	}
	q.waitCompaction()
	if q.Compactions() != 0 {
		t.Fatal("compacted without being asked to")
	}
}

// A Store that is not a Compactor (the in-memory double, any wrapper) is left
// alone, and CompactNow says so.
func TestCompactUnsupportedStore(t *testing.T) {
	q, err := New(NewMemStore(), WithCompaction(1, true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CompactNow(); err != ErrCompactionUnsupported {
		t.Fatalf("CompactNow = %v, want ErrCompactionUnsupported", err)
	}
	q.Expire()
	q.waitCompaction()
	if q.LogSize() != 0 {
		t.Fatalf("LogSize = %d for a store with no size", q.LogSize())
	}
}

// The size gauge tracks the file through appends, batches and a compaction.
func TestFileStoreLogSizeTracksFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	check := func(when string) {
		t.Helper()
		if fs.LogSize() != fileSize(t, path) {
			t.Fatalf("%s: LogSize %d != file size %d", when, fs.LogSize(), fileSize(t, path))
		}
	}
	check("empty")
	_ = fs.Append(Record{Op: opEnqueue, EventID: "a", Type: "T"})
	check("append")
	_ = fs.AppendBatch([]Record{{Op: opEvict, EventID: "a"}, {Op: opEnqueue, EventID: "b", Type: "T"}})
	check("batch")
	if _, err := fs.Compact(); err != nil {
		t.Fatal(err)
	}
	check("compact")
	_ = fs.Append(Record{Op: opAccept, EventID: "b", ListenerID: "h"})
	check("append after compact")
	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if reopened.LogSize() != fileSize(t, path) {
		t.Fatalf("reopened LogSize %d != file size %d", reopened.LogSize(), fileSize(t, path))
	}
}

// Close waits for, and a later Compact refuses after, a closed store.
func TestFileStoreCompactAfterClose(t *testing.T) {
	fs, err := NewFileStore(filepath.Join(t.TempDir(), "queue.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	_ = fs.Close()
	if _, err := fs.Compact(); err != os.ErrClosed {
		t.Fatalf("Compact after Close = %v, want os.ErrClosed", err)
	}
}

// --- perf -----------------------------------------------------------------

// writeLiveShapedLog writes a log shaped like the production one measured on
// 2026-10-02 (182,076 lines / 33 MB: ~62k enqueues, ~58k accepts, ~62k evicts, a
// handful of gate records, ~120 events still live): returns the record count.
func writeLiveShapedLog(t *testing.T, path string, events, live int) int {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	at := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)
	n := 0
	put := func(r Record) {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
		n++
	}
	for i := 0; i < events; i++ {
		id := fmt.Sprintf("pr.reconcile:acme/widgets#%d", i)
		ts := at.Add(time.Duration(i) * time.Second)
		put(Record{
			Op: opEnqueue, EventID: id, Type: "pr.reconcile", At: ts, ExpiresAt: ts, EnqueuedAt: ts,
			Payload: map[string]any{"id": id, "metadata": map[string]any{"change": "sweep"}, "title": id, "type": "pr"},
		})
		if i < events-live {
			if i%16 != 0 { // ~94% were accepted before eviction, as in production
				put(Record{Op: opAccept, EventID: id, ListenerID: "role:reconcile"})
			}
			put(Record{Op: opEvict, EventID: id})
		}
		if i%20000 == 0 {
			put(Record{Op: opGateSet, GateType: "SYSTEM_PAUSE", Description: "paused by the operator", Owner: "operator", At: ts})
			put(Record{Op: opGateCleared, GateType: "SYSTEM_PAUSE", Owner: "operator", At: ts})
		}
	}
	return n
}

// measureReplay times a cold New() over path (a restart) and reports the size.
func measureReplay(t *testing.T, path string) (time.Duration, string) {
	t.Helper()
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	start := time.Now()
	q, err := New(fs)
	if err != nil {
		t.Fatal(err)
	}
	return time.Since(start), stateOf(q)
}

// Replay time after compacting a live-shaped (~180k-record) log is bounded, and
// the compacted log rebuilds the identical state.
func TestCompactLiveShapedReplayPerf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	records := writeLiveShapedLog(t, path, 62000, 120)
	sizeBefore := fileSize(t, path)
	replayBefore, stateBefore := measureReplay(t, path)

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := fs.Compact()
	_ = fs.Close()
	if err != nil {
		t.Fatal(err)
	}
	replayAfter, stateAfter := measureReplay(t, path)

	t.Logf("live-shaped log: %d records, %d bytes -> %d records, %d bytes (compaction took %s)",
		records, sizeBefore, st.RecordsAfter, st.BytesAfter, st.Duration)
	t.Logf("replay (New) before=%s after=%s", replayBefore, replayAfter)

	if stateAfter != stateBefore {
		t.Fatal("compacted live-shaped log rebuilt a different state")
	}
	if st.RecordsAfter*50 > records {
		t.Fatalf("compaction kept %d of %d records; expected live state only", st.RecordsAfter, records)
	}
	if st.BytesAfter*50 > sizeBefore {
		t.Fatalf("compaction kept %d of %d bytes; expected live state only", st.BytesAfter, sizeBefore)
	}
	if replayAfter*5 > replayBefore {
		t.Fatalf("replay after compaction (%s) is not clearly faster than before (%s)", replayAfter, replayBefore)
	}
}

// Manual measurement hook: point PG_ROUTER_LIVE_QUEUE_COPY at a COPY of a real
// queue.jsonl (never the live file) to see the before/after numbers on it.
func TestCompactLiveCopyMeasurement(t *testing.T) {
	src := os.Getenv("PG_ROUTER_LIVE_QUEUE_COPY")
	if src == "" {
		t.Skip("PG_ROUTER_LIVE_QUEUE_COPY not set")
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	replayBefore, stateBefore := measureReplay(t, path)
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := fs.Compact()
	_ = fs.Close()
	if err != nil {
		t.Fatal(err)
	}
	replayAfter, stateAfter := measureReplay(t, path)
	t.Logf("LIVE COPY: %d bytes / %d records -> %d bytes / %d records; compaction %s", st.BytesBefore, st.RecordsBefore, st.BytesAfter, st.RecordsAfter, st.Duration)
	t.Logf("LIVE COPY: replay (New) before=%s after=%s", replayBefore, replayAfter)
	if stateAfter != stateBefore {
		t.Fatal("compaction changed the live copy's replayed state")
	}
}
