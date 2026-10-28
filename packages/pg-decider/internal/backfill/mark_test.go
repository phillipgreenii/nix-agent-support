package backfill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// fakeDaemon is a double of the two programs the marking step runs (the umbrella
// connector and pg-desk) with the daemon's watch bookkeeping behind it: a read with
// --watch marks a PR watched, `pr inspect` counts the watched ones among the PRs
// named, and `pg-desk pr refresh` is recorded. The real daemon half of the exchange
// is covered by the daemon's own tests (the real binary over a fake forge CLI); the
// real pg-desk half by the real-tool scenario of internal/prchange.
type fakeDaemon struct {
	t       *testing.T
	watched map[string]bool
	// ignoreWatch lists PRs a watch read answers ok for but does not watch (a daemon
	// that dropped the flag), showFail PRs whose read fails with the given error code.
	ignoreWatch map[string]bool
	showFail    map[string]string
	refreshFail map[string]bool
	calls       []string
	inspects    int
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	return &fakeDaemon{t: t, watched: map[string]bool{}, ignoreWatch: map[string]bool{}, showFail: map[string]string{}, refreshFail: map[string]bool{}}
}

func (d *fakeDaemon) run(_ context.Context, c Call) Result {
	d.calls = append(d.calls, c.Name+" "+strings.Join(c.Args, " "))
	a := c.Args
	switch {
	case c.Name == "pg-desk" && len(a) == 3 && a[0] == "pr" && a[1] == "refresh":
		if d.refreshFail[a[2]] {
			return Result{Code: 3, Stderr: []byte("refresh refused\n")}
		}
		return Result{}
	case c.Name == "pg-connector" && len(a) >= 3 && a[0] == "pr" && a[1] == "show":
		pr := a[2]
		if code, bad := d.showFail[pr]; bad {
			return Result{Code: 1, Stdout: []byte(fmt.Sprintf(`{"protocolVersion":1,"error":{"code":%q,"message":"forced"}}`+"\n", code))}
		}
		if hasArg(a, "--watch") && !d.ignoreWatch[pr] {
			d.watched[pr] = true
		}
		return Result{Stdout: []byte(`{"protocolVersion":1,"result":{"id":"` + pr + `"}}` + "\n")}
	case c.Name == "pg-connector" && len(a) >= 3 && a[0] == "pr" && a[1] == "inspect":
		d.inspects++
		var ids []string
		for _, x := range a[2:] {
			if strings.HasPrefix(x, "--") {
				break
			}
			ids = append(ids, x)
		}
		n := 0
		for _, id := range ids {
			if d.watched[id] {
				n++
			}
		}
		out := fmt.Sprintf(`{"protocolVersion":1,"result":{"instance":"pr","results":[],"counts":{"requested":%d,"watched":%d}}}`, len(ids), n)
		return Result{Stdout: []byte(out + "\n")}
	}
	d.t.Fatalf("unexpected call %s %v", c.Name, a)
	return Result{}
}

func hasArg(a []string, w string) bool {
	for _, x := range a {
		if x == w {
			return true
		}
	}
	return false
}

