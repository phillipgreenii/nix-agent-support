package route

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/candidate"
	"github.com/phillipgreenii/pg-ccaudit/internal/classify"
	"github.com/phillipgreenii/pg-ccaudit/internal/gold"
)

// TestEveryClassRoutesSomewhere is criterion 7, asserted exhaustively rather than by
// sampling: an unrouted finding is a finding nobody owns, and it survives every
// review precisely because it looks like work.
func TestEveryClassRoutesSomewhere(t *testing.T) {
	for _, cl := range classify.Classes {
		f := Finding{Kind: KindMistake, Class: cl, Occurrences: 1, MainLoop: 1}
		r, _ := Decide(f, "")
		if r == "" {
			t.Errorf("class %s produced an EMPTY route", cl)
		}
		found := false
		for _, known := range Routes {
			if r == known {
				found = true
			}
		}
		if !found {
			t.Errorf("class %s routed to %q, which is not in the taxonomy", cl, r)
		}
	}
}

func TestRoutingPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		f      Finding
		hint   string
		want   Route
		inAlso string
	}{
		{
			name: "not-a-mistake is closed explicitly, never dropped",
			f:    Finding{Class: classify.ClassNotAMistake, Occurrences: 9, MainLoop: 9},
			want: RouteNotActionable,
		},
		{
			name: "tooling defect is not an instruction problem",
			f:    Finding{Class: classify.ClassToolingDefect, Occurrences: 3, MainLoop: 3},
			want: RouteNotActionable,
		},
		{
			name: "permission friction goes to the approver, not to a rule",
			f:    Finding{Class: classify.ClassPermissionFriction, Occurrences: 3, MainLoop: 3},
			want: RoutePermissionConfig,
		},
		{
			name: "a skipped verification can be enforced mechanically",
			f:    Finding{Class: classify.ClassVerificationMiss, Occurrences: 3, MainLoop: 3},
			want: RouteHook,
		},
		{
			name: "a guidance defect routes BACK to the instruction that caused it",
			f:    Finding{Class: classify.ClassSpecificationMiss, Occurrences: 3, MainLoop: 3},
			hint: "workspace-rule",
			want: RouteWorkspaceRule,
		},
		{
			name:   "a subagent-dominated finding must reach the subagent brief",
			f:      Finding{Class: classify.ClassSpecificationMiss, Occurrences: 10, MainLoop: 2, Subagent: 8},
			want:   RouteSubagentPrompt,
			inAlso: "does not reliably reach",
		},
		{
			name:   "a near-even split is routed once and says so",
			f:      Finding{Class: classify.ClassSpecificationMiss, Occurrences: 10, MainLoop: 5, Subagent: 5},
			want:   RouteGlobalRule,
			inAlso: "the subagent brief needs the same change",
		},
		{
			name: "a denial candidate is permission config whatever its class",
			f:    Finding{Class: classify.ClassSpecificationMiss, Signal: string(candidate.Denial), Occurrences: 2, MainLoop: 2},
			want: RoutePermissionConfig,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, also := Decide(tc.f, tc.hint)
			if got != tc.want {
				t.Errorf("route=%s, want %s", got, tc.want)
			}
			if tc.inAlso != "" && !strings.Contains(also, tc.inAlso) {
				t.Errorf("also note %q does not mention %q", also, tc.inAlso)
			}
		})
	}
}

// A hint may NARROW a route among instruction artifacts. It must never PROMOTE one
// to mechanical enforcement: "add a hook" is a decision about what the machine
// forbids, not a per-finding judgement a classifier gets to make.
func TestAHintCannotPromoteToMechanicalEnforcement(t *testing.T) {
	f := Finding{Class: classify.ClassSpecificationMiss, Occurrences: 3, MainLoop: 3}
	for _, hint := range []string{"hook", "permission-config", "not-actionable", "nonsense"} {
		got, _ := Decide(f, hint)
		if got == RouteHook || got == RoutePermissionConfig || got == RouteNotActionable {
			t.Errorf("hint %q promoted the finding to %s", hint, got)
		}
	}
	got, _ := Decide(f, "skill")
	if got != RouteSkill {
		t.Errorf("hint \"skill\" must narrow the route, got %s", got)
	}
}

