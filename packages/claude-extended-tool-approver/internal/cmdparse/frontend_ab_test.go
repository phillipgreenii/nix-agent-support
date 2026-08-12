package cmdparse

// THE LATENCY GATE and the DISAGREEMENT CENSUS for ADR 0039's migration step 1.
//
// ADR 0039's Decision item 7 makes latency a GATE, not a finding: the recorded
// candidate figures were taken with an INCOMPLETE lowering and are a LOWER BOUND,
// so the conclusion "the candidate is not slower than what it replaces" is not yet
// proven. The pass criterion is that, measured over the SAME corpus, the complete
// lowering shows MEAN and P99 both no worse than the outgoing front end's. A
// regression in `max` alone MAY be accepted with a recorded reason; a p99
// regression MUST NOT be waived, because the hook runs on every tool call.
//
// The comparison MUST be a SAME-SNAPSHOT A/B. The historical figures
// (15.139 µs / 7.167 µs outgoing, 3.943 µs / 2.458 µs candidate) MUST NOT be used
// as the baseline: they were measured on an incomplete lowering AND the corpus is
// live and has grown since.
//
// This harness is env-gated so `nix flake check` does not depend on a corpus
// snapshot that exists only on a developer machine:
//
//	CETA_AB_SNAPSHOT=/path/to/corpus-snapshot.jsonl \
//	CETA_AB_REPORT=/path/to/report.txt \
//	go test ./internal/cmdparse/ -run TestFrontEndAB -timeout 30m -v
//
// The snapshot is JSONL: one JSON-encoded command STRING per line. It is extracted
// from the production asklog READ-ONLY — `sqlite3 -readonly` fails on that file
// with SQLite error 14, so the URI form
// `file:$HOME/.local/share/claude-extended-tool-approver/asks.db?immutable=1` is
// the one that works. The harness itself never opens the asklog; it reads only the
// extracted snapshot, so no run of it can write to production.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	abSnapshotEnv = "CETA_AB_SNAPSHOT"
	abReportEnv   = "CETA_AB_REPORT"
	// abReps is how many times each front end is run per command before its
	// per-command time is recorded as the mean of those runs. Interleaving the two
	// front ends per command (rather than timing all of one then all of the other)
	// is what makes scheduler drift and thermal drift affect BOTH sides equally.
	abReps = 3
)

func TestFrontEndAB(t *testing.T) {
	snapshot := os.Getenv(abSnapshotEnv)
	if snapshot == "" {
		t.Skipf("set %s to a JSONL corpus snapshot to run the latency gate and census", abSnapshotEnv)
	}
	cmds := loadSnapshot(t, snapshot)
	t.Logf("snapshot %s: %d commands", snapshot, len(cmds))

	var report strings.Builder
	measureAB(t, &report, cmds)
	censusAB(t, &report, cmds)

	if path := os.Getenv(abReportEnv); path != "" {
		if err := os.WriteFile(path, []byte(report.String()), 0o600); err != nil {
			t.Fatalf("write report: %v", err)
		}
		t.Logf("report written to %s", path)
	}
	t.Log("\n" + report.String())
}

func loadSnapshot(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- an operator-supplied snapshot path
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	var out []string
	seen := map[string]bool{}
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var cmd string
		if err := json.Unmarshal(line, &cmd); err != nil {
			t.Fatalf("snapshot line is not a JSON string: %v", err)
		}
		if cmd == "" || seen[cmd] {
			continue
		}
		seen[cmd] = true
		out = append(out, cmd)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan snapshot: %v", err)
	}
	return out
}

