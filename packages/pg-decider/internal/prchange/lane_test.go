package prchange

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Composition-logic tests of the delivery key, the lane write path call and the
// run record, against a double for the three tools. The real tools are exercised
// in realtools_test.go.

const planWithWork = `{"actions":[
 {"op":"create","kind":"anchor","target":null,"fields":{},"rule":"anchor.lazy"},
 {"op":"create","kind":"review-pr","target":null,"fields":{},"rule":"review.head-advanced"},
 {"op":"annotate","kind":"","target":"ready_to_land","fields":{},"rule":"land.ready"}],
 "skipped":[{"rule":"conflict.present","reason":"not matched"},{"rule":"fixci.failing-on-head","reason":"suppressed"}]}`

const planEmpty = `{"actions":[],"skipped":[{"rule":"all.closed","reason":"not matched"}]}`

const applyCounters = `{"contract":"pg-decider.run-counters/v1","type":"pr","id":"acme/api#7","rules":{` +
	`"anchor.lazy":{"planned":1,"applied":1,"deduped":0,"failed":0,"skipped":0},` +
	`"review.head-advanced":{"planned":1,"applied":0,"deduped":0,"failed":1,"skipped":0},` +
	`"land.ready":{"planned":1,"applied":0,"deduped":1,"failed":0,"skipped":0}},"escalations":0}`

// hydrated is a script whose refresh and view read succeed.
func hydrated() *script {
	return (&script{}).
		on("pg-desk pr refresh", ok("refreshed pr acme/api#7: reconcile (version 3)\n")).
		on("pg-desk pr show", ok(hydratedView(3, false, "open")))
}

// laneCalls returns the documents handed to `pg-desk pr lane-record`.
func laneCalls(t *testing.T, s *script) []laneInput {
	t.Helper()
	var out []laneInput
	for i, c := range s.calls {
		if !strings.HasPrefix(c, "pg-desk pr lane-record") {
			continue
		}
		var in laneInput
		dec := json.NewDecoder(strings.NewReader(s.inputs[i]))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			t.Fatalf("lane-record input %q: %v", s.inputs[i], err)
		}
		out = append(out, in)
	}
	return out
}

// deciderCalls returns the calls to the decider that decide (not the read-only
// `plan --json` reads), with their standard input.
func deciderCalls(s *script) (calls, inputs []string) {
	for i, c := range s.calls {
		if strings.HasPrefix(c, "pg-decider ") && !strings.HasSuffix(c, " --json") {
			calls, inputs = append(calls, c), append(inputs, s.inputs[i])
		}
	}
	return calls, inputs
}

func readRecords(t *testing.T, path string) []RunRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []RunRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r RunRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("a line of the run record is not one JSON record: %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	return out
}

func TestApplyPassesFromItemOnStdinWithTheDeliveryKey(t *testing.T) {
	cases := []struct {
		name, seq, rawID string
		wantKey          int64
		wantArgs         string // the decider's argv after the program name
	}{
		{"the change sequence", "17", testID, 17, "apply pr acme/api#7 --from-item -"},
		{"a sequence in exponent form (10^7)", "1e+07", testID, 10000000, "apply pr acme/api#7 --from-item -"},
		{"a missing sequence takes the id suffix", "<no value>", testID + "@0123456789ab", 0x0123456789ab, "apply pr acme/api#7 --from-item -"},
		{"an empty sequence takes the id suffix", "", testID + "@00000000ffff", 0xffff, "apply pr acme/api#7 --from-item -"},
		{"the sequence wins over the suffix", "9", testID + "@0123456789ab", 9, "apply pr acme/api#7 --from-item -"},
		{"a bare id and no sequence passes no --from-item", "", testID, 0, "apply pr acme/api#7"},
		{"a sequence of 0 and a bare id passes no --from-item", "0", testID, 0, "apply pr acme/api#7"},
		{"a suffix of zeros passes no --from-item", "<no value>", testID + "@000000000000", 0, "apply pr acme/api#7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := hydrated().on("pg-decider plan pr", ok(planEmpty)).on("pg-decider apply", ok(""))
			o, _, errb := opts(t, ModeApply, s)
			o.Seq, o.RawID = c.seq, c.rawID
			if got := Run(context.Background(), o); got != ExitOK {
				t.Fatalf("exit %d: %s", got, errb)
			}
			calls, inputs := deciderCalls(s)
			if len(calls) != 1 || calls[0] != "pg-decider "+c.wantArgs {
				t.Fatalf("decider calls = %q, want one of %q", calls, c.wantArgs)
			}
			if c.wantKey == 0 {
				if inputs[0] != "" {
					t.Fatalf("a run with no key handed the decider an item: %q", inputs[0])
				}
				return
			}
			var it struct {
				ID, Type, Title string
				Metadata        struct {
					EntityType string `json:"entity_type"`
					EntityID   string `json:"entity_id"`
					Kind       string
					Seq        int64
				}
			}
			if err := json.Unmarshal([]byte(inputs[0]), &it); err != nil {
				t.Fatalf("the item on stdin: %v: %q", err, inputs[0])
			}
			if it.ID != c.rawID || it.Metadata.Seq != c.wantKey || it.Metadata.EntityType != "pr" || it.Metadata.EntityID != testID || it.Metadata.Kind == "" || it.Type == "" {
				t.Fatalf("item = %+v, want id %q seq %d", it, c.rawID, c.wantKey)
			}
			if it.Metadata.Seq == 0 {
				t.Fatal("a key of 0 was passed")
			}
		})
	}
}

