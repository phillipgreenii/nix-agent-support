package prchange

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/backfill"
)

// The run record's timings: per-stage wall time, in whole milliseconds, on the
// run's own clock. A fake clock that the Runner double advances by a fixed
// amount per call makes every figure exact.

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// timedRunner wraps s.run so a call whose line starts with a key advances the
// clock by its value.
func timedRunner(s *script, c *fakeClock, cost map[string]time.Duration) Runner {
	return func(ctx context.Context, call Call) Result {
		line := strings.TrimSpace(call.Name + " " + strings.Join(call.Args, " "))
		for prefix, d := range cost {
			if strings.HasPrefix(line, prefix) {
				c.advance(d)
			}
		}
		return s.run(ctx, call)
	}
}

const timedApplyCounters = `{"contract":"pg-decider.run-counters/v1","type":"pr","id":"acme/api#7","rules":{},"escalations":0,` +
	`"lookup_items":40,"lookup_ceiling":7000,"lookup_ms":250}`

func readRecord(t *testing.T, path string) (line string, rec RunRecord) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line = strings.TrimSpace(string(b))
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("record %q: %v", line, err)
	}
	return line, rec
}

func i64(n int64) *int64 { return &n }

func TestAnApplyRunRecordsEveryStageTiming(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	s := hydrated().
		// The apply's own plan lists work, so the residual is read (an empty plan
		// followed by a clean apply skips the residual read).
		on("pg-decider plan pr acme/api#7 --json", ok(planEmpty)).
		on("pg-decider apply", exit(0, planWithWork, timedApplyCounters+"\n"))
	o, _, errb := opts(t, ModeApply, s)
	o.Clock = clock.now
	o.RunRecordPath = filepath.Join(t.TempDir(), RunRecordFileName)
	o.Runner = timedRunner(s, clock, map[string]time.Duration{
		"pg-desk pr refresh":                   1200 * time.Millisecond,
		"pg-desk pr show":                      80 * time.Millisecond,
		"pg-decider plan pr acme/api#7 --json": 300 * time.Millisecond,
		"pg-decider apply":                     4000 * time.Millisecond,
	})
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d: %s", got, errb)
	}
	_, rec := readRecord(t, o.RunRecordPath)

	wantTimings := TimingsRecord{LockWaitMs: i64(0), HydrateMs: i64(1200), ViewReadMs: i64(80), DecideMs: i64(4300)}
	if rec.Timings == nil || *rec.Timings.LockWaitMs != 0 || *rec.Timings.HydrateMs != 1200 || *rec.Timings.ViewReadMs != 80 || *rec.Timings.DecideMs != 4300 {
		t.Errorf("timings = %s, want %s", mustJSON(rec.Timings), mustJSON(wantTimings))
	}
	d := rec.Decide
	if d == nil {
		t.Fatal("no decide record")
	}
	// apply 4000, residual 300: the decide stage is their sum; an apply makes no
	// pre-plan call.
	if d.PrePlanMs != nil || *d.ApplyMs != 4000 || *d.ResidualMs != 300 {
		t.Errorf("decide stages pre=%v apply=%v residual=%v", d.PrePlanMs, *d.ApplyMs, *d.ResidualMs)
	}
	if d.LookupMs == nil || *d.LookupMs != 250 || d.LookupItems == nil || *d.LookupItems != 40 {
		t.Errorf("lookup = %v ms, %v items; want 250 and 40", d.LookupMs, d.LookupItems)
	}

	// The same figures reach pg-desk's lane write path, under the member names
	// of lanestate.StageTimings (the verb refuses unknown members, so a drift
	// from pg-desk's list fails there).
	in := laneCalls(t, s)[0]
	got := mustJSON(in.Timings)
	const want = `{"lock_wait_ms":0,"hydrate_ms":1200,"view_read_ms":80,"decide_ms":4300,"lookup_ms":250,"apply_ms":4000,"residual_ms":300}`
	if got != want {
		t.Errorf("lane timings = %s, want %s", got, want)
	}
}

// A run record with no measured stage sends no timings member, and a pg-desk
// that does not know the member (an older build) costs the run only the
// timings: the rest of the document still lands.
func TestATimingsRefusalByTheWritePathIsDroppedAndTheRunStillLands(t *testing.T) {
	s := hydrated().
		on("pg-decider plan pr", ok(planWithWork)).
		onSeq("pg-desk pr lane-record", exit(1, "", `lane-record: decode run: json: unknown field "timings"`), ok("recorded\n"))
	o, _, errb := opts(t, ModePlan, s)
	o.RunRecordPath = filepath.Join(t.TempDir(), RunRecordFileName)
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d: %s", got, errb)
	}
	lanes := laneCalls(t, s)
	if len(lanes) != 2 || lanes[0].Timings == nil || lanes[1].Timings != nil || lanes[1].Residual == nil {
		t.Fatalf("lane inputs = %+v", lanes)
	}
	if r := readRecords(t, o.RunRecordPath)[0]; r.Lane.Result != laneRecorded {
		t.Fatalf("lane = %+v", r.Lane)
	}
}

func TestARunWithNoMeasuredStageSendsNoTimingsMember(t *testing.T) {
	if laneTimingsOf(RunRecord{}) != nil {
		t.Fatal("an empty record produced a timings member")
	}
}

