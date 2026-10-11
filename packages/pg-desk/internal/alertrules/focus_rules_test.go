package alertrules

// Guards the daily-focus alert rules of grafana/alerting/alerts.yaml (Phase 15
// metrics families: internal/metrics/focus.go and the change-flow families).
//
// No PromQL engine is a dependency of this module, so each rule's semantics are
// modelled here (aggregate the matching series of the instant query A, reduce
// it with `last`, apply the `gt` threshold, then hold the condition for the
// `for:` duration over a minute-by-minute series) and the YAML is pinned to the
// exact expression, `for:` and threshold the model implements -- the approach
// of sweep_bound_test.go and sync_stale_test.go.

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// focusSample is one series of an instant vector: its labels and value.
type focusSample struct {
	labels map[string]string
	v      float64
}

// focusRule is one rule's contract: the uid, the pinned expression and `for:`,
// the threshold, and agg, which models the expression over one instant vector
// (ok is false when the result is empty, i.e. NoData).
type focusRule struct {
	uid       string
	expr      string
	forText   string
	forDur    time.Duration
	threshold float64
	agg       func(in []focusSample) (v float64, ok bool)
}

func matchSeries(in []focusSample, keep func(map[string]string) bool) []focusSample {
	var out []focusSample
	for _, s := range in {
		if keep(s.labels) {
			out = append(out, s)
		}
	}
	return out
}

func maxOf(in []focusSample) (float64, bool) {
	if len(in) == 0 {
		return 0, false
	}
	m := in[0].v
	for _, s := range in[1:] {
		if s.v > m {
			m = s.v
		}
	}
	return m, true
}

func sumOf(in []focusSample) (float64, bool) {
	if len(in) == 0 {
		return 0, false
	}
	var t float64
	for _, s := range in {
		t += s.v
	}
	return t, true
}

func labelEq(k, want string) func(map[string]string) bool {
	return func(l map[string]string) bool { return l[k] == want }
}

// focusRules lists the eight rules. Every threshold is `gt`.
var focusRules = []focusRule{
	{
		uid:       "pg-desk-focus-pending-beads-age",
		expr:      `max(pg_desk_focus_oldest_pending_bead_age_seconds{reason="unminted"})`,
		forText:   "10m",
		forDur:    10 * time.Minute,
		threshold: 1800,
		agg: func(in []focusSample) (float64, bool) {
			return maxOf(matchSeries(in, labelEq("reason", "unminted")))
		},
	},
	{
		uid:       "pg-desk-focus-divergence-selected-held",
		expr:      `max(pg_desk_focus_divergence{kind="selected_bead_held"})`,
		forText:   "30m",
		forDur:    30 * time.Minute,
		threshold: 0,
		agg: func(in []focusSample) (float64, bool) {
			return maxOf(matchSeries(in, labelEq("kind", "selected_bead_held")))
		},
	},
	{
		uid:       "pg-desk-focus-divergence-removed-claimable",
		expr:      `max(pg_desk_focus_divergence{kind="removed_bead_open_unclaimed"})`,
		forText:   "1h",
		forDur:    time.Hour,
		threshold: 0,
		agg: func(in []focusSample) (float64, bool) {
			return maxOf(matchSeries(in, labelEq("kind", "removed_bead_open_unclaimed")))
		},
	},
	{
		uid:       "pg-desk-focus-annotation-divergence",
		expr:      `sum(pg_desk_focus_divergence{kind=~"row_without_annotation|annotation_without_row"})`,
		forText:   "15m",
		forDur:    15 * time.Minute,
		threshold: 0,
		agg: func(in []focusSample) (float64, bool) {
			// =~ is fully anchored in PromQL.
			re := regexp.MustCompile(`^(?:row_without_annotation|annotation_without_row)$`)
			return sumOf(matchSeries(in, func(l map[string]string) bool { return re.MatchString(l["kind"]) }))
		},
	},
	{
		uid:       "pg-desk-focus-terminal-source-bead-open",
		expr:      `max(pg_desk_focus_divergence{kind="terminal_source_bead_open"})`,
		forText:   "1h",
		forDur:    time.Hour,
		threshold: 0,
		agg: func(in []focusSample) (float64, bool) {
			return maxOf(matchSeries(in, labelEq("kind", "terminal_source_bead_open")))
		},
	},
	{
		uid:       "pg-desk-watch-listing-degraded",
		expr:      `sum(pg_desk_watch_listing_status{status!="ok"})`,
		forText:   "30m",
		forDur:    30 * time.Minute,
		threshold: 0,
		agg: func(in []focusSample) (float64, bool) {
			return sumOf(matchSeries(in, func(l map[string]string) bool { return l["status"] != "ok" }))
		},
	},
	{
		uid:       "pg-desk-focus-rank-degraded",
		expr:      `sum(pg_desk_focus_rank_degraded_inputs)`,
		forText:   "30m",
		forDur:    30 * time.Minute,
		threshold: 0,
		agg:       sumOf,
	},
	{
		uid: "pg-desk-focus-consumer-lag",
		// The threshold is PROVISIONAL: the operator checkpoint sets the final
		// budget from the measured decider cost (serve.md says so too).
		expr:      `max(pg_desk_consumer_lag{type="issue"})`,
		forText:   "30m",
		forDur:    30 * time.Minute,
		threshold: 500,
		agg: func(in []focusSample) (float64, bool) {
			return maxOf(matchSeries(in, labelEq("type", "issue")))
		},
	},
}