func TestPlanModeNeverPassesFromItem(t *testing.T) {
	s := hydrated().on("pg-decider plan pr", ok(planEmpty))
	o, _, errb := opts(t, ModePlan, s)
	o.Seq = "17"
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d: %s", got, errb)
	}
	for _, c := range s.calls {
		if strings.Contains(c, "--from-item") {
			t.Fatalf("plan was given --from-item: %q", c)
		}
	}
}

func TestAGarbageSequenceIsAUsageErrorBeforeAnyWork(t *testing.T) {
	s := hydrated()
	o, _, errb := opts(t, ModeApply, s)
	o.Seq = "soon"
	if got := Run(context.Background(), o); got != ExitUsage {
		t.Fatalf("exit %d: %s", got, errb)
	}
	if len(s.calls) != 0 {
		t.Fatalf("a refused argument list ran %v", s.calls)
	}
}

// An apply run records itself ONCE, with the mode it ran in; the read-only plan
// it makes afterwards is the residual of that run, never a run of its own.
func TestApplyRecordsOneRunAndPersistsTheResidualAfterTheApply(t *testing.T) {
	s := hydrated().
		onSeq("pg-decider plan pr acme/api#7 --json", ok(planWithWork), ok(`{"actions":[{"op":"create","kind":"review-pr","target":null,"fields":{},"rule":"review.head-advanced"}],"skipped":[]}`)).
		on("pg-decider apply", exit(2, "", applyCounters+"\nrule review.head-advanced failed\n"))
	o, _, _ := opts(t, ModeApply, s)
	o.RunRecordPath = filepath.Join(t.TempDir(), RunRecordFileName)
	o.Seq = "17"
	if got := Run(context.Background(), o); got != ExitDecidePartial {
		t.Fatalf("exit %d", got)
	}
	lanes := laneCalls(t, s)
	if len(lanes) != 1 {
		t.Fatalf("%d lane-record calls, want exactly one (the residual plan is not a run)", len(lanes))
	}
	in := lanes[0]
	if in.Mode != "apply" || in.PR != testID {
		t.Fatalf("lane input = %+v", in)
	}
	if in.Residual == nil || *in.Residual != 1 {
		t.Fatalf("residual = %v, want the 1 action the plan after the apply still lists", in.Residual)
	}
	if len(in.Events) != 1 || in.Events[0] != (laneEvent{"decide", ClassDecidePartial}) {
		t.Fatalf("events = %+v", in.Events)
	}
	want := map[string]int{}
	for _, d := range in.Decisions {
		want[strings.Join([]string{d.Rule, d.Action, d.Outcome, d.Reason}, "|")] += max(d.Count, 1)
	}
	for k, n := range map[string]int{
		"anchor.lazy|create|applied|":                   1,
		"review.head-advanced|create|failed|":           1,
		"land.ready|annotate|deduped|":                  1,
		"conflict.present|skip|skipped|not matched":     1,
		"fixci.failing-on-head|skip|skipped|suppressed": 1,
	} {
		if want[k] != n {
			t.Errorf("decision %q = %d, want %d (all: %v)", k, want[k], n, want)
		}
	}
	recs := readRecords(t, o.RunRecordPath)
	if len(recs) != 1 {
		t.Fatalf("%d run records, want 1", len(recs))
	}
	r := recs[0]
	if r.Residual == nil || *r.Residual != 1 || r.Mode != "apply" || r.Entity != testID || r.EventID != testID || r.Seq != 17 || r.SeqSource != KeySourceSequence {
		t.Fatalf("record = %+v", r)
	}
	if r.ErrorClass != ClassDecidePartial || r.FailingStage != StageDecide || r.ExitCode != ExitDecidePartial || r.Decide == nil || r.Decide.ExitCode != 2 {
		t.Fatalf("record failure fields = %+v / %+v", r, r.Decide)
	}
	if r.Decide.ByKind["review-pr"] != 1 || r.Decide.ByKind["anchor"] != 1 || r.Decide.ByKind["annotation"] != 1 || len(r.Decide.Actions) != 3 {
		t.Fatalf("by kind = %v, actions = %v", r.Decide.ByKind, r.Decide.Actions)
	}
	if r.Adoption == nil || r.Adoption.Outcome != "none" || len(r.Adoption.Candidates) != 1 ||
		r.Adoption.Candidates[0] != (AnchorCandidate{ID: "bd-a", State: "open", Keyed: true}) {
		t.Fatalf("adoption = %+v", r.Adoption)
	}
	if r.RunID == "" || r.At == "" || r.FinishedAt == "" || r.Lane.Result != laneRecorded {
		t.Fatalf("record = %+v", r)
	}
	if r.Hydrate.Result != hydrateOK || r.Hydrate.ExitCode == nil || *r.Hydrate.ExitCode != 0 {
		t.Fatalf("hydrate = %+v", r.Hydrate)
	}
}