// measureAB is the same-snapshot A/B. Both front ends see the SAME command in the
// same iteration, so the two distributions are drawn under identical conditions.
func measureAB(t *testing.T, report *strings.Builder, cmds []string) {
	t.Helper()

	// Warm both front ends so neither pays first-call costs (the parser pool's first
	// allocation, lazily built regexps) inside the measured loop.
	for _, c := range cmds[:minInt(500, len(cmds))] {
		_ = OutgoingFrontEnd(c)
		_ = ParseShell(c)
	}

	oldNS := make([]float64, 0, len(cmds))
	newNS := make([]float64, 0, len(cmds))
	// Parseable-only series: the candidate returns EARLY on a parse failure, which
	// is faster, so a whole-snapshot figure flatters it. Reporting both is what
	// stops the gate being passed on the strength of the 0.03% of rows that fail.
	oldParsableNS := make([]float64, 0, len(cmds))
	newParsableNS := make([]float64, 0, len(cmds))

	var oldLeaves, newLeaves int64
	var unparseable int

	for _, cmd := range cmds {
		var oldTotal, newTotal time.Duration
		var nLeavesOld, nLeavesNew int
		var unp bool
		for r := 0; r < abReps; r++ {
			t0 := time.Now()
			leaves := OutgoingFrontEnd(cmd)
			oldTotal += time.Since(t0)
			nLeavesOld = len(leaves)

			t1 := time.Now()
			sp := ParseShell(cmd)
			newTotal += time.Since(t1)
			nLeavesNew = len(sp.Leaves)
			unp = sp.Unparseable
		}
		o := float64(oldTotal.Nanoseconds()) / abReps
		n := float64(newTotal.Nanoseconds()) / abReps
		oldNS = append(oldNS, o)
		newNS = append(newNS, n)
		if !unp {
			oldParsableNS = append(oldParsableNS, o)
			newParsableNS = append(newParsableNS, n)
		} else {
			unparseable++
		}
		oldLeaves += int64(nLeavesOld)
		newLeaves += int64(nLeavesNew)
	}

	oldStats := summarize(oldNS)
	newStats := summarize(newNS)
	oldPStats := summarize(oldParsableNS)
	newPStats := summarize(newParsableNS)

	fmt.Fprintf(report, "=== LATENCY GATE (ADR 0039 Decision item 7) ===\n")
	fmt.Fprintf(report, "method: same-snapshot A/B, interleaved per command, %d reps per command per side\n", abReps)
	fmt.Fprintf(report, "outgoing front end = StripCommentsPreservingHeredocs then Parse\n")
	fmt.Fprintf(report, "candidate          = ParseShell (parse PLUS complete lowering to leaves)\n")
	fmt.Fprintf(report, "snapshot size      = %d distinct commands (%d unparseable by the candidate)\n\n",
		len(cmds), unparseable)

	fmt.Fprintf(report, "ALL ROWS\n")
	writeStats(report, "  outgoing ", oldStats)
	writeStats(report, "  candidate", newStats)
	fmt.Fprintf(report, "  ratios: mean %.3fx  p50 %.3fx  p99 %.3fx  max %.3fx  (candidate/outgoing; <1 is faster)\n\n",
		newStats.mean/oldStats.mean, newStats.p50/oldStats.p50, newStats.p99/oldStats.p99, newStats.max/oldStats.max)

	fmt.Fprintf(report, "PARSEABLE ROWS ONLY (the candidate's early return on a parse failure removed)\n")
	writeStats(report, "  outgoing ", oldPStats)
	writeStats(report, "  candidate", newPStats)
	fmt.Fprintf(report, "  ratios: mean %.3fx  p50 %.3fx  p99 %.3fx  max %.3fx\n\n",
		newPStats.mean/oldPStats.mean, newPStats.p50/oldPStats.p50, newPStats.p99/oldPStats.p99, newPStats.max/oldPStats.max)

	// The gate is judged on the PARSEABLE-ONLY series, which is the conservative
	// choice: it denies the candidate the speed-up it gets from folding a parse
	// failure to a floor without lowering anything.
	meanOK := newPStats.mean <= oldPStats.mean
	p99OK := newPStats.p99 <= oldPStats.p99
	verdict := "PASS"
	if !meanOK || !p99OK {
		verdict = "FAIL"
	}
	fmt.Fprintf(report, "GATE VERDICT: %s (mean no worse: %v; p99 no worse: %v)\n", verdict, meanOK, p99OK)
	if newPStats.max > oldPStats.max {
		fmt.Fprintf(report, "NOTE: max regressed (%.3f us -> %.3f us). ADR 0039 permits a max-only regression\n"+
			"      with a recorded reason: max is ONE pathological input, and the p99 above is the\n"+
			"      figure the every-tool-call hook is judged on.\n",
			oldPStats.max/1000, newPStats.max/1000)
	}
	fmt.Fprintf(report, "\nLEAF TOTALS over the snapshot: outgoing=%d candidate=%d delta=%+d (%+.2f%%)\n\n",
		oldLeaves, newLeaves, newLeaves-oldLeaves,
		100*float64(newLeaves-oldLeaves)/float64(oldLeaves))

	if verdict == "FAIL" {
		t.Errorf("LATENCY GATE FAILED: candidate mean=%.3fus p99=%.3fus vs outgoing mean=%.3fus p99=%.3fus. "+
			"ADR 0039 Decision item 7: work MUST stop and report.",
			newPStats.mean/1000, newPStats.p99/1000, oldPStats.mean/1000, oldPStats.p99/1000)
	}
}

