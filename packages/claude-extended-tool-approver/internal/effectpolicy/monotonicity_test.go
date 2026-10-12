// monotonicity_test.go - tc-o14i5.3.15 (Phase 2m, design item m).
//
// TestVerdictMonotonicity is the property test "verdict(X; Y) is never looser
// than the stricter of X and Y", over pairs drawn from the golden corpus
// (internal/goldencorpus/testdata/corpus.json).
//
// Terminology (the packet's own reading; the corpus shape supports it): a
// GOLDEN PAIR is two Bash rows (X, Y) of the corpus; verdict(X; Y) is the
// decision effectpolicy.Evaluate returns when X and Y are written as two
// consecutive lines of ONE command ("X\nY"), so their effects are folded
// together as a single composite graph. Strictness, most to least strict, is
// Reject > Abstain > Approve (P1's fail-closed fold). The property: the joint
// verdict's strictness is >= max(strictness(verdict(X)), strictness(verdict(Y))).
// Equivalently: a Reject anywhere forces a joint Reject, and an Abstain
// anywhere forces a joint Abstain-or-Reject, never an Approve.
//
// Why newline and not ";": a trailing "# comment" on X would swallow a ";"
// and silently drop Y from the joint command, which is a property of shell
// syntax, not an engine defect. A newline always starts a new simple command.
//
// Scope: the corpus rows that share the default path state (cwd and project
// root /repo) and an EMPTY rules/config fixture, so every row is judged under
// the same policy context and a pair is comparable. Rows with an operator
// fixture (vetted hosts, kube contexts, ...) are excluded because X and Y
// would not share one configuration. This is stated here and not silently
// filtered.
//
// Size: pairs are drawn deterministically (a fixed-seed 64-bit LCG over the
// ordered pairs of distinct rows, so both X;Y and Y;X orders occur). The local
// default is monotonicityDefaultPairs (10^4, the exit criterion); a larger sweep:
//
//	CETA_MONOTONICITY_PAIRS=50000 go test ./internal/effectpolicy/ \
//	    -run TestVerdictMonotonicity -v
package effectpolicy

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/pathspec"
)

// monotonicityDefaultPairs is the default pair count: the Phase 2 exit
// criterion's 10^4 (about 10s of CPU; measured 9.4s for 10^4 on the dev host).
// CETA_MONOTONICITY_PAIRS overrides it (a smaller number for a quick local
// run, a larger one for a deeper sweep).
const monotonicityDefaultPairs = 10000

// strictness orders decisions: Approve < Abstain < Reject.
func strictness(d evalcontract.Decision) int {
	switch d {
	case evalcontract.Reject:
		return 2
	case evalcontract.Abstain:
		return 1
	default:
		return 0
	}
}

// monotonicityCommands returns the Bash commands of the corpus rows that share
// the default path state and carry no operator fixture (see the file comment).
func monotonicityCommands(t testing.TB) (cmds []string, cwd, root string) {
	t.Helper()
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("reading %s: %v", corpusPath, err)
	}
	var rows []goldencorpus.Row
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("unmarshalling %s: %v", corpusPath, err)
	}
	for _, r := range rows {
		if r.ToolInput.ToolName != "Bash" || !reflect.DeepEqual(r.RulesConfig, goldencorpus.RulesConfigFixture{}) {
			continue
		}
		ps := r.CWDPathState
		if ps.CWD != "/repo" || ps.ProjectRoot != "/repo" || len(ps.GitTracked) > 0 || len(ps.Gitignored) > 0 || ps.WorktreeState != "" || ps.Home != "" {
			continue
		}
		if c, _ := r.ToolInput.ToolInput["command"].(string); c != "" {
			cmds = append(cmds, c)
		}
	}
	return cmds, "/repo", "/repo"
}

func TestVerdictMonotonicity(t *testing.T) {
	cmds, cwd, root := monotonicityCommands(t)
	if len(cmds) < 100 {
		t.Fatalf("only %d comparable corpus rows; the corpus shape changed - revisit this test's scope", len(cmds))
	}

	// Hermetic: no real git, no real user config.
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(pathspec.SetWorktreeStateProbe(func(string) (pathspec.WorktreeState, error) {
		return pathspec.WorktreeUnknown, fmt.Errorf("monotonicity test: no real git")
	}))
	t.Cleanup(pathspec.SetGitTrackedProbe(func(string, string, bool) (bool, error) { return false, nil }))

	pairs := monotonicityDefaultPairs
	if v := os.Getenv("CETA_MONOTONICITY_PAIRS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("CETA_MONOTONICITY_PAIRS=%q: want a positive integer", v)
		}
		pairs = n
	}

	reg := cmddesc.DefaultRegistry()
	policies := DefaultPolicies()
	graphPolicies := DefaultGraphPolicies()
	eval := func(command string) evalcontract.Decision {
		return Evaluate(evalcontract.Request{Command: command, CWD: cwd, ProjectRoot: root}, reg, policies, graphPolicies).Decision
	}

	single := make([]evalcontract.Decision, len(cmds))
	for i, c := range cmds {
		single[i] = eval(c)
	}
	hist := map[evalcontract.Decision]int{}
	for _, d := range single {
		hist[d]++
	}
	for _, d := range []evalcontract.Decision{evalcontract.Approve, evalcontract.Abstain, evalcontract.Reject} {
		if hist[d] == 0 {
			t.Fatalf("no corpus row evaluates to %s; the property would be vacuous for that class", d)
		}
	}

	n := uint64(len(cmds))
	state := uint64(0x9E3779B97F4A7C15) // fixed seed: the sample is reproducible
	next := func() uint64 {
		state = state*6364136223846793005 + 1442695040888963407
		return state >> 33
	}
	seen := map[[2]int]bool{}
	checked := 0
	var tighter, equal int
	for checked < pairs {
		i, j := int(next()%n), int(next()%n)
		if i == j || seen[[2]int{i, j}] {
			continue
		}
		seen[[2]int{i, j}] = true
		checked++

		joint := eval(cmds[i] + "\n" + cmds[j])
		want := max(strictness(single[i]), strictness(single[j]))
		got := strictness(joint)
		if got < want {
			t.Errorf("verdict(X; Y) = %s is looser than the stricter of X=%s, Y=%s\n  X: %q\n  Y: %q",
				joint, single[i], single[j], cmds[i], cmds[j])
		}
		if got > want {
			tighter++
		} else {
			equal++
		}
	}
	t.Logf("checked %d ordered pairs from %d comparable corpus rows: joint == stricter for %d, strictly tighter for %d (single-row verdicts: %v)", checked, len(cmds), equal, tighter, hist)
}
