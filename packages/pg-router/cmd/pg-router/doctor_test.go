package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
)

// doctorWorkspace is a hermetic config plus handler stub for config check and
// doctor tests: every environment variable the config reads points inside dir.
type doctorWorkspace struct {
	dir     string
	logDir  string
	roleDir string
}

// newDoctorWorkspace writes a one-query, one-role config whose query command is
// queryCmd, and points the environment at it. The role's handler command is left
// unset; tests set PG_ROUTER_HANDLER_COMMAND themselves.
func newDoctorWorkspace(t *testing.T, queryCmd string) doctorWorkspace {
	t.Helper()
	dir := shortDir(t)
	w := doctorWorkspace{dir: dir, logDir: filepath.Join(dir, "log"), roleDir: filepath.Join(dir, "roles")}
	for _, d := range []string{w.logDir, w.roleDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := `[[query]]
name = "k11-src"
emits = ["k11.live"]
type = "command"
[query.command]
argv = ["` + queryCmd + `", "[]"]
format = "json"
[query.trigger]
kind = "manual"

[[role]]
name = "k11-live"
enabled = true
binds = ["k11.live"]
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_ROUTER_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("PG_ROUTER_GLOBAL_CONFIG", filepath.Join(dir, "no-global.toml"))
	t.Setenv("PG_ROUTER_LOG_DIR", w.logDir)
	t.Setenv("PG_ROUTER_HANDLER_COMMAND", "")
	t.Setenv("PG_ROUTER_HANDLER_COMMAND_DIR", w.roleDir)
	t.Setenv("PG_ROUTER_SOCKET", "")
	t.Setenv("PG_ROUTER_TOKEN", "")
	return w
}

// writeRoleConfig writes the role-config file the core hands the handler.
func (w doctorWorkspace) writeRoleConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(w.roleDir, "k11-live.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeStub writes an executable /bin/sh handler stub: it reads and discards
// stdin, runs onValidate when its first argument is "validate", and exits 9 for
// any other subcommand.
func (w doctorWorkspace) writeStub(t *testing.T, name, onValidate string) string {
	t.Helper()
	path := filepath.Join(w.dir, name)
	script := "#!/bin/sh\ncat >/dev/null\nif [ \"$1\" = validate ]; then\n" + onValidate + "\nfi\nexit 9\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// killPidFile kills the process whose pid file names, if any: the hanging stub's
// child outlives the handler and would hold the test binary's stderr open.
func killPidFile(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func lowerValidateTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := doctorValidateTimeout
	doctorValidateTimeout = d
	t.Cleanup(func() { doctorValidateTimeout = old })
}

// config check reports a missing query command, a missing handler command and a
// bad role-config in ONE run, and exits 1 (design section 10, V-3).
func TestConfigCheck_ReportsEveryProblemInOneRun(t *testing.T) {
	w := newDoctorWorkspace(t, "/nonexistent/k11-producer")
	t.Setenv("PG_ROUTER_HANDLER_COMMAND", "/nonexistent/handler")
	w.writeRoleConfig(t, "{ not json")

	var stdout, stderr strings.Builder
	if code := configCheck(&stdout, &stderr); code != exitGeneric {
		t.Fatalf("exit = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want exactly 3 problem lines, got %d:\n%s", len(lines), stderr.String())
	}
	for _, want := range []string{"/nonexistent/k11-producer", "/nonexistent/handler", "not well-formed JSON"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("no problem line mentions %q:\n%s", want, stderr.String())
		}
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty on failure", stdout.String())
	}
}

func TestConfigCheck_CleanConfigPrintsOKAndExitsZero(t *testing.T) {
	w := newDoctorWorkspace(t, "/bin/echo")
	t.Setenv("PG_ROUTER_HANDLER_COMMAND", w.writeStub(t, "handler", "exit 0"))
	w.writeRoleConfig(t, "{}")

	var stdout, stderr strings.Builder
	if code := configCheck(&stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "config ok" {
		t.Errorf("stdout = %q, want %q", got, "config ok")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// doctor against a handler with validate, one without it, and one that hangs
// (design section 10, V-3).
func TestDoctor_HandlerValidateVerdicts(t *testing.T) {
	cases := []struct {
		name       string
		onValidate string // %PID% is replaced by a pid file path a test cleanup kills
		timeout    time.Duration
		wantExit   int
		wantLines  []string
	}{
		{
			name:       "with validate",
			onValidate: `echo '{"schemaVersion":"1","problems":[],"warnings":["watch out"]}'; exit 0`,
			wantExit:   exitGeneric, // no daemon: the socket probe FAILs
			wantLines:  []string{"ok   validate k11-live: valid", "info validate k11-live: warning: watch out"},
		},
		{
			name:       "with a problem",
			onValidate: `echo '{"schemaVersion":"1","problems":["pool dir missing"],"warnings":[]}'; exit 0`,
			wantExit:   exitGeneric,
			wantLines:  []string{"FAIL validate k11-live: pool dir missing"},
		},
		{
			name:       "without validate",
			onValidate: `exit 2`,
			wantExit:   exitGeneric,
			wantLines:  []string{"info validate k11-live: not supported"},
		},
		{
			name: "hangs",
			// no exec: the sleep child holds the handler's stdout, which proves the deadline bound
			onValidate: `sleep 600 & echo $! > %PID%; wait`,
			timeout:    400 * time.Millisecond,
			wantExit:   exitGeneric,
			wantLines:  []string{"FAIL validate k11-live: timed out after"},
		},
		{
			name:       "fails to run",
			onValidate: `exit 5`,
			wantExit:   exitGeneric,
			wantLines:  []string{"FAIL validate k11-live:", "exited 5"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			timeout := tc.timeout
			if timeout == 0 {
				timeout = 10 * time.Second // generous: a loaded host must not turn a verdict into a timeout
			}
			lowerValidateTimeout(t, timeout)
			w := newDoctorWorkspace(t, "/bin/echo")
			pidFile := filepath.Join(w.dir, "child.pid")
			t.Cleanup(func() { killPidFile(pidFile) })
			t.Setenv("PG_ROUTER_HANDLER_COMMAND", w.writeStub(t, "handler", strings.ReplaceAll(tc.onValidate, "%PID%", pidFile)))
			w.writeRoleConfig(t, "{}")

			var out strings.Builder
			start := time.Now()
			code := doctor(&out, "", "")
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("doctor took %s; the validate deadline must bound it", elapsed)
			}
			if code != tc.wantExit {
				t.Errorf("exit = %d, want %d:\n%s", code, tc.wantExit, out.String())
			}
			for _, want := range tc.wantLines {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, out.String())
				}
			}
			if !strings.Contains(out.String(), "ok   config: config ok") {
				t.Errorf("output lacks the config verdict:\n%s", out.String())
			}
		})
	}
}

// With no daemon running the socket probe FAILs (and so fails the run) while the
// three probes that read the daemon report info, and the state dir probe passes.
func TestDoctor_NoDaemon(t *testing.T) {
	w := newDoctorWorkspace(t, "/bin/echo")
	t.Setenv("PG_ROUTER_HANDLER_COMMAND", w.writeStub(t, "handler", `echo '{"schemaVersion":"1","problems":[],"warnings":[]}'; exit 0`))
	w.writeRoleConfig(t, "{}")

	var out strings.Builder
	if code := doctor(&out, "", ""); code != exitGeneric {
		t.Fatalf("exit = %d, want 1:\n%s", code, out.String())
	}
	for _, want := range []string{
		"ok   state dir: " + w.logDir + " is writable",
		"FAIL socket:",
		"info gates: daemon not reachable",
		"info unavailable roles: daemon not reachable",
		"info progress: daemon not reachable",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if n := strings.Count(out.String(), "FAIL"); n != 1 {
		t.Errorf("want exactly one FAIL (the socket), got %d:\n%s", n, out.String())
	}
}

// A state dir that is missing FAILs the state dir probe.
func TestDoctor_StateDirMissing(t *testing.T) {
	w := newDoctorWorkspace(t, "/bin/echo")
	t.Setenv("PG_ROUTER_LOG_DIR", filepath.Join(w.dir, "missing"))
	t.Setenv("PG_ROUTER_HANDLER_COMMAND", w.writeStub(t, "handler", "exit 2"))
	w.writeRoleConfig(t, "{}")

	var out strings.Builder
	if code := doctor(&out, "", ""); code != exitGeneric {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "FAIL state dir:") {
		t.Errorf("output lacks a state dir FAIL:\n%s", out.String())
	}
}

// When the config does not load, doctor reports each problem, still runs the
// probes that need no config, and skips the per-role validate probes, saying why.
func TestDoctor_ConfigFailureSkipsValidate(t *testing.T) {
	w := newDoctorWorkspace(t, "/nonexistent/k11-producer")
	t.Setenv("PG_ROUTER_HANDLER_COMMAND", "/nonexistent/handler")
	w.writeRoleConfig(t, "{ not json")

	var out strings.Builder
	if code := doctor(&out, "", ""); code != exitGeneric {
		t.Fatalf("exit = %d, want 1", code)
	}
	for _, want := range []string{
		"FAIL config:", "/nonexistent/k11-producer", "/nonexistent/handler", "not well-formed JSON",
		"ok   state dir:", "FAIL socket:", "info validate: skipped: the config did not load",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "validate k11-live") {
		t.Errorf("a role's validate probe ran although the config did not load:\n%s", out.String())
	}
}

// A role whose handler command is unset gets a FAIL naming the reason.
func TestDoctor_HandlerCommandUnset(t *testing.T) {
	w := newDoctorWorkspace(t, "/bin/echo")
	w.writeRoleConfig(t, "{}")

	var out strings.Builder
	if code := doctor(&out, "", ""); code != exitGeneric {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "FAIL validate k11-live:") || !strings.Contains(out.String(), "PG_ROUTER_HANDLER_COMMAND") {
		t.Errorf("want a validate FAIL naming PG_ROUTER_HANDLER_COMMAND:\n%s", out.String())
	}
}

// Against a running core, doctor reads gates, unavailable roles with their
// reason, and per-role progress from its status reply, and exits 1 for the
// unavailable role.
func TestDoctor_RunningCoreReportsGatesUnavailableRolesAndProgress(t *testing.T) {
	f := bootWithValidate(t, failsRole("bad"))
	ctx, cancel := context.WithCancel(context.Background())
	accepted := make(chan error, 1)
	go func() { accepted <- f.svc.Accept(ctx) }()
	t.Cleanup(func() { cancel(); <-accepted })
	deadline := time.Now().Add(2 * time.Second)
	for f.svc.State() != conformance.Started && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	w := newDoctorWorkspace(t, "/bin/echo")
	t.Setenv("PG_ROUTER_HANDLER_COMMAND", w.writeStub(t, "handler", "exit 2"))
	w.writeRoleConfig(t, "{}")

	var out strings.Builder
	code := doctor(&out, f.svc.Ref().Socket, f.svc.Ref().Token)
	if code != exitGeneric {
		t.Fatalf("exit = %d, want 1 (role bad is unavailable):\n%s", code, out.String())
	}
	for _, want := range []string{
		"ok   socket: " + f.svc.Ref().Socket + " is reachable",
		"info gates: none active",
		"FAIL unavailable roles: bad: pool dir missing; unknown key",
		"info progress: bad: last progress",
		"info progress: good: last progress",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "progress: off:") {
		t.Errorf("a disabled role has no progress series, yet doctor printed one:\n%s", out.String())
	}
}

func TestDoctor_RunDoctorUsage(t *testing.T) {
	if code := runDoctor([]string{"extra"}); code != exitUsage {
		t.Errorf("runDoctor(extra) = %d, want usage", code)
	}
	if code := runDoctor([]string{"--nope"}); code != exitUsage {
		t.Errorf("runDoctor(--nope) = %d, want usage", code)
	}
}
