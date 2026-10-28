package prchange

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/backfill"
)

// The backfill driver against the real composed command (docket pg2-6mo45.3, packet
// P4b; design 6.5 and the test row "Backfill driver and plan summary"). The driver
// (internal/backfill) runs the BUILT pg-pr-change, which runs the BUILT pg-desk and
// pg-decider, in the hermetic rig of rig_test.go; the forge side is the fixture-driven
// fake connector with its control files. The fake `gh` `remaining` control is the
// `remaining` control file of this rig: a wrapper around the real command appends the
// next reading of it to the old backend's event log after every PR, which is the
// signal the driver reads. Nothing here is a mock of the tool under test.

// minBackfillScenarios is the number of subtests TestBackfillRealTools has; the nix
// check requires at least this many PASS lines (keep it equal to the number of run
// calls and to the nix check's minScenarios).
const minBackfillScenarios = 10

// The four PRs: #7 an open PR of mine whose anchor is open (nothing to do: the
// anchor carries every key the decider mirrors from the PR, url and dedup_key
// included, because a missing one makes anchor.backfill and adoption plan an
// `issue update`, which the fake connector does not answer, so an apply pass
// would end decide_partial), #8 and
// #12 merged PRs with no work items, #11 an open PR of mine with an unaddressed
// comment (the decider wants to write work items).
const fxBackfill = `{
  "name": "backfill-set",
  "description": "Four PRs for the backfill driver.",
  "entities": ["acme/api#7", "acme/api#8", "acme/api#11", "acme/api#12"],
  "prs": [
    {"number": 7, "title": "Add the export command", "author": "phillipgreenii"},
    {"number": 8, "title": "Tidy the readme", "author": "teammate", "state": "closed", "merged": true},
    {"number": 11, "title": "Add retry to client", "author": "phillipgreenii",
      "comments": [{"id": "c1", "author": "teammate", "body": "Please handle the timeout case."}]},
    {"number": 12, "title": "Rename the flag", "author": "teammate", "state": "closed", "merged": true}
  ],
  "beads": [{"id": "bd-7a", "title": "acme/api#7: Add the export command",
    "metadata": {"repo": "acme/api", "pr_number": "7", "state": "open", "branch": "feature-7", "base": "main", "author": "phillipgreenii", "draft": "false",
      "url": "https://code.example/acme/api/pull/7", "dedup_key": "pr:acme/api#7:anchor"}}]
}`

var bfIDs = []string{"acme/api#7", "acme/api#8", "acme/api#11", "acme/api#12"}

// bfRig is the rig plus the driver's side of it.
type bfRig struct {
	*rig
	set     *backfill.Set
	wrapper string
	rateLog string
}

func newBFRig(t *testing.T, ids ...string) *bfRig {
	t.Helper()
	r := newRigWith(t, fxBackfill, rigOpts{cutover: true})
	// The tracker double accepts the work items the decider creates.
	r.ctlWrite("tracker.accepts", "")
	b := &bfRig{rig: r, rateLog: filepath.Join(r.dir, "old-backend-events.jsonl")}
	b.set = &backfill.Set{Contract: backfill.SetContract}
	for _, id := range ids {
		b.set.Entries = append(b.set.Entries, backfill.Entry{PR: id, State: backfill.StateOpen, From: []string{backfill.FromQuery}})
	}
	// The wrapper runs the real command, then appends the next reading of the
	// `remaining` control (line n+1 after the n-th call, the last line repeating)
	// to the old backend's log. It takes its own tools from the test process's PATH
	// only after the real command has run, so the programs under test keep the sparse
	// PATH they were given.
	script := "#!/bin/sh\nCTL=" + shq(r.ctl) + "\nLOG=" + shq(b.rateLog) + "\n" + shq(r.changeBin) + ` "$@"
rc=$?
PATH=` + shq(os.Getenv("PATH")) + `":/usr/bin:/bin"
n=0
if [ -f "$CTL/remaining.n" ]; then read -r n < "$CTL/remaining.n"; fi
n=$((n + 1))
printf '%s\n' "$n" > "$CTL/remaining.n"
v=$(sed -n "$((n + 1))p" "$CTL/remaining")
if [ -z "$v" ]; then v=$(tail -n 1 "$CTL/remaining"); fi
printf '{"time":"%s","graphql_remaining":%s}\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$v" >> "$LOG"
exit $rc
`
	b.wrapper = filepath.Join(r.dir, "change-and-read-remaining")
	must(t, os.WriteFile(b.wrapper, []byte(script), 0o755))
	b.remaining(9000)
	return b
}