func TestPreventabilityOrdersMechanicalAboveInstruction(t *testing.T) {
	// The ORDERING is the load-bearing part of these weights, not the values.
	if !(Preventability(RouteHook) > Preventability(RoutePermissionConfig) &&
		Preventability(RoutePermissionConfig) > Preventability(RouteGlobalRule) &&
		Preventability(RouteGlobalRule) > Preventability(RouteWorkspaceRule) &&
		Preventability(RouteWorkspaceRule) > Preventability(RouteSkill) &&
		Preventability(RouteNotActionable) == 0) {
		t.Error("preventability must order mechanical > always-on instruction > scoped instruction > opt-in, with not-actionable at zero")
	}
}

func TestScoreIsFrequencyTimesCostTimesPreventability(t *testing.T) {
	f := Finding{Occurrences: 10, CostMS: 2000, Route: RouteGlobalRule}
	// 10 * (1 + 2000/1000) * 0.60 = 10 * 3 * 0.6 = 18
	if got := Score(f); got != 18 {
		t.Errorf("Score=%v, want 18 — the formula printed in the report must be the formula used", got)
	}
	// The cost multiplier is floored at 1 so a finding with NO measurable span still
	// ranks on frequency and preventability instead of collapsing to zero. A denied
	// tool call is a single event: there are not two timestamps to subtract.
	noSpan := Finding{Occurrences: 10, CostMS: 0, Route: RouteGlobalRule}
	if got := Score(noSpan); got != 6 {
		t.Errorf("Score with no measurable span=%v, want 6", got)
	}
	// not-actionable must never outrank anything, however frequent or slow.
	huge := Finding{Occurrences: 10000, CostMS: 99999999, Route: RouteNotActionable}
	if Score(huge) != 0 {
		t.Error("a not-actionable finding must score zero")
	}
}

func TestRankIsATotalOrder(t *testing.T) {
	// Two runs over the same data must produce byte-identical reports, or two
	// censuses cannot be diffed — which is the whole point of versioning the queries.
	in := []Finding{
		{Signature: "b", Occurrences: 1, Route: RouteGlobalRule, Kind: KindMistake},
		{Signature: "a", Occurrences: 1, Route: RouteGlobalRule, Kind: KindMistake},
		{Signature: "c", Occurrences: 5, Route: RouteGlobalRule, Kind: KindMistake},
	}
	first := Rank(in)
	second := Rank(in)
	for i := range first {
		if first[i].Signature != second[i].Signature {
			t.Fatalf("Rank is not deterministic at %d: %s vs %s", i, first[i].Signature, second[i].Signature)
		}
	}
	if first[0].Signature != "c" {
		t.Errorf("highest score first, got %s", first[0].Signature)
	}
	if first[1].Signature != "a" || first[2].Signature != "b" {
		t.Errorf("ties must break on signature, got %s then %s", first[1].Signature, first[2].Signature)
	}
}

