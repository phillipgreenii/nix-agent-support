package route

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/phillipgreenii/pg-ccaudit/internal/candidate"
	"github.com/phillipgreenii/pg-ccaudit/internal/classify"
	"github.com/phillipgreenii/pg-ccaudit/internal/gold"
	"github.com/phillipgreenii/pg-ccaudit/internal/query"
)

// approverRefusals are the recorded refusal wordings that belong to the permission
// and hook layer rather than to any instruction.
//
// This is an explicit, short, MEASURED list rather than a general classifier,
// because it has to be auditable: every entry below was observed in the corpus with
// its count, and a reader can check each one. It is also the fragile part of command
// failure routing — a refusal reworded upstream stops matching — so a command
// failure that does not match here still gets routed (by its main-loop/subagent
// split), it simply does not get the permission-config shortcut.
var approverRefusals = []struct {
	Fragment string
	Note     string
}{
	{"access to .git directory is blocked", "approver rule, not an instruction"},
	{"refusing a commit on the primary branch", "approver rule, not an instruction"},
	{"Permission for this action was denied", "permission layer refused a correct action"},
	{"Permission for this tool use was denied", "permission layer refused a correct action"},
	{"denied by the Claude Code auto mode classifier", "permission layer refused a correct action"},
	{"Blocked by classifier", "permission layer refused a correct action"},
}

// humanTerminated are error bodies whose recorded elapsed time is dominated by a
// PERSON deciding rather than by the agent working.
//
// `cost-by-signature` measures elapsed as the wall time between a tool_use line's
// timestamp and its tool_result's, which is exactly right for a command that ran and
// failed. For a call a person REFUSED or INTERRUPTED, the result does not arrive
// until the person acts, so the same measurement is their reading-and-deciding time.
// Measured, it is enormous and it distorts the whole ranking: 14 user rejections
// carried 99,396,772 ms (27.6 hours) and took first place in the report, and a SINGLE
// rejection carried 39,969,899 ms (11.1 hours) and took eighth — above a
// 387-occurrence class. None of that is agent waste.
//
// So the cost is dropped to zero for these, exactly as candidate.StartTS is left
// empty for the human-terminated Tier 1 signals, and such a finding ranks on
// frequency and preventability — which is all the evidence there is. The classifier
// denial is NOT in this list: that verdict is machine-made and arrives promptly.
var humanTerminated = []string{
	"The user doesn't want to proceed with this tool use",
	"[Request interrupted by user",
	"Permission for this tool use was denied",
}

func isHumanTerminated(signature string) bool {
	for _, s := range humanTerminated {
		if strings.Contains(signature, s) {
			return true
		}
	}
	return false
}

// Report is one complete census.
type Report struct {
	Since string `json:"since"`
	Until string `json:"until"`

	Coverage map[string]string  `json:"coverage"`
	Sources  []candidate.Source `json:"sources"`
	Empty    []candidate.Signal `json:"empty_signals"`

	Classifier    string        `json:"classifier"`
	PromptVersion int           `json:"prompt_version"`
	Cost          classify.Cost `json:"cost"`

	// FileChannel is the count of corrections the operator wrote into FILES rather
	// than into a session. It is a first-class field, not a footnote: a
	// transcript-only correction rate undercounts by however many of these there
	// are, and reporting the transcript rate as "the correction rate" is wrong.
	FileChannel      int      `json:"file_channel_corrections"`
	FileChannelPaths []string `json:"file_channel_paths,omitempty"`

	Findings []Finding `json:"findings"`

	Evaluation *classify.Comparison `json:"evaluation,omitempty"`
}

// CommandFailures builds command-failure findings from the aggregate queries the
// failure census already ships.
//
// They are built from AGGREGATES rather than per-occurrence rows on purpose: a
// command failure needs no semantic judgement to be a failure, so there is nothing
// for Tier 2 to decide and nothing gained by materialising 2,397 individual rows.
// Three queries are joined by signature — sidechain-split for the counts and the
// routing split, cost-by-signature for the MEASURED elapsed cost, and first-seen for
// the dates criterion 10 needs.
func CommandFailures(ctx context.Context, db *sql.DB, since, until string) ([]Finding, error) {
	splits, err := rows(ctx, db, "sidechain-split", nil, since, until)
	if err != nil {
		return nil, err
	}
	costs, err := rows(ctx, db, "cost-by-signature", nil, since, until)
	if err != nil {
		return nil, err
	}
	seen, err := rows(ctx, db, "first-seen", nil, since, until)
	if err != nil {
		return nil, err
	}
	// The runaway discount is applied to every finding rather than to whichever ones
	// someone remembered to check, which is why `concentration-by-signature` exists:
	// `session-concentration` takes one signature at a time, so folding that in would
	// mean one query per signature.
	conc, err := rows(ctx, db, "concentration-by-signature", nil, since, until)
	if err != nil {
		return nil, err
	}
	worstBySig := map[string]int64{}
	for _, r := range conc {
		worstBySig[r.str("signature")] = r.num("worst_session")
	}

	costBySig := map[string]int64{}
	for _, r := range costs {
		costBySig[r.str("signature")] = r.num("elapsed_ms_sum")
	}
	firstBySig := map[string][2]string{}
	for _, r := range seen {
		firstBySig[r.str("signature")] = [2]string{r.str("first_seen"), r.str("last_seen")}
	}

	out := make([]Finding, 0, len(splits))
	for _, r := range splits {
		sig := r.str("signature")
		if isHumanTerminated(sig) {
			costBySig[sig] = 0
		}
		f := Finding{
			Kind:         KindCommandFailure,
			Signature:    sig,
			Signal:       "command-failure",
			Occurrences:  int(r.num("total")),
			Sessions:     int(r.num("sessions")),
			MainLoop:     int(r.num("main_loop")),
			Subagent:     int(r.num("subagent")),
			CostMS:       costBySig[sig],
			WorstSession: int(worstBySig[sig]),
			Evidence:     sig,
			Confidence:   "high",
		}
		if d, ok := firstBySig[sig]; ok {
			f.FirstSeen, f.LastSeen = d[0], d[1]
		}
		f.Route, f.AlsoNote = decideCommandFailure(f)
		out = append(out, f)
	}
	return out, nil
}