// remaining sets the fake gh `remaining` control: the first value is the reading
// before the first PR, each next one the reading after the next call.
func (b *bfRig) remaining(vals ...int) {
	b.t.Helper()
	var sb strings.Builder
	for _, v := range vals {
		sb.WriteString(itoa(v) + "\n")
	}
	b.ctlWrite("remaining", sb.String())
	b.ctlWrite("remaining.n", "0\n")
	f, err := os.OpenFile(b.rateLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	must(b.t, err)
	_, err = f.WriteString(`{"time":"` + time.Now().UTC().Format("2006-01-02T15:04:05Z") + `","graphql_remaining":` + itoa(vals[0]) + "}\n")
	must(b.t, err)
	must(b.t, f.Close())
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}

// hermetic runs the driver's children under the rig's environment and nothing else.
func (r *rig) hermetic(ctx context.Context, c backfill.Call) backfill.Result {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Env = r.env
	cmd.Dir = filepath.Join(r.dir, "work")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := backfill.Result{Stdout: []byte(out.String()), Stderr: []byte(errb.String())}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.Code = ee.ExitCode()
	default:
		res.Code, res.StartErr = -1, err
	}
	return res
}

// drive runs the driver once in mode and checks what every ending shares: the last
// line starts with its outcome word and the exit code is never 1 or 9.
func (b *bfRig) drive(mode string, mut ...func(*backfill.RunOptions)) *backfill.RunResult {
	b.t.Helper()
	o := backfill.RunOptions{
		Mode: mode, Set: b.set, RunRecord: b.runRecordPath(), ChangeCmd: b.wrapper,
		Rate: backfill.LogSource{Path: b.rateLog}, Floor: backfill.DefaultFloor,
		Env: b.env, Runner: b.hermetic, Progress: io.Discard, Pace: 0,
	}
	for _, m := range mut {
		m(&o)
	}
	res, err := backfill.Run(context.Background(), o)
	if err != nil {
		b.t.Fatalf("the driver refused the run: %v", err)
	}
	if res.Code == 1 || res.Code == 9 {
		b.t.Fatalf("the driver exited %d, a code it never uses: %s", res.Code, res.Line)
	}
	if !strings.HasPrefix(res.Line, res.Outcome+" ") || strings.Contains(res.Line, "\n") {
		b.t.Fatalf("the last line is not one line starting %s: %q", res.Outcome, res.Line)
	}
	return res
}

func (b *bfRig) log() *backfill.Log {
	b.t.Helper()
	lg, err := backfill.ReadLog(b.runRecordPath())
	must(b.t, err)
	return lg
}

// connectorCalls counts the connector calls of one PR that start with prefix.
func (b *bfRig) connectorCalls(prefix, pr string) int {
	n := 0
	for _, l := range b.calls(prefix) {
		if strings.Contains(l, pr) {
			n++
		}
	}
	return n
}

