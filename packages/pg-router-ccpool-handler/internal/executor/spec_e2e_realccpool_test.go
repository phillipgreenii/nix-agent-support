package executor

// End-to-end test of the pg-wrap spec chain under REAL tmux (pg-wrap design
// ruling 30; docket pg2-12gr4.8, V-3: a fake of the tool under test does not
// count). One pool runs the module-RENDERED strict specs: its `claude.bin` is
// the fixture's real `pg-wrap-exec`, each session's PG_WRAP_SPEC names a spec
// file the nix module rendered, and the spec's `exec` is a probe that records
// the env the strict spec left it and then runs the fake-claude stub. The stub
// fires the REAL `ccpool hook` resolved through the strict PATH
// (`claudeBaseline`'s), so a session that reaches ready proves the baseline is
// sufficient. The cases:
//
//  1. `ccpool new` reaches ready under the rendered strict spec;
//  2. two roles with DIFFERENT specs in ONE pool are both live, each payload
//     seeing its own spec's env;
//  3. breaking one role's spec makes only that role's dispatch decline
//     (launch-failed) while the other stays live;
//  4. a spec file missing at runtime is a launch decline;
//  5. a resume after a spec change runs the new spec;
//  6. (opt-in, PG_ROUTER_CCPOOL_HANDLER_E2E_REAL_CLAUDE=1) the real claude
//     reaches ready under the real strict spec, plugin script hooks included;
//     fake-claude runs no plugin hooks, so only a real claude can show that.
//
// The fixture is the nix check `pg-router-ccpool-handler-spec-fixtures`, whose
// out path arrives in PG_ROUTER_CCPOOL_HANDLER_SPEC_FIXTURE. Without it the
// test skips with one line, unless PG_ROUTER_CCPOOL_HANDLER_REQUIRE_CCPOOL is
// set (the nix check sets it), which turns the skip into a failure. A missing
// ccpool or tmux is handled by newRealSandbox the same way. The sandbox
// harness is prior_sessions_realccpool_test.go's: its own tmux servers under a
// sandbox TMUX_TMPDIR, never the operator's.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
)

const (
	// specFixtureEnv names the fixture's out path.
	specFixtureEnv = "PG_ROUTER_CCPOOL_HANDLER_SPEC_FIXTURE"
	// specRealClaudeEnv opts case 6 (a real, logged-in claude) in.
	specRealClaudeEnv = "PG_ROUTER_CCPOOL_HANDLER_E2E_REAL_CLAUDE"
)

// tomlLineValue returns the value of the first line `<key> = "<value>"` in a
// TOML text as pkgs.formats.toml renders it (no TOML library is needed).
func tomlLineValue(t *testing.T, text, key string) string {
	t.Helper()
	prefix := key + " = \""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			if v, ok := strings.CutSuffix(rest, "\""); ok {
				return v
			}
		}
	}
	t.Fatalf("no %s line in the fixture's pool config:\n%s", key, text)
	return ""
}

