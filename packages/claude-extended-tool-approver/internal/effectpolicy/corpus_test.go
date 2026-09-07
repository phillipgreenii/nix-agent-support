//go:build corpus

// This is the CORPUS COVERAGE HARNESS (spike slice 3b, harness 2): it reads
// the real decision database's distinct Bash commands and measures how much
// of that REAL-WORLD corpus the spike's schema/registry model can fully
// account for — coverage, not verdicts. It carries `//go:build corpus` so it
// NEVER runs under a default `go test ./...` (or even `-tags integration`):
// it needs a real, operator-specific database path (CETA_CORPUS_DB) that
// does not exist in CI or in a fresh checkout, and — because that database
// can contain operator/employer command text — it is deliberately never run
// automatically. Run it by hand:
//
//	CETA_CORPUS_DB=$HOME/.local/share/claude-extended-tool-approver/asks.db \
//	CETA_CORPUS_REPORT=/path/to/scratch/corpus-report.txt \
//	go test -tags corpus -run TestCorpus -v ./internal/effectpolicy/
//
// It opens the database READ-ONLY (mode=ro plus the query_only pragma, which
// TestCorpus itself verifies by attempting — and requiring the driver to
// reject — a write) and never copies it anywhere. Only the AGGREGATE numbers
// this file prints belong in any report derived from a run against real
// data; the raw command corpus itself must never be committed or quoted
// verbatim outside the operator's own machine.
package effectpolicy

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectgraph"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// bashToolInput decodes just the field this harness needs from a
// tool_decisions.tool_input_json row. Defined locally (not
// hookio.BashToolInput) so this file needs no import of internal/hookio —
// unlike agreement_integration_test.go, this harness has no reason to reach
// the live engine at all, so there is no reason to widen its dependencies.
type bashToolInput struct {
	Command string `json:"command"`
}

// freqCount is one (key, count) pair for a top-N-by-frequency report.
type freqCount struct {
	Key   string
	Count int
}