type stats struct {
	n                        int
	mean, p50, p90, p99, max float64
	total                    float64
}

func summarize(ns []float64) stats {
	if len(ns) == 0 {
		return stats{}
	}
	sorted := make([]float64, len(ns))
	copy(sorted, ns)
	sort.Float64s(sorted)
	var sum float64
	for _, v := range sorted {
		sum += v
	}
	return stats{
		n:     len(sorted),
		mean:  sum / float64(len(sorted)),
		p50:   pct(sorted, 0.50),
		p90:   pct(sorted, 0.90),
		p99:   pct(sorted, 0.99),
		max:   sorted[len(sorted)-1],
		total: sum,
	}
}

func pct(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(q*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

func writeStats(w *strings.Builder, label string, s stats) {
	fmt.Fprintf(w, "%s n=%d mean=%.3fus p50=%.3fus p90=%.3fus p99=%.3fus max=%.3fus total=%.3fs\n",
		label, s.n, s.mean/1000, s.p50/1000, s.p90/1000, s.p99/1000, s.max/1000, s.total/1e9)
}

// censusAB is the DISAGREEMENT CENSUS and the ROW-BY-ROW leaf-delta accounting.
//
// ADR 0039's Enforcement forbids BLANKET annotation of transitions — three beads in
// this chain shipped on a blanket plan and were wrong each time. So every row that
// disagrees is assigned a cause by a MECHANICAL classifier over the leaf keys, the
// per-cause leaf deltas are required to SUM EXACTLY to the total delta, and any row
// the classifier cannot attribute is dumped verbatim for inspection rather than
// absorbed into an "other" bucket.
func censusAB(t *testing.T, report *strings.Builder, cmds []string) {
	t.Helper()

	type causeStat struct {
		rows      int
		leafDelta int
		examples  []string
	}
	causes := map[string]*causeStat{}
	record := func(cause string, delta int, example string) {
		cs := causes[cause]
		if cs == nil {
			cs = &causeStat{}
			causes[cause] = cs
		}
		cs.rows++
		cs.leafDelta += delta
		if len(cs.examples) < 5 {
			cs.examples = append(cs.examples, example)
		}
	}

	var (
		total         int
		agree         int
		unparseable   int
		dialectAttrib int
		contentDiff   int
		rawDiff       int
		shapeDiff     int
		totalDelta    int
		unclassified  []string
		dropped       []string
	)
	dialects := map[string]int{}

	for _, cmd := range cmds {
		total++
		old := OutgoingFrontEnd(cmd)
		sp := ParseShell(cmd)
		delta := len(sp.Leaves) - len(old)
		totalDelta += delta

		if sp.Unparseable {
			unparseable++
			if sp.Dialect != "" {
				dialectAttrib++
				dialects[sp.Dialect]++
			}
			// I1b forfeiture: no leaf is examined, so any Reject a leaf would have
			// earned is forfeited. Every such row is reported.
			record("candidate-unparseable (I1b forfeiture)", delta, cmd)
			continue
		}

		d := CompareFrontEndsWith(cmd, old)
		if d.RawDiffers > 0 {
			rawDiff++
		}
		if d.ShapeDiffers {
			shapeDiff++
		}
		if !d.ContentDiffers() {
			agree++
			if delta != 0 {
				// Content agrees as a multiset yet the counts differ: only possible via
				// duplicate leaf keys, which is itself worth naming.
				record("duplicate-leaf-multiplicity", delta, cmd)
			}
			continue
		}
		contentDiff++
		cause := classifyDisagreement(old, sp.Leaves, d)
		record(cause, delta, cmd)
		if cause == causeUnclassified && len(unclassified) < 200 {
			unclassified = append(unclassified, fmt.Sprintf("  %q\n    old:%s\n    new:%s",
				cmd, dumpLeaves(old), dumpLeaves(sp.Leaves)))
		}
		// The DANGEROUS direction — an outgoing leaf the candidate does not produce,
		// with no keyword or continuation explanation — is dumped in full regardless of
		// how few rows it is. This is the only class that could LOSE a judgement, so it
		// is the one class a count alone must not settle.
		if cause == causeCandidateDropped && len(dropped) < 200 {
			dropped = append(dropped, fmt.Sprintf("  %q\n    old:%s\n    new:%s",
				cmd, dumpLeaves(old), dumpLeaves(sp.Leaves)))
		}
	}

	fmt.Fprintf(report, "=== DISAGREEMENT CENSUS over the whole snapshot ===\n")
	fmt.Fprintf(report, "rows compared              = %d\n", total)
	fmt.Fprintf(report, "content-identical          = %d (%.4f%%)\n", agree, 100*float64(agree)/float64(total))
	fmt.Fprintf(report, "content disagreements      = %d (%.4f%%)\n", contentDiff, 100*float64(contentDiff)/float64(total))
	fmt.Fprintf(report, "candidate unparseable      = %d (%.4f%%) -- I1b floor, every one a forfeiture\n",
		unparseable, 100*float64(unparseable)/float64(total))
	fmt.Fprintf(report, "  of which dialect-attributed = %d\n", dialectAttrib)
	for d, n := range dialects {
		fmt.Fprintf(report, "    %s: %d\n", d, n)
	}
	fmt.Fprintf(report, "Raw differs (matched leaves) = %d (I12 redefines Raw; expected on heredoc-bearing leaves)\n", rawDiff)
	fmt.Fprintf(report, "pipeline shape differs       = %d\n\n", shapeDiff)

	fmt.Fprintf(report, "=== LEAF-COUNT DELTA, ROW BY ROW BY CAUSE ===\n")
	fmt.Fprintf(report, "total leaf delta over the snapshot = %+d\n", totalDelta)
	fmt.Fprintf(report, "%-52s %8s %10s\n", "cause", "rows", "leafDelta")
	keys := make([]string, 0, len(causes))
	for k := range causes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return causes[keys[i]].rows > causes[keys[j]].rows })
	sumRows, sumDelta := 0, 0
	for _, k := range keys {
		cs := causes[k]
		fmt.Fprintf(report, "%-52s %8d %+10d\n", k, cs.rows, cs.leafDelta)
		sumRows += cs.rows
		sumDelta += cs.leafDelta
	}
	fmt.Fprintf(report, "%-52s %8d %+10d\n", "TOTAL (must equal the figures above)", sumRows, sumDelta)
	if sumDelta != totalDelta {
		t.Errorf("leaf-delta accounting does not close: causes sum to %+d, snapshot delta is %+d. "+
			"ADR 0039 forbids blanket annotation, so an unaccounted delta is a failure.", sumDelta, totalDelta)
	}
	fmt.Fprintf(report, "\nexamples per cause:\n")
	for _, k := range keys {
		fmt.Fprintf(report, "  %s\n", k)
		for _, e := range causes[k].examples {
			fmt.Fprintf(report, "      %q\n", truncate(e, 160))
		}
	}
	if len(dropped) > 0 {
		fmt.Fprintf(report, "\n=== CANDIDATE-DROPPED-LEAF ROWS (the DANGEROUS direction; every one dumped) ===\n")
		for _, u := range dropped {
			fmt.Fprintf(report, "%s\n", u)
		}
	}
	if len(unclassified) > 0 {
		fmt.Fprintf(report, "\n=== UNCLASSIFIED ROWS (dumped verbatim; blanket annotation is FORBIDDEN) ===\n")
		for _, u := range unclassified {
			fmt.Fprintf(report, "%s\n", u)
		}
	}
	fmt.Fprintln(report)
}

