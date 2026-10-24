package prchange

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/config"
)

func notFoundScript() *script {
	return (&script{}).on("pg-desk pr refresh", exit(3, "class=not_found\n", "pr acme/api#7 not found"))
}

func searchAnswer(rows string, hits string) Result {
	return ok(fmt.Sprintf(`{"sources":[%s],"groups":[%s]}`, rows, hits))
}

func beadsRow(status string, count int) string {
	return fmt.Sprintf(`{"source":%q,"status":%q,"count":%d}`, BeadsSource, status, count)
}

func hitGroup(source string, hits ...string) string {
	return fmt.Sprintf(`{"source":%q,"results":[%s]}`, source, strings.Join(hits, ","))
}

func hit(id, title, source string) string {
	return fmt.Sprintf(`{"type":"issue","id":%q,"title":%q,"source":%q}`, id, title, source)
}

var escTitle = EscalationTitle(testID)

func TestNotFoundWithNoAnchorMarksInactiveInBothModes(t *testing.T) {
	for _, mode := range []string{ModeApply, ModePlan} {
		t.Run(mode, func(t *testing.T) {
			s := notFoundScript().
				on("pg-desk pr show", ok(hydratedView(2, false, ""))).
				on("pg-desk pr mark-inactive acme/api#7", ok("marked pr acme/api#7 inactive\n"))
			o, _, errb := opts(t, mode, s)
			if got := Run(context.Background(), o); got != ExitHydrateNotFound {
				t.Fatalf("exit %d: %s", got, errb)
			}
			if !s.called("pg-desk pr mark-inactive acme/api#7") {
				t.Fatalf("not marked inactive: %v", s.calls)
			}
			for _, c := range s.calls {
				if strings.HasPrefix(c, "pg-connector") || strings.HasPrefix(c, "pg-decider") {
					t.Fatalf("a no-anchor not-found made a tracker call or ran the decider: %v", s.calls)
				}
			}
			if !strings.Contains(errb.String(), "class=not_found") || !strings.Contains(errb.String(), "no open anchor") {
				t.Fatalf("line %q", errb)
			}
		})
	}
}

func TestNotFoundOfAnAbsentEntityMarksInactive(t *testing.T) {
	s := notFoundScript().
		on("pg-desk pr show", exit(1, "", "pr show: acme/api#7 does not resolve to a stored entity")).
		on("pg-desk pr mark-inactive", ok("nothing to do\n"))
	o, _, errb := opts(t, ModeApply, s)
	if got := Run(context.Background(), o); got != ExitHydrateNotFound {
		t.Fatalf("exit %d: %s", got, errb)
	}
}

func TestNotFoundWithAClosedAnchorIsTheNoAnchorCase(t *testing.T) {
	s := notFoundScript().
		on("pg-desk pr show", ok(hydratedView(2, false, "open"))).
		on("pg-connector issue show bd-a --fresh", ok(`{"result":{"id":"bd-a","state":"closed"}}`)).
		on("pg-desk pr mark-inactive", ok("marked\n"))
	o, _, errb := opts(t, ModeApply, s)
	if got := Run(context.Background(), o); got != ExitHydrateNotFound {
		t.Fatalf("exit %d: %s", got, errb)
	}
	if !s.called("pg-desk pr mark-inactive") || s.called("pg-connector issue create") || s.called("pg-connector search") {
		t.Fatalf("calls %v", s.calls)
	}
}

func TestNotFoundWithAnOpenAnchorInPlanModeWritesNothing(t *testing.T) {
	s := notFoundScript().
		on("pg-desk pr show", ok(hydratedView(2, false, "open"))).
		on("pg-connector issue show bd-a --fresh", ok(`{"result":{"id":"bd-a","state":"open"}}`))
	o, _, errb := opts(t, ModePlan, s)
	if got := Run(context.Background(), o); got != ExitHydrateNotFound {
		t.Fatalf("exit %d: %s", got, errb)
	}
	for _, c := range s.calls {
		if strings.HasPrefix(c, "pg-connector search") || strings.HasPrefix(c, "pg-connector issue create") ||
			strings.HasPrefix(c, "pg-desk pr mark-inactive") || strings.HasPrefix(c, "pg-decider") {
			t.Fatalf("plan mode wrote or decided: %v", s.calls)
		}
	}
	if !strings.Contains(errb.String(), "plan mode records this and writes nothing") || !strings.Contains(errb.String(), ": run pg-pr-change apply "+testID) {
		t.Fatalf("line %q", errb)
	}
}

