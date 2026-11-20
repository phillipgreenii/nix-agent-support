package pggwmworktree

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d = filepath.Join(d, "zr")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(context.Background(), "git", "-C", d, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return d
}

// eval runs the rule and reports whether it applied (a not-applicable result
// comes back as an error that hookio.Verdict folds into a plain approve).
func eval(r *Rule, in *hookio.HookInput) (hookio.RuleResult, bool) {
	res, err := r.Evaluate(in)
	if err != nil {
		return hookio.RuleResult{}, false
	}
	return res, true
}

func bashInput(cwd, cmd string) *hookio.HookInput {
	ti, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return &hookio.HookInput{SessionID: "sess-9", CWD: cwd, ToolName: "Bash", ToolInput: ti}
}

func writeCfg(t *testing.T, repo string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("[repos.\""+repo+"\"]\nmin = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExceptionIsAllowedLoggedAndCounted(t *testing.T) {
	repo := gitRepo(t)
	state := t.TempDir()
	fixed := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	r := NewWith(writeCfg(t, repo), state, nil, nil, func() time.Time { return fixed })

	if res, applied := eval(r, bashInput(repo, `PG_GWM_EXCEPTION='needs a raw tree' git worktree add /tmp/x -b b`)); applied {
		t.Fatalf("got %v; want the rule to step aside", res)
	}
	raw, err := os.ReadFile(filepath.Join(state, obsDirName, logFileName))
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]string
	if err := json.Unmarshal(bytes.TrimSpace(raw), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v: %s", err, raw)
	}
	want := map[string]string{
		"ts": "2026-11-20T01:02:03Z", "session_id": "sess-9", "tool": "git worktree add",
		"repo": repo, "reason": "needs a raw tree", "cwd": repo,
	}
	if len(rec) != len(want) {
		t.Errorf("log fields = %v, want exactly %v", rec, want)
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("log[%s] = %q, want %q", k, rec[k], v)
		}
	}
	c := ReadCounters(state)
	if c.Exceptions["git worktree add"] != 1 || c.Denies != 0 {
		t.Errorf("counters = %+v", c)
	}

	// A second exception and a deny both move their counters.
	_, _ = eval(r, bashInput(repo, `PG_GWM_EXCEPTION=again bin/worktree create f`))
	deny, _ := eval(r, bashInput(repo, `git worktree add /tmp/y -b c`))
	if deny.Decision != hookio.Reject {
		t.Fatalf("unmarked add = %v, want Reject", deny.Decision)
	}
	c = ReadCounters(state)
	if c.Exceptions["bin/worktree create"] != 1 || c.Exceptions["git worktree add"] != 1 || c.Denies != 1 {
		t.Errorf("counters = %+v", c)
	}
}

func TestEmptyReasonIsADenyAndLogsNothing(t *testing.T) {
	repo := gitRepo(t)
	state := t.TempDir()
	r := NewWith(writeCfg(t, repo), state, nil, nil, nil)
	for _, cmd := range []string{`PG_GWM_EXCEPTION= git worktree add /tmp/x`, `PG_GWM_EXCEPTION='  ' git worktree add /tmp/x`} {
		res, _ := eval(r, bashInput(repo, cmd))
		if res.Decision != hookio.Reject || !strings.Contains(res.Reason, "reason is empty") {
			t.Errorf("%q: got %v (%s), want Reject naming the empty reason", cmd, res.Decision, res.Reason)
		}
	}
	if _, err := os.Stat(filepath.Join(state, obsDirName, logFileName)); err == nil {
		t.Error("an empty-reason deny must not write an exception log line")
	}
}

func TestUnreadableConfigFailsOpenWithWarning(t *testing.T) {
	repo := gitRepo(t)
	cfgDir := filepath.Join(t.TempDir(), "config.toml")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	r := NewWith(cfgDir, t.TempDir(), nil, &warn, nil)
	if res, applied := eval(r, bashInput(repo, `git worktree add /tmp/x -b b`)); applied {
		t.Fatalf("got %v; want fail open", res)
	}
	if !strings.Contains(warn.String(), "warning") || !strings.Contains(warn.String(), "failing open") {
		t.Errorf("warning = %q", warn.String())
	}
}

func TestMissingConfigIsSilentlyAllowed(t *testing.T) {
	repo := gitRepo(t)
	var warn bytes.Buffer
	r := NewWith(filepath.Join(t.TempDir(), "absent.toml"), "", nil, &warn, nil)
	if res, applied := eval(r, bashInput(repo, `git worktree add /tmp/x -b b`)); applied || warn.Len() != 0 {
		t.Errorf("missing config: got %v, warning %q; want allowed and silent", res, warn.String())
	}
}

func TestSessionEnvMarkerForEnterWorktree(t *testing.T) {
	repo := gitRepo(t)
	env := map[string]string{}
	r := NewWith(writeCfg(t, repo), t.TempDir(), func(k string) string { return env[k] }, nil, nil)
	in := &hookio.HookInput{SessionID: "s", CWD: repo, ToolName: "EnterWorktree", ToolInput: []byte(`{}`)}
	res, _ := eval(r, in)
	if res.Decision != hookio.Reject || !strings.Contains(res.Reason, "session environment") {
		t.Fatalf("got %v (%s), want a deny naming the session environment", res.Decision, res.Reason)
	}
	env[ExceptionEnv] = "operator ruling"
	if res, applied := eval(r, in); applied {
		t.Fatalf("with the session marker got %v", res)
	}
}

func TestParsesConfigShapes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	body := `# c
[repos."/a/b"]
min = 2
[repos."/a/b".other]
x = 1

[[repos."/a/b".exceptions]]
path = "/a/slot-*"
reason = "r"
owner = 'o'
review-by = "2027-01-01"

[[repos."/a/b".exceptions]]
path = "/a/**/tool"
reason = "r2" # trailing
owner = "o"
review-by = "2027-01-01"

[repos.'/c']
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	ex, ok := cfg.repos["/a/b"]
	if !ok || len(ex) != 2 || ex[0].Owner != "o" || ex[1].Reason != "r2" {
		t.Fatalf("repos = %+v", cfg.repos)
	}
	if _, ok := cfg.repos["/c"]; !ok {
		t.Error("single-quoted key not read")
	}
	if !ex[0].matches("/a/slot-a") || ex[0].matches("/a/x/slot-a") || !ex[1].matches("/a/x/y/tool") {
		t.Error("glob semantics differ from pg-gwm's")
	}
}