// decideCommandFailure routes an errored tool call.
//
// The split is what decides it, which is the correction the manual audit needed: in
// the census that motivated this index 53% of all errors were subagent, a figure
// that on its own says nothing, while the PER-CLASS split is what routed each fix —
// a fabricated-absolute-root class at 104 main-loop / 0 subagent belongs in the
// always-on rules, and a Bash-timeout class at 54 / 73 has to reach the subagent
// brief or it is not fixed at all.
func decideCommandFailure(f Finding) (Route, string) {
	for _, ar := range approverRefusals {
		if strings.Contains(f.Signature, ar.Fragment) {
			return RoutePermissionConfig, ar.Note
		}
	}
	if f.SubagentShare() >= SubagentDominant {
		return RouteSubagentPrompt, fmt.Sprintf("%d of %d occurrences were in a subagent; a rule in the always-on user rules does not reliably reach one",
			f.Subagent, f.MainLoop+f.Subagent)
	}
	note := ""
	if f.Subagent > 0 && f.MainLoop > 0 {
		note = fmt.Sprintf("split %d main-loop / %d subagent — the subagent brief needs the same change",
			f.MainLoop, f.Subagent)
	}
	return RouteGlobalRule, note
}

// Coverage reads the coverage query into a flat map for the provenance header.
func Coverage(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rs, err := rows(ctx, db, "coverage", nil, "", "")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rs {
		for k := range r {
			out[k] = r.str(k)
		}
	}
	return out, nil
}

// row is a query result row addressed by column name.
type row map[string]any