const (
	causeUnclassified     = "UNCLASSIFIED -- inspect row by row"
	causeCandidateDropped = "outgoing leaf absent from the candidate (DANGEROUS -- dumped in full below)"
)

// hasContinuationDebris reports whether any leaf carries LINE-CONTINUATION debris:
// a `\` immediately followed by a newline inside a token, or a token that is a bare
// `\`.
//
// This is a defect of the OUTGOING front end, and the largest single cause of leaf
// disagreement in the corpus. splitCompound scans with escapeUnquoted=true, so a
// `\`+newline is consumed as an escaped PAIR — correctly not a separator — but both
// BYTES are then copied into the segment buffer, and tokenize keeps them as ordinary
// token bytes. A multi-line `curl \` therefore parses to the bogus executable
// "\\ncurl" and to argument tokens like "\\n-H", so no argv[0]-keyed rule matches and
// the flags a rule would inspect are mis-spelled. The real parser joins the
// continuation, which is why the candidate reports `curl` with clean flags.
func hasContinuationDebris(leaves []ParsedCommand) bool {
	tokenHasDebris := func(s string) bool {
		return s == "\\" || strings.Contains(s, "\\\n")
	}
	for _, pc := range leaves {
		if tokenHasDebris(pc.Executable) {
			return true
		}
		for _, a := range pc.Args {
			if tokenHasDebris(a) {
				return true
			}
		}
	}
	return false
}

