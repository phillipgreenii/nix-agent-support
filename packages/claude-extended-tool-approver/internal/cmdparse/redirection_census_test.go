//go:build corpus

package cmdparse_test

// TestRedirectionCensus is the ADR 0039 replay-discipline verification for the
// redirection-seam slice (Gap A: hookio.Redirection.LiveExpansion, Gap B:
// hookio.Redirection.Append). It walks a frozen, READ-ONLY snapshot of the
// real ask-log corpus, parses every distinct Bash `.command` value, and for
// every redirection found — including one inside a `$( )`/backtick
// substitution leaf — compares the two new parser-fact fields against the
// OLD, now-replaced heuristics they exist to retire:
//
//   - LiveExpansion vs strings.ContainsAny(Path, "$`")
//   - Append        vs strings.Contains(Operator, ">>")
//
// Per LOWERING.md's "Offline discipline" and ADR 0039's replayability
// requirement (this check needs no working directory and runs on every
// distinct command), this reads a snapshot the operator VACUUM INTOs from the
// live asks.db, never the live database itself, matching
// internal/setup/guard3_parsecount_test.go's and coverage_test.go's own
// env-gating convention: no CETA_CORPUS_DB, no run (skip, not fail) — a
// corpus-shaped guard that only runs on one operator's laptop is not a guard
// anyone else's CI can depend on, so this is a REPLAY report, not a gate.
//
// Command text is a private corpus and MUST NOT reach this test's own
// stdout/`-v` output or any committed file (this repo is public — see the
// repo CLAUDE.md's "Public Repository" section). Only the aggregate counts
// and the bucket NAMES (static descriptive strings, never derived from a row)
// go to `t.Logf`. Up to maxExamplesPerBucket real example rows per bucket go
// ONLY to the file named by CETA_CORPUS_REPORT, for a human to inspect
// locally — never printed, never committed.
//
// This file lives in package cmdparse_test (an EXTERNAL test package, not
// `package cmdparse`) because internal/asklog imports internal/cmdparse
// already (asklog/summary.go); an internal test file importing asklog back
// would be a dependency cycle for the test binary.
//
// Run:
//
//	CETA_CORPUS_DB=/path/to/snapshot.db CETA_CORPUS_REPORT=/path/to/report.txt \
//	  go test -tags corpus -run TestRedirectionCensus -v ./internal/cmdparse/
import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

const (
	// corpusDBEnvVar names the read-only snapshot (a VACUUM INTO copy, or a
	// live db opened with immutable=1 if sqlite3 was unavailable to make the
	// copy — see this test's caller for which applied).
	corpusDBEnvVar = "CETA_CORPUS_DB"
	// corpusReportEnvVar names the file that receives the full report,
	// examples included. Optional: without it, only the aggregate t.Logf
	// lines are produced.
	corpusReportEnvVar = "CETA_CORPUS_REPORT"
	// maxExamplesPerBucket bounds how many real corpus rows the report file
	// keeps per disagreement bucket — enough to characterize the shape,
	// small enough that the report stays a report and not a corpus re-export.
	maxExamplesPerBucket = 20
)

// reProcSubstTarget matches a process-substitution operator anywhere in a
// redirect target (`<(cmd)`, `>(cmd)`) — the one live-expansion shape that
// carries neither a `$` nor a backtick, so it is the expected content of the
// "old said static, new says live" bucket.
var reProcSubstTarget = regexp.MustCompile(`[<>]\(`)

func TestRedirectionCensus(t *testing.T) {
	dbPath := os.Getenv(corpusDBEnvVar)
	if dbPath == "" {
		t.Skipf("%s not set; skipping corpus census (see this file's doc comment)", corpusDBEnvVar)
	}
	store, err := asklog.NewReadOnlyStore(dbPath)
	if err != nil {
		t.Fatalf("open corpus snapshot read-only: %v", err)
	}
	defer func() { _ = store.Close() }()

	rows, err := store.DB().Query(
		`SELECT DISTINCT json_extract(tool_input_json, '$.command') AS c
		   FROM tool_decisions
		  WHERE tool_name = 'Bash' AND json_extract(tool_input_json, '$.command') IS NOT NULL`,
	)
	if err != nil {
		t.Fatalf("query distinct commands: %v", err)
	}
	var commands []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan command: %v", err)
		}
		commands = append(commands, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate commands: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close rows: %v", err)
	}

	c := newRedirectionCensus()
	for _, cmd := range commands {
		c.totalCommands++
		sp := cmdparse.ParseShell(cmd)
		if sp.Unparseable {
			continue
		}
		c.parseable++
		walkRedirectionLeaves(sp.Leaves, func(leaf cmdparse.ParsedCommand) {
			for _, r := range leaf.Redirections {
				c.totalRedirections++
				c.observeLive(r.Path, r.LiveExpansion, cmd)
				c.observeAppend(r.Operator, r.Append, cmd)
			}
		})
	}

	t.Logf("redirection census: %d commands, %d parseable, %d redirections",
		c.totalCommands, c.parseable, c.totalRedirections)
	t.Logf("LiveExpansion: %d agree, %d disagree", c.liveAgree, c.liveDisagree)
	for _, b := range sortedKeys(c.liveBuckets) {
		t.Logf("  live bucket [%d]: %s", c.liveBuckets[b], b)
	}
	t.Logf("Append: %d agree, %d disagree", c.appendAgree, c.appendDisagree)
	for _, b := range sortedKeys(c.appendBuckets) {
		t.Logf("  append bucket [%d]: %s", c.appendBuckets[b], b)
	}

	if reportPath := os.Getenv(corpusReportEnvVar); reportPath != "" {
		if err := os.WriteFile(reportPath, []byte(c.report()), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}
	}
}