func mkCand(sig candidate.Signal, seq int64, sidechain bool, span int64) candidate.Candidate {
	return candidate.Candidate{
		Signal: sig, Path: "a.jsonl", Seq: seq, Key: string(sig) + ":a.jsonl#" + itoa(seq),
		SessionID: "S1", IsSidechain: sidechain, SpanMS: span, TS: "2026-08-01T00:00:00.000Z",
		Signature: "shape-" + string(sig), Excerpt: "evidence", Detail: map[string]string{},
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestFromClassificationsGroupsAndSplitsBySidechain(t *testing.T) {
	cls := []classify.Classification{
		{Candidate: mkCand(candidate.Undo, 1, false, 1000), Class: classify.ClassSelfCaught, Confidence: "low", Prevention: "weak"},
		{Candidate: mkCand(candidate.Undo, 2, true, 2000), Class: classify.ClassSelfCaught, Confidence: "high", Prevention: "strong", RouteHint: "skill"},
		{Candidate: mkCand(candidate.Undo, 3, true, 3000), Class: classify.ClassSelfCaught, Confidence: "medium", Prevention: "mid"},
	}
	fs := FromClassifications(cls)
	if len(fs) != 1 {
		t.Fatalf("got %d findings, want 1 — three occurrences of one shape are ONE finding", len(fs))
	}
	f := fs[0]
	if f.Occurrences != 3 || f.MainLoop != 1 || f.Subagent != 2 {
		t.Errorf("occurrences=%d main=%d sub=%d, want 3/1/2", f.Occurrences, f.MainLoop, f.Subagent)
	}
	if f.CostMS != 6000 {
		t.Errorf("cost_ms=%d, want 6000 (measured spans summed)", f.CostMS)
	}
	// 2 of 3 subagent is the dominance threshold, so this must reach the brief.
	if f.Route != RouteSubagentPrompt {
		t.Errorf("route=%s, want %s", f.Route, RouteSubagentPrompt)
	}
	// The finding quotes the classification that was most SURE, not whichever arrived
	// last.
	if f.Prevention != "strong" || f.Confidence != "high" {
		t.Errorf("prevention=%q confidence=%q, want the high-confidence one", f.Prevention, f.Confidence)
	}
	if f.Sessions != 1 || f.WorstSession != 3 {
		t.Errorf("sessions=%d worst=%d, want 1/3 — the runaway discount needs both", f.Sessions, f.WorstSession)
	}
}

func TestSupplementaryCandidatesNeverCarryAFinding(t *testing.T) {
	ack := mkCand(candidate.Ack, 1, false, 0)
	ack.Supplementary = true
	fs := FromClassifications([]classify.Classification{
		{Candidate: ack, Class: classify.ClassSelfCaught},
	})
	if len(fs) != 0 {
		// Counting acknowledgments as findings would double-count every mistake the
		// agent happened to admit and under-count every one it did not — the exact
		// distortion that makes an "acknowledged mistake rate" read as a mistake rate.
		t.Fatalf("got %d findings from a supplementary candidate, want 0", len(fs))
	}
}

func TestNotAMistakeIsEmittedAsAnExplicitClose(t *testing.T) {
	fs := FromClassifications([]classify.Classification{
		{Candidate: mkCand(candidate.Churn, 1, false, 500), Class: classify.ClassNotAMistake},
	})
	if len(fs) != 1 {
		t.Fatalf("got %d findings, want 1 — 'we looked and there is nothing to fix' is a RESULT", len(fs))
	}
	if fs[0].Route != RouteNotActionable {
		t.Errorf("route=%s, want %s", fs[0].Route, RouteNotActionable)
	}
	if Score(Rank(fs)[0]) != 0 {
		t.Error("an explicit close must score zero so it informs without competing for attention")
	}
}

func TestHumanTerminatedCostIsNotCountedAsAgentWaste(t *testing.T) {
	// Measured: 14 user rejections carried 99,396,772 ms of "elapsed" — 27.6 hours of
	// a person reading and deciding — and that put them FIRST in the ranked report,
	// above a 387-occurrence class. None of it is agent waste.
	for _, sig := range []string{
		"The user doesn't want to proceed with this tool use. The tool use was rejected.",
		"[Request interrupted by user for tool use]",
		"Permission for this tool use was denied.",
	} {
		if !isHumanTerminated(sig) {
			t.Errorf("%q must be treated as human-terminated", sig)
		}
	}
	// The classifier verdict is machine-made and arrives promptly, so its elapsed time
	// IS a measurement of agent-facing latency.
	if isHumanTerminated("Permission for this action was denied by the Claude Code auto mode classifier.") {
		t.Error("a classifier denial is machine-decided; its elapsed time is real")
	}
}

func TestCommandFailureRoutingUsesTheSplit(t *testing.T) {
	cases := []struct {
		name string
		f    Finding
		want Route
	}{
		{
			"approver refusal is not an instruction problem",
			Finding{
				Signature: "access to .git directory is blocked — modify git metadata through git commands only",
				MainLoop:  10, Subagent: 31,
			},
			RoutePermissionConfig,
		},
		{
			"subagent-dominated must reach the brief",
			Finding{Signature: "Exit code N Command timed out", MainLoop: 0, Subagent: 25},
			RouteSubagentPrompt,
		},
		{
			"main-loop dominated belongs in the always-on rules",
			Finding{Signature: "File does not exist", MainLoop: 338, Subagent: 49},
			RouteGlobalRule,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := decideCommandFailure(tc.f)
			if got != tc.want {
				t.Errorf("route=%s, want %s", got, tc.want)
			}
		})
	}
}

func TestRenderStatesTheFileChannelUndercountAndTheFormula(t *testing.T) {
	rep := Report{
		Coverage:      map[string]string{"files": "2435", "lines_bad": "0"},
		Classifier:    "baseline",
		PromptVersion: 1,
		Sources: []candidate.Source{
			{Signal: candidate.TypedTurn, Query: "typed-turn-candidates", Version: 1, Rows: 1209},
			{Signal: candidate.HookRejection, Query: "hook-rejections", Version: 1, Rows: 0},
		},
		Empty:       []candidate.Signal{candidate.HookRejection},
		FileChannel: 18,
		Findings: Rank([]Finding{
			{
				Kind: KindMistake, Signature: "s", Class: classify.ClassSelfCaught, Occurrences: 3,
				MainLoop: 1, Subagent: 2, Route: RouteSubagentPrompt, Prevention: "do x",
				FirstSeen: "2026-07-01T00:00:00Z", LastSeen: "2026-08-01T00:00:00Z",
			},
		}),
	}
	var sb strings.Builder
	if err := Render(&sb, rep); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		// The undercount statement is mandatory: a transcript-only correction count
		// presented as "the correction rate" is wrong, and this is where that is said.
		"STRUCTURALLY INVISIBLE",
		"file-channel corrections found: 18",
		// The ranking formula must be printed so any row's position is re-derivable.
		"score = occurrences x (1 + cost_ms/1000) x preventability(route)",
		"MEASURED wall time",
		// An empty detector must be called out, not left to look healthy.
		"EMPTY SIGNALS: hook-rejection",
		// Provenance: query names WITH versions.
		"typed-turn-candidates    v1  rows=1209",
		// criterion 10's per-signature dates.
		"first_seen=2026-07-01T00:00:00Z last_seen=2026-08-01T00:00:00Z",
		// criterion 8's split, on the finding itself.
		"main_loop=1 subagent=2",
		"Routing totals",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q", want)
		}
	}
}

