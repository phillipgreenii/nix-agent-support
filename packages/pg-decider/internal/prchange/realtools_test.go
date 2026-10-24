package prchange

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// Scenario fixtures. The anchor shape is the parity gate's (a bead titled
// "<repo>#<n>: <title>" carrying the PR's repo and number).
const (
	fxOpenAnchor = `{
  "name": "open-anchor",
  "description": "An open PR of mine whose anchor is open: the PR is later reported gone.",
  "entities": ["acme/api#7"],
  "prs": [{"number": 7, "title": "Add the export command", "author": "phillipgreenii"}],
  "beads": [{"id": "bd-7a", "title": "acme/api#7: Add the export command",
    "metadata": {"repo": "acme/api", "pr_number": "7", "state": "open", "branch": "feature-7", "base": "main", "author": "phillipgreenii", "draft": "false"}}]
}`
	fxMerged = `{
  "name": "merged-no-work",
  "description": "A merged PR with no work items: the decider has nothing to do and writes nothing.",
  "entities": ["acme/api#8"],
  "prs": [{"number": 8, "title": "Tidy the readme", "author": "teammate", "state": "closed", "merged": true}]
}`
	fxLegacy = `{
  "name": "legacy-row",
  "description": "A PR whose store row is the legacy shape the cutover leaves: version 0, never hydrated.",
  "entities": ["acme/api#9"],
  "legacy_seeded": ["acme/api#9"],
  "prs": [{"number": 9, "title": "Add retry to client", "author": "phillipgreenii"}]
}`
	fxMine = `{
  "name": "mine-with-feedback",
  "description": "An open PR of mine with an unaddressed review comment: the decider wants to write work items.",
  "entities": ["acme/api#11"],
  "prs": [{"number": 11, "title": "Add retry to client", "author": "phillipgreenii",
    "comments": [{"id": "c1", "author": "teammate", "body": "Please handle the timeout case."}]}]
}`
)

const (
	id7  = "acme/api#7"
	id8  = "acme/api#8"
	id9  = "acme/api#9"
	id11 = "acme/api#11"
)

// minScenarios is the number of subtests TestPRChangeRealTools has; the nix
// check requires at least this many PASS lines, so a filtered-out or renamed
// scenario cannot pass vacuously. Keep it equal to the number of t.Run calls.
const minScenarios = 20

func contains(t *testing.T, what, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, got)
		}
	}
}

func lacksRetryWords(t *testing.T, what, got string) {
	t.Helper()
	for _, w := range retryWords {
		if strings.Contains(strings.ToLower(got), w) {
			t.Errorf("%s carries the router's retry word %q:\n%s", what, w, got)
		}
	}
}