// A golden of the record's new members: names and shape, so a rename is a
// deliberate change.
func TestTheTimingMembersHaveTheirPublishedNames(t *testing.T) {
	rec := RunRecord{
		Contract: RunRecordContract,
		Timings:  &TimingsRecord{LockWaitMs: i64(1), HydrateMs: i64(2), ViewReadMs: i64(3), DecideMs: i64(4)},
		Decide: &DecideRecord{
			Rules: []RuleOutcome{}, Actions: []PlannedAction{}, ByKind: map[string]int{},
			LookupMs: i64(5), LookupItems: i64(6), PrePlanMs: i64(7), ApplyMs: i64(8), ResidualMs: i64(9),
		},
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"timings":{"lock_wait_ms":1,"hydrate_ms":2,"view_read_ms":3,"decide_ms":4}`,
		`"lookup_ms":5,"lookup_items":6,"pre_plan_ms":7,"apply_ms":8,"residual_ms":9`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("record %s lacks %s", b, want)
		}
	}
}

func TestAPlanRunRecordsThePlanReadAndTheDeciderCallButNoLookupDuration(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	s := hydrated().
		on("pg-decider plan pr acme/api#7 --json", ok(planEmpty)).
		on("pg-decider plan pr acme/api#7", ok("nothing to do\n"))
	o, _, errb := opts(t, ModePlan, s)
	o.Clock = clock.now
	o.RunRecordPath = filepath.Join(t.TempDir(), RunRecordFileName)
	o.Runner = timedRunner(s, clock, map[string]time.Duration{"pg-decider plan pr acme/api#7": 90 * time.Millisecond})
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d: %s", got, errb)
	}
	_, rec := readRecord(t, o.RunRecordPath)
	d := rec.Decide
	if d == nil || d.ApplyMs == nil || *d.ApplyMs != 90 || d.PrePlanMs == nil || *d.PrePlanMs != 90 || d.ResidualMs != nil || d.LookupMs != nil {
		t.Errorf("decide = %s", mustJSON(d))
	}
}

func TestARunThatEndsAtTheLockRecordsOnlyTheLockWait(t *testing.T) {
	s := hydrated()
	o, _, _ := opts(t, ModeApply, s)
	o.RunRecordPath = filepath.Join(t.TempDir(), RunRecordFileName)
	o.LockWait = 50 * time.Millisecond
	held, err := AcquireLock(context.Background(), o.LockDir, testID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if got := Run(context.Background(), o); got != ExitLockTimeout {
		t.Fatalf("exit %d", got)
	}
	line, rec := readRecord(t, o.RunRecordPath)
	if rec.Timings == nil || rec.Timings.LockWaitMs == nil || *rec.Timings.LockWaitMs < 40 ||
		rec.Timings.HydrateMs != nil || rec.Timings.ViewReadMs != nil || rec.Timings.DecideMs != nil {
		t.Errorf("timings = %s (record %s)", mustJSON(rec.Timings), line)
	}
}

// A refresh that fails has a hydrate time and no view read or decide time.
func TestAFailedRefreshRecordsOnlyLockAndHydrate(t *testing.T) {
	s := (&script{}).on("pg-desk pr refresh", exit(1, "", "old-schema store"))
	o, _, _ := opts(t, ModeApply, s)
	o.RunRecordPath = filepath.Join(t.TempDir(), RunRecordFileName)
	Run(context.Background(), o)
	_, rec := readRecord(t, o.RunRecordPath)
	if rec.Timings == nil || rec.Timings.HydrateMs == nil || rec.Timings.ViewReadMs != nil || rec.Timings.DecideMs != nil || rec.Decide != nil {
		t.Errorf("timings = %s", mustJSON(rec.Timings))
	}
}

// Old readers: a line with the new members decodes into the shape a reader
// built before them has, and the backfill driver reads it unchanged. A line
// from before the members (none of them) decodes into the current shape.
func TestOldReadersToleratTheTimingMembers(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	s := hydrated().
		on("pg-decider apply", exit(0, planEmpty, timedApplyCounters+"\n"))
	o, _, errb := opts(t, ModeApply, s)
	o.Clock = clock.now
	o.RunRecordPath = filepath.Join(t.TempDir(), RunRecordFileName)
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d: %s", got, errb)
	}
	line, _ := readRecord(t, o.RunRecordPath)

	// A reader that predates the members, strict about unknown keys at no level
	// (encoding/json's default), reads the rest.
	type oldDecide struct {
		ExitCode int            `json:"exit_code"`
		ByKind   map[string]int `json:"by_kind"`
	}
	var old struct {
		Contract string     `json:"contract"`
		Entity   string     `json:"entity"`
		ExitCode int        `json:"exit_code"`
		Decide   *oldDecide `json:"decide"`
	}
	if err := json.Unmarshal([]byte(line), &old); err != nil || old.Contract != RunRecordContract || old.Entity != testID || old.Decide == nil {
		t.Fatalf("old reader: %v %+v", err, old)
	}
	lg, err := backfill.ReadLog(o.RunRecordPath)
	if err != nil || len(lg.Records) != 1 || lg.Ignored != 0 || lg.Records[0].Entity != testID {
		t.Fatalf("the backfill driver: %v %+v", err, lg)
	}

	// A line written before the members existed decodes, members absent.
	legacy := `{"contract":"pg-pr-change.run-record/v1","run_id":"0123456789abcdef","at":"2026-10-24T10:00:00Z","finished_at":"2026-10-24T10:00:03Z",` +
		`"entity":"acme/api#7","event_id":"acme/api#7","seq":0,"seq_source":"","mode":"apply","hydrate":{"result":"ok"},"exit_code":0,` +
		`"decide":{"exit_code":0,"rules":[],"actions":[],"by_kind":{}},"lane_record":{"result":"recorded"}}`
	var cur RunRecord
	if err := json.Unmarshal([]byte(legacy), &cur); err != nil || cur.Timings != nil || cur.Decide == nil || cur.Decide.ApplyMs != nil || cur.Decide.LookupMs != nil {
		t.Fatalf("legacy line: %v %+v", err, cur)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