func TestBackfillRealTools(t *testing.T) {
	scenarios := 0
	run := func(name string, f func(*testing.T)) {
		scenarios++
		t.Run(name, f)
	}

	run("with-the-remaining-control-under-the-floor-the-run-reports-FLOOR-and-a-restart-in-the-same-mode-redoes-no-finished-PR", func(t *testing.T) {
		b := newBFRig(t, bfIDs...)
		// 9000 before the first PR, 4000 after it, 2500 after the second.
		b.remaining(9000, 4000, 2500)
		res := b.drive(backfill.ModePlan)
		if res.Outcome != backfill.OutcomeFloor || res.Code != backfill.ExitFloor || res.Counts.Done != 2 || res.Counts.Remaining != 2 {
			t.Fatalf("%s (code %d)", res.Line, res.Code)
		}
		contains(t, "last line", res.Line, "floor=3000", "newest=2500")
		if got := len(b.log().Records); got != 2 {
			t.Fatalf("%d run records, want 2", got)
		}
		// The summary names the two PRs the run never reached.
		s := backfill.Summarize(b.log(), b.set, backfill.ModePlan, 0, time.Now())
		if s.OK() || s.WithRecord != 2 || len(s.Missing) != 2 || !strings.Contains(s.Missing[0].Why, "no plan-mode record") {
			t.Fatalf("summary: with record %d, missing %+v", s.WithRecord, s.Missing)
		}

		// Restart with the budget back: the finished PRs are not run again.
		b.remaining(9000)
		before := len(b.calls("pr show"))
		res = b.drive(backfill.ModePlan)
		if res.Outcome != backfill.OutcomeDone || res.Counts.Skipped != 2 || res.Counts.Done != 2 {
			t.Fatalf("%s", res.Line)
		}
		if got := len(b.calls("pr show")) - before; got != 2 {
			t.Errorf("the restart read %d PRs, want the 2 unfinished ones", got)
		}
		for _, done := range bfIDs[:2] {
			if n := b.connectorCalls("pr show", done); n != 1 {
				t.Errorf("%s was read %d times, want 1", done, n)
			}
		}
		s = backfill.Summarize(b.log(), b.set, backfill.ModePlan, 0, time.Now())
		if !s.OK() || s.Enumerated != 4 || s.WithRecord != 4 {
			t.Fatalf("summary after DONE: %v", s.Problems)
		}
	})

	run("a-switch-from-plan-to-apply-redoes-every-PR-and-a-new-pass-redoes-them-again", func(t *testing.T) {
		b := newBFRig(t, bfIDs...)
		if res := b.drive(backfill.ModePlan); res.Outcome != backfill.OutcomeDone {
			t.Fatalf("%s", res.Line)
		}
		res := b.drive(backfill.ModeApply, func(o *backfill.RunOptions) { o.NoLimits = true })
		if res.Outcome != backfill.OutcomeDone || res.Counts.Skipped != 0 || res.Counts.Done != 4 {
			t.Fatalf("apply after plan: %s", res.Line)
		}
		if got := len(b.deciderRuns()); got != 8 {
			t.Errorf("the decider decided %d times over a plan pass and an apply pass of 4 PRs, want 8: %q", got, b.deciderRuns())
		}
		// Same mode again: nothing to do. A new pass: everything again.
		again := b.drive(backfill.ModeApply, func(o *backfill.RunOptions) { o.NoLimits = true })
		if again.Counts.Skipped != 4 || again.Counts.Done != 0 {
			t.Fatalf("the same pass again: %s", again.Line)
		}
		fresh := b.drive(backfill.ModeApply, func(o *backfill.RunOptions) { o.NoLimits = true; o.NewPass = true })
		if fresh.Counts.Skipped != 0 || fresh.Counts.Done != 4 {
			t.Fatalf("a new pass: %s", fresh.Line)
		}
	})

	run("a-5xx-and-a-rate-limit-are-recorded-named-in-FAILED-and-nothing-is-decided-on-an-unhydrated-PR", func(t *testing.T) {
		b := newBFRig(t, bfIDs...)
		b.remaining(9000, 4000, 2500) // two PRs finish, then the floor
		if res := b.drive(backfill.ModePlan); res.Outcome != backfill.OutcomeFloor {
			t.Fatalf("%s", res.Line)
		}
		decidedBefore := len(b.deciderRuns())
		b.remaining(9000)
		// A 502 for one PR, a rate limit for the other: both reach the driver as
		// the command's transient class, retryable by a re-run.
		b.ctlWrite("fail.pr-show", "unavailable")
		b.ctlWrite("fail.pr-show.message", "HTTP 502: bad gateway")
		res := b.drive(backfill.ModePlan, func(o *backfill.RunOptions) { o.Pace = time.Millisecond })
		if res.Outcome != backfill.OutcomeFailed || res.Code != backfill.ExitFailed || res.Stop != "" {
			t.Fatalf("%s (code %d)", res.Line, res.Code)
		}
		contains(t, "last line", res.Line, "skipped=2", "failed=2", "transient=acme/api#11,acme/api#12")
		if got := len(b.deciderRuns()); got != decidedBefore {
			t.Errorf("the decider decided on a PR that could not be hydrated (%d runs, was %d)", got, decidedBefore)
		}
		b.ctlWrite("fail.pr-show.message", "GraphQL rate limit exceeded, retry after the window resets")
		again := b.drive(backfill.ModePlan, func(o *backfill.RunOptions) { o.NewPass = false })
		if again.Outcome != backfill.OutcomeFailed || !strings.Contains(again.Line, "transient=acme/api#11,acme/api#12") {
			t.Fatalf("%s", again.Line)
		}
		// The summary names them, failed and unhydrated alike.
		s := backfill.Summarize(b.log(), b.set, backfill.ModePlan, 0, time.Now())
		if s.OK() || len(s.Missing) != 2 || !strings.Contains(s.Missing[0].Why, "class transient") {
			t.Fatalf("summary: %+v", s.Missing)
		}
		// The forge recovers: the re-run resumes, skipping the two that finished.
		b.ctlRemove("fail.pr-show")
		fin := b.drive(backfill.ModePlan)
		if fin.Outcome != backfill.OutcomeDone || fin.Counts.Skipped != 2 || fin.Counts.Done != 2 {
			t.Fatalf("%s", fin.Line)
		}
		if got := b.neverHydrated(); got != "0" {
			t.Errorf("%s PR(s) are still unhydrated after DONE", got)
		}
	})

	run("a-degraded-read-ends-FAILED-naming-the-PR-and-no-decide-runs-on-it", func(t *testing.T) {
		b := newBFRig(t, "acme/api#11")
		b.ctlWrite("fail.detail", "unavailable")
		res := b.drive(backfill.ModeApply, func(o *backfill.RunOptions) { o.NoLimits = true })
		if res.Outcome != backfill.OutcomeFailed || res.Code != backfill.ExitFailed || !strings.Contains(res.Line, "degraded=acme/api#11") {
			t.Fatalf("%s (code %d)", res.Line, res.Code)
		}
		if got := b.deciderRuns(); len(got) != 0 {
			t.Errorf("the decider ran on a partially hydrated PR: %q", got)
		}
		if n := b.created(); n != 0 {
			t.Errorf("%d work items were created from a partial read", n)
		}
	})

	run("an-unauthenticated-run-stops-the-driver-at-once-with-its-own-exit-code", func(t *testing.T) {
		b := newBFRig(t, bfIDs...)
		b.ctlWrite("fail.pr-show", "unauthenticated")
		res := b.drive(backfill.ModePlan)
		if res.Code != backfill.ExitStopped || res.Outcome != backfill.OutcomeFailed || !strings.Contains(res.Line, "stop=") || !strings.Contains(res.Line, "unauthenticated") {
			t.Fatalf("%s (code %d)", res.Line, res.Code)
		}
		if n := len(b.calls("pr show")); n != 1 {
			t.Errorf("the driver read %d PRs after an unauthenticated run, want 1", n)
		}
		if res.Counts.Remaining != 3 {
			t.Errorf("remaining = %d", res.Counts.Remaining)
		}
	})

	run("a-PR-the-forge-reports-gone-is-a-final-result-that-a-resume-does-not-run-again", func(t *testing.T) {
		b := newBFRig(t, "acme/api#7", "acme/api#8")
		b.ctlWrite("fail.pr-show", "not_found")
		res := b.drive(backfill.ModePlan)
		if res.Outcome != backfill.OutcomeDone || res.Counts.NotFound != 2 {
			t.Fatalf("%s", res.Line)
		}
		before := len(b.calls("pr show"))
		res = b.drive(backfill.ModePlan)
		if res.Counts.Skipped != 2 || len(b.calls("pr show")) != before {
			t.Fatalf("%s", res.Line)
		}
		// The plan summary does not vouch for them.
		if s := backfill.Summarize(b.log(), b.set, backfill.ModePlan, 0, time.Now()); s.OK() || len(s.Missing) != 2 || !strings.Contains(s.Missing[0].Why, "class not_found") {
			t.Fatalf("summary: %+v", s.Missing)
		}
	})

	run("the-apply-pass-creates-what-the-plan-summary-announced-and-a-limit-one-below-stops-the-run-after-the-PR-that-exceeded-it", func(t *testing.T) {
		b := newBFRig(t, bfIDs...)
		if res := b.drive(backfill.ModePlan); res.Outcome != backfill.OutcomeDone {
			t.Fatalf("%s", res.Line)
		}
		s := backfill.Summarize(b.log(), b.set, backfill.ModePlan, 0, time.Now())
		if !s.OK() {
			t.Fatalf("summary: %v", s.Problems)
		}
		expected := map[string]int{}
		total := 0
		for kind, ops := range s.Counts {
			if ops["create"] > 0 {
				expected[kind] = ops["create"]
				total += ops["create"]
			}
		}
		if total == 0 {
			t.Fatalf("non-vacuity: the plan announced no create: %+v", s.Counts)
		}
		// The operator records the expected creates; the apply pass meets them.
		res := b.drive(backfill.ModeApply, func(o *backfill.RunOptions) { o.Limits = expected })
		if res.Outcome != backfill.OutcomeDone || res.Stop != "" {
			t.Fatalf("%s", res.Line)
		}
		if got := b.created(); got != total {
			t.Errorf("the apply pass made %d creates, the plan announced %d", got, total)
		}
		// The same pass with one fewer expected create of the busiest kind stops
		// after the PR that made the extra one (the create is made: a stop
		// criterion is not a cap).
		b2 := newBFRig(t, bfIDs...)
		if res := b2.drive(backfill.ModePlan); res.Outcome != backfill.OutcomeDone {
			t.Fatalf("%s", res.Line)
		}
		tight := map[string]int{}
		var worst string
		for k, n := range expected {
			tight[k] = n
			worst = k
		}
		tight[worst]--
		res = b2.drive(backfill.ModeApply, func(o *backfill.RunOptions) { o.Limits = tight })
		if res.Code != backfill.ExitStopped || !strings.Contains(res.Stop, "create-limit") || !strings.Contains(res.Stop, "kind "+worst) {
			t.Fatalf("%s (code %d)", res.Line, res.Code)
		}
		if b2.created() == 0 {
			t.Error("the PR that exceeded the limit was prevented, not stopped after")
		}
	})

	run("after-the-flip-a-second-apply-pass-writes-nothing-and-the-residual-check-passes", func(t *testing.T) {
		// A set of PRs the decider has nothing to write for (two merged PRs with no
		// work items): the tracker double does not model its own writes back into
		// the view, so a PR with writes would be planned again.
		b := newBFRig(t, "acme/api#8", "acme/api#12")
		if res := b.drive(backfill.ModePlan); res.Outcome != backfill.OutcomeDone {
			t.Fatalf("%s", res.Line)
		}
		flip := time.Now().UTC().Add(-time.Minute)
		_, err := backfill.WriteFlipMarker(context.Background(), backfill.FlipMarkerOptions{RunRecord: b.runRecordPath(), Time: flip})
		must(t, err)
		first := b.drive(backfill.ModeApply, func(o *backfill.RunOptions) { o.Limits = map[string]int{} })
		if first.Outcome != backfill.OutcomeDone {
			t.Fatalf("%s", first.Line)
		}
		written := b.created()
		second := b.drive(backfill.ModeApply, func(o *backfill.RunOptions) { o.Limits = map[string]int{}; o.NewPass = true })
		if second.Outcome != backfill.OutcomeDone || second.Counts.Done != 2 {
			t.Fatalf("%s", second.Line)
		}
		if b.created() != written || written != 0 {
			t.Errorf("creates after the first pass: %d, after the second: %d", written, b.created())
		}
		lg := b.log()
		if ws, bound, ok := backfill.WritesSinceFlip(lg); !ok || !bound.Equal(flip.Truncate(time.Second)) || len(ws) != 0 {
			t.Errorf("writes since the flip: %v bound %v %v", ws, bound, ok)
		}
		s := backfill.Summarize(lg, b.set, backfill.ModeApply, 0, time.Now())
		if !s.OK() || s.WithRecord != 2 {
			t.Fatalf("the apply summary: %v", s.Problems)
		}
		for pr, v := range s.Residuals {
			if v != 0 {
				t.Errorf("%s: residual %d", pr, v)
			}
		}
	})

	run("the-by-timestamp-listing-since-the-flip-marker-lists-the-real-creates-of-the-apply-pass", func(t *testing.T) {
		b := newBFRig(t, bfIDs...)
		_, err := backfill.WriteFlipMarker(context.Background(), backfill.FlipMarkerOptions{RunRecord: b.runRecordPath()})
		must(t, err)
		if res := b.drive(backfill.ModeApply, func(o *backfill.RunOptions) { o.NoLimits = true }); res.Outcome != backfill.OutcomeDone {
			t.Fatalf("%s", res.Line)
		}
		ws, _, ok := backfill.WritesSinceFlip(b.log())
		if !ok || len(ws) == 0 {
			t.Fatalf("no writes listed (%v)", ok)
		}
		creates := 0
		for _, w := range ws {
			if w.PR == "acme/api#8" || w.PR == "acme/api#12" {
				t.Errorf("a write of %s, a merged PR the decider has nothing to write for: %+v", w.PR, w)
			}
			if w.Op == "create" {
				creates++
			}
		}
		if creates != b.created() {
			t.Errorf("the listing shows %d creates, the tracker saw %d", creates, b.created())
		}
		var out bytes.Buffer
		if n, ok := backfill.PrintWrites(&out, b.log()); !ok || n != len(ws) {
			t.Errorf("printed %d", n)
		}
	})

	run("mark-watched-reads-each-open-anchor-PR-with-the-watch-option-then-hydrates-it-with-the-real-pg-desk-and-never-decides-or-writes", func(t *testing.T) {
		b := newBFRig(t) // the set below is the marking step's own
		b.ctlWrite("watched", "")
		b.ctlWrite("nowatch", "")
		set := &backfill.Set{
			Contract: backfill.SetContract, OpenAnchorCount: 4, OpenAnchorPRCount: 3,
			Entries: []backfill.Entry{
				{PR: "acme/api#7", State: backfill.StateOpen, From: []string{backfill.FromAnchor}, AnchorIDs: []string{"bd-7a"}},
				{PR: "acme/api#8", State: backfill.StateMerged, From: []string{backfill.FromAnchor}, AnchorIDs: []string{"bd-8a"}},
				{PR: "acme/api#11", State: backfill.StateUnknown, From: []string{backfill.FromAnchor}, AnchorIDs: []string{"bd-11a"}},
				{PR: "acme/api#12", State: backfill.StateOpen, From: []string{backfill.FromQuery}, Queries: []string{"mine"}},
			},
			Unparseable: []backfill.Unparseable{{From: backfill.FromAnchor, ID: "bd-x", Title: "no PR named", Reason: "not a PR title"}},
		}
		mark := func() *backfill.MarkResult {
			t.Helper()
			res, err := backfill.MarkWatched(context.Background(), backfill.MarkOptions{
				Set: set, Rate: backfill.LogSource{Path: b.rateLog}, Floor: backfill.DefaultFloor,
				// Absolute paths: the hermetic runner resolves a bare name on the test
				// process's PATH, and the sandbox's own programs are in binDir.
				Connector: filepath.Join(b.binDir, "pg-connector"), Desk: filepath.Join(b.binDir, "pg-desk"),
				Env: b.env, Runner: b.hermetic, Progress: io.Discard,
			})
			must(t, err)
			return res
		}
		res := mark()
		if res.Code != backfill.ExitDone || res.Watched != 2 || res.Expected != 2 || res.Marked != 2 || res.Refreshed != 2 {
			t.Fatalf("%s (code %d)", res.Line, res.Code)
		}
		if got := b.ctlRead("watched"); got != "acme/api#7\nacme/api#11\n" {
			t.Errorf("the watched set is %q: the merged-PR anchor and the query-only PR must not be marked", got)
		}
		// The refresh was the real pg-desk's: both marked PRs are now hydrated in its store.
		if got := b.sqlite("SELECT COUNT(*) FROM entity WHERE entity_type = 'pr' AND version > 0;"); got != "2" {
			t.Errorf("%s hydrated PR rows, want 2", got)
		}
		// Nothing was decided or written: the decider never ran and the tracker saw no create.
		if got := b.deciderRuns(); len(got) != 0 {
			t.Errorf("the decider ran during the marking step: %q", got)
		}
		if n := b.created(); n != 0 {
			t.Errorf("%d work items were created", n)
		}
		// The negative: a daemon that answers the watch read but does not watch one
		// PR makes the step fail its count check.
		b.ctlWrite("watched", "")
		b.ctlWrite("nowatch", "acme/api#11\n")
		bad := mark()
		if bad.Code != backfill.ExitMarkCheckFailed || bad.Watched != 1 || bad.Expected != 2 {
			t.Fatalf("a PR that was not watched passed the check: %s (code %d)", bad.Line, bad.Code)
		}
	})

	if scenarios != minBackfillScenarios {
		t.Errorf("the test declares %d scenarios, but minBackfillScenarios is %d (and the nix check's minScenarios must equal it)", scenarios, minBackfillScenarios)
	}
}