// censusKeywords are the words the OUTGOING front end turned into
// pseudo-executables because it split compounds on `;`/newlines without a grammar.
// Each such leaf is a leaf the candidate correctly does not produce.
//
// It is a SUPERSET of the package's own shellKeywords (pipesink.go), which lists
// only the ones EffectiveExec must step past. The census needs every keyword the
// text split could manufacture, including the terminators (`fi`, `done`, `esac`)
// and the bracket forms, so the two are deliberately separate tables.
var censusKeywords = map[string]bool{
	"if": true, "then": true, "elif": true, "else": true, "fi": true,
	"for": true, "while": true, "until": true, "do": true, "done": true,
	"case": true, "esac": true, "in": true, "select": true,
	"function": true, "time": true, "coproc": true,
	"[[": true, "]]": true, "((": true, "))": true, "{": true, "}": true,
}

// classifyDisagreement assigns ONE mechanically-derived cause to a row whose leaf
// content disagrees. The order is most-specific-first, and the final branch is an
// explicit UNCLASSIFIED so nothing is silently absorbed.
// hasSplitProcSubstDebris reports the OUTGOING front end's process-substitution
// truncation: splitCompound declines `<`/`>` as live bytes and tracks no `<(` extent,
// so a top-level operator INSIDE a process-substitution body splits the segment
// there. `diff -u <(cat x | jq .) <(cat y | jq .)` therefore lowers to an argument
// literally spelled "<(cat" and a bogus executable "jq -S .)", with neither body
// lifted. The real parser makes the extent structural, which is why the candidate
// reports the two fabricated /dev/fd/63 operands and both bodies.
func hasSplitProcSubstDebris(old, candidate []ParsedCommand) bool {
	oldSplit := false
	for _, pc := range old {
		for _, a := range pc.Args {
			if strings.HasPrefix(a, "<(") || strings.HasPrefix(a, ">(") {
				oldSplit = true
			}
		}
		if strings.HasSuffix(pc.Executable, ")") {
			oldSplit = true
		}
	}
	if !oldSplit {
		return false
	}
	for _, pc := range candidate {
		if len(pc.ProcessSubstitutions) > 0 {
			return true
		}
	}
	return false
}

