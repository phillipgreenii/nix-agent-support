package backfill

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// DefaultMaxPlanAge is the bound on the age of the oldest plan record: a plan
// older than this MUST be re-planned before the flip (the runbook states it).
const DefaultMaxPlanAge = 48 * time.Hour

// ExitSummaryFailed is the exit code of `summary` when a check does not hold
// (0 when every check holds). The driver's own codes are 0, 10, 11, 12 and 64.
const ExitSummaryFailed = 20

// The operations of the per-kind counts. `adopt` is an adoption rule's action
// (an item re-linked to its PR), whatever the action's operation word is; the
// others are the decider's operations.
var summaryOps = []string{"adopt", "create", "reopen", "close", "update", "annotate"}

var adoptionRules = map[string]bool{"adoption": true, "adoption.node-id": true}

// Gap is an enumerated PR the summary cannot vouch for, and why.
type Gap struct {
	PR  string
	Why string
}

// Summary is what `summary --mode plan` reads out of the run record.
type Summary struct {
	Mode       string
	Enumerated int
	// WithRecord is the number of PRs whose latest record of the mode succeeded.
	WithRecord int
	Missing    []Gap
	// Counts is, per kind, the number of planned actions of each operation.
	Counts      map[string]map[string]int
	Unadoptable []Gap
	SecondAnch  []string
	Oldest      time.Time
	OldestPR    string
	Age         time.Duration
	MaxAge      time.Duration
	Stale       bool
	// Residuals is, in apply mode, each PR's persisted residual (-1: unknown).
	Residuals map[string]int
	// Problems are the failed checks, in words; empty means every check holds.
	Problems []string
}

// OK reports whether every check holds.
func (s *Summary) OK() bool { return len(s.Problems) == 0 }

// Summarize reads the latest record per PR of mode for every PR of the set. In
// plan mode it counts the plan and checks coverage, second anchors and the age of
// the oldest plan; in apply mode it reads each PR's persisted residual and checks
// it is zero for every enumerated PR.
func Summarize(lg *Log, set *Set, mode string, maxAge time.Duration, now time.Time) *Summary {
	s := &Summary{Mode: mode, Enumerated: len(set.Entries), Counts: map[string]map[string]int{}, Residuals: map[string]int{}, MaxAge: maxAge}
	latest := lg.Latest(mode, nil)
	for _, e := range set.Entries {
		r, ok := latest[e.PR]
		if !ok {
			s.Missing = append(s.Missing, Gap{e.PR, "no " + mode + "-mode record"})
			continue
		}
		if r.ExitCode != 0 {
			why := fmt.Sprintf("the latest %s-mode run failed (exit %d, class %s)", mode, r.ExitCode, firstNonEmpty(r.ErrorClass, ExitClasses[r.ExitCode].Class, "unknown"))
			s.Missing = append(s.Missing, Gap{e.PR, why})
			continue
		}
		if r.Decide == nil || r.Decide.PlanError != "" {
			s.Missing = append(s.Missing, Gap{e.PR, "the latest " + mode + "-mode record has no readable plan (" + firstNonEmpty(decidePlanError(r), "no decide stage") + ")"})
			continue
		}
		s.WithRecord++
		if mode == ModeApply {
			switch {
			case r.Residual == nil:
				s.Residuals[e.PR] = -1
				s.Problems = append(s.Problems, fmt.Sprintf("%s: the apply record has no residual (%s)", e.PR, firstNonEmpty(r.ResidualError, "not recorded")))
			default:
				s.Residuals[e.PR] = *r.Residual
				if *r.Residual != 0 {
					s.Problems = append(s.Problems, fmt.Sprintf("%s: residual %d after the apply (a read-only plan still lists actions)", e.PR, *r.Residual))
				}
			}
			continue
		}
		for _, a := range r.Decide.Actions {
			op := a.Op
			if adoptionRules[a.Rule] {
				op = "adopt"
			}
			if s.Counts[a.Kind] == nil {
				s.Counts[a.Kind] = map[string]int{}
			}
			s.Counts[a.Kind][op]++
		}
		if r.Adoption != nil && r.Adoption.Outcome == "unadoptable" {
			var ids []string
			for _, c := range r.Adoption.Candidates {
				if !c.Keyed {
					ids = append(ids, c.ID)
				}
			}
			s.Unadoptable = append(s.Unadoptable, Gap{e.PR, strings.Join(ids, ",")})
		}
		if r.Adoption != nil && len(r.Adoption.Candidates) > 0 {
			for _, a := range r.Decide.Actions {
				if a.Kind == "anchor" && a.Op == "create" {
					s.SecondAnch = append(s.SecondAnch, e.PR)
					break
				}
			}
		}
		if s.Oldest.IsZero() || r.Time.Before(s.Oldest) {
			s.Oldest, s.OldestPR = r.Time, e.PR
		}
	}
	if len(s.Missing) > 0 {
		if mode == ModePlan {
			s.Problems = append(s.Problems, fmt.Sprintf("coverage: %d of %d enumerated PRs have no usable plan-mode record", len(s.Missing), s.Enumerated))
		} else {
			s.Problems = append(s.Problems, fmt.Sprintf("coverage: %d of %d enumerated PRs have no usable apply-mode record", len(s.Missing), s.Enumerated))
		}
	}
	if mode == ModePlan {
		if len(s.SecondAnch) > 0 {
			s.Problems = append(s.Problems, fmt.Sprintf("%d PR(s) would get a second anchor", len(s.SecondAnch)))
		}
		if !s.Oldest.IsZero() {
			s.Age = now.Sub(s.Oldest)
			if maxAge > 0 && s.Age > maxAge {
				s.Stale = true
				s.Problems = append(s.Problems, fmt.Sprintf("the oldest plan record is %s old, over the bound %s: re-plan before the flip", s.Age.Round(time.Minute), maxAge))
			}
		}
	}
	return s
}