// walkRedirectionLeaves visits every leaf in leaves and, recursively, every
// leaf reachable through a substitution — either a leaf's own `$( )`/backtick
// substitutions or a heredoc body's substitutions — so a redirection nested
// inside a command substitution is counted exactly like a top-level one.
func walkRedirectionLeaves(leaves []cmdparse.ParsedCommand, visit func(cmdparse.ParsedCommand)) {
	for _, leaf := range leaves {
		visit(leaf)
		for _, s := range leaf.Substitutions {
			walkRedirectionLeaves(s.Leaves, visit)
		}
		for _, h := range leaf.Heredocs {
			for _, s := range h.Substitutions {
				walkRedirectionLeaves(s.Leaves, visit)
			}
		}
	}
}

type redirectionCensus struct {
	totalCommands     int
	parseable         int
	totalRedirections int

	liveAgree, liveDisagree     int
	appendAgree, appendDisagree int

	liveBuckets    map[string]int
	appendBuckets  map[string]int
	liveExamples   map[string][]string
	appendExamples map[string][]string
}

func newRedirectionCensus() *redirectionCensus {
	return &redirectionCensus{
		liveBuckets:    map[string]int{},
		appendBuckets:  map[string]int{},
		liveExamples:   map[string][]string{},
		appendExamples: map[string][]string{},
	}
}

func (c *redirectionCensus) observeLive(path string, newLive bool, cmd string) {
	oldLive := strings.ContainsAny(path, "$`")
	if oldLive == newLive {
		c.liveAgree++
		return
	}
	c.liveDisagree++
	bucket := liveDisagreeShape(path, oldLive)
	c.liveBuckets[bucket]++
	if len(c.liveExamples[bucket]) < maxExamplesPerBucket {
		c.liveExamples[bucket] = append(c.liveExamples[bucket], fmt.Sprintf("target=%q command=%q", path, cmd))
	}
}

// liveDisagreeShape names the bucket for a LiveExpansion disagreement. Only
// two shapes are structurally possible given wordHasLiveExpansion's rules
// (see that function's own doc): a target that carries a `$`/backtick byte
// with no LIVE meaning (single-quoted, backslash-escaped, or a bare
// unexpandable `$`/backtick), and a target that is live with NEITHER byte
// present (process substitution is the only such shape this parser lowers
// into a redirect target; an ExtGlob pattern is the other type
// wordHasLiveExpansion treats as live, kept as its own residual bucket in
// case one surfaces).
func liveDisagreeShape(path string, oldLive bool) string {
	if oldLive {
		return "old=true new=false: target contains $ or ` but is not a live expansion " +
			"(single-quoted, backslash-escaped, or a bare unexpandable $/`)"
	}
	if reProcSubstTarget.MatchString(path) {
		return "old=false new=true: process substitution target (<(...) or >(...)), no $/` byte"
	}
	return "old=false new=true: live with no $/` byte, not process substitution (e.g. an extglob pattern)"
}

func (c *redirectionCensus) observeAppend(operator string, newAppend bool, cmd string) {
	oldAppend := strings.Contains(operator, ">>")
	if oldAppend == newAppend {
		c.appendAgree++
		return
	}
	c.appendDisagree++
	bucket := fmt.Sprintf("old=%v new=%v operator=%q", oldAppend, newAppend, operator)
	c.appendBuckets[bucket]++
	if len(c.appendExamples[bucket]) < maxExamplesPerBucket {
		c.appendExamples[bucket] = append(c.appendExamples[bucket], fmt.Sprintf("operator=%q command=%q", operator, cmd))
	}
}

func (c *redirectionCensus) report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "CETA redirection census\n")
	fmt.Fprintf(&b, "total commands:     %d\n", c.totalCommands)
	fmt.Fprintf(&b, "parseable commands: %d\n", c.parseable)
	fmt.Fprintf(&b, "total redirections: %d\n\n", c.totalRedirections)

	fmt.Fprintf(&b, "LiveExpansion: %d agree, %d disagree\n", c.liveAgree, c.liveDisagree)
	for _, k := range sortedKeys(c.liveBuckets) {
		fmt.Fprintf(&b, "  [%d] %s\n", c.liveBuckets[k], k)
		for _, ex := range c.liveExamples[k] {
			fmt.Fprintf(&b, "      %s\n", ex)
		}
	}

	fmt.Fprintf(&b, "\nAppend: %d agree, %d disagree\n", c.appendAgree, c.appendDisagree)
	for _, k := range sortedKeys(c.appendBuckets) {
		fmt.Fprintf(&b, "  [%d] %s\n", c.appendBuckets[k], k)
		for _, ex := range c.appendExamples[k] {
			fmt.Fprintf(&b, "      %s\n", ex)
		}
	}
	return b.String()
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