// hasShreddedArrayLiteral reports the OUTGOING front end's multi-line array-literal
// shred: `t=(\n "a"\n "b"\n)` is split on the NEWLINES inside the paren group, so
// each element becomes a bogus executable and the assignment's value is emptied.
// commandStartOffset does glue a top-level bare paren group, but splitCompound runs
// FIRST and has already cut the segment apart. The candidate keeps the array as one
// assignment value.
func hasShreddedArrayLiteral(old, candidate []ParsedCommand) bool {
	newArray := false
	for _, pc := range candidate {
		for _, e := range pc.EnvVars {
			if strings.HasPrefix(e.Value, "(") && strings.Contains(e.Value, "\n") {
				newArray = true
			}
		}
	}
	if !newArray {
		return false
	}
	for _, pc := range old {
		for _, e := range pc.EnvVars {
			if e.Value == "" {
				return true
			}
		}
	}
	return false
}

// hasRetainedOuterQuote reports the OUTGOING front end keeping an UNBALANCED outer
// quote on a token. It happens where a heredoc lives inside a command substitution
// inside a double-quoted argument — `git commit -m "$(cat <<'EOF' … EOF)"` — because
// unquote only strips quoting when the WHOLE token is wrapped, and the extent pass
// left the token no longer wrapped. The candidate's token is properly unquoted.
func hasRetainedOuterQuote(old, candidate []ParsedCommand) bool {
	oldBad := false
	for _, pc := range old {
		for _, a := range pc.Args {
			if strings.HasPrefix(a, `"`) && !strings.HasSuffix(a, `"`) {
				oldBad = true
			}
		}
	}
	if !oldBad {
		return false
	}
	for _, pc := range candidate {
		for _, a := range pc.Args {
			if strings.HasPrefix(a, "$(") {
				return true
			}
		}
	}
	return false
}

// hasBangOperand reports the OUTGOING front end keeping bash's `!` negation as an
// OPERAND of a keyword pseudo-leaf: `until ! pgrep -f x; do …; done` lowers to
// Executable "until" with Args ["!", "pgrep", "-f", "x"], so the real command is two
// tokens deep. The parser models negation as Stmt.Negated, so the candidate reports
// `pgrep` as the executable and no rule has to step past anything.
func hasBangOperand(old []ParsedCommand) bool {
	for _, pc := range old {
		if pc.Executable == "!" {
			return true
		}
		if len(pc.Args) > 0 && pc.Args[0] == "!" {
			return true
		}
	}
	return false
}