// specPool is pool with the fixture's `claude.bin` (pg-wrap-exec) and
// `plugin_dir`, and the fixture's 30 s wait for ready. A real claude with
// plugins may need more than the harness's 8 s; the fake is ready in well under
// a second and the failing cases end at once through ccpool's fail-fast wait,
// so the longer bound costs the other cases nothing. The fixture's tmux socket
// is not copied: the sandbox pool runs on ccpool's pool-mode derived socket.
func (sb *realSandbox) specPool(t *testing.T, fixture string) *realPool {
	t.Helper()
	p := sb.pool(t)
	raw, err := os.ReadFile(filepath.Join(fixture, "pools", "handler", "config.toml"))
	if err != nil {
		t.Fatalf("read the fixture's handler pool config: %v", err)
	}
	text := string(raw)
	bin := tomlLineValue(t, text, "bin")
	pluginDir := tomlLineValue(t, text, "plugin_dir")
	if !strings.HasSuffix(bin, "/bin/pg-wrap-exec") {
		t.Fatalf("the fixture's claude.bin must be pg-wrap-exec; got %q", bin)
	}
	poolCfg := fmt.Sprintf(`[pool]
max_sessions = 50
auto_reap = false
[claude]
bin = %q
plugin_dir = %q
[wait]
timeout = "30s"
[notify]
adapter = "none"
[usage_gate]
enabled = false
[retry]
enabled = false
`, bin, pluginDir)
	if err := os.WriteFile(filepath.Join(p.poolDir, "config.toml"), []byte(poolCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// envLines reads a probe's env dump (one NAME=value per line).
func envLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the payload's env dump: %v", err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

func hasEnv(lines []string, line string) bool {
	for _, l := range lines {
		if l == line {
			return true
		}
	}
	return false
}

func hasEnvName(lines []string, name string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, name+"=") {
			return true
		}
	}
	return false
}

func TestSpecE2E(t *testing.T) {
	fixture := os.Getenv(specFixtureEnv)
	if fixture == "" {
		msg := specFixtureEnv + " is not set (the nix check pg-router-ccpool-handler-spec-fixtures output)"
		if os.Getenv(requireRealCCPoolEnv) != "" {
			t.Fatal(msg + "; " + requireRealCCPoolEnv + " is set, so this is a failure, not a skip")
		}
		t.Skip(msg)
	}
	realHome := os.Getenv("HOME") // before newRealSandbox points HOME into the sandbox: case 6's login

	sb := newRealSandbox(t)
	p := sb.specPool(t, fixture)
	d := t.TempDir()   // the probes' env dumps, shared by every case
	tmp := t.TempDir() // hand-made spec files

	// The strict spec keeps neither FAKE_CLAUDE_TRANSCRIPT nor FAKE_CLAUDE_DIR,
	// so the stub writes its transcripts under /tmp, outside the sandbox.
	t.Cleanup(func() {
		for _, s := range p.list() {
			if s.TranscriptPath != "" && strings.HasPrefix(filepath.Base(s.TranscriptPath), "fake-claude-") {
				_ = os.Remove(s.TranscriptPath)
			}
		}
	})

	specOf := func(role string) string { return filepath.Join(fixture, "specs", role+".toml") }
	// env is the item-context part of what the adapter's launch sends.
	env := func(role, item, spec string) map[string]string {
		return map[string]string{
			"PG_WRAP_SPEC": spec, "K20_ENV_DIR": d, "PG_ROUTER_ROLE": role,
			"PG_ROUTER_ITEM_ID": item, "PG_ROUTER_ITEM_TYPE": "task",
		}
	}

	t.Run("case1_ready", func(t *testing.T) {
		if err := p.launch("k20-a-1", "k20-a-1", nil, env("k20-a", "k20-item-a1", specOf("k20-a"))); err != nil {
			t.Fatalf("ccpool new under the rendered strict spec must reach ready: %v", err)
		}
		p.wantState("k20-a-1", ccpool.StateReady, true)
	})
	if t.Failed() {
		t.Fatal("the later cases use the session case 1 started")
	}

	t.Run("case2_two_specs", func(t *testing.T) {
		if err := p.launch("k20-b-1", "k20-b-1", nil, env("k20-b", "k20-item-b1", specOf("k20-b"))); err != nil {
			t.Fatalf("launch k20-b-1: %v", err)
		}
		p.wantState("k20-b-1", ccpool.StateReady, true)
		for _, id := range []string{"k20-a-1", "k20-b-1"} {
			if s, ok := p.row(id); !ok || !s.Live {
				t.Errorf("both roles must be live in the one pool; %s: present=%v %+v", id, ok, s)
			}
		}
		for _, c := range []struct{ role, spec string }{{"k20-a", "a"}, {"k20-b", "b"}} {
			lines := envLines(t, filepath.Join(d, c.role+".env"))
			if !hasEnv(lines, "K20_SPEC="+c.spec) {
				t.Errorf("%s's payload must see its own spec's K20_SPEC=%s; got %v", c.role, c.spec, lines)
			}
			if !hasEnv(lines, "PG_ROUTER_ROLE="+c.role) {
				t.Errorf("%s's payload must see PG_ROUTER_ROLE=%s; got %v", c.role, c.role, lines)
			}
			// A spot-check, not an allowlist: the shell's own PWD, SHLVL and _
			// may appear.
			for _, name := range []string{"CCPOOL_POOL", "HOME"} {
				if !hasEnvName(lines, name) {
					t.Errorf("%s's payload must keep %s; got %v", c.role, name, lines)
				}
			}
			for _, name := range []string{"PG_WRAP_SPEC", "CCPOOL_BIN", "XDG_STATE_HOME"} {
				if hasEnvName(lines, name) {
					t.Errorf("%s's payload must not see %s under the strict spec; got %v", c.role, name, lines)
				}
			}
		}
	})

	// decline dispatches the k20-a role through the real adapter with the given
	// spec and expects the launch decline.
	decline := func(t *testing.T, spec, item, ext string) {
		t.Helper()
		role := realRole("k20-a")
		role.SessionEnv = map[string]string{"PG_WRAP_SPEC": spec, "K20_ENV_DIR": d}
		_, err := p.dispatch(role, item, "", "", ext)
		if !errors.Is(err, ErrLaunchFailed) {
			t.Fatalf("a spec pg-wrap-exec cannot load must decline launch-failed (reason launch-failed, kind fault); got %v", err)
		}
		// The adapter purges the abandoned session, so a row that remains is
		// not live.
		if s, ok := p.row(ext); ok && s.Live {
			t.Errorf("the declined launch must not leave a live session; %+v", s)
		}
	}
	stillLive := func(t *testing.T, ids ...string) {
		t.Helper()
		for _, id := range ids {
			if s, ok := p.row(id); !ok || !s.Live {
				t.Errorf("%s must be unaffected by another role's decline; present=%v %+v", id, ok, s)
			}
		}
	}

	t.Run("case3_broken_spec", func(t *testing.T) {
		// A relative first wrap word is rejected by pg-wrap's spec decode, so
		// pg-wrap-exec exits 97 before the payload.
		broken := filepath.Join(tmp, "broken.toml")
		body := "schema_version = 1\nwrap = [\"relative/env K20_SPEC=x\"]\nexec = [\"/usr/bin/true\"]\n"
		if err := os.WriteFile(broken, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		decline(t, broken, "k20-item-a2", "k20-a-2")
		stillLive(t, "k20-a-1", "k20-b-1")
	})

	t.Run("case4_missing_spec", func(t *testing.T) {
		decline(t, filepath.Join(tmp, "missing.toml"), "k20-item-a3", "k20-a-3")
		stillLive(t, "k20-b-1")
	})

	t.Run("case5_resume", func(t *testing.T) {
		if !hasEnv(envLines(t, filepath.Join(d, "k20-a.env")), "K20_SPEC=a") {
			t.Fatal("precondition: k20-a's current spec is K20_SPEC=a")
		}
		// Soft close: the transcript stays and the row stays, not live.
		if err := p.cc.Close(p.ctx(), "k20-a-1", false); err != nil {
			t.Fatalf("close k20-a-1: %v", err)
		}
		if s := p.mustRow("k20-a-1"); s.Live {
			t.Fatalf("precondition: k20-a-1 must be closed; %+v", s)
		}
		raw, err := os.ReadFile(specOf("k20-a"))
		if err != nil {
			t.Fatal(err)
		}
		changed := bytes.ReplaceAll(raw, []byte(`K20_SPEC=a"`), []byte(`K20_SPEC=a2"`))
		if bytes.Equal(raw, changed) {
			t.Fatalf("the spec has no K20_SPEC=a to change:\n%s", raw)
		}
		changedSpec := filepath.Join(tmp, "a.toml")
		if err := os.WriteFile(changedSpec, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		// ccpool takes its resume route (the row and its transcript exist); the
		// probe maps `--resume <id>` to `--session-id <id>`. A nil error means
		// ready, and the probe writes its env file before it execs the stub that
		// fires the ready hook, so the file is current once the launch returns.
		if err := p.launch("k20-a-1", "k20-a-1", nil, env("k20-a", "k20-item-a1", changedSpec)); err != nil {
			t.Fatalf("the resume under the changed spec must reach ready: %v", err)
		}
		p.wantState("k20-a-1", ccpool.StateReady, true)
		lines := envLines(t, filepath.Join(d, "k20-a.env"))
		if !hasEnv(lines, "K20_SPEC=a2") || hasEnv(lines, "K20_SPEC=a") {
			t.Errorf("a resume after a spec change must run the NEW spec (K20_SPEC=a2); got %v", lines)
		}
	})

	t.Run("case6_real_claude", func(t *testing.T) {
		if os.Getenv(specRealClaudeEnv) != "1" {
			t.Skip(specRealClaudeEnv + "=1 opts in a real, logged-in claude under the real strict spec")
		}
		// The sandbox HOME holds no login; claude needs the operator's. HOME is
		// in pg-wrap's base keep, so the spec needs no change.
		e := env("k20-real", "k20-item-r1", specOf("k20-real"))
		delete(e, "K20_ENV_DIR")
		e["HOME"] = realHome
		defer func() { _ = p.cc.Close(p.ctx(), "k20-real-1", true) }()
		if err := p.launch("k20-real-1", "k20-real-1", nil, e); err != nil {
			t.Fatalf("a real claude must reach ready under the real strict spec, plugin script hooks included: %v", err)
		}
	})
}