func TestPlanRecordsThePlannedActionsAndTheResidualIsTheirCount(t *testing.T) {
	s := hydrated().on("pg-decider plan pr", ok(planWithWork))
	o, _, errb := opts(t, ModePlan, s)
	o.RunRecordPath = filepath.Join(t.TempDir(), RunRecordFileName)
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d: %s", got, errb)
	}
	in := laneCalls(t, s)[0]
	if in.Mode != "plan" || in.Residual == nil || *in.Residual != 3 || len(in.Events) != 0 || in.LastOutcome != "ok" {
		t.Fatalf("lane input = %+v", in)
	}
	planned := 0
	for _, d := range in.Decisions {
		if d.Outcome == "planned" {
			planned += max(d.Count, 1)
		}
	}
	if planned != 3 {
		t.Fatalf("planned decisions = %d: %+v", planned, in.Decisions)
	}
	rec := readRecords(t, o.RunRecordPath)[0]
	if rec.Decide == nil || len(rec.Decide.Rules) != 5 {
		t.Fatalf("rule outcomes = %+v", rec.Decide)
	}
	var skipReasons []string
	for _, ro := range rec.Decide.Rules {
		if ro.Outcome == "skipped" {
			skipReasons = append(skipReasons, ro.Rule+":"+ro.Reason)
		}
	}
	if fmt.Sprint(skipReasons) != "[conflict.present:not matched fixci.failing-on-head:suppressed]" {
		t.Fatalf("skip reasons = %v", skipReasons)
	}
}

// A run that stops at hydrate still appends one record with its class and no
// decide fields, and counts one lane run, in plan mode and in apply mode.
func TestARunThatStopsAtHydrateIsStillRecorded(t *testing.T) {
	for _, mode := range []string{ModePlan, ModeApply} {
		t.Run(mode, func(t *testing.T) {
			s := (&script{}).
				on("pg-desk pr refresh", exit(3, "class=not_found\n", "not found")).
				on("pg-desk pr show", exit(1, "", "pr acme/api#7 does not resolve")).
				on("pg-desk pr mark-inactive", ok("marked pr acme/api#7 inactive\n"))
			o, _, errb := opts(t, mode, s)
			o.RunRecordPath = filepath.Join(t.TempDir(), RunRecordFileName)
			if got := Run(context.Background(), o); got != ExitHydrateNotFound {
				t.Fatalf("exit %d: %s", got, errb)
			}
			if s.called("pg-decider") {
				t.Fatal("the decider ran")
			}
			lanes := laneCalls(t, s)
			if len(lanes) != 1 || lanes[0].Mode != mode || len(lanes[0].Events) != 1 || lanes[0].Events[0] != (laneEvent{"hydrate", ClassNotFound}) {
				t.Fatalf("lane inputs = %+v", lanes)
			}
			if lanes[0].Residual != nil || len(lanes[0].Decisions) != 0 {
				t.Fatalf("a run that never decided carries a residual or decisions: %+v", lanes[0])
			}
			recs := readRecords(t, o.RunRecordPath)
			if len(recs) != 1 {
				t.Fatalf("%d records", len(recs))
			}
			r := recs[0]
			if r.ErrorClass != ClassNotFound || r.FailingStage != StageHydrate || r.Decide != nil || r.Adoption != nil || r.Residual != nil ||
				r.Hydrate.Result != hydrateFailed || r.Hydrate.Class != ClassNotFound || r.ExitCode != ExitHydrateNotFound {
				t.Fatalf("record = %+v", r)
			}
			raw, _ := os.ReadFile(o.RunRecordPath)
			if strings.Contains(string(raw), `"decide"`) {
				t.Fatalf("a hydrate-only record carries decide fields: %s", raw)
			}
		})
	}
}