func classifyDisagreement(old, candidate []ParsedCommand, d ShadowDiff) string {
	oldKeyword, oldOther := 0, 0
	for _, k := range d.OnlyOld {
		if keyLooksKeyword(k) {
			oldKeyword++
		} else {
			oldOther++
		}
	}
	newData, newOther := 0, 0
	for _, k := range d.OnlyNew {
		if strings.Contains(k, " data-raw=") {
			newData++
		} else {
			newOther++
		}
	}

	switch {
	case hasSplitProcSubstDebris(old, candidate):
		return "outgoing process-substitution body split at an operator inside it"
	case hasShreddedArrayLiteral(old, candidate):
		return "outgoing multi-line array literal shredded into bogus executables"
	case hasRetainedOuterQuote(old, candidate):
		return "outgoing token retained an unbalanced outer quote (heredoc inside a substitution)"
	case hasContinuationDebris(old) && !hasContinuationDebris(candidate):
		// Checked FIRST because it co-occurs with every other cause: a multi-line
		// command usually also contains a loop or an if, and attributing the row to
		// the keyword removal would hide the tokenisation defect that is actually
		// moving the leaf content.
		return "outgoing line-continuation token debris removed (backslash-newline)"
	case oldKeyword > 0 && oldOther == 0 && newOther == 0 && newData == 0:
		return "outgoing shell-keyword pseudo-leaves removed"
	case oldKeyword > 0 && oldOther == 0 && newData > 0 && newOther == 0:
		return "keyword pseudo-leaves removed; data leaf (word list/case subject/test) added"
	case oldKeyword > 0 && newOther > 0:
		return "keyword pseudo-leaves removed; real commands in branches/bodies now judged (I14)"
	case oldKeyword == 0 && oldOther == 0 && newData > 0 && newOther == 0:
		return "data leaf added (word list / case subject / arithmetic or test span)"
	case oldKeyword == 0 && oldOther == 0 && newOther > 0:
		return "candidate judges a command the outgoing front end dropped (I14 coverage)"
	case oldOther > 0 && newOther == 0 && newData == 0 && oldKeyword == 0:
		return causeCandidateDropped
	case hasBangOperand(old):
		return "outgoing kept bash's `!` negation as an operand of a keyword pseudo-leaf"
	case sameExecutablesDifferentTokens(old, candidate):
		return "same executables, argument tokenisation differs (quoting/expansion)"
	case oldKeyword > 0:
		// The remaining keyword rows: an outgoing keyword pseudo-leaf disappeared AND
		// the surrounding leaves were re-derived rather than merely dropped, which the
		// three branches above do not cover between them. The attribution is still
		// MECHANICAL — the predicate is "an outgoing leaf whose executable is a shell
		// keyword is absent from the candidate" — so this is a named cause and not a
		// blanket annotation. It is deliberately LAST among the keyword branches so a
		// more specific cause always wins.
		return "keyword pseudo-leaves removed; surrounding leaf set re-derived"
	default:
		return causeUnclassified
	}
}

// keyLooksKeyword reports whether a leaf key's executable is a shell keyword the
// outgoing front end manufactured out of a compound's syntax.
func keyLooksKeyword(key string) bool {
	const prefix = `exec="`
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	rest := key[len(prefix):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return false
	}
	return censusKeywords[rest[:end]]
}

func sameExecutablesDifferentTokens(old, candidate []ParsedCommand) bool {
	oldExec := execMultiset(old)
	newExec := execMultiset(candidate)
	if len(oldExec) != len(newExec) {
		return false
	}
	for k, v := range oldExec {
		if newExec[k] != v {
			return false
		}
	}
	return true
}

func execMultiset(leaves []ParsedCommand) map[string]int {
	out := map[string]int{}
	for _, pc := range leaves {
		if pc.Executable != "" {
			out[pc.Executable]++
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// BenchmarkFrontEnds is the microbenchmark form of the same A/B, for a quick
// comparison on a fixed representative set when no corpus snapshot is at hand. It
// is NOT the gate — the gate is TestFrontEndAB over a real corpus snapshot,
// because a hand-picked set cannot represent the distribution the hook sees.
func BenchmarkFrontEnds(b *testing.B) {
	cmds := []string{
		"ls -la",
		"git status --porcelain",
		"cd /tmp && ls | grep foo",
		`grep -rn "pattern" . | head -20`,
		"for f in *.md; do echo $f; done",
		"cat <<EOF\nbody\nEOF",
		"FOO=1 BAR=2 env rm -rf /tmp/x",
		`jq -r '.data[] | select(.x=="y") | .z' file.json`,
		"diff <(a) >(b) > /tmp/out 2>&1",
		"if [ -f x ]; then rm x; fi",
	}
	b.Run("outgoing", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = OutgoingFrontEnd(cmds[i%len(cmds)])
		}
	})
	b.Run("candidate", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = ParseShell(cmds[i%len(cmds)])
		}
	})
}