func TestRenderMarksARunawayFinding(t *testing.T) {
	rep := Report{Findings: Rank([]Finding{
		{
			Kind: KindCommandFailure, Signature: "s", Occurrences: 28, Sessions: 7,
			WorstSession: 17, MainLoop: 13, Subagent: 15, Route: RouteGlobalRule,
		},
	})}
	var sb strings.Builder
	if err := Render(&sb, rep); err != nil {
		t.Fatalf("Render: %v", err)
	}
	// 17 of 28 in one session is one agent stuck in a loop, not a systemic problem,
	// and a standing rule proposed for it is wasted.
	if !strings.Contains(sb.String(), "RUNAWAY DISCOUNT") {
		t.Error("a finding concentrated in one session must be marked for the runaway discount")
	}
}

func TestFileChannelCounts(t *testing.T) {
	n, paths := FileChannel(gold.Set{Entries: []gold.Entry{
		{ID: "file:/b/FEEDBACK.md", Source: gold.SourceFileChannel},
		{ID: "file:/a/feedback_x.md", Source: gold.SourceFileChannel},
		{ID: "typed-turn:a.jsonl#1", Source: gold.SourceHandLabelled, Class: "user-correction"},
	}})
	if n != 2 {
		t.Errorf("file channel count=%d, want 2", n)
	}
	if paths[0] != "/a/feedback_x.md" || paths[1] != "/b/FEEDBACK.md" {
		t.Errorf("paths must be sorted and un-prefixed, got %v", paths)
	}
}