// condition models queries A, B and C: aggregate, reduce(last) and `gt`. NoData
// is never true (noDataState: OK maps it to Normal).
func (r focusRule) condition(in []focusSample) bool {
	v, ok := r.agg(in)
	return ok && v > r.threshold
}

// firesAt models Grafana's pending -> firing hold at a 1m evaluation interval:
// the rule fires once the condition has been continuously true for the `for:`
// duration, so the first true sample at minute t0 fires at t0 + for. Any false
// or NoData sample resets the hold. A nil sample is a NoData evaluation.
func (r focusRule) firesAt(samples [][]focusSample) (firstFiring int, fired bool) {
	runStart := -1
	for i, s := range samples {
		if !r.condition(s) {
			runStart = -1
			continue
		}
		if runStart < 0 {
			runStart = i
		}
		if time.Duration(i-runStart)*time.Minute >= r.forDur {
			return i, true
		}
	}
	return 0, false
}

// repeat returns n identical minute samples.
func repeat(s []focusSample, n int) [][]focusSample {
	out := make([][]focusSample, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// hot returns a sample set that makes the rule's condition true and cold one
// that leaves it false, for the rule's own selector.
func (r focusRule) hot() []focusSample {
	switch r.uid {
	case "pg-desk-focus-pending-beads-age":
		return []focusSample{{map[string]string{"reason": "unminted"}, 1801}}
	case "pg-desk-focus-divergence-selected-held":
		return []focusSample{{map[string]string{"kind": "selected_bead_held"}, 1}}
	case "pg-desk-focus-divergence-removed-claimable":
		return []focusSample{{map[string]string{"kind": "removed_bead_open_unclaimed"}, 1}}
	case "pg-desk-focus-annotation-divergence":
		return []focusSample{{map[string]string{"kind": "annotation_without_row"}, 1}}
	case "pg-desk-focus-terminal-source-bead-open":
		return []focusSample{{map[string]string{"kind": "terminal_source_bead_open"}, 1}}
	case "pg-desk-watch-listing-degraded":
		return []focusSample{{map[string]string{"type": "pr", "status": "degraded"}, 1}}
	case "pg-desk-focus-rank-degraded":
		return []focusSample{{map[string]string{"input": "age_fallback"}, 1}}
	case "pg-desk-focus-consumer-lag":
		return []focusSample{{map[string]string{"type": "issue", "consumer": "focus"}, 501}}
	}
	panic("unknown rule " + r.uid)
}

func (r focusRule) cold() []focusSample {
	switch r.uid {
	case "pg-desk-focus-pending-beads-age":
		// At the threshold exactly (gt, not gte), and a different reason far over it.
		return []focusSample{
			{map[string]string{"reason": "unminted"}, 1800},
			{map[string]string{"reason": "hidden"}, 999999},
		}
	case "pg-desk-focus-divergence-selected-held":
		return []focusSample{{map[string]string{"kind": "selected_bead_held"}, 0}, {map[string]string{"kind": "stale_period"}, 3}}
	case "pg-desk-focus-divergence-removed-claimable":
		// removed_bead_claimed is a different, legitimate kind.
		return []focusSample{{map[string]string{"kind": "removed_bead_open_unclaimed"}, 0}, {map[string]string{"kind": "removed_bead_claimed"}, 2}}
	case "pg-desk-focus-annotation-divergence":
		// Zero-filled kinds read 0; other kinds do not count.
		return []focusSample{
			{map[string]string{"kind": "row_without_annotation"}, 0},
			{map[string]string{"kind": "annotation_without_row"}, 0},
			{map[string]string{"kind": "stale_period"}, 4},
		}
	case "pg-desk-focus-terminal-source-bead-open":
		return []focusSample{{map[string]string{"kind": "terminal_source_bead_open"}, 0}, {map[string]string{"kind": "selected_bead_held"}, 5}}
	case "pg-desk-watch-listing-degraded":
		return []focusSample{{map[string]string{"type": "pr", "status": "ok"}, 3}, {map[string]string{"type": "pr", "status": "degraded"}, 0}}
	case "pg-desk-focus-rank-degraded":
		return []focusSample{{map[string]string{"input": "age_fallback"}, 0}, {map[string]string{"input": "unmapped_priority"}, 0}}
	case "pg-desk-focus-consumer-lag":
		// At the provisional threshold exactly; a pr-type consumer far over it.
		return []focusSample{
			{map[string]string{"type": "issue", "consumer": "focus"}, 500},
			{map[string]string{"type": "pr", "consumer": "other"}, 99999},
		}
	}
	panic("unknown rule " + r.uid)
}

// focusRuleBlock returns the text of one rule from its uid line up to the next
// rule.
func focusRuleBlock(t *testing.T, uid string) string {
	t.Helper()
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "- uid: "+uid+"\n")
	if i < 0 {
		t.Fatalf("rule %s not found", uid)
	}
	rest := src[i+1:]
	if j := strings.Index(rest, "- uid:"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// TestFocusRulesPinTheYAML pins, per rule, the exact expression, `for:`,
// threshold and the fixed rule shape (severity warning, noDataState OK,
// execErrState Error, instant query A on datasource prometheus, B reduce last,
// C threshold gt).
func TestFocusRulesPinTheYAML(t *testing.T) {
	for _, r := range focusRules {
		t.Run(r.uid, func(t *testing.T) {
			blk := focusRuleBlock(t, r.uid)
			m := regexp.MustCompile(`(?m)^\s*expr: (.+)$`).FindStringSubmatch(blk)
			if m == nil {
				t.Fatal("no expr in rule")
			}
			if m[1] != r.expr {
				t.Errorf("rule expr drifted from the modelled one:\n got %q\nwant %q", m[1], r.expr)
			}
			if got := regexp.MustCompile(`(?m)^\s*for: (.+)$`).FindStringSubmatch(blk); got == nil || got[1] != r.forText {
				t.Errorf("for: = %v, want %q", got, r.forText)
			}
			if got := regexp.MustCompile(`(?m)^\s*params: \[(.+)\]$`).FindStringSubmatch(blk); got == nil || got[1] != strconv.FormatFloat(r.threshold, 'f', -1, 64) {
				t.Errorf("threshold params = %v, want [%v]", got, r.threshold)
			}
			for _, need := range []string{
				"condition: C", "noDataState: OK", "execErrState: Error", "severity: warning",
				"datasourceUid: prometheus", "instant: true", "reducer: last", "type: threshold", "type: gt",
			} {
				if !strings.Contains(blk, need) {
					t.Errorf("rule lost %q", need)
				}
			}
			if strings.Count(blk, "expr:") != 1 {
				t.Errorf("rule has %d expr lines, want exactly one", strings.Count(blk, "expr:"))
			}
			if !strings.Contains(r.expr, "{") && r.uid != "pg-desk-focus-rank-degraded" {
				t.Errorf("model error: %s has no label selector", r.uid)
			}
		})
	}
}

// TestFocusRuleFiresOnlyAfterForHolds: a series above the threshold for less
// than `for:` does not fire; for `for:` (the first true sample plus the
// duration) it fires; a dip resets the hold.
func TestFocusRuleFiresOnlyAfterForHolds(t *testing.T) {
	for _, r := range focusRules {
		t.Run(r.uid, func(t *testing.T) {
			mins := int(r.forDur / time.Minute)
			// Samples at minutes 0..mins-1: mins-1 minutes of hold, short of for:.
			if _, fired := r.firesAt(repeat(r.hot(), mins)); fired {
				t.Errorf("fired after only %d true minutes (< for %s)", mins, r.forText)
			}
			// Minutes 0..mins: the hold spans exactly for:, so it fires at minute mins.
			at, fired := r.firesAt(repeat(r.hot(), mins+1))
			if !fired || at != mins {
				t.Errorf("firesAt = (%d, %v), want (%d, true)", at, fired, mins)
			}
			// A dip to the cold set one minute before for: elapses resets the hold.
			s := append(repeat(r.hot(), mins-1), r.cold())
			s = append(s, repeat(r.hot(), mins)...)
			if _, fired := r.firesAt(s); fired {
				t.Errorf("fired although a cold sample reset the hold")
			}
		})
	}
}

// TestFocusRuleNeverFiresOnAQuietOrEmptyState: a legitimately empty state (no
// draft, no plan, no series at all: NoData under noDataState: OK) and a state
// that sits at or below the threshold, or on a selector the rule does not
// match, never fires however long it lasts.
func TestFocusRuleNeverFiresOnAQuietOrEmptyState(t *testing.T) {
	for _, r := range focusRules {
		t.Run(r.uid, func(t *testing.T) {
			long := int(r.forDur/time.Minute) * 3
			if _, fired := r.firesAt(repeat(nil, long)); fired {
				t.Error("fired on NoData (no series)")
			}
			if _, fired := r.firesAt(repeat(r.cold(), long)); fired {
				t.Error("fired on a quiet state (zero, at-threshold or foreign-selector series)")
			}
		})
	}
}

// TestFocusRuleNoDataResetsTheHold: a NoData evaluation in the middle of a hold
// resets it (noDataState: OK is Normal, not "keep last state").
func TestFocusRuleNoDataResetsTheHold(t *testing.T) {
	for _, r := range focusRules {
		t.Run(r.uid, func(t *testing.T) {
			mins := int(r.forDur / time.Minute)
			s := append(repeat(r.hot(), mins-1), nil)
			s = append(s, repeat(r.hot(), mins)...)
			if _, fired := r.firesAt(s); fired {
				t.Error("fired although a NoData sample reset the hold")
			}
		})
	}
}

// TestUnmintedAgeHoldsTenMinutesBeforeFiring is the packet's headline case: an
// unminted pending-bead age over 1800 s fires only after 10 minutes of holding,
// and a plan with only legitimate permanent pending reasons never fires.
func TestUnmintedAgeHoldsTenMinutesBeforeFiring(t *testing.T) {
	r := focusRules[0]
	if r.uid != "pg-desk-focus-pending-beads-age" {
		t.Fatalf("focusRules[0] = %s", r.uid)
	}
	old := []focusSample{{map[string]string{"reason": "unminted"}, 2400}}
	if at, fired := r.firesAt(repeat(old, 30)); !fired || at != 10 {
		t.Errorf("firesAt = (%d, %v), want (10, true)", at, fired)
	}
	permanent := []focusSample{
		{map[string]string{"reason": "hidden"}, 90000},
		{map[string]string{"reason": "suppressed"}, 90000},
		{map[string]string{"reason": "no_bead_by_design"}, 90000},
		{map[string]string{"reason": "unwatched"}, 90000},
		{map[string]string{"reason": "source_terminal"}, 90000},
	}
	if _, fired := r.firesAt(repeat(permanent, 60)); fired {
		t.Error("fired on a legitimate permanent pending reason")
	}
}

// TestAnnotationDivergenceSumsBothKinds: either kind alone, or a sum of both,
// fires; neither of the other kinds does.
func TestAnnotationDivergenceSumsBothKinds(t *testing.T) {
	var r focusRule
	for _, c := range focusRules {
		if c.uid == "pg-desk-focus-annotation-divergence" {
			r = c
		}
	}
	for name, kind := range map[string]string{"row": "row_without_annotation", "annotation": "annotation_without_row"} {
		if !r.condition([]focusSample{{map[string]string{"kind": kind}, 1}}) {
			t.Errorf("%s alone did not satisfy the condition", name)
		}
	}
	if r.condition([]focusSample{{map[string]string{"kind": "row_without_annotation_x"}, 1}}) {
		t.Error("an unanchored kind match satisfied the condition")
	}
}

// TestFocusRulesAreNamedInServeDoc keeps docs/behavior/pg-desk/serve.md naming
// each uid with its expression and `for:`. The doc lives outside this module,
// so a sandboxed checkout without it skips rather than fails (the same hole the
// beadstate doc test has, tracked separately).
func TestFocusRulesAreNamedInServeDoc(t *testing.T) {
	b, err := os.ReadFile("../../../../docs/behavior/pg-desk/serve.md")
	if err != nil {
		t.Skipf("serve.md not visible from this checkout: %v", err)
	}
	doc := string(b)
	for _, r := range focusRules {
		var row string
		for _, line := range strings.Split(doc, "\n") {
			if strings.Contains(line, "`"+r.uid+"`") && strings.HasPrefix(strings.TrimSpace(line), "|") {
				row = line
			}
		}
		if row == "" {
			t.Errorf("serve.md has no table row for %s", r.uid)
			continue
		}
		// A pipe inside a markdown table cell is written `\|`.
		row = strings.ReplaceAll(row, `\|`, `|`)
		if !strings.Contains(row, "`"+r.expr+"`") {
			t.Errorf("serve.md row for %s does not carry the expression %q: %s", r.uid, r.expr, row)
		}
		if !strings.Contains(row, "`> "+strconv.FormatFloat(r.threshold, 'f', -1, 64)+"`") {
			t.Errorf("serve.md row for %s does not carry the threshold %v: %s", r.uid, r.threshold, row)
		}
		if !strings.Contains(row, "`"+r.forText+"`") {
			t.Errorf("serve.md row for %s does not carry for %q: %s", r.uid, r.forText, row)
		}
	}
	if !strings.Contains(doc, "provisional") {
		t.Error("serve.md does not state that the lag threshold is provisional")
	}
}
