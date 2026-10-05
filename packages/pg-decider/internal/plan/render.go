// Package plan renders a decide.Decide result for `pg-decider plan`, as the
// human-readable form of design 7.2 and as --json (design 9.7).
package plan

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// JSON prints exactly {"actions": [...], "skipped": [{"rule","reason","facts"}]}
// with non-nil arrays, indented, newline-terminated.
func JSON(w io.Writer, res action.PlanResult) error {
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// Text prints the design 7.2 form: a header line, a "linked work:" line, an
// "actions:" block and a "skipped:" block (each block may be empty).
func Text(w io.Writer, v *view.View, res action.PlanResult) error {
	var sb strings.Builder
	sb.WriteString(Header(v) + "\n")
	sb.WriteString("linked work: " + linkedWork(v) + "\n")

	sb.WriteString("\nactions:\n")
	if err := table(&sb, len(res.Actions), func(i int) []string {
		a := res.Actions[i]
		return []string{string(a.Op), subject(a), "rule=" + a.Rule, FactSummary(a.Facts)}
	}); err != nil {
		return err
	}

	sb.WriteString("\nskipped:\n")
	if err := table(&sb, len(res.Skipped), func(i int) []string {
		s := res.Skipped[i]
		return []string{s.Rule, s.Reason, FactSummary(s.Facts)}
	}); err != nil {
		return err
	}
	_, err := io.WriteString(w, sb.String())
	return err
}

// Header is the first line of the text form:
// <type> <id>  <relationship>  <state>  <ready|draft>  head=<short sha>  ci=<status>  conflict=<yes|no>.
// ci= is the snapshot's checks_rollup; conflict= is yes when mergeable is
// CONFLICTING. Only PR views carry a snapshot, so other types print just the
// type, id and relationship.
func Header(v *view.View) string {
	id := fmt.Sprintf("%s %s", v.Type, v.ID)
	rel := orDash(v.Decorations.Relationship)
	if v.Type != "pr" {
		return id + "  " + rel
	}
	s := v.Snapshot
	readiness := "ready"
	if s.Draft {
		readiness = "draft"
	}
	ci := s.ChecksRollup
	if ci == "" {
		ci = "unknown"
	}
	conflict := "no"
	if s.Mergeable == "CONFLICTING" {
		conflict = "yes"
	}
	return fmt.Sprintf("%s  %s  %s  %s  head=%s  ci=%s  conflict=%s",
		id, rel, orDash(s.State), readiness, short(s.HeadSHA), ci, conflict)
}

// linkedWork summarizes the work items linked from the view, grouped by kind
// in contract order: "<kind> (<state>[, detail])".
func linkedWork(v *view.View) string {
	ix := workitem.BuildIndex(v)
	var parts []string
	for _, k := range workitem.Kinds() {
		for _, it := range ix.ByKind(k) {
			state := orDash(it.State)
			switch k {
			case workitem.KindReviewPR:
				if h := it.Metadata["head_sha"]; h != "" {
					state += ", reviewed_head=" + short(h)
				}
			case workitem.KindProcessFeedback:
				if d := it.Digest(); d != "" {
					state += ", fbsum:" + d
				}
			}
			parts = append(parts, fmt.Sprintf("%s (%s)", k, state))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// subject is the second column of an action line: the work-item kind, or for
// an annotation the annotation key.
func subject(a action.Action) string {
	if a.Kind != "" {
		return a.Kind
	}
	if a.Target != nil {
		return *a.Target
	}
	return "-"
}

// table writes n two-space-indented rows with aligned columns. A trailing
// empty cell is dropped so no line ends in whitespace.
func table(sb *strings.Builder, n int, row func(int) []string) error {
	tw := tabwriter.NewWriter(sb, 0, 0, 2, ' ', 0)
	for i := 0; i < n; i++ {
		cells := row(i)
		for len(cells) > 0 && cells[len(cells)-1] == "" {
			cells = cells[:len(cells)-1]
		}
		if _, err := fmt.Fprintln(tw, "  "+strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// FactSummary renders facts as space-separated key=value pairs in key order.
// Strings print bare, string lists as [a, b], nil as null.
func FactSummary(facts map[string]any) string {
	if len(facts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(facts))
	for k := range facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + factValue(facts[k])
	}
	return strings.Join(parts, " ")
}

func factValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case []string:
		return "[" + strings.Join(x, ", ") + "]"
	case []any:
		items := make([]string, len(x))
		for i, e := range x {
			items[i] = factValue(e)
		}
		return "[" + strings.Join(items, ", ") + "]"
	default:
		return fmt.Sprint(x)
	}
}

func short(sha string) string {
	if sha == "" {
		return "-"
	}
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