func TestALockTimeoutIsInTheRunRecordButNotTheLaneState(t *testing.T) {
	s := hydrated()
	o, _, errb := opts(t, ModeApply, s)
	o.RunRecordPath = filepath.Join(t.TempDir(), RunRecordFileName)
	lock, err := AcquireLock(context.Background(), o.LockDir, testID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if got := Run(context.Background(), o); got != ExitLockTimeout {
		t.Fatalf("exit %d: %s", got, errb)
	}
	if len(laneCalls(t, s)) != 0 {
		t.Fatal("a run that never held the lock wrote the PR's lane state")
	}
	r := readRecords(t, o.RunRecordPath)[0]
	if r.FailingStage != StageLock || r.Hydrate.Result != hydrateNotRun || r.Lane.Result != laneSkipped || r.Decide != nil {
		t.Fatalf("record = %+v", r)
	}
}

// The decider's lookup figures are forwarded when it reports them (it starts to
// in a later packet); the extraction is a seam, shown here with a stub value.
func TestLookupFiguresAreForwardedToTheLaneWritePath(t *testing.T) {
	orig := lookupFromDecider
	t.Cleanup(func() { lookupFromDecider = orig })
	items, ceiling := int64(412), int64(500)
	lookupFromDecider = func(Result) (*int64, *int64) { return &items, &ceiling }

	s := hydrated().on("pg-decider plan pr", ok(planEmpty))
	o, _, errb := opts(t, ModePlan, s)
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d: %s", got, errb)
	}
	in := laneCalls(t, s)[0]
	if in.LookupItems == nil || *in.LookupItems != 412 || in.LookupCeiling == nil || *in.LookupCeiling != 500 {
		t.Fatalf("lookup figures = %v, %v", in.LookupItems, in.LookupCeiling)
	}
	// Without a report (today's decider) the figures are left out, so the store
	// keeps what it has.
	lookupFromDecider = orig
	s = hydrated().on("pg-decider plan pr", ok(planEmpty))
	o, _, _ = opts(t, ModePlan, s)
	Run(context.Background(), o)
	if in := laneCalls(t, s)[0]; in.LookupItems != nil || in.LookupCeiling != nil {
		t.Fatalf("figures invented: %v %v", in.LookupItems, in.LookupCeiling)
	}
}

// A lane write path that refuses the document because of a decision (a rule or
// reason outside its closed list) must not cost the run its counters and state.
func TestADecisionRefusedByTheWritePathIsDroppedAndTheRunStillLands(t *testing.T) {
	s := hydrated().
		on("pg-decider plan pr", ok(planWithWork)).
		onSeq("pg-desk pr lane-record", exit(1, "", "lane-record: lanestate: decision rule \"x\" is not one of [...]"), ok("recorded\n"))
	o, _, errb := opts(t, ModePlan, s)
	o.RunRecordPath = filepath.Join(t.TempDir(), RunRecordFileName)
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d: %s", got, errb)
	}
	lanes := laneCalls(t, s)
	if len(lanes) != 2 || len(lanes[0].Decisions) == 0 || len(lanes[1].Decisions) != 0 {
		t.Fatalf("lane inputs = %+v", lanes)
	}
	if lanes[1].Residual == nil || lanes[1].Mode != "plan" {
		t.Fatalf("the retry lost the run: %+v", lanes[1])
	}
	r := readRecords(t, o.RunRecordPath)[0]
	if r.Lane.Result != laneRecorded || !r.Lane.DroppedDecisions {
		t.Fatalf("lane = %+v", r.Lane)
	}
}