func openAnchorScript() *script {
	return notFoundScript().
		on("pg-desk pr show", ok(hydratedView(2, false, "open"))).
		on("pg-connector issue show bd-a --fresh", ok(`{"result":{"id":"bd-a","state":"in_progress"}}`))
}

func TestNotFoundWithAnOpenAnchorEscalatesOnce(t *testing.T) {
	s := openAnchorScript().
		on("pg-connector search "+escTitle, searchAnswer(beadsRow("succeeded", 0), "")).
		on("pg-connector issue create", ok(`{"result":{"id":"bd-esc"}}`))
	o, _, errb := opts(t, ModeApply, s)
	o.Config = &config.Config{AgentTrackerBackend: "beads"}
	if got := Run(context.Background(), o); got != ExitHydrateNotFound {
		t.Fatalf("exit %d: %s", got, errb)
	}
	var create string
	for _, c := range s.calls {
		if strings.HasPrefix(c, "pg-connector issue create") {
			create = c
		}
	}
	for _, want := range []string{"--title " + escTitle, "--labels human", "--parent bd-a", "--backend beads", "class: not_found", "pg-desk pr show " + testID + " --json"} {
		if !strings.Contains(create, want) {
			t.Fatalf("create %q lacks %q", create, want)
		}
	}
	if !strings.Contains(escTitle, testID) {
		t.Fatalf("title %q does not name the PR", escTitle)
	}
	if !strings.Contains(errb.String(), "escalated as bd-esc") {
		t.Fatalf("line %q", errb)
	}
	if s.called("pg-desk pr mark-inactive") || s.called("pg-decider") {
		t.Fatalf("an open anchor must not be marked inactive nor decided: %v", s.calls)
	}
}

func TestRedeliveredNotFoundFindsTheEarlierEscalationOpenOrClosed(t *testing.T) {
	s := openAnchorScript().
		on("pg-connector search", searchAnswer(beadsRow("succeeded", 1), hitGroup(BeadsSource, hit("bd-old", escTitle, BeadsSource))))
	o, _, errb := opts(t, ModeApply, s)
	if got := Run(context.Background(), o); got != ExitHydrateNotFound {
		t.Fatalf("exit %d: %s", got, errb)
	}
	if s.called("pg-connector issue create") {
		t.Fatalf("a second escalation was raised: %v", s.calls)
	}
	if !strings.Contains(errb.String(), "already escalated as bd-old") {
		t.Fatalf("line %q", errb)
	}
}

func TestSearchJudgment(t *testing.T) {
	other := `{"source":"pg-connector-github","status":"degraded","count":0,"reason":"rate limited"}`
	cases := []struct {
		name      string
		search    Result
		wantCode  int
		wantMake  bool
		wantRetry bool
	}{
		{"a similar title is not the same", searchAnswer(beadsRow("succeeded", 1), hitGroup(BeadsSource, hit("bd-x", escTitle+" later", BeadsSource))), ExitHydrateNotFound, true, false},
		{"a hit of another source with the title is ignored", searchAnswer(beadsRow("succeeded", 1)+","+other, hitGroup("pg-connector-github", hit("gh-1", escTitle, "pg-connector-github"))), ExitHydrateNotFound, true, false},
		{"a degraded non-beads row is ignored", searchAnswer(beadsRow("succeeded", 0)+","+other, ""), ExitHydrateNotFound, true, false},
		{"a degraded beads row is transient", searchAnswer(beadsRow("degraded", 0), ""), ExitHydrateTransient, false, true},
		{"a disabled beads row is transient", searchAnswer(beadsRow("disabled", 0), ""), ExitHydrateTransient, false, true},
		{"a row at the cap is transient", searchAnswer(beadsRow("succeeded", SearchCap), ""), ExitHydrateTransient, false, true},
		{"a row just under the cap is trusted", searchAnswer(beadsRow("succeeded", SearchCap-1), ""), ExitHydrateNotFound, true, false},
		{"no beads row is transient", searchAnswer(other, ""), ExitHydrateTransient, false, true},
		{"no rows at all is transient", searchAnswer("", ""), ExitHydrateTransient, false, true},
		{"unparseable is transient", ok("boom"), ExitHydrateTransient, false, true},
		{"a failed search with no output is transient", exit(1, "", "no registry"), ExitHydrateTransient, false, true},
		{"killed is transient", Result{Code: -1, Signaled: true}, ExitHydrateTransient, false, true},
		{"exit 3 with a degraded beads row is transient", func() Result {
			r := searchAnswer(beadsRow("degraded", 0), "")
			r.Code = 3
			return r
		}(), ExitHydrateTransient, false, true},
		{"exit 2 with a healthy beads row is trusted", func() Result {
			r := searchAnswer(beadsRow("succeeded", 0)+","+other, "")
			r.Code = 2
			return r
		}(), ExitHydrateNotFound, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := openAnchorScript().
				on("pg-connector search", c.search).
				on("pg-connector issue create", ok(`{"result":{"id":"bd-esc"}}`))
			o, _, errb := opts(t, ModeApply, s)
			if got := Run(context.Background(), o); got != c.wantCode {
				t.Fatalf("exit %d, want %d: %s", got, c.wantCode, errb)
			}
			if s.called("pg-connector issue create") != c.wantMake {
				t.Fatalf("created = %v, want %v: %v", !c.wantMake, c.wantMake, s.calls)
			}
			if hasRetryWord(errb.String(), "unavailable") != c.wantRetry {
				t.Fatalf("line %q: retry = %v, want %v", errb, !c.wantRetry, c.wantRetry)
			}
		})
	}
}