func TestPRChangeRealTools(t *testing.T) {
	scenarios := 0
	run := func(name string, f func(*testing.T)) {
		scenarios++
		t.Run(name, f)
	}

	run("a-502-is-transient-with-the-routers-retry-wording-and-never-escalated-as-not-found", func(t *testing.T) {
		r := newRig(t, fxOpenAnchor, true)
		r.hydrate()
		r.ctlWrite("fail.pr-show", "unavailable")
		// The message even says the repository is missing: the wire code wins.
		r.ctlWrite("fail.pr-show.message", "HTTP 502: repository not found")
		res := r.change("apply", id7)
		if res.code != ExitHydrateTransient {
			t.Fatalf("exit %d, want %d:\n%s", res.code, ExitHydrateTransient, res.stderr)
		}
		contains(t, "failure line", res.stderr, "stage=hydrate", "class=transient", "unavailable", ": run pg-desk pr refresh "+id7)
		if got := r.ctlRead("created.log"); got != "" {
			t.Errorf("an escalation was raised for a transient failure: %q", got)
		}
		if len(r.calls("search")) != 0 || len(r.deciderRuns()) != 0 {
			t.Errorf("a transient refresh searched the tracker or ran the decider")
		}
	})

	run("a-null-node-is-not-found-and-apply-raises-exactly-one-escalation", func(t *testing.T) {
		r := newRig(t, fxOpenAnchor, true)
		r.hydrate()
		r.ctlWrite("fail.pr-show", "not_found")
		res := r.change("apply", id7)
		if res.code != ExitHydrateNotFound {
			t.Fatalf("exit %d, want %d:\n%s", res.code, ExitHydrateNotFound, res.stderr)
		}
		contains(t, "failure line", res.stderr, "class=not_found", "escalated as bd-esc-1", ": run pg-desk pr show "+id7+" --json")
		lacksRetryWords(t, "not_found line", res.stderr)
		if n := r.created(); n != 1 {
			t.Fatalf("want one escalation, got %d: %q", n, r.ctlRead("created.log"))
		}
		contains(t, "the escalation", r.ctlRead("created.log"), "--title "+EscalationTitle(id7), "--labels human", "--parent bd-7a")
		if len(r.deciderRuns()) != 0 {
			t.Error("the decider ran after a not-found refresh")
		}
		if got := r.active(id7); got != "1" {
			t.Errorf("a PR with an open anchor was marked inactive (active = %s)", got)
		}

		// A redelivered change finds the earlier escalation and raises no second
		// one: the tracker now answers the search with it.
		r.ctlWrite("search.out", `{"sources":[{"source":"pg-connector-issue-beads","status":"succeeded","count":1}],"groups":[{"source":"pg-connector-issue-beads","results":[{"type":"issue","id":"bd-esc-1","title":"`+EscalationTitle(id7)+`","url":"","source":"pg-connector-issue-beads"}]}]}`+"\n")
		again := r.change("apply", id7+"@0123456789ab")
		if again.code != ExitHydrateNotFound {
			t.Fatalf("redelivery: exit %d:\n%s", again.code, again.stderr)
		}
		contains(t, "redelivery line", again.stderr, "already escalated as bd-esc-1")
		if n := r.created(); n != 1 {
			t.Errorf("a redelivered change raised a second escalation (%d in total)", n)
		}
	})

	run("not-found-in-plan-mode-records-only-with-no-tracker-write-and-no-escalation", func(t *testing.T) {
		r := newRig(t, fxOpenAnchor, true)
		r.hydrate()
		r.ctlWrite("fail.pr-show", "not_found")
		res := r.change("plan", id7)
		if res.code != ExitHydrateNotFound {
			t.Fatalf("exit %d:\n%s", res.code, res.stderr)
		}
		contains(t, "plan line", res.stderr, "class=not_found", "plan mode records this and writes nothing")
		if r.ctlRead("created.log") != "" || len(r.calls("search")) != 0 {
			t.Error("plan mode wrote to the tracker or searched it for an escalation")
		}
		if r.active(id7) != "1" {
			t.Error("plan mode marked a PR with an open anchor inactive")
		}
	})

	for _, mode := range []string{"plan", "apply"} {
		mode := mode
		run("not-found-with-no-open-anchor-is-marked-inactive-in-"+mode+"-mode-and-drops-out-of-the-unhydrated-count", func(t *testing.T) {
			r := newRig(t, fxLegacy, true, id9)
			if got := r.neverHydrated(); got != "1" {
				t.Fatalf("setup: the never-hydrated count is %s, want 1", got)
			}
			r.ctlWrite("fail.pr-show", "not_found")
			res := r.change(mode, id9)
			if res.code != ExitHydrateNotFound {
				t.Fatalf("exit %d:\n%s", res.code, res.stderr)
			}
			contains(t, "line", res.stderr, "class=not_found", "no open anchor", "marked inactive")
			if got := r.neverHydrated(); got != "0" {
				t.Errorf("the never-hydrated count is %s after the run, want 0", got)
			}
			if got := r.active(id9); got != "0" {
				t.Errorf("the PR is still active (%s)", got)
			}
			if r.ctlRead("created.log") != "" || len(r.calls("search")) != 0 || len(r.calls("issue create")) != 0 {
				t.Error("a not-found PR with no open anchor touched the tracker")
			}
			if len(r.deciderRuns()) != 0 {
				t.Error("the decider ran")
			}
		})
	}

	run("a-degraded-read-exits-hydrate-degraded-and-runs-no-decide", func(t *testing.T) {
		r := newRig(t, fxMerged, true)
		r.ctlWrite("fail.detail", "unavailable")
		res := r.change("apply", id8)
		if res.code != ExitHydrateDegraded {
			t.Fatalf("exit %d, want %d:\n%s", res.code, ExitHydrateDegraded, res.stderr)
		}
		contains(t, "line", res.stderr, "class=degraded", "unavailable")
		if len(r.deciderRuns()) != 0 {
			t.Error("the decider ran after a degraded refresh")
		}
	})

	run("a-refresh-that-reports-no-change-still-runs-the-decider", func(t *testing.T) {
		r := newRig(t, fxMerged, true)
		r.hydrate()
		res := r.change("apply", id8)
		if res.code != ExitOK {
			t.Fatalf("exit %d:\n%s", res.code, res.stderr)
		}
		contains(t, "refresh line", res.stdout, "no change")
		if got := r.deciderRuns(); len(got) != 1 || got[0] != "apply pr "+id8 {
			t.Fatalf("decider runs = %q, want exactly [apply pr %s]", got, id8)
		}
		// The router's retry-window form of the id is the same PR.
		res = r.change("plan", id8+"@0123456789ab")
		if res.code != ExitOK {
			t.Fatalf("retry-window id: exit %d:\n%s", res.code, res.stderr)
		}
		if got := r.deciderRuns(); len(got) != 2 || got[1] != "plan pr "+id8 {
			t.Fatalf("decider runs = %q", got)
		}
	})

	run("both-id-forms-take-the-same-lock-and-a-wait-that-times-out-exits-transient", func(t *testing.T) {
		r := newRig(t, fxMerged, true)
		r.hydrate()
		kill := r.startBlocked("apply", id8)
		defer kill()
		for _, id := range []string{id8 + "@0123456789ab", id8} {
			res := r.change("apply", id, "--lock-wait", "300ms")
			if res.code != ExitLockTimeout {
				t.Fatalf("%s: exit %d, want %d:\n%s", id, res.code, ExitLockTimeout, res.stderr)
			}
			contains(t, "lock line", res.stderr, "stage=lock", "class=transient", "unavailable")
		}
	})

	run("a-killed-lock-holder-releases-the-lock", func(t *testing.T) {
		r := newRig(t, fxMerged, true)
		r.hydrate()
		kill := r.startBlocked("apply", id8)
		if res := r.change("apply", id8, "--lock-wait", "200ms"); res.code != ExitLockTimeout {
			t.Fatalf("while held: exit %d:\n%s", res.code, res.stderr)
		}
		kill()
		r.ctlRemove("block")
		res := r.change("apply", id8+"@0123456789ab", "--lock-wait", "5s")
		if res.code != ExitOK {
			t.Fatalf("after the holder was killed: exit %d:\n%s", res.code, res.stderr)
		}
	})

	run("a-refresh-exit-1-is-hydrate_error-is-not-retryable-and-runs-no-decide", func(t *testing.T) {
		// A store that was never cut over to schema 2: refresh refuses with exit 1.
		r := newRig(t, fxMerged, false)
		res := r.change("apply", id8)
		if res.code != ExitHydrateError {
			t.Fatalf("exit %d, want %d:\n%s", res.code, ExitHydrateError, res.stderr)
		}
		contains(t, "line", res.stderr, "stage=hydrate", "class=hydrate_error", ": run pg-desk pr refresh "+id8)
		lacksRetryWords(t, "hydrate_error line", res.stderr)
		if len(r.deciderRuns()) != 0 {
			t.Error("the decider ran after a refresh exit 1")
		}
	})

	run("the-failure-lines-of-every-non-retryable-class-carry-none-of-the-routers-retry-words", func(t *testing.T) {
		r := newRig(t, fxOpenAnchor, true)
		r.hydrate()
		// Upstream text stuffed with all three router retry words, under each
		// wire code that is NOT retryable.
		r.ctlWrite("fail.pr-show.message", "Service Unavailable: deadline exceeded after signal: killed")
		for code, class := range map[string]string{"unauthenticated": "unauthenticated", "invalid_argument": "hydrate_error"} {
			r.ctlWrite("fail.pr-show", code)
			res := r.change("apply", id7)
			want := map[string]int{"unauthenticated": ExitHydrateUnauthenticated, "hydrate_error": ExitHydrateError}[class]
			if res.code != want {
				t.Fatalf("%s: exit %d, want %d:\n%s", code, res.code, want, res.stderr)
			}
			contains(t, code+" line", res.stderr, "class="+class)
			lacksRetryWords(t, code+" line", res.stderr)
			if !strings.HasPrefix(strings.TrimSpace(res.stderr), "pg-pr-change:") || strings.Count(strings.TrimSpace(res.stderr), "\n") != 0 {
				t.Errorf("%s: not exactly one failure line: %q", code, res.stderr)
			}
		}
		// The not_found line, with an upstream text of its own.
		r.ctlWrite("fail.pr-show", "not_found")
		res := r.change("plan", id7)
		lacksRetryWords(t, "not_found line", res.stderr)
	})

	run("a-partial-decide-is-retryable-and-its-lines-are-re-wrapped-with-the-next-command", func(t *testing.T) {
		r := newRig(t, fxMine, true)
		res := r.change("apply", id11)
		if res.code != ExitDecidePartial {
			t.Fatalf("exit %d, want %d:\n%s", res.code, ExitDecidePartial, res.stderr)
		}
		var failure string
		for _, l := range strings.Split(strings.TrimSpace(res.stderr), "\n") {
			if strings.HasPrefix(l, "pg-pr-change: ") && strings.Contains(l, "stage=decide") {
				failure = l
			}
		}
		if failure == "" {
			t.Fatalf("no decide failure line:\n%s", res.stderr)
		}
		contains(t, "decide line", failure, "class=decide_partial", "unavailable", ": run pg-decider plan pr "+id11)
		// The decider's run counters line is passed through unchanged.
		contains(t, "stderr", res.stderr, "pg-decider.run-counters/v1")
		if len(r.deciderRuns()) != 1 {
			t.Errorf("decider runs = %q", r.deciderRuns())
		}
	})

	run("plan-mode-prints-the-plan-and-writes-nothing", func(t *testing.T) {
		r := newRig(t, fxMine, true)
		res := r.change("plan", id11)
		if res.code != ExitOK {
			t.Fatalf("exit %d:\n%s", res.code, res.stderr)
		}
		contains(t, "plan output", res.stdout, "actions:", "pr "+id11)
		if len(r.calls("issue create")) != 0 || len(r.calls("issue comment")) != 0 {
			t.Error("plan mode wrote to the tracker")
		}
		if got := r.deciderRuns(); len(got) != 1 || got[0] != "plan pr "+id11 {
			t.Errorf("decider runs = %q", got)
		}
	})

	run("the-command-never-exits-1-or-9-and-refuses-a-bad-id-with-a-usage-code", func(t *testing.T) {
		r := newRig(t, fxMerged, true)
		for _, args := range [][]string{
			{},
			{"apply"},
			{"sync", id8},
			{"apply", "8"},
			{"apply", "https://example.test/o/r/pull/8"},
			{"apply", id8, "--bogus"},
			{"apply", id8, "--lock-wait", "soon"},
		} {
			res := r.change(args...)
			if res.code != ExitUsage {
				t.Errorf("%v: exit %d, want %d:\n%s", args, res.code, ExitUsage, res.stderr)
			}
			contains(t, "usage line", res.stderr, "pg-pr-change: usage:", ": run pg-pr-change --help")
		}
		for _, args := range [][]string{{"--help"}, {"--version"}} {
			if res := r.change(args...); res.code != ExitOK {
				t.Errorf("%v: exit %d:\n%s", args, res.code, res.stderr)
			}
		}
		if len(r.calls("pr ")) != 0 || len(r.deciderRuns()) != 0 {
			t.Errorf("a refused argument list refreshed or decided a PR: %v %v", r.calls("pr "), r.deciderRuns())
		}
	})

	run("a-search-at-the-100-hit-cap-or-a-degraded-beads-row-exits-transient-and-raises-no-escalation", func(t *testing.T) {
		r := newRig(t, fxOpenAnchor, true)
		r.hydrate()
		r.ctlWrite("fail.pr-show", "not_found")
		for name, search := range map[string]struct {
			out  string
			code string
		}{
			"at the cap":           {`{"sources":[{"source":"pg-connector-issue-beads","status":"succeeded","count":100}],"groups":[]}`, "0"},
			"a degraded row":       {`{"sources":[{"source":"pg-connector-issue-beads","status":"degraded","count":0,"reason":"bd failed"}],"groups":[]}`, "3"},
			"no beads row at all":  {`{"sources":[],"groups":[]}`, "3"},
			"an unreadable answer": {`not json`, "1"},
		} {
			r.ctlWrite("search.out", search.out+"\n")
			r.ctlWrite("search.code", search.code)
			res := r.change("apply", id7)
			if res.code != ExitHydrateTransient {
				t.Fatalf("%s: exit %d, want %d:\n%s", name, res.code, ExitHydrateTransient, res.stderr)
			}
			contains(t, name+" line", res.stderr, "class=transient", "unavailable")
			if r.created() != 0 {
				t.Fatalf("%s: an escalation was raised from a lookup that cannot be trusted", name)
			}
		}
	})

	run("a-degraded-non-beads-row-is-ignored-and-a-failing-open-anchor-probe-is-transient", func(t *testing.T) {
		r := newRig(t, fxOpenAnchor, true)
		r.hydrate()
		r.ctlWrite("fail.pr-show", "not_found")
		r.ctlWrite("search.out", `{"sources":[{"source":"pg-connector-issue-beads","status":"succeeded","count":0},{"source":"pg-connector-github","status":"degraded","count":0,"reason":"rate limited"}],"groups":[]}`+"\n")
		res := r.change("apply", id7)
		if res.code != ExitHydrateNotFound || r.created() != 1 {
			t.Fatalf("a degraded unrelated source blocked the escalation: exit %d, %d created:\n%s", res.code, r.created(), res.stderr)
		}

		// The probe of the anchor reads the tracker: when pg-desk cannot even
		// read its own stored view the answer is transient, never "no anchor".
		r2 := newRig(t, fxOpenAnchor, true)
		r2.hydrate()
		r2.ctlWrite("fail.pr-show", "not_found")
		r2.sqlite("ALTER TABLE entity RENAME TO entity_hidden;")
		res = r2.change("apply", id7)
		if res.code != ExitHydrateTransient {
			t.Fatalf("an unreadable store: exit %d, want %d:\n%s", res.code, ExitHydrateTransient, res.stderr)
		}
		contains(t, "probe line", res.stderr, "the open-anchor probe failed", "class=transient", "unavailable")
		if r2.created() != 0 || len(r2.calls("search")) != 0 {
			t.Fatalf("a failing probe still searched or escalated")
		}
	})

	run("a-failing-change-delivered-twice-counts-once-with-or-without-a-sequence-and-changes-that-differ-count-each", func(t *testing.T) {
		// Every delivery fails (the tracker refuses the anchor create), so the
		// decider's failure counter is the observable: it rises once per
		// DELIVERY, not once per run.
		const key = "failures.anchor.lazy"
		cases := []struct {
			name       string
			deliveries [][]string // the argv after `apply`
			counts     []string   // the failure count after each delivery
			fromItem   []bool     // whether the decider was handed --from-item
			seqRecord  string     // metadata.seq the decider recorded for the last delivery
		}{
			{"a change sequence", [][]string{{id11, "5"}, {id11, "5"}, {id11, "6"}}, []string{"1", "1", "2"}, []bool{true, true, true}, "6"},
			{
				"the id suffix of the old adapter (no sequence at all)",
				[][]string{{oldAdapterID(id11, "head-a", "feedback")}, {oldAdapterID(id11, "head-a", "feedback")}, {oldAdapterID(id11, "head-b", "feedback")}, {oldAdapterID(id11, "head-b", "push")}},
				[]string{"1", "1", "2", "3"},
				[]bool{true, true, true, true},
				strconv.FormatInt(suffixValue(oldAdapterID(id11, "head-b", "push")), 10),
			},
			{"a missing template key (<no value>) falls to the id suffix", [][]string{{oldAdapterID(id11, "h", "k"), "<no value>"}, {oldAdapterID(id11, "h", "k"), "<no value>"}}, []string{"1", "1"}, []bool{true, true}, strconv.FormatInt(suffixValue(oldAdapterID(id11, "h", "k")), 10)},
			{"a sequence in exponent form (10^7)", [][]string{{id11, "1e+07"}, {id11, "1e+07"}}, []string{"1", "1"}, []bool{true, true}, "10000000"},
			{"a bare id counts every run (no retry window, no router retry)", [][]string{{id11}, {id11}}, []string{"1", "2"}, []bool{false, false}, ""},
			{"a sequence of 0 is never passed", [][]string{{id11, "0"}, {id11, "0"}}, []string{"1", "2"}, []bool{false, false}, ""},
		}
		for _, c := range cases {
			r := newRig(t, fxMine, true)
			r.ctlWrite("fail.create", "unavailable")
			for i, d := range c.deliveries {
				res := r.change(append([]string{"apply"}, d...)...)
				if res.code != ExitDecidePartial {
					t.Fatalf("%s, delivery %d: exit %d, want %d:\n%s", c.name, i+1, res.code, ExitDecidePartial, res.stderr)
				}
				if got := r.annotation(id11, key); got != c.counts[i] {
					t.Fatalf("%s: after delivery %d the failure count is %q, want %q", c.name, i+1, got, c.counts[i])
				}
				runs := r.deciderRuns()
				if got := strings.Contains(runs[len(runs)-1], "--from-item -"); got != c.fromItem[i] {
					t.Fatalf("%s, delivery %d: decider argv %q, --from-item = %v, want %v", c.name, i+1, runs[len(runs)-1], got, c.fromItem[i])
				}
			}
			if got := r.annotation(id11, "failure_seq.anchor.lazy"); got != c.seqRecord {
				t.Errorf("%s: the decider recorded seq %q, want %q", c.name, got, c.seqRecord)
			}
		}
	})

	run("the-audit-comment-names-the-delivery-key-from-the-sequence-or-the-old-adapters-id-suffix", func(t *testing.T) {
		old := oldAdapterID(id11, "head0011", "changed")
		for _, c := range []struct {
			name string
			args []string
			want string
		}{
			{"a change sequence", []string{id11, "42"}, "seq: 42\n"},
			{"an exponent-form sequence", []string{id11, "1.234567e+06"}, "seq: 1234567\n"},
			{"the old adapter's item (no sequence, an @<12 hex> id)", []string{old}, fmt.Sprintf("seq: %d\n", suffixValue(old))},
			{"a bare id passes no key", []string{id11}, "seq: none\n"},
		} {
			r := newRig(t, fxMine, true)
			r.change(append([]string{"apply"}, c.args...)...)
			if got := r.ctlRead("calls.log"); !strings.Contains(got, c.want) {
				t.Errorf("%s: no audit comment carries %q:\n%s", c.name, c.want, got)
			}
		}
	})

	run("each-run-is-recorded-and-a-by-hand-plan-run-after-an-apply-run-changes-neither-the-lane-mode-nor-the-flip-time", func(t *testing.T) {
		r := newRigWith(t, fxMine, rigOpts{cutover: true, laneMode: "apply"})
		res := r.change("apply", id11, "3")
		if res.code != ExitDecidePartial {
			t.Fatalf("apply: exit %d:\n%s", res.code, res.stderr)
		}
		// The residual is the actions a read-only plan still lists after the
		// apply, read here the same way.
		after := r.exec(context.Background(), r.binDir+"/pg-decider", "plan", "pr", id11, "--json")
		var plan struct{ Actions []json.RawMessage }
		must(t, json.Unmarshal([]byte(after.stdout), &plan))
		if len(plan.Actions) == 0 {
			t.Fatal("setup: the plan after the apply lists nothing, so the residual would not be told from a missing one")
		}

		first := r.lane()
		if first["pr_lane.state.mode"] != "apply" || first["pr_lane.state.flip_at"] == "" || first["pr_lane.state.last_run_at"] == "" {
			t.Fatalf("lane-level state after an apply run: %v", first)
		}
		var pr struct {
			Mode        string `json:"mode"`
			LastOutcome string `json:"last_outcome"`
			Residual    int    `json:"residual"`
		}
		must(t, json.Unmarshal([]byte(first["pr_lane.pr."+id11]), &pr))
		if pr.Mode != "apply" || pr.Residual != len(plan.Actions) || pr.LastOutcome != "decide.decide_partial" {
			t.Fatalf("per-PR state = %+v (residual want %d): %v", pr, len(plan.Actions), first)
		}
		if first["pr_lane.runs.decide.decide_partial"] != "1" {
			t.Fatalf("the lane-run counter after one failed apply: %v", first)
		}
		for k := range first {
			if strings.HasPrefix(k, "pr_lane.decisions.") && strings.Contains(k, "|planned|") {
				t.Fatalf("the residual plan after the apply was recorded as a run (%s): %v", k, first)
			}
		}

		// A by-hand plan run after the apply run.
		if res := r.change("plan", id11); res.code != ExitOK {
			t.Fatalf("plan: exit %d:\n%s", res.code, res.stderr)
		}
		second := r.lane()
		for _, k := range []string{"pr_lane.state.mode", "pr_lane.state.flip_at", "pr_lane.pr." + id11} {
			if second[k] != first[k] {
				t.Errorf("a by-hand plan run changed %s: %q -> %q", k, first[k], second[k])
			}
		}
		planned := false
		for k := range second {
			planned = planned || (strings.HasPrefix(k, "pr_lane.decisions.") && strings.Contains(k, "|planned|"))
		}
		if !planned {
			t.Errorf("the plan run's decisions were not counted: %v", second)
		}

		recs := r.runRecords()
		if len(recs) != 2 {
			t.Fatalf("%d run records, want 2", len(recs))
		}
		a, p := recs[0], recs[1]
		if a.Mode != "apply" || a.Entity != id11 || a.Seq != 3 || a.SeqSource != KeySourceSequence || a.ExitCode != ExitDecidePartial || a.ErrorClass != ClassDecidePartial ||
			a.Hydrate.Result != hydrateOK || a.Decide == nil || a.Decide.ExitCode != 2 || len(a.Decide.Actions) == 0 || a.Residual == nil || *a.Residual != len(plan.Actions) || a.Lane.Result != laneRecorded {
			t.Errorf("apply record = %+v / %+v", a, a.Decide)
		}
		if p.Mode != "plan" || p.ExitCode != 0 || p.Decide == nil || p.Decide.ExitCode != 0 || len(p.Decide.Rules) == 0 || p.Adoption == nil {
			t.Errorf("plan record = %+v / %+v", p, p.Decide)
		}
		if a.RunID == p.RunID || a.RunID == "" {
			t.Errorf("run ids %q and %q", a.RunID, p.RunID)
		}
	})

	run("a-run-that-stops-at-hydrate-appends-a-record-with-its-class-and-no-decide-fields-and-counts-one-lane-run-in-each-mode", func(t *testing.T) {
		r := newRigWith(t, fxLegacy, rigOpts{cutover: true, legacy: []string{id9}, laneMode: "plan"})
		r.ctlWrite("fail.pr-show", "not_found")
		for i, mode := range []string{"plan", "apply"} {
			res := r.change(mode, id9)
			if res.code != ExitHydrateNotFound {
				t.Fatalf("%s: exit %d:\n%s", mode, res.code, res.stderr)
			}
			if got := r.lane()["pr_lane.runs.hydrate.not_found"]; got != strconv.Itoa(i+1) {
				t.Fatalf("after the %s run the lane-run counter is %q, want %d", mode, got, i+1)
			}
			recs := r.runRecords()
			if len(recs) != i+1 {
				t.Fatalf("%d records after the %s run", len(recs), mode)
			}
			rec := recs[i]
			if rec.Mode != mode || rec.ErrorClass != ClassNotFound || rec.FailingStage != StageHydrate || rec.Decide != nil || rec.Adoption != nil || rec.Residual != nil ||
				rec.Hydrate.Result != hydrateFailed || rec.Lane.Result != laneRecorded {
				t.Fatalf("%s record = %+v", mode, rec)
			}
		}
		raw := r.ctlRead("decider.log")
		if raw != "" {
			t.Errorf("the decider ran: %q", raw)
		}
		if !strings.Contains(r.lane()["pr_lane.marked_inactive"], "1") {
			t.Errorf("the not-found PR was not counted as marked inactive: %v", r.lane())
		}
	})

	if scenarios != minScenarios {
		t.Errorf("the test declares %d scenarios, but minScenarios is %d (and the nix check's minScenarios must equal it)", scenarios, minScenarios)
	}
}

// oldAdapterID is the item id the old source adapter gives a change under a
// retry window: the entity id and `@` and 12 hexadecimal digits, a stable
// digest of the change kind, the entity and its head (the adapter's rows carry
// no sequence, kinds or fields). One change gives one id, a change that differs
// in head or in kind another.
func oldAdapterID(entity, head, kind string) string {
	sum := sha256.Sum256([]byte(kind + "|" + entity + "|" + head))
	return entity + "@" + hex.EncodeToString(sum[:])[:12]
}

// suffixValue is the integer value of an item id's 12 hexadecimal digits.
func suffixValue(id string) int64 {
	_, hexDigits, _ := strings.Cut(id, "@")
	n, err := strconv.ParseInt(hexDigits, 16, 64)
	if err != nil {
		panic(err)
	}
	return n
}