// A lane write that fails never changes the exit code (the decider has acted),
// is said once, and says nothing the router would read as a reason to retry a
// failure that is not retryable.
func TestALaneWriteFailureIsANoteNotAFailure(t *testing.T) {
	s := hydrated().
		on("pg-decider plan pr", ok(planEmpty)).
		on("pg-decider apply", exit(1, "", "bad config")).
		on("pg-desk pr lane-record", exit(1, "", "store unavailable: deadline exceeded: signal: killed"))
	var out, errb strings.Builder
	o, _, _ := opts(t, ModeApply, s)
	o.Stdout, o.Stderr = &out, &errb
	o.RunRecordPath = filepath.Join(t.TempDir(), RunRecordFileName)
	if got := Run(context.Background(), o); got != ExitDecideFailed {
		t.Fatalf("exit %d: %s", got, errb.String())
	}
	for _, w := range retryWords {
		if hasRetryWord(errb.String(), w) {
			t.Fatalf("a non-retryable failure's stderr carries %q after the lane note:\n%s", w, errb.String())
		}
	}
	if !strings.Contains(errb.String(), "lane state not recorded") {
		t.Fatalf("no note:\n%s", errb.String())
	}
	if r := readRecords(t, o.RunRecordPath)[0]; r.Lane.Result != laneFailed || r.Lane.Error == "" {
		t.Fatalf("lane = %+v", r.Lane)
	}
}

func TestAdoptionOutcomeAndCandidateAnchorsAreRecorded(t *testing.T) {
	// The view links an anchor that carries no dedup key and whose title is the
	// PR's: the adoption rule has an item to adopt.
	view := `{"contract":"pg-desk.view/v1","type":"pr","id":"acme/api#7","version":3,"stale":false,"snapshot":{"id":"acme/api#7","repo":"acme/api","number":7,"title":"a change","state":"open"},"links":[` +
		`{"type":"issue","id":"bd-a","relation":"work","state":"open","title":"acme/api#7: a change","metadata":{}}]}`
	plan := `{"actions":[{"op":"update","kind":"anchor","target":"bd-a","fields":{},"rule":"adoption"}],"skipped":[]}`
	s := (&script{}).
		on("pg-desk pr refresh", ok("refreshed\n")).
		on("pg-desk pr show", ok(view)).
		on("pg-decider plan pr", ok(plan))
	o, _, errb := opts(t, ModePlan, s)
	o.RunRecordPath = filepath.Join(t.TempDir(), RunRecordFileName)
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d: %s", got, errb)
	}
	r := readRecords(t, o.RunRecordPath)[0]
	if r.Adoption == nil || r.Adoption.Outcome != "planned" || len(r.Adoption.Candidates) != 1 {
		t.Fatalf("adoption = %+v", r.Adoption)
	}
	c := r.Adoption.Candidates[0]
	if c.ID != "bd-a" || c.State != "open" || c.Keyed || c.MatchedBy == "" {
		t.Fatalf("candidate = %+v", c)
	}
}

// Concurrent runs append whole lines: no line is split, shortened or fused with
// another, whatever the line's size.
func TestConcurrentAppendsNeverInterleaveLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", RunRecordFileName)
	const writers, each = 12, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				rec := RunRecord{
					Contract: RunRecordContract, RunID: fmt.Sprintf("w%02d-%03d", w, i), Entity: testID,
					// Big enough that one record is several pages of a pipe: a
					// write that is not atomic would be split.
					Decide: &DecideRecord{PlanError: strings.Repeat("x", 9000+w*131+i)},
				}
				if err := AppendRunRecord(path, rec); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range readRecords(t, path) {
		if seen[r.RunID] {
			t.Fatalf("record %s appears twice", r.RunID)
		}
		seen[r.RunID] = true
		var w, i int
		if _, err := fmt.Sscanf(r.RunID, "w%d-%d", &w, &i); err != nil || len(r.Decide.PlanError) != 9000+w*131+i {
			t.Fatalf("record %s was cut or fused: plan error length %d", r.RunID, len(r.Decide.PlanError))
		}
	}
	if len(seen) != writers*each {
		t.Fatalf("%d records, want %d", len(seen), writers*each)
	}
}

