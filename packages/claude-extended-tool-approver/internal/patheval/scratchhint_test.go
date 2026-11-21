package patheval_test

// Creating a new top-level directory under ~/.cache, through the real rule chain
// (setup.RuleChain, the same list the hook runs), with a fixture home that holds the
// tool caches the machine really has.
//
// Verdicts of the real approver BEFORE this change, recorded 2026-11-21 (the
// binary built from the parent commit, run as a PreToolUse hook with no CETA_EXTRA_*
// environment): `mkdir ~/.cache/foo` abstained ("safe-commands: mkdir references
// non-writable path ~/.cache/foo (deferred to claude-code)") and a Write to
// ~/.cache/foo/x abstained ("path-safety: path access unknown: ~/.cache/foo/x
// (deferred to claude-code)"). So the guard needs no new classification: the cache
// directory is already neither temp nor read-write, and the scratch root is already
// read-write through the machine's extraTempRoots/extraReadWriteRoots (lib/tmp-root.nix).
// What was missing is the reason text: an unattended session turns an abstain into a
// deny with only that text to go on, and "non-writable path" did not say where scratch
// belongs, so an agent retried the same write. These cases pin that the reason names
// CLAUDE_CODE_TMPDIR and its value (read from the process environment, not hardcoded).
//
// The guard sees only paths a command or tool names; a tool that creates
// ~/.cache/<x> by itself (uv, colima) names no path and is out of its reach.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/engine"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/setup"
)

func scratchEngine(t *testing.T) (eng *engine.Engine, home, scratch string) {
	t.Helper()
	// The home must NOT lie under a temporary root (a t.TempDir home would make
	// ~/.cache temp, and redirections into it would be approved as disposable); it
	// need not exist, the evaluator resolves through the nearest existing ancestor.
	home = "/nonexistent-ceta-scratch-home"
	scratch = patheval.ResolveRealPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("WORKSPACE_ROOT", "")
	t.Setenv("GRADLE_USER_HOME", "")
	t.Setenv("CETA_EXTRA_READONLY_ROOTS", "")
	t.Setenv("CETA_EXTRA_READONLY_PREFIX_ROOTS", "")
	t.Setenv("CETA_EXTRA_TEMP_ROOTS", scratch)
	t.Setenv("CETA_EXTRA_READWRITE_ROOTS", scratch)
	t.Setenv(patheval.ClaudeCodeTmpdirEnv, scratch)
	pe := patheval.NewWithCWD(searchProject, searchProject)
	eng = engine.New()
	eng.SetPathEvaluator(pe)
	eng.RegisterRules(setup.RuleChain(eng, pe, configrules.Load(filepath.Join(home, "none.json")), nil, nil)...)
	return eng, home, scratch
}

func TestCacheScratchGuard_ReasonNamesClaudeCodeTmpdir(t *testing.T) {
	eng, home, scratch := scratchEngine(t)
	cache := filepath.Join(home, ".cache")

	type row struct {
		name, tool, input string
		approve           bool
	}
	rows := []row{
		{"tool cache mkdir", "Bash", "mkdir " + cache + "/uv/wheels", false},
		{"tool cache Write", "Write", cache + "/colima/x", false},
		{"new scratch name mkdir", "Bash", "mkdir " + cache + "/foo", false},
		{"new scratch name mkdir tilde", "Bash", "mkdir ~/.cache/foo", false},
		{"new scratch name mkdir -p", "Bash", "mkdir -p " + cache + "/foo/bar", false},
		{"new scratch name Write", "Write", cache + "/foo/x", false},
		{"heredoc write", "Bash", "cat > " + cache + "/foo/x <<'EOF'\nhi\nEOF", false},
		{"redirection write", "Bash", "echo hi > " + cache + "/foo/x", false},
		{"scratch root mkdir", "Bash", "mkdir " + scratch + "/foo", true},
		{"scratch root mkdir -p", "Bash", "mkdir -p " + scratch + "/foo/bar", true},
		{"scratch root Write", "Write", scratch + "/foo/x", true},
		{"scratch root heredoc", "Bash", "cat > " + scratch + "/x <<'EOF'\nhi\nEOF", true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			var raw string
			if r.tool == "Bash" {
				raw = string(jsonCommand(r.input))
			} else {
				raw = `{"file_path":` + jsonString(r.input) + `,"content":"hi"}`
			}
			got := eng.EvaluateHook(&hookio.HookInput{
				ToolName: r.tool, ToolInput: []byte(raw), CWD: searchProject,
			})
			if r.approve {
				if got.Decision != hookio.Approve {
					t.Fatalf("%s: %s (%s), want approve", r.input, got.Decision, got.Reason)
				}
				return
			}
			if got.Decision == hookio.Approve {
				t.Fatalf("%s: approved (%s), want not approved", r.input, got.Reason)
			}
			if want := patheval.ClaudeCodeTmpdirEnv + "=" + scratch; !strings.Contains(got.Reason, want) {
				t.Fatalf("%s: reason %q does not contain %q", r.input, got.Reason, want)
			}
		})
	}
}

// TestCacheScratchGuard_OtherPathsKeepTheirReason: the hint is only for the cache
// directory; every other refusal reason is unchanged.
func TestCacheScratchGuard_OtherPathsKeepTheirReason(t *testing.T) {
	eng, home, _ := scratchEngine(t)
	for _, p := range []string{home + "/.local/foo", home + "/cachefoo/x", "/etc/foo"} {
		got := eng.EvaluateHook(&hookio.HookInput{
			ToolName: "Write", ToolInput: []byte(`{"file_path":` + jsonString(p) + `,"content":"hi"}`), CWD: searchProject,
		})
		if strings.Contains(got.Reason, patheval.ClaudeCodeTmpdirEnv) {
			t.Errorf("%s: reason %q names CLAUDE_CODE_TMPDIR but the path is not under the cache directory", p, got.Reason)
		}
	}
}

// TestScratchHint_Unset: with the variable unset the reason still points away from
// the cache directory and says why it cannot name a value.
func TestScratchHint_Unset(t *testing.T) {
	_, home, _ := scratchEngine(t)
	t.Setenv(patheval.ClaudeCodeTmpdirEnv, "")
	pe := patheval.NewWithCWD(searchProject, searchProject)
	h := pe.ScratchHint(filepath.Join(home, ".cache", "foo"))
	if !strings.Contains(h, patheval.ClaudeCodeTmpdirEnv) || !strings.Contains(h, "not set") {
		t.Errorf("hint %q", h)
	}
	if pe.ScratchHint(filepath.Join(home, "elsewhere")) != "" {
		t.Errorf("hint for a path outside the cache directory must be empty")
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func jsonCommand(cmd string) []byte {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}