func (r row) str(k string) string {
	switch v := r[k].(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

func (r row) num(k string) int64 {
	switch v := r[k].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func rows(ctx context.Context, db *sql.DB, name string, args []string, since, until string) ([]row, error) {
	q, err := query.Lookup(name)
	if err != nil {
		return nil, err
	}
	res, err := query.Run(ctx, db, query.Request{
		Query: q, Args: args, Since: since, Until: until, Format: query.FormatJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", name, err)
	}
	out := make([]row, 0, len(res.Rows))
	for _, raw := range res.Rows {
		m := make(row, len(res.Columns))
		for i, c := range res.Columns {
			if i < len(raw) {
				m[c] = raw[i]
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// FileChannelNote is the sentence the report MUST carry whenever it quotes a
// correction count.
const FileChannelNote = `The operator sometimes writes a correction into a FILE instead of into a session.
Those corrections are STRUCTURALLY INVISIBLE to a transcript census — no turn exists
to detect — so the transcript-derived correction count below is a LOWER BOUND, short
by an unknown margin. Never present it as "the correction rate".`

// Render writes the report DIRECTLY to w, section by section, rather than
// assembling the whole thing in memory first (bead pg2-ohvpk): a report
// killed partway through printing must leave whatever it had already
// written intact on w, not lose it to a buffer that was never flushed.
//
// Every Fprint* call below writes straight to w; none of it goes through an
// intermediate strings.Builder. Errors from individual writes are not
// checked mid-report — matching this function's existing tolerance, which
// only ever surfaced a write failure from the (formerly single) final write
// — because *os.File writes to stdout do not fail in the cases this tool
// runs in, and the point of this change is durability of what already
// landed, not new error handling for a failure mode that was never handled
// before either.
func Render(w io.Writer, rep Report) error {
	window := "all"
	if rep.Since != "" || rep.Until != "" {
		from, to := rep.Since, rep.Until
		if from == "" {
			from = "-inf"
		}
		if to == "" {
			to = "+inf"
		}
		window = "[" + from + "," + to + ")"
	}
	fmt.Fprintf(w, "# pg-ccaudit mistake census — window=%s\n\n", window)

	fmt.Fprintln(w, "## Provenance")
	keys := make([]string, 0, len(rep.Coverage))
	for k := range rep.Coverage {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+rep.Coverage[k])
	}
	fmt.Fprintf(w, "coverage: %s\n", strings.Join(parts, " "))
	fmt.Fprintf(w, "classifier: %s (prompt v%d)\n", rep.Classifier, rep.PromptVersion)
	fmt.Fprintf(w, "%s\n", rep.Cost.Line())
	fmt.Fprintln(w, "tier 1 signals:")
	for _, s := range rep.Sources {
		fmt.Fprintf(w, "  %-18s %-24s v%d  rows=%d\n", s.Signal, s.Query, s.Version, s.Rows)
	}
	if len(rep.Empty) > 0 {
		names := make([]string, 0, len(rep.Empty))
		for _, e := range rep.Empty {
			names = append(names, string(e))
		}
		fmt.Fprintf(w, "  EMPTY SIGNALS: %s\n", strings.Join(names, ", "))
		fmt.Fprintln(w, "  An empty detector is NOT evidence that the thing it detects did not happen.")
		fmt.Fprintln(w, "  Check the query's notes before reading a zero as good news.")
	}

	fmt.Fprintln(w, "\n## Undercount — the file channel")
	fmt.Fprintln(w, FileChannelNote)
	fmt.Fprintf(w, "file-channel corrections found: %d\n", rep.FileChannel)
	for _, p := range rep.FileChannelPaths {
		fmt.Fprintf(w, "  %s\n", p)
	}

	fmt.Fprintln(w, "\n## Ranked findings — mistakes AND command failures, one list")
	fmt.Fprintln(w, "score = occurrences x (1 + cost_ms/1000) x preventability(route)")
	fmt.Fprintln(w, "cost_ms is MEASURED wall time (transcript timestamps), never estimated; 0 means no")
	fmt.Fprintln(w, "span was measurable, not that it was free. preventability: hook 1.00,")
	fmt.Fprintln(w, "permission-config 0.90, global-rule / subagent-prompt-template 0.60, workspace-rule")
	fmt.Fprintln(w, "0.50, skill / slash-command 0.40, not-actionable 0.00.")
	fmt.Fprintln(w)

	for i, f := range rep.Findings {
		fmt.Fprintf(w, "%d. [%s] %s\n", i+1, f.Route, f.Signature)
		fmt.Fprintf(w, "   kind=%s", f.Kind)
		if f.Class != "" {
			fmt.Fprintf(w, " class=%s", f.Class)
		}
		if f.Signal != "" {
			fmt.Fprintf(w, " signal=%s", f.Signal)
		}
		fmt.Fprintf(w, " score=%.1f\n", f.Score)
		fmt.Fprintf(w, "   occurrences=%d sessions=%d worst_session=%d main_loop=%d subagent=%d cost_ms=%d\n",
			f.Occurrences, f.Sessions, f.WorstSession, f.MainLoop, f.Subagent, f.CostMS)
		fmt.Fprintf(w, "   first_seen=%s last_seen=%s\n", dash(f.FirstSeen), dash(f.LastSeen))
		if f.WorstSession > 1 && f.Sessions > 0 && f.WorstSession*2 >= f.Occurrences {
			fmt.Fprintf(w, "   RUNAWAY DISCOUNT: %d of %d occurrences are one session — weight this down\n",
				f.WorstSession, f.Occurrences)
		}
		if f.Prevention != "" {
			fmt.Fprintf(w, "   prevention: %s\n", f.Prevention)
		}
		if f.AlsoNote != "" {
			fmt.Fprintf(w, "   also: %s\n", f.AlsoNote)
		}
		if f.RouteHint != "" && Route(f.RouteHint) != f.Route {
			fmt.Fprintf(w, "   route hint was %q; the routing table chose %s\n", f.RouteHint, f.Route)
		}
		if f.Evidence != "" {
			fmt.Fprintf(w, "   evidence: %s\n", oneLine(f.Evidence, 200))
		}
		fmt.Fprintln(w)
	}

	byRoute := map[Route]int{}
	for _, f := range rep.Findings {
		byRoute[f.Route]++
	}
	fmt.Fprintln(w, "## Routing totals — every finding carries exactly one route")
	for _, r := range Routes {
		fmt.Fprintf(w, "  %-26s %d\n", r, byRoute[r])
	}

	if rep.Evaluation != nil {
		fmt.Fprintln(w, "\n## Tier 2 evaluation")
		classify.Render(w, *rep.Evaluation)
	}

	return nil
}

// RenderJSON writes the report as JSON, for a consumer that wants to re-rank or
// diff two censuses rather than read prose.
func RenderJSON(w io.Writer, rep Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n-3] + "..."
	}
	return s
}

// FileChannel counts the file-channel corrections and returns their paths.
func FileChannel(set gold.Set) (int, []string) {
	entries := set.FileChannel()
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		paths = append(paths, strings.TrimPrefix(e.ID, "file:"))
	}
	sort.Strings(paths)
	return len(entries), paths
}
