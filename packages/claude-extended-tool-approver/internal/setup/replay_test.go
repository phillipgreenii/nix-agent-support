package setup

// THE DIFFERENTIAL VERDICT REPLAY (ADR 0039's Enforcement item 5).
//
// Every migration step MUST publish a transition table, and the gate is NO
// TRANSITION IN THE LESS-RESTRICTIVE DIRECTION under
// `Approve < Abstain(NoOpinion) < Ask < Reject`. It is worded that way rather than as
// "toward approve" so that I1b's `Reject -> Abstain` FORFEITURE is caught rather than
// passing silently.
//
// This harness is the successor to the deleted `frontend_ab_test.go`, which compared
// the candidate seam against the outgoing front end IN THE SAME BINARY. That is no
// longer possible and no longer meaningful: the outgoing front end is deleted (I8
// forbids keeping it), so the comparison is now between TWO TREES. The harness
// therefore emits a per-row verdict FILE and the comparison is a diff of two runs —
// one on the base commit, one on the flip.
//
// ============================== READ-ONLY DISCIPLINE ==========================
//
// ADR 0039's Consequences: "Replay MUST be run offline. Hook mode writes the shared
// production decision log". Three rules, all mandatory:
//
//  1. The engine is built with `NewEngineForCWD` (this package's factory, which is
//     also why the harness lives HERE: internal/setup imports internal/engine, so a
//     test in the engine package cannot reach the factory without an import cycle)
//     and driven through
//     `EvaluateHook` — the same path the live hook takes for a real decision, so the
//     replay cannot drift from it — with NO asklog store attached.
//  2. `XDG_DATA_HOME` is redirected to a temp dir, so anything that resolves the
//     default asklog path cannot reach the production database.
//  3. `cmd_evaluate` MUST NOT be used. It opens the shared production asklog
//     READ-WRITE (bead pg2-cbihz). Neither may `baseline` or `compare`, for the same
//     reason: both call `asklog.NewStore(asklog.DefaultDBPath())`.
//
// `XDG_CONFIG_HOME` is deliberately NOT redirected. The `config-rules` rule reads its
// schema from there, so redirecting it would change verdicts and the replay would be
// measuring the redirect.
//
// ================================== RUNNING IT ================================
//
//	CETA_REPLAY_SNAPSHOT=/path/to/replay-rows.jsonl \
//	CETA_REPLAY_OUT=/path/to/verdicts.tsv \
//	  go test ./internal/setup/ -run TestCorpusVerdictReplay -timeout 6h -v
//
// The package is `./internal/setup/` — where this file lives, and where it MUST live for
// the reason given above (the engine factory is here). The path was recorded as
// `./internal/engine/` until pg2-wq3ki ran it: that spelling matches no test, so it exits
// 0 with "no tests to run" and a reader following it measures NOTHING while believing the
// replay ran.
//
// A SECOND TREE IS REQUIRED and the harness cannot supply it: the gate is a DIFF of two
// runs, so compile the test binary once per tree (`go test -c -o /scratch/<tree>.test
// ./internal/setup`) and run each against the SAME snapshot, then join the two output
// files on the row index.
//
// Each input line is `{"command": "...", "cwd": "...", "permission_mode": "..."}`.
// Extract it READ-ONLY from a `VACUUM INTO` snapshot opened with `?immutable=1`:
//
//	sqlite3 "file:$HOME/.local/share/claude-extended-tool-approver/asks.db?immutable=1" \
//	  "VACUUM INTO '/tmp/snap.db';"
//	sqlite3 -noheader /tmp/snap.db "SELECT json_object('command', c, 'cwd', w,
//	    'permission_mode', COALESCE(pm,'')) FROM (
//	    SELECT DISTINCT json_extract(tool_input_json,'\$.command') AS c, cwd AS w,
//	      permission_mode AS pm FROM tool_decisions
//	    WHERE excluded=0 AND tool_name='Bash'
//	      AND json_extract(tool_input_json,'\$.command') IS NOT NULL);" > replay-rows.jsonl
//
// Each output line is `<row-index>\t<decision>\t<module>`, so two runs are compared
// with a plain join on the row index. The decision and the module are BOTH emitted:
// the module is what makes a transition attributable to a cause rather than merely
// counted, which ADR 0039's Enforcement requires ("blanket annotation of transitions
// is FORBIDDEN — three beads in this chain shipped on a blanket plan and were wrong
// each time").
//
// ============================ SKIPS ARE NOT THE WHOLE ==========================
//
// About a third of corpus rows name a working directory that no longer exists and NO
// VERDICT CAN BE PRODUCED for them: the path evaluator classifies against a real
// filesystem. Those rows are emitted with the decision `skip-stale-cwd` and counted,
// and the replayable subset MUST NOT be presented as the whole.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/engine"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// Replay env vars.
const (
	ReplaySnapshotEnvVar = "CETA_REPLAY_SNAPSHOT"
	ReplayOutEnvVar      = "CETA_REPLAY_OUT"
)