func TestOpenAnchorProbeFailuresAreTransient(t *testing.T) {
	cases := []struct {
		name string
		s    *script
	}{
		{"store read fails", notFoundScript().on("pg-desk pr show", exit(1, "", "store locked"))},
		{"view unparseable", notFoundScript().on("pg-desk pr show", ok("nope"))},
		{"anchor read fails", notFoundScript().
			on("pg-desk pr show", ok(hydratedView(2, false, "open"))).
			on("pg-connector issue show", exit(1, `{"error":{"code":"unavailable","message":"tracker down"}}`, ""))},
		{"anchor read has no state", notFoundScript().
			on("pg-desk pr show", ok(hydratedView(2, false, "open"))).
			on("pg-connector issue show", ok(`{"result":{"id":"bd-a"}}`))},
		{"mark inactive fails", notFoundScript().
			on("pg-desk pr show", ok(hydratedView(2, false, ""))).
			on("pg-desk pr mark-inactive", exit(1, "", "database is locked"))},
		{"create fails", openAnchorScript().
			on("pg-connector search", searchAnswer(beadsRow("succeeded", 0), "")).
			on("pg-connector issue create", exit(1, `{"error":{"code":"backend_error","message":"bd failed"}}`, ""))},
		{"create returns no id", openAnchorScript().
			on("pg-connector search", searchAnswer(beadsRow("succeeded", 0), "")).
			on("pg-connector issue create", ok(`{"result":{}}`))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, _, errb := opts(t, ModeApply, c.s)
			if got := Run(context.Background(), o); got != ExitHydrateTransient {
				t.Fatalf("exit %d, want %d: %s", got, ExitHydrateTransient, errb)
			}
			if !hasRetryWord(errb.String(), "unavailable") {
				t.Fatalf("line %q is not retryable", errb)
			}
		})
	}
}

func TestBeadsDirReachesEveryTrackerCall(t *testing.T) {
	var seen []string
	s := openAnchorScript().
		on("pg-connector search", searchAnswer(beadsRow("succeeded", 0), "")).
		on("pg-connector issue create", ok(`{"result":{"id":"bd-esc"}}`))
	o, _, _ := opts(t, ModeApply, s)
	o.Config = &config.Config{BeadsDir: "/beads/dir"}
	inner := o.Runner
	o.Runner = func(ctx context.Context, c Call) Result {
		if c.Name == "pg-connector" {
			seen = append(seen, strings.Join(c.Env, ","))
		}
		return inner(ctx, c)
	}
	Run(context.Background(), o)
	if len(seen) != 3 {
		t.Fatalf("saw %d connector calls: %v", len(seen), seen)
	}
	for _, e := range seen {
		if e != "PG_CONNECTOR_ISSUE_BEADS_DIR=/beads/dir" {
			t.Fatalf("env = %q", e)
		}
	}
}
