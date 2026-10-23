package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router/conformance"
)

// originFixture writes a launch config whose one watched origin's repoRoot is
// repoRoot, K=1 so a single failed probe gates, and returns the config path, the
// state dir, and the log of every call to the fake `bd`/`ccpool` binaries.
// PATH is narrowed to that directory so a call that escaped the gate is visible.
func originFixture(t *testing.T, repoRoot string, extra ...map[string]any) (cfgPath, stateDir, callLog string) {
	t.Helper()
	dir := t.TempDir()
	stateDir = filepath.Join(dir, "state")
	callLog = filepath.Join(dir, "calls.log")
	scripts := map[string]string{}
	for _, name := range []string{"bd", "ccpool"} {
		scripts[name] = fmt.Sprintf("echo %s \"$@\" >> %s\nexit 1\n", name, callLog)
	}
	t.Setenv("PATH", stubBin(t, scripts))

	origins := []map[string]any{{"key": "git.example.test/o/a", "repoRoot": repoRoot}}
	origins = append(origins, extra...)
	cfg := map[string]any{
		"repoRoot": repoRoot,
		"originProbe": map[string]any{
			"origins": origins, "failureThreshold": 1, "ttl": 60e9, "timeout": 5e9, "stateDir": stateDir,
		},
	}
	b, _ := json.Marshal(cfg)
	cfgPath = filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return cfgPath, stateDir, callLog
}

// workerWithIsolation is a worker role whose isolation table is the given JSON.
func workerWithIsolation(t *testing.T, isolation string) roleUnderTest {
	t.Helper()
	return roleUnderTest{"worker", configFrom(t, fmt.Sprintf(`{"ccpool":{"actor":"a","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"hi","isolation":%s}}`, isolation))}
}

// A gated origin declines with exit 9 and reason origin-unavailable, and makes
// ZERO bd/ccpool calls (INV-CCH-10).
func TestDispatch_gatedOriginDeclinesWithoutBeadsCalls(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "unmounted", "repo") // step 1 fails: mount-missing
	cfgPath, stateDir, callLog := originFixture(t, missing)
	r := workerWithIsolation(t, `{}`)
	code, out := dispatchWith(t, r.name, r.cfg, cfgPath, defaultEvent)
	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want ExitBusy (%d); stdout=%q", code, conformance.ExitBusy, out)
	}
	if reply := decodeReply(t, out); reply["reason"] != "origin-unavailable" {
		t.Fatalf("reply = %q, want reason origin-unavailable", out)
	}
	if _, err := os.Stat(callLog); err == nil {
		b, _ := os.ReadFile(callLog)
		t.Fatalf("a decline must make no bd/ccpool call, saw:\n%s", b)
	}
	// The origin key is in the state file, never in the wire reason.
	if strings.Contains(out, "git.example.test") {
		t.Errorf("origin key leaked into the wire reason: %s", out)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "origin-state", "git.example.test__o__a.json")); err != nil {
		t.Errorf("state file not written: %v", err)
	}
}

// A role with no resolvable origin is never gated, even while origin A is.
func TestDispatch_roleWithoutOriginIsNeverGated(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "unmounted", "repo")
	cfgPath, _, _ := originFixture(t, missing)
	// A workforest role has no single repo root, so it has no origin. It gets past
	// the gate and reaches the (fake, failing) ccpool, i.e. it does NOT return the
	// origin-unavailable decline.
	r := workerWithIsolation(t, `{"type":"workforest"}`)
	_, out := dispatchWith(t, r.name, r.cfg, cfgPath, defaultEvent)
	if strings.Contains(out, "origin-unavailable") {
		t.Fatalf("role with no origin was gated: %s", out)
	}
}

func TestDispatchRepoRoot(t *testing.T) {
	cfg := config.Default()
	cfg.RepoRoot = "/r"
	ccp := func(typ, path string) roles.Role {
		return roles.Role{CCPool: &roles.CCPoolConfig{Isolation: roles.IsolationConfig{Type: typ, Path: path}}}
	}
	cases := []struct {
		name string
		role roles.Role
		want string
	}{
		{"default isolation", ccp("", ""), "/r"},
		{"worktree", ccp("worktree", ""), "/r"},
		{"none", ccp("none", ""), "/r"},
		{"path", ccp("path", "/fixed"), "/fixed"},
		{"workforest", ccp("workforest", ""), ""},
		{"command role", roles.Role{Command: &roles.CommandConfig{}}, ""},
	}
	for _, tc := range cases {
		if got := dispatchRepoRoot(tc.role, cfg); got != tc.want {
			t.Errorf("%s: dispatchRepoRoot = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestOrigin_statusGoldenAndKillSwitch(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "unmounted", "repo")
	cfgPath, stateDir, _ := originFixture(t, missing)

	var out, errb bytes.Buffer
	if code := runOriginTo([]string{"status", "--config", cfgPath}, &out, &errb); code != conformance.ExitOK {
		t.Fatalf("status exit = %d stderr=%s", code, errb.String())
	}
	wantUnchecked := "KEY                   CLASS      GATED  IGNORED  SINCE  FAILURES  LAST_ERROR\n" +
		"git.example.test/o/a  unchecked  false  false    -      0         -\n"
	if out.String() != wantUnchecked {
		t.Fatalf("status (unchecked)\n got:\n%s\nwant:\n%s", out.String(), wantUnchecked)
	}

	out.Reset()
	if code := runOriginTo([]string{"ignore", "--config", cfgPath, "git.example.test/o/a"}, &out, &errb); code != conformance.ExitOK {
		t.Fatalf("ignore exit = %d stderr=%s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(stateDir, "origin-state", "git.example.test__o__a.disable")); err != nil {
		t.Fatalf("kill-switch file missing: %v", err)
	}
	// With the kill switch on, a dispatch is never declined by the origin gate.
	r := workerWithIsolation(t, `{}`)
	if _, body := dispatchWith(t, r.name, r.cfg, cfgPath, defaultEvent); strings.Contains(body, "origin-unavailable") {
		t.Fatalf("ignored origin still declined: %s", body)
	}

	out.Reset()
	if code := runOriginTo([]string{"status", "--config", cfgPath}, &out, &errb); code != conformance.ExitOK {
		t.Fatalf("status exit = %d", code)
	}
	if !strings.Contains(out.String(), "unchecked  false  true") {
		t.Fatalf("status must show ignored=true:\n%s", out.String())
	}

	out.Reset()
	if code := runOriginTo([]string{"unignore", "--config", cfgPath, "git.example.test/o/a"}, &out, &errb); code != conformance.ExitOK {
		t.Fatalf("unignore exit = %d", code)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "origin-state", "git.example.test__o__a.disable")); err == nil {
		t.Fatal("kill-switch file must be removed by unignore")
	}
}

func TestOrigin_usageErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "x")
	cfgPath, _, _ := originFixture(t, missing)
	for name, args := range map[string][]string{
		"no verb":          {},
		"unknown verb":     {"pause", "--config", cfgPath},
		"ignore no key":    {"ignore", "--config", cfgPath},
		"ignore unwatched": {"ignore", "--config", cfgPath, "git.example.test/o/nope"},
		"traversal key":    {"ignore", "--config", cfgPath, "../../etc/passwd"},
	} {
		var out, errb bytes.Buffer
		if code := runOriginTo(args, &out, &errb); code != conformance.ExitUsage {
			t.Errorf("%s: exit = %d, want ExitUsage (%d)", name, code, conformance.ExitUsage)
		}
	}
}