// bashToolInput builds the `{"command": "..."}` tool input the hook receives.
func bashToolInput(t *testing.T, cmd string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(hookio.BashToolInput{Command: cmd})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	return b
}

type replayRow struct {
	Command        string `json:"command"`
	CWD            string `json:"cwd"`
	PermissionMode string `json:"permission_mode"`
}

// TestCorpusVerdictReplay emits one verdict per corpus row. It ASSERTS nothing about
// transitions — a transition is a property of TWO runs, and this harness is one of
// them; the gate is applied by diffing the two output files. What it does assert is
// that the replay itself is sound: every non-skipped row produces a decision, and no
// row panics.
func TestCorpusVerdictReplay(t *testing.T) {
	snapshot := os.Getenv(ReplaySnapshotEnvVar)
	out := os.Getenv(ReplayOutEnvVar)
	if snapshot == "" || out == "" {
		t.Skipf("set %s and %s to run the corpus verdict replay", ReplaySnapshotEnvVar, ReplayOutEnvVar)
	}

	// READ-ONLY DISCIPLINE, rule 2: nothing may resolve the production asklog path.
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	in, err := os.Open(snapshot) // #nosec G304 -- an operator-supplied local snapshot
	if err != nil {
		t.Fatalf("open %s: %v", snapshot, err)
	}
	defer func() { _ = in.Close() }()

	of, err := os.Create(out) // #nosec G304 -- an operator-supplied local output path
	if err != nil {
		t.Fatalf("create %s: %v", out, err)
	}
	defer func() { _ = of.Close() }()
	w := bufio.NewWriterSize(of, 1<<20)
	defer func() { _ = w.Flush() }()

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)

	// Engines are cached PER CWD. `setup.NewEngineForCWD` depends on nothing else, and
	// building one per row costs a settings load and a path-evaluator construction on
	// every one of ~190k rows. The cache is applied identically on both trees, so it
	// cannot bias the diff.
	engines := map[string]*engine.Engine{}
	cwdExists := map[string]bool{}

	var rows, replayed, skipped int
	counts := map[string]int{}

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec replayRow
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("row %d: %v", rows+1, err)
		}
		rows++

		ok, seen := cwdExists[rec.CWD]
		if !seen {
			st, statErr := os.Stat(rec.CWD)
			ok = statErr == nil && st.IsDir()
			cwdExists[rec.CWD] = ok
		}
		if !ok {
			skipped++
			counts["skip-stale-cwd"]++
			fmt.Fprintf(w, "%d\tskip-stale-cwd\t-\n", rows)
			continue
		}

		eng := engines[rec.CWD]
		if eng == nil {
			eng = NewEngineForCWD(rec.CWD)
			engines[rec.CWD] = eng
		}
		input := &hookio.HookInput{
			ToolName:       "Bash",
			ToolInput:      bashToolInput(t, rec.Command),
			CWD:            rec.CWD,
			PermissionMode: rec.PermissionMode,
			HookEventName:  "PreToolUse",
		}
		res := eng.EvaluateHook(input)
		module := res.Module
		if module == "" {
			module = "-"
		}
		replayed++
		counts[res.Decision.String()]++
		fmt.Fprintf(w, "%d\t%s\t%s\n", rows, res.Decision, module)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", snapshot, err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush %s: %v", out, err)
	}

	if replayed == 0 {
		t.Fatalf("no row was replayable; the snapshot or the cwd set is wrong")
	}
	t.Logf("REPLAY: %d rows, %d replayed, %d skipped (stale cwd) across %d distinct cwds; decisions=%v",
		rows, replayed, skipped, len(cwdExists), counts)
}
