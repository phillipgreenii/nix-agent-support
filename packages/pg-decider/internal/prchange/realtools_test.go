package prchange

import (
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
const minScenarios = 16

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
	t.Run("a-502-is-transient-with-the-routers-retry-wording-and-never-escalated-as-not-found", func(t *testing.T) {
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

	t.Run("a-null-node-is-not-found-and-apply-raises-exactly-one-escalation", func(t *testing.T) {
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

	t.Run("not-found-in-plan-mode-records-only-with-no-tracker-write-and-no-escalation", func(t *testing.T) {
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
		t.Run("not-found-with-no-open-anchor-is-marked-inactive-in-"+mode+"-mode-and-drops-out-of-the-unhydrated-count", func(t *testing.T) {
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

	t.Run("a-degraded-read-exits-hydrate-degraded-and-runs-no-decide", func(t *testing.T) {
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

	t.Run("a-refresh-that-reports-no-change-still-runs-the-decider", func(t *testing.T) {
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

	t.Run("both-id-forms-take-the-same-lock-and-a-wait-that-times-out-exits-transient", func(t *testing.T) {
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

	t.Run("a-killed-lock-holder-releases-the-lock", func(t *testing.T) {
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

	t.Run("a-refresh-exit-1-is-hydrate_error-is-not-retryable-and-runs-no-decide", func(t *testing.T) {
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

	t.Run("the-failure-lines-of-every-non-retryable-class-carry-none-of-the-routers-retry-words", func(t *testing.T) {
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

	t.Run("a-partial-decide-is-retryable-and-its-lines-are-re-wrapped-with-the-next-command", func(t *testing.T) {
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

	t.Run("plan-mode-prints-the-plan-and-writes-nothing", func(t *testing.T) {
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

	t.Run("the-command-never-exits-1-or-9-and-refuses-a-bad-id-with-a-usage-code", func(t *testing.T) {
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

	t.Run("a-search-at-the-100-hit-cap-or-a-degraded-beads-row-exits-transient-and-raises-no-escalation", func(t *testing.T) {
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

	t.Run("a-degraded-non-beads-row-is-ignored-and-a-failing-open-anchor-probe-is-transient", func(t *testing.T) {
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
}