// topN sorts counts by count descending, key ascending (for a deterministic
// tie-break), and returns at most n entries.
func topN(counts map[string]int, n int) []freqCount {
	out := make([]freqCount, 0, len(counts))
	for k, c := range counts {
		out = append(out, freqCount{k, c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Normalisation for insufficiency reasons, so "unknown flag -x" and "read of
// /home/alice/x.txt: no-read-of-unreadable-path (zone unknown)" bucket with
// every other row that differs only in WHICH path/host/number appeared. Order
// matters: quoted spans first (a sed program's own script text can itself
// contain digits, slashes or dots that the later, coarser passes would
// otherwise mangle before the whole span is collapsed), then path-or-host
// tokens, then bare numbers.
var (
	reQuotedDouble   = regexp.MustCompile(`"[^"]*"`)
	reQuotedBacktick = regexp.MustCompile("`[^`]*`")
	// rePathToken matches a whitespace-delimited token containing a `/` or
	// starting with `~` — an absolute/relative path, a home-relative path, or
	// a dynamic-expansion placeholder like "$D/id_rsa" the corpus command
	// itself wrote.
	rePathToken = regexp.MustCompile(`[^\s\[\]():]*[/~][^\s\[\]():]*`)
	// reHostToken matches a dotted, domain-shaped token (also catches a bare
	// filename like "README.md", which is exactly the kind of
	// argument-specific text this normalisation exists to collapse).
	reHostToken = regexp.MustCompile(`\b[A-Za-z0-9][A-Za-z0-9.-]*\.[A-Za-z]{2,}\b`)
	reDigits    = regexp.MustCompile(`\b\d+\b`)
	reSpaces    = regexp.MustCompile(`\s+`)
)

func normalizeReason(s string) string {
	s = reQuotedDouble.ReplaceAllString(s, `"…"`)
	s = reQuotedBacktick.ReplaceAllString(s, "`…`")
	s = rePathToken.ReplaceAllString(s, "<path>")
	s = reHostToken.ReplaceAllString(s, "<host>")
	s = reDigits.ReplaceAllString(s, "N")
	s = reSpaces.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// commandStat is what TestCorpus computes per distinct command.
type commandStat struct {
	parseable          bool
	hasCommandNode     bool
	fullyRegistered    bool // every Command node's basename is in the registry (vacuously true if hasCommandNode is false)
	fullyCovered       bool // fullyRegistered AND no Command node is MarkInsufficient
	unregisteredBases  []string
	insufficientReason []string // only populated when fullyRegistered but !fullyCovered
	decision           evalcontract.Decision
}

// evaluateForCorpus parses command, builds the interpreted graph exactly as
// Evaluate does, and reports the coverage facts plus the top-level decision.
// It duplicates a slice of Evaluate's own logic (rather than calling it and
// re-deriving coverage from the response alone) only to reach the PER-NODE
// registration fact — whether n.Leaf's basename is in reg — which Evaluate's
// Response does not expose; everything else (the Interpreted graph, the
// Decision) is exactly what Evaluate itself produces, over the SAME throwaway
// CWD/root for every command (paths in the corpus won't exist under it; see
// this file's top comment — that is fine, this measures model SUFFICIENCY,
// not zone verdicts).
func evaluateForCorpus(command, cwd string, reg cmddesc.Registry, policies []Policy, graphPolicies []GraphPolicy) commandStat {
	var st commandStat
	sp := cmdparse.ParseShell(command)
	if sp.Unparseable {
		return st
	}
	st.parseable = true

	resp := Evaluate(evalcontract.Request{Command: command, CWD: cwd, ProjectRoot: cwd}, reg, policies, graphPolicies)
	st.decision = resp.Decision

	st.fullyRegistered = true
	st.fullyCovered = true
	for i := range resp.Interpreted.Nodes {
		n := &resp.Interpreted.Nodes[i]
		if n.Kind != effectgraph.NodeCommand {
			continue
		}
		st.hasCommandNode = true
		if n.Leaf != nil && n.Leaf.Executable != "" {
			base := path.Base(n.Leaf.Executable)
			if _, ok := reg.Lookup(base); !ok {
				st.fullyRegistered = false
				st.unregisteredBases = append(st.unregisteredBases, base)
			}
		}
		if n.Mark == effectgraph.MarkInsufficient {
			st.fullyCovered = false
			if n.MarkReason != "" {
				st.insufficientReason = append(st.insufficientReason, n.MarkReason)
			}
		}
	}
	if !st.hasCommandNode {
		st.fullyCovered = false
	}
	if !st.fullyRegistered {
		st.fullyCovered = false
	}
	return st
}

// openCorpusDB opens dbPath strictly read-only: mode=ro plus the query_only
// pragma (belt and suspenders — mode=ro alone still lets a WAL-mode SQLite
// connection attempt the write-ahead machinery; query_only=1 rejects any
// write statement outright, which the test below confirms empirically rather
// than trusting the DSN silently). No copy of dbPath is ever made; this
// process opens the operator's own file in place.
func openCorpusDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open corpus db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Verify read-only-ness mechanically rather than trusting the DSN: a
	// no-op write against a real table must be rejected by the driver.
	if _, err := db.Exec(`DELETE FROM tool_decisions WHERE 1 = 0`); err == nil {
		t.Fatal("corpus db connection is NOT read-only (a no-op DELETE succeeded) — refusing to proceed")
	}
	return db
}

// distinctBashCommands reads every distinct Bash command string from
// tool_decisions.tool_input_json (see internal/asklog/store.go's schema:
// tool_name/tool_input_json columns, migration version 1). Deduplication is
// by the DECODED command string (not the raw JSON text), matching how
// hookio.HookInput.BashCommand() itself extracts it in production.
func distinctBashCommands(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT tool_input_json FROM tool_decisions WHERE tool_name = 'Bash'`)
	if err != nil {
		t.Fatalf("query tool_decisions: %v", err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	var commands []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan tool_input_json: %v", err)
		}
		var ti bashToolInput
		if err := json.Unmarshal([]byte(raw), &ti); err != nil {
			continue
		}
		if ti.Command == "" || seen[ti.Command] {
			continue
		}
		seen[ti.Command] = true
		commands = append(commands, ti.Command)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tool_decisions: %v", err)
	}
	sort.Strings(commands)
	return commands
}

// TestCorpus measures the spike's coverage of the real decision corpus. See
// this file's top comment for how to run it, and the package doc comment on
// commandStat/evaluateForCorpus for exactly what "coverage" means here.
func TestCorpus(t *testing.T) {
	dbPath := os.Getenv("CETA_CORPUS_DB")
	if dbPath == "" {
		t.Skip("CETA_CORPUS_DB not set — this harness needs a real decision database; see this file's top comment for how to run it")
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("CETA_CORPUS_DB=%s: %v", dbPath, err)
	}

	db := openCorpusDB(t, dbPath)
	commands := distinctBashCommands(t, db)
	if len(commands) == 0 {
		t.Fatal("corpus db has zero distinct Bash commands — is tool_name really 'Bash' in this schema version?")
	}

	reg := cmddesc.DefaultRegistry()
	policies := DefaultPolicies()
	graphPolicies := DefaultGraphPolicies()
	cwd := t.TempDir()

	var (
		total            = len(commands)
		parseable        int
		fullyCovered     int
		fullyRegistered  int
		hasUnregistered  int
		decisionCounts   = map[string]int{}
		unregisteredFreq = map[string]int{}
		reasonFreq       = map[string]int{}
	)

	for _, cmd := range commands {
		st := evaluateForCorpus(cmd, cwd, reg, policies, graphPolicies)
		if st.parseable {
			parseable++
			decisionCounts[st.decision.String()]++
		}
		if st.fullyRegistered {
			fullyRegistered++
		} else {
			hasUnregistered++
			for _, b := range st.unregisteredBases {
				unregisteredFreq[b]++
			}
		}
		if st.fullyCovered {
			fullyCovered++
		} else if st.fullyRegistered && st.parseable && st.hasCommandNode {
			for _, r := range st.insufficientReason {
				reasonFreq[normalizeReason(r)]++
			}
		}
	}

	coverage := float64(fullyCovered) / float64(total)
	restrictedCoverage := 0.0
	if fullyRegistered > 0 {
		restrictedCoverage = float64(fullyCovered) / float64(fullyRegistered)
	}
	unregisteredFraction := float64(hasUnregistered) / float64(total)

	topBasenames := topN(unregisteredFreq, 30)
	topReasons := topN(reasonFreq, 30)

	var report strings.Builder
	fmt.Fprintf(&report, "CETA corpus coverage report\n")
	fmt.Fprintf(&report, "db: %s\n", dbPath)
	fmt.Fprintf(&report, "total distinct Bash commands: %d\n", total)
	fmt.Fprintf(&report, "parseable: %d (%.2f%%)\n", parseable, 100*float64(parseable)/float64(total))
	fmt.Fprintf(&report, "fully covered (every leaf registered, no node insufficient): %d (%.2f%% of total)\n", fullyCovered, 100*coverage)
	fmt.Fprintf(&report, "fully registered (every leaf has a schema): %d (%.2f%% of total)\n", fullyRegistered, 100*float64(fullyRegistered)/float64(total))
	fmt.Fprintf(&report, "coverage restricted to fully-registered commands: %d/%d (%.2f%%)\n", fullyCovered, fullyRegistered, 100*restrictedCoverage)
	fmt.Fprintf(&report, "has >=1 unregistered leaf: %d (%.2f%% of total)\n", hasUnregistered, 100*unregisteredFraction)
	fmt.Fprintf(&report, "\ntop %d unregistered basenames by occurrence:\n", len(topBasenames))
	for _, fc := range topBasenames {
		fmt.Fprintf(&report, "  %-30s %d\n", fc.Key, fc.Count)
	}
	fmt.Fprintf(&report, "\ntop %d insufficiency reasons (normalised) among fully-registered-but-insufficient commands:\n", len(topReasons))
	for _, fc := range topReasons {
		fmt.Fprintf(&report, "  %-6d %s\n", fc.Count, fc.Key)
	}
	fmt.Fprintf(&report, "\ndecision histogram (parseable commands only):\n")
	for _, d := range []string{evalcontract.Approve.String(), evalcontract.Reject.String(), evalcontract.Abstain.String()} {
		fmt.Fprintf(&report, "  %-10s %d\n", d, decisionCounts[d])
	}

	t.Log("\n" + report.String())

	if out := os.Getenv("CETA_CORPUS_REPORT"); out != "" {
		if err := os.WriteFile(out, []byte(report.String()), 0o644); err != nil {
			t.Fatalf("write CETA_CORPUS_REPORT=%s: %v", out, err)
		}
		t.Logf("report written to %s", out)
	}
}
