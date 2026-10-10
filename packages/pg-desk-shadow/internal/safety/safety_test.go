package safety

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func policy(t *testing.T) Policy {
	t.Helper()
	root := Resolve(t.TempDir())
	home := Resolve(t.TempDir())
	p := Policy{
		Scratch: root, StateHome: filepath.Join(root, "state"), RuntimeDir: filepath.Join(root, "run"), TmpDir: filepath.Join(root, "tmp"),
		BinDir: filepath.Join(root, "bin"), DeskConfig: filepath.Join(root, "config", "pg-desk.yaml"), PRConfig: filepath.Join(root, "config", "pg-pr.yaml"),
		BeadsDir: filepath.Join(home, "beads-ws"), Home: home, LiveRoots: []string{filepath.Join(home, ".local", "state")},
	}
	for _, d := range []string{p.StateHome, p.RuntimeDir, p.TmpDir, p.BinDir, p.BeadsDir, filepath.Dir(p.PRConfig)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writePR(t, p, "connector:\n  issue:\n    - command: [pg-connector-issue-beads, --beads-dir, "+p.BeadsDir+"]\n      name: beads-a\n")
	return p
}

func writePR(t *testing.T, p Policy, body string) {
	t.Helper()
	if err := os.WriteFile(p.PRConfig, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestVerifyPinsTheBakedInBeadsDir covers pg2-ghmw0: a --beads-dir word baked
// into a registered command beats BEADS_DIR, so Verify must read the scratch
// pg-pr config and refuse any --beads-dir that is not the policy directory.
func TestVerifyPinsTheBakedInBeadsDir(t *testing.T) {
	p := policy(t)
	env := ChildEnv(p, "/usr/bin:/bin")
	live := filepath.Join(Resolve(t.TempDir()), "live-tracker")
	cases := map[string]string{
		"live dir, split word": "connector:\n  issue:\n    - command: [pg-connector-issue-beads, --beads-dir, " + live + "]\n      name: a\n",
		"live dir, = form":     "search:\n  sources:\n    - command: [pg-connector-issue-beads, \"--beads-dir=" + live + "\"]\n      name: a\n",
		"second one is live":   "x:\n  - command: [b, --beads-dir, " + p.BeadsDir + "]\n    name: a\n  - command: [b, --beads-dir, " + live + "]\n    name: b\n",
		"flag with no value":   "x:\n  - command: [b, --beads-dir]\n    name: a\n",
		"not a YAML document":  "a: [unterminated\n",
	}
	for name, body := range cases {
		writePR(t, p, body)
		err := Verify(env, p)
		if err == nil {
			t.Errorf("%s: Verify accepted a config that reads %s", name, live)
			continue
		}
		if !strings.Contains(err.Error(), "PG_PR_CONFIG") {
			t.Errorf("%s: error %q does not name PG_PR_CONFIG", name, err)
		}
	}
	// Both spellings of the policy directory pass, and so does a config with no flag.
	for name, body := range map[string]string{
		"split":   "x:\n  - command: [b, --beads-dir, " + p.BeadsDir + "]\n    name: a\n",
		"= form":  "x:\n  - command: [b, \"--beads-dir=" + p.BeadsDir + "\"]\n    name: a\n",
		"no flag": "x:\n  - command: [b]\n    name: a\n",
	} {
		writePR(t, p, body)
		if err := Verify(env, p); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := os.Remove(p.PRConfig); err != nil {
		t.Fatal(err)
	}
	if err := Verify(env, p); err == nil {
		t.Error("an unreadable scratch pg-pr config must be refused")
	}
}

func TestVerifyHermeticKeepsBakedInBeadsDirInScratch(t *testing.T) {
	p := policy(t)
	p.BeadsDir = ""
	env := ChildEnv(p, "/usr/bin:/bin")
	writePR(t, p, "x:\n  - command: [b, --beads-dir, "+filepath.Join(p.Scratch, "beads-ws")+"]\n    name: a\n")
	if err := Verify(env, p); err != nil {
		t.Fatal(err)
	}
	writePR(t, p, "x:\n  - command: [b, --beads-dir, "+t.TempDir()+"]\n    name: a\n")
	if err := Verify(env, p); err == nil {
		t.Error("hermetic mode must refuse a --beads-dir outside the scratch directory")
	}
}

func set(env []string, k, v string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, k+"=") {
			out = append(out, kv)
		}
	}
	if v != "\x00unset" {
		out = append(out, k+"="+v)
	}
	return out
}

func TestChildEnvPasses(t *testing.T) {
	p := policy(t)
	if err := Verify(ChildEnv(p, "/usr/bin:/bin"), p); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRefusesEachVariable(t *testing.T) {
	p := policy(t)
	base := ChildEnv(p, "/usr/bin:/bin")
	other := t.TempDir()
	cases := []struct {
		name, key, val string
	}{
		{"state unset", "XDG_STATE_HOME", "\x00unset"},
		{"state relative", "XDG_STATE_HOME", "state"},
		{"state live", "XDG_STATE_HOME", filepath.Join(p.Home, ".local", "state")},
		{"state below live", "XDG_STATE_HOME", filepath.Join(p.Home, ".local", "state", "x")},
		{"state elsewhere", "XDG_STATE_HOME", other},
		{"runtime elsewhere", "XDG_RUNTIME_DIR", other},
		{"runtime unset", "XDG_RUNTIME_DIR", "\x00unset"},
		{"tmpdir elsewhere", "TMPDIR", other},
		{"desk config differs", "PG_DESK_CONFIG", filepath.Join(other, "pg-desk.yaml")},
		{"desk config unset", "PG_DESK_CONFIG", "\x00unset"},
		{"pr config differs", "PG_PR_CONFIG", filepath.Join(other, "pg-pr.yaml")},
		{"beads dir differs", "BEADS_DIR", other},
		{"beads dir unset", "BEADS_DIR", "\x00unset"},
		{"issue beads dir differs", "PG_CONNECTOR_ISSUE_BEADS_DIR", other},
		{"dolt autostart on", "BEADS_DOLT_AUTO_START", "1"},
		{"dolt autostart unset", "BEADS_DOLT_AUTO_START", "\x00unset"},
		{"events file override", "PG_CONNECTOR_PR_GITHUB_EVENTS_FILE", filepath.Join(other, "e.jsonl")},
		{"stray variable", "GH_TOKEN", "x"},
		{"path not scratch first", "PATH", "/usr/bin:" + p.BinDir},
		{"home differs", "HOME", other},
	}
	for _, c := range cases {
		err := Verify(set(append([]string(nil), base...), c.key, c.val), p)
		if err == nil {
			t.Errorf("%s: Verify accepted it", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.key) && c.key != "PATH" {
			t.Errorf("%s: error %q does not name %s", c.name, err, c.key)
		}
	}
}

func TestVerifyHermeticRefusesBeadsVars(t *testing.T) {
	p := policy(t)
	p.BeadsDir = ""
	writePR(t, p, "x:\n  - command: [b]\n    name: a\n")
	base := ChildEnv(p, "/usr/bin:/bin")
	if err := Verify(base, p); err != nil {
		t.Fatal(err)
	}
	if err := Verify(append(base, "BEADS_DIR=/x"), p); err == nil {
		t.Fatal("hermetic mode must refuse BEADS_DIR")
	}
}

func TestProfile(t *testing.T) {
	prof := Profile(t.TempDir())
	for _, want := range []string{"(version 1)", "(allow default)", "(deny file-write*)", `(literal "/dev/null")`, `(literal "/dev/tty")`, `(literal "/dev/dtracehelper")`, `(regex #"^/dev/fd/")`, "(subpath"} {
		if !strings.Contains(prof, want) {
			t.Errorf("profile lacks %s: %s", want, prof)
		}
	}
}

func TestDenialDetection(t *testing.T) {
	if !DenialInOutput("sh: /tmp/x: Operation not permitted") || DenialInOutput("all good") {
		t.Fatal("DenialInOutput")
	}
	log := "2026-10-07 Sandbox: pg-desk(123) deny(1) file-write-create /Users/x/y\n2026-10-07 Sandbox: Safari(9) deny(1) file-write-create /z\n"
	if n := CountDenials(log, []string{"pg-desk"}); n != 1 {
		t.Fatalf("CountDenials = %d", n)
	}
}

// requireTool fails (instead of skipping) when PG_DESK_SHADOW_REQUIRE_TOOLS=1,
// so the gate run cannot pass vacuously.
func requireTool(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		return
	}
	if os.Getenv("PG_DESK_SHADOW_REQUIRE_TOOLS") == "1" {
		t.Fatalf("required tool %s is missing", path)
	}
	t.Skipf("%s not available here (nested sandboxes are unavailable in the nix build sandbox)", path)
}

// sandboxUsable reports whether sandbox-exec can run at all in this
// environment (it cannot inside a nix build sandbox).
func sandboxUsable(t *testing.T) {
	t.Helper()
	requireTool(t, DefaultSandboxExec)
	// Apply the REAL profile to a trivial command: inside the nix build
	// sandbox sandbox-exec exists but cannot nest (sandbox_apply fails).
	argv := Wrap(DefaultSandboxExec, t.TempDir(), []string{"/usr/bin/true"})
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		if os.Getenv("PG_DESK_SHADOW_REQUIRE_TOOLS") == "1" {
			t.Fatalf("sandbox-exec unusable: %v %s", err, out)
		}
		t.Skipf("sandbox-exec unusable here: %v", err)
	}
}

func TestSandboxEnforcement(t *testing.T) {
	sandboxUsable(t)
	root := Resolve(t.TempDir())
	tmp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(Resolve(t.TempDir()), "stray")
	run := func(script string) error {
		argv := Wrap(DefaultSandboxExec, root, []string{"/bin/sh", "-c", script})
		return exec.Command(argv[0], argv[1:]...).Run()
	}
	if err := run("echo x > " + outside); err == nil {
		t.Fatal("a write outside the scratch directory succeeded under the sandbox")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("the outside file exists")
	}
	if err := run("echo x > " + filepath.Join(tmp, "in") + " && echo > /dev/null && : > " + filepath.Join(tmp, "x.lock")); err != nil {
		t.Fatalf("permitted writes (scratch file, /dev/null, a lock file under the scratch TMPDIR) were denied: %v", err)
	}
	if err := SelfTest(context.Background(), DefaultSandboxExec, root, tmp); err != nil {
		t.Fatalf("SelfTest: %v", err)
	}
}

func TestSelfTestRefusesMissingTool(t *testing.T) {
	root := Resolve(t.TempDir())
	if err := SelfTest(context.Background(), filepath.Join(root, "no-such-sandbox-exec"), root, root); err == nil {
		t.Fatal("a missing sandbox tool must refuse the start")
	}
}

func TestSelfTestRefusesAnEnforcementlessSandbox(t *testing.T) {
	root := Resolve(t.TempDir())
	fake := filepath.Join(root, "sandbox-exec")
	// A "sandbox" that ignores the profile and runs the command unrestricted.
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nshift 2\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := SelfTest(context.Background(), fake, root, root)
	if err == nil || !strings.Contains(err.Error(), "FAILED") {
		t.Fatalf("a non-enforcing sandbox must be refused, got %v", err)
	}
}