func (d *fakeDaemon) count(prefix string) int {
	n := 0
	for _, c := range d.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// markSet builds a set the way Enumerate would: anchors with the PR state their
// metadata records, a query-only PR, and unmappable anchors.
func markSet(unmapped int, anchorPRs map[string]string, queryOnly ...string) *Set {
	s := &Set{Contract: SetContract, Unparseable: []Unparseable{}}
	for _, pr := range sortedKeys(anchorPRs) {
		s.Entries = append(s.Entries, Entry{PR: pr, State: anchorPRs[pr], From: []string{FromAnchor}, AnchorIDs: []string{"a-" + pr}})
	}
	for _, pr := range queryOnly {
		s.Entries = append(s.Entries, Entry{PR: pr, State: StateOpen, From: []string{FromQuery}, Queries: []string{"mine"}})
	}
	s.OpenAnchorPRCount = len(anchorPRs)
	s.OpenAnchorCount = len(anchorPRs) + unmapped
	for i := 0; i < unmapped; i++ {
		s.Unparseable = append(s.Unparseable, Unparseable{From: FromAnchor, ID: fmt.Sprintf("bd-%d", i), Title: "no pr here", Reason: "the anchor's title is not <owner>/<repo>#<number>: <title>"})
	}
	return s
}

func markOpts(d *fakeDaemon, s *Set, out *bytes.Buffer) MarkOptions {
	return MarkOptions{
		Set: s, Runner: d.run, Rate: &fakeRate{vals: []int{9000}}, Floor: 3000,
		Progress: out, Pace: 0,
	}
}

func TestMarkWatchedMarksRefreshesAndTheCountEqualsTheOpenAnchorCount(t *testing.T) {
	d := newFakeDaemon(t)
	set := markSet(0, map[string]string{"o/r#1": StateOpen, "o/r#2": StateUnknown, "o/r#3": StateOpen}, "o/r#9")
	var out bytes.Buffer
	res, err := MarkWatched(context.Background(), markOpts(d, set, &out))
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != ExitDone || res.Outcome != OutcomeDone || res.Watched != 3 || res.Expected != 3 || !res.Checked {
		t.Fatalf("%s (code %d)", res.Line, res.Code)
	}
	for _, want := range []string{"mode=mark-watched", "open_anchor_prs=3", "marked=3", "refreshed=3", "watched=3", "expected=3"} {
		if !strings.Contains(res.Line, want) {
			t.Errorf("last line lacks %q: %s", want, res.Line)
		}
	}
	// A query-only PR is never marked: it is the daemon's watched queries' to follow.
	if d.watched["o/r#9"] || strings.Contains(strings.Join(d.calls, "\n"), "o/r#9") {
		t.Errorf("a PR with no open anchor was touched: %v", d.calls)
	}
	// Every mark came before every refresh, and the refresh is pg-desk's hydrate-only one.
	lastShow, firstRefresh := -1, len(d.calls)
	for i, c := range d.calls {
		if strings.HasPrefix(c, "pg-connector pr show") {
			lastShow = i
			if !strings.Contains(c, "--watch") {
				t.Errorf("a mark read without the watch option: %s", c)
			}
		}
		if strings.HasPrefix(c, "pg-desk pr refresh") && i < firstRefresh {
			firstRefresh = i
		}
	}
	if lastShow > firstRefresh {
		t.Errorf("a refresh ran before the last mark: %v", d.calls)
	}
	if d.count("pg-desk pr refresh") != 3 || d.count("pg-connector pr inspect") != 1 {
		t.Errorf("calls: %v", d.calls)
	}
	// Nothing but the connector and pg-desk ran: no decider, no change command.
	for _, c := range d.calls {
		if !strings.HasPrefix(c, "pg-connector pr ") && !strings.HasPrefix(c, "pg-desk pr refresh ") {
			t.Errorf("unexpected program: %s", c)
		}
	}
}

func TestMarkWatchedACountMismatchFailsTheStep(t *testing.T) {
	d := newFakeDaemon(t)
	d.ignoreWatch["o/r#2"] = true // the daemon answers the read but does not watch the PR
	set := markSet(0, map[string]string{"o/r#1": StateOpen, "o/r#2": StateOpen})
	res, _ := MarkWatched(context.Background(), markOpts(d, set, &bytes.Buffer{}))
	if res.Code != ExitMarkCheckFailed || res.Outcome != OutcomeFailed || res.Watched != 1 || res.Expected != 2 {
		t.Fatalf("%s (code %d)", res.Line, res.Code)
	}
	if !strings.HasPrefix(res.Line, "FAILED ") || !strings.Contains(res.Line, "watched count is 1") {
		t.Errorf("last line: %s", res.Line)
	}
}

func TestMarkWatchedNeverPassesAtZeroEqualsZero(t *testing.T) {
	for name, set := range map[string]*Set{
		"only a query PR":               markSet(0, nil, "o/r#9"),
		"every anchor's PR is merged":   markSet(0, map[string]string{"o/r#1": StateMerged, "o/r#2": StateClosed}),
		"only unmappable anchors":       markSet(2, nil, "o/r#9"),
		"anchors none, queries present": markSet(0, map[string]string{}, "o/r#9"),
	} {
		t.Run(name, func(t *testing.T) {
			d := newFakeDaemon(t)
			res, _ := MarkWatched(context.Background(), markOpts(d, set, &bytes.Buffer{}))
			if res.Code == ExitDone || res.Code != ExitMarkCheckFailed || !strings.Contains(res.CheckError, "zero") {
				t.Fatalf("%s (code %d)", res.Line, res.Code)
			}
			if d.count("pg-desk") != 0 || d.count("pg-connector pr show") != 0 {
				t.Errorf("a call was made with nothing to mark: %v", d.calls)
			}
		})
	}
}

func TestMarkWatchedListsUnmappableAnchorsAndSkipsMergedOrClosedPRs(t *testing.T) {
	d := newFakeDaemon(t)
	set := markSet(2, map[string]string{"o/r#1": StateOpen, "o/r#2": StateMerged, "o/r#3": StateClosed})
	var out bytes.Buffer
	res, _ := MarkWatched(context.Background(), markOpts(d, set, &out))
	if res.Code != ExitDone || res.Expected != 1 || res.Watched != 1 || len(res.SkippedClosed) != 2 || len(res.Unmapped) != 2 {
		t.Fatalf("%s (code %d)", res.Line, res.Code)
	}
	if d.watched["o/r#2"] || d.watched["o/r#3"] || strings.Contains(strings.Join(d.calls, "\n"), "o/r#2") {
		t.Errorf("a merged or closed PR was marked: %v", d.calls)
	}
	text := out.String()
	for _, want := range []string{"UNMAPPED anchor bd-0", "UNMAPPED anchor bd-1", "skip o/r#2", "skip o/r#3"} {
		if !strings.Contains(text, want) {
			t.Errorf("progress lacks %q:\n%s", want, text)
		}
	}
	if !strings.Contains(res.Line, "unmapped=2") || !strings.Contains(res.Line, "skipped_closed=2") {
		t.Errorf("last line: %s", res.Line)
	}
}

func TestMarkWatchedAFailedReadIsListedAndTheRunCanBeRepeated(t *testing.T) {
	d := newFakeDaemon(t)
	d.showFail["o/r#2"] = "unavailable"
	set := markSet(0, map[string]string{"o/r#1": StateOpen, "o/r#2": StateOpen})
	res, _ := MarkWatched(context.Background(), markOpts(d, set, &bytes.Buffer{}))
	if res.Code != ExitFailed || res.Marked != 1 || res.Refreshed != 1 || len(res.Failed) != 1 || res.Failed[0].PR != "o/r#2" || res.Failed[0].Stage != "mark" {
		t.Fatalf("%s (code %d)", res.Line, res.Code)
	}
	if !strings.Contains(res.Line, "failed_prs=o/r#2") || !strings.HasPrefix(res.Line, "FAILED ") {
		t.Errorf("last line: %s", res.Line)
	}
	// The PR whose mark failed is not refreshed, and the check still counts it.
	if d.count("pg-desk pr refresh o/r#2") != 0 || res.Expected != 2 || res.Watched != 1 {
		t.Errorf("%s, calls %v", res.Line, d.calls)
	}
	// The same step against a daemon that now answers is idempotent and passes.
	d.showFail = map[string]string{}
	res, _ = MarkWatched(context.Background(), markOpts(d, set, &bytes.Buffer{}))
	if res.Code != ExitDone || res.Watched != 2 {
		t.Fatalf("rerun: %s", res.Line)
	}
}

func TestMarkWatchedARefreshFailureIsListed(t *testing.T) {
	d := newFakeDaemon(t)
	d.refreshFail["o/r#1"] = true
	set := markSet(0, map[string]string{"o/r#1": StateOpen, "o/r#2": StateOpen})
	res, _ := MarkWatched(context.Background(), markOpts(d, set, &bytes.Buffer{}))
	if res.Code != ExitFailed || res.Refreshed != 1 || len(res.Failed) != 1 || res.Failed[0].Stage != "refresh" {
		t.Fatalf("%s (code %d)", res.Line, res.Code)
	}
}

func TestMarkWatchedAnUnauthenticatedReadStopsAtOnce(t *testing.T) {
	d := newFakeDaemon(t)
	d.showFail["o/r#1"] = "unauthenticated"
	set := markSet(0, map[string]string{"o/r#1": StateOpen, "o/r#2": StateOpen})
	res, _ := MarkWatched(context.Background(), markOpts(d, set, &bytes.Buffer{}))
	if res.Code != ExitStopped || !strings.Contains(res.Stop, "unauthenticated") || d.count("pg-connector pr show") != 1 {
		t.Fatalf("%s (code %d), calls %v", res.Line, res.Code, d.calls)
	}
}

func TestMarkWatchedStopsUnderTheFloorAndWithoutARateSignal(t *testing.T) {
	d := newFakeDaemon(t)
	set := markSet(0, map[string]string{"o/r#1": StateOpen, "o/r#2": StateOpen})
	o := markOpts(d, set, &bytes.Buffer{})
	o.Rate = &fakeRate{vals: []int{9000, 2500}} // fine before the first read, under the floor before the second
	res, _ := MarkWatched(context.Background(), o)
	if res.Code != ExitFloor || res.Outcome != OutcomeFloor || res.Marked != 1 || !strings.HasPrefix(res.Line, "FLOOR ") || d.count("pg-desk") != 0 || d.inspects != 0 {
		t.Fatalf("%s (code %d), calls %v", res.Line, res.Code, d.calls)
	}

	d = newFakeDaemon(t)
	o = markOpts(d, set, &bytes.Buffer{})
	o.Rate = &fakeRate{err: fmt.Errorf("no reading")}
	res, _ = MarkWatched(context.Background(), o)
	if res.Code != ExitStopped || !strings.Contains(res.Stop, "rate-signal-unavailable") || len(d.calls) != 0 {
		t.Fatalf("%s (code %d), calls %v", res.Line, res.Code, d.calls)
	}
}

func TestMarkWatchedInspectsInChunksAndAddsTheCounts(t *testing.T) {
	d := newFakeDaemon(t)
	prs := map[string]string{}
	for i := 0; i < MarkInspectChunk+5; i++ {
		prs[fmt.Sprintf("o/r#%d", i+1)] = StateOpen
	}
	res, _ := MarkWatched(context.Background(), markOpts(d, markSet(0, prs), &bytes.Buffer{}))
	if res.Code != ExitDone || d.inspects != 2 || res.Watched != MarkInspectChunk+5 || res.Expected != MarkInspectChunk+5 {
		t.Fatalf("%s (code %d), inspects %d", res.Line, res.Code, d.inspects)
	}
}

func TestMarkWatchedAnInspectionThatFailsOrIsUnreadableFailsTheCheck(t *testing.T) {
	set := markSet(0, map[string]string{"o/r#1": StateOpen})
	for name, wrap := range map[string]func(Result) Result{
		"an error answer": func(Result) Result {
			return Result{Code: 1, Stdout: []byte(`{"protocolVersion":1,"error":{"code":"unavailable","message":"down"}}` + "\n")}
		},
		"garbage": func(Result) Result { return Result{Stdout: []byte("not json")} },
	} {
		t.Run(name, func(t *testing.T) {
			d := newFakeDaemon(t)
			o := markOpts(d, set, &bytes.Buffer{})
			o.Runner = func(ctx context.Context, c Call) Result {
				r := d.run(ctx, c)
				if len(c.Args) > 1 && c.Args[1] == "inspect" {
					return wrap(r)
				}
				return r
			}
			res, _ := MarkWatched(context.Background(), o)
			if res.Code == ExitDone || res.Checked || res.CheckError == "" {
				t.Fatalf("%s (code %d)", res.Line, res.Code)
			}
		})
	}
}

func TestMarkWatchedPassesThePRBackendAndRefusesAnEmptySet(t *testing.T) {
	d := newFakeDaemon(t)
	o := markOpts(d, markSet(0, map[string]string{"o/r#1": StateOpen}), &bytes.Buffer{})
	o.PRBackend = "daemon-instance"
	if res, _ := MarkWatched(context.Background(), o); res.Code != ExitDone {
		t.Fatalf("%s", res.Line)
	}
	for _, c := range d.calls {
		if strings.HasPrefix(c, "pg-connector") && !strings.Contains(c, "--backend daemon-instance") {
			t.Errorf("the PR backend is not pinned: %s", c)
		}
	}
	if _, err := MarkWatched(context.Background(), MarkOptions{Set: &Set{}, Rate: &fakeRate{vals: []int{9000}}}); err == nil {
		t.Error("an empty set accepted")
	}
	if _, err := MarkWatched(context.Background(), MarkOptions{Set: markSet(0, map[string]string{"o/r#1": StateOpen})}); err == nil {
		t.Error("a missing rate source accepted")
	}
}

func TestMarkTargetsDoesNotMarkAQueryOnlyOrAClosedPR(t *testing.T) {
	s := markSet(0, map[string]string{"o/r#1": StateOpen, "o/r#2": StateMerged, "o/r#3": StateUnknown}, "o/r#4")
	targets, skipped := MarkTargets(s)
	b1, _ := json.Marshal(targets)
	b2, _ := json.Marshal(skipped)
	if string(b1) != `["o/r#1","o/r#3"]` || string(b2) != `["o/r#2"]` {
		t.Fatalf("targets %s skipped %s", b1, b2)
	}
}