func decidePlanError(r Record) string {
	if r.Decide == nil {
		return ""
	}
	return r.Decide.PlanError
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// Print writes the summary as `key: value` lines in a fixed order, so a person and
// a script read the same thing.
func (s *Summary) Print(w io.Writer) {
	fmt.Fprintf(w, "summary: mode=%s\n", s.Mode)
	fmt.Fprintf(w, "enumerated: %d\n", s.Enumerated)
	fmt.Fprintf(w, "with %s-mode record: %d\n", s.Mode, s.WithRecord)
	fmt.Fprintf(w, "without a usable %s-mode record: %d\n", s.Mode, len(s.Missing))
	for _, g := range s.Missing {
		fmt.Fprintf(w, "  %s: %s\n", g.PR, g.Why)
	}
	if s.Mode == ModeApply {
		fmt.Fprintf(w, "residual per PR:\n")
		for _, pr := range sortedKeys(s.Residuals) {
			v := fmt.Sprint(s.Residuals[pr])
			if s.Residuals[pr] < 0 {
				v = "unknown"
			}
			fmt.Fprintf(w, "  %s: %s\n", pr, v)
		}
	} else {
		fmt.Fprintf(w, "planned actions per kind (%s):\n", strings.Join(summaryOps, " "))
		kinds := sortedKeys(s.Counts)
		if len(kinds) == 0 {
			fmt.Fprintln(w, "  (none)")
		}
		for _, k := range kinds {
			var parts []string
			for _, op := range summaryOps {
				parts = append(parts, fmt.Sprintf("%s=%d", op, s.Counts[k][op]))
			}
			fmt.Fprintf(w, "  %s: %s\n", k, strings.Join(parts, " "))
		}
		fmt.Fprintf(w, "unadoptable items: %d\n", len(s.Unadoptable))
		for _, g := range s.Unadoptable {
			fmt.Fprintf(w, "  %s: %s\n", g.PR, g.Why)
		}
		fmt.Fprintf(w, "PRs that would get a second anchor: %d\n", len(s.SecondAnch))
		sort.Strings(s.SecondAnch)
		for _, pr := range s.SecondAnch {
			fmt.Fprintf(w, "  %s\n", pr)
		}
		if s.Oldest.IsZero() {
			fmt.Fprintln(w, "oldest plan record: none")
		} else {
			state := "within the bound"
			if s.Stale {
				state = "STALE: re-plan before the flip"
			}
			fmt.Fprintf(w, "oldest plan record: %s (%s) age=%s bound=%s %s\n", s.Oldest.Format(time.RFC3339), s.OldestPR, s.Age.Round(time.Second), s.MaxAge, state)
		}
	}
	if s.OK() {
		fmt.Fprintln(w, "checks: PASS")
	} else {
		fmt.Fprintln(w, "checks: FAIL")
		for _, p := range s.Problems {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
}