func TestTheRunRecordIsNotTheLegacyRunRecord(t *testing.T) {
	if RunRecordFileName == "run-record.log" || strings.HasPrefix(RunRecordFileName, "run-record.log") {
		t.Fatalf("the run record file name %q collides with pg-desk's run-record.log", RunRecordFileName)
	}
}

// The real extraction (the default of lookupFromDecider): the figures the decider
// reports in its run counters line, in its --json plan, and on its failure path.
const planJSONWithLookup = `{"actions":[],"skipped":[{"rule":"all.closed","reason":"not matched"}],"lookup_items":3443,"lookup_ceiling":7000}`

func figures(t *testing.T, in laneInput, items, ceiling int64) {
	t.Helper()
	if in.LookupItems == nil || *in.LookupItems != items || in.LookupCeiling == nil || *in.LookupCeiling != ceiling {
		t.Fatalf("lookup figures = %v, %v, want %d and %d", in.LookupItems, in.LookupCeiling, items, ceiling)
	}
}

func TestPlanModeForwardsTheFiguresOfThePlanJSONRead(t *testing.T) {
	s := hydrated().
		on("pg-decider plan pr acme/api#7 --json", ok(planJSONWithLookup)).
		on("pg-decider plan pr", ok("pr acme/api#7  mine\nlookup: 3443 work items listed, ceiling 7000\n"))
	o, _, errb := opts(t, ModePlan, s)
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d: %s", got, errb)
	}
	figures(t, laneCalls(t, s)[0], 3443, 7000)
}

func TestApplyModeForwardsTheFiguresOfTheCountersLine(t *testing.T) {
	line := strings.TrimSuffix(applyCounters, "}") + `,"lookup_items":3443,"lookup_ceiling":7000}`
	s := hydrated().
		on("pg-decider plan pr", ok(planWithWork)).
		on("pg-decider apply", Result{Stderr: []byte(line + "\n")})
	o, _, errb := opts(t, ModeApply, s)
	Run(context.Background(), o)
	if len(laneCalls(t, s)) != 1 {
		t.Fatalf("lane calls: %v\n%s", s.calls, errb)
	}
	figures(t, laneCalls(t, s)[0], 3443, 7000)
}

// A decider that failed closed on the lookup (exit 4) still reports the figures
// in its failure line, and the composed command persists them on that path: the
// run is classed decide_failed and the gauges keep following the lane.
func TestAFailedClosedLookupIsDecideFailedAndStillForwardsTheFigures(t *testing.T) {
	failLine := `{"contract":"pg-decider.run-counters/v1","type":"pr","id":"acme/api#7","rules":{},"escalations":0,` +
		`"failures":{"lookup-above-ceiling":1},"lookup_items":7100,"lookup_ceiling":7000}`
	for _, mode := range []string{ModePlan, ModeApply} {
		t.Run(mode, func(t *testing.T) {
			s := hydrated().
				on("pg-decider plan pr", Result{Code: 4, Stderr: []byte("pg-decider: cannot plan pr acme/api#7: work-item lookup: above the ceiling\n" + failLine + "\n")}).
				on("pg-decider apply", Result{Code: 4, Stderr: []byte("pg-decider: cannot apply pr acme/api#7: work-item lookup: above the ceiling\n" + failLine + "\n")})
			o, _, errb := opts(t, mode, s)
			got := Run(context.Background(), o)
			if got != ExitDecideFailed {
				t.Fatalf("exit %d, want %d (decide_failed): %s", got, ExitDecideFailed, errb)
			}
			if !strings.Contains(errb.String(), "class=decide_failed") {
				t.Errorf("failure line lacks the class: %s", errb)
			}
			calls := laneCalls(t, s)
			if len(calls) != 1 {
				t.Fatalf("lane calls = %d: %v", len(calls), s.calls)
			}
			figures(t, calls[0], 7100, 7000)
			if calls[0].LastOutcome != "decide.decide_failed" {
				t.Errorf("last outcome = %q", calls[0].LastOutcome)
			}
		})
	}
}

// A decider that reports neither figure leaves them out (no invented zero).
func TestADeciderThatReportsNoFiguresForwardsNone(t *testing.T) {
	s := hydrated().on("pg-decider plan pr", ok(planEmpty))
	o, _, _ := opts(t, ModePlan, s)
	Run(context.Background(), o)
	if in := laneCalls(t, s)[0]; in.LookupItems != nil || in.LookupCeiling != nil {
		t.Fatalf("figures invented: %v %v", in.LookupItems, in.LookupCeiling)
	}
}
