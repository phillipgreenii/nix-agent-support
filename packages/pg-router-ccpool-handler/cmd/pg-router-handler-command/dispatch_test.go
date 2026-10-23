package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
)

// The payload env and argv tests are the command executor's own (internal/
// executor/command_env_test.go and command_test.go), driven through the
// adapter's real request path: a JSON request on stdin whose role object
// carries the config.

// shAdds are the variables /bin/sh itself adds to what it was started with.
var shAdds = map[string]bool{"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true}

var envIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// envNames reads an `env | sort` dump and returns its variable names, sorted,
// minus what /bin/sh adds itself and any line whose name is not a valid
// identifier (continuation lines of a multi-line value, as the nix sandbox
// environment has).
func envNames(t *testing.T, path string) (names []string, kv map[string]string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("probe wrote no env file: %v", err)
	}
	kv = map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		name, val, ok := strings.Cut(line, "=")
		if !ok || shAdds[name] || !envIdent.MatchString(name) {
			continue
		}
		names = append(names, name)
		kv[name] = val
	}
	sort.Strings(names)
	return names, kv
}

// clearAmbientEnv removes every variable from the test process's environment
// (restored at cleanup), so an `env | sort` probe sees one line per variable.
func clearAmbientEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		t.Setenv(k, "") // registers the restore
		_ = os.Unsetenv(k)
	}
}

func setHandlerEnv(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"HOME": "/h", "USER": "u", "LOGNAME": "u", "TMPDIR": os.TempDir(), "LANG": "C", "TERM": "xterm", "COLORTERM": "truecolor",
		"BEADS_ACTOR": "a", "BEADS_DIR": "/b", "WORKSPACE_ROOT": "/w", "PG_ROUTER_TEST_MODE": "1",
		"SECRET_TOKEN": "s3cret", "PATH_EXTRA": "x",
	} {
		t.Setenv(k, v)
	}
	t.Setenv("PATH", "/custom/bin:/usr/bin:/bin")
}

// runEnvProbe dispatches a real command role whose payload dumps `env | sort`
// and returns the variables it saw.
func runEnvProbe(t *testing.T, strict bool) (names []string, kv map[string]string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "env.txt")
	cfg := cmdConfig(strict, "/bin/sh", "-c", `env | sort > "$1"`, "probe", out)
	req := request(t, eventWith(map[string]any{"id": "b-1", "type": "task", "title": "a title"}), "cmdrole", cfg, true)
	exit, reply := dispatch(context.Background(), t, req)
	if exit != conformance.ExitOK {
		t.Fatalf("dispatch exit %d, reply %s", exit, reply)
	}
	return envNames(t, out)
}

// TestDispatch_StrictBaseEnvGolden pins the strict payload env by exact name:
// the minimal base env, the participant markers and the three PG_ROUTER_* item
// context variables, and nothing else.
func TestDispatch_StrictBaseEnvGolden(t *testing.T) {
	setHandlerEnv(t)
	names, kv := runEnvProbe(t, true)
	want := []string{
		"BEADS_ACTOR", "BEADS_DIR", "COLORTERM", "HOME", "LANG", "LOGNAME", "PATH",
		"PG_ROUTER_ITEM_ID", "PG_ROUTER_ITEM_TYPE", "PG_ROUTER_ROLE", "PG_ROUTER_TEST_MODE",
		"TERM", "TMPDIR", "USER", "WORKSPACE_ROOT",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("strict env names = %v\nwant %v", names, want)
	}
	for k, v := range map[string]string{
		"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "PG_ROUTER_ROLE": "cmdrole",
		"PG_ROUTER_ITEM_ID": "b-1", "PG_ROUTER_ITEM_TYPE": "task", "HOME": "/h", "BEADS_ACTOR": "a",
	} {
		if kv[k] != v {
			t.Errorf("%s = %q, want %q", k, kv[k], v)
		}
	}
	if _, leaked := kv["SECRET_TOKEN"]; leaked {
		t.Error("strict env leaked SECRET_TOKEN")
	}
}

// TestDispatch_NotStrictInheritsEnv: with strictBaseEnv off the payload inherits
// the adapter's env plus the three PG_ROUTER_* variables.
func TestDispatch_NotStrictInheritsEnv(t *testing.T) {
	clearAmbientEnv(t)
	setHandlerEnv(t)
	names, kv := runEnvProbe(t, false)
	want := map[string]bool{"PG_ROUTER_ROLE": true, "PG_ROUTER_ITEM_ID": true, "PG_ROUTER_ITEM_TYPE": true}
	for _, kvs := range os.Environ() {
		if n, _, _ := strings.Cut(kvs, "="); !shAdds[n] {
			want[n] = true
		}
	}
	var wantNames []string
	for n := range want {
		wantNames = append(wantNames, n)
	}
	sort.Strings(wantNames)
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("non-strict env names = %v\nwant %v", names, wantNames)
	}
	if kv["SECRET_TOKEN"] != "s3cret" || kv["PATH"] != "/custom/bin:/usr/bin:/bin" {
		t.Errorf("inherited values lost: SECRET_TOKEN=%q PATH=%q", kv["SECRET_TOKEN"], kv["PATH"])
	}
	if kv["PG_ROUTER_ROLE"] != "cmdrole" || kv["PG_ROUTER_ITEM_ID"] != "b-1" || kv["PG_ROUTER_ITEM_TYPE"] != "task" {
		t.Errorf("item context wrong: %v", kv)
	}
}

// TestDispatch_TemplatedArgvIsOneLiteralArgument: a templated argv element built
// from an untrusted item title reaches the wrapper as ONE literal argument;
// nothing is shell-parsed.
func TestDispatch_TemplatedArgvIsOneLiteralArgument(t *testing.T) {
	dir := t.TempDir()
	pwned := filepath.Join(dir, "pwned")
	out := filepath.Join(dir, "args.txt")
	title := "x; touch " + pwned + " $(id)"
	// /usr/bin/env stands in for a wrapper prefix; the probe counts its args.
	cfg := cmdConfig(false, "/usr/bin/env", "W=1", "/bin/sh", "-c", `printf '%s\n' "$#" "$1" > "$2"`, "probe", "{{.Item.Title}}", out)
	req := request(t, eventWith(map[string]any{"id": "b-1", "type": "task", "title": title}), "cmdrole", cfg, true)
	if exit, reply := dispatch(context.Background(), t, req); exit != conformance.ExitOK {
		t.Fatalf("dispatch exit %d, reply %s", exit, reply)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// "$#" counts "{{.Item.Title}}" and out: 2.
	if got, want := string(b), "2\n"+title+"\n"; got != want {
		t.Fatalf("wrapper saw %q, want %q", got, want)
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("the title was shell-parsed: the injected touch ran")
	}
}

// waitPid reads the pid the payload's script wrote, killing it at cleanup.
func waitPid(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				t.Cleanup(func() { _ = syscall.Kill(n, syscall.SIGKILL) })
				return n
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the payload never wrote its child's pid")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitGone fails when pid is still alive after a grace period.
func waitGone(t *testing.T, pid int, what string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("%s %d survived (orphaned)", what, pid)
		}
	}
}

// TestDispatch_CancelKillsTheWholeGroup: cancelling the dispatch context ends the
// payload's whole process group, so a non-exec child (sleep behind sh) does not
// orphan.
func TestDispatch_CancelKillsTheWholeGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	cfg := cmdConfig(false, "/bin/sh", "-c", `sleep 600 & echo $! > "`+pidFile+`"; wait`)
	req := request(t, eventWith(map[string]any{"id": "b1"}), "cmdrole", cfg, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		exit  int
		reply string
	}
	done := make(chan result, 1)
	go func() {
		exit, reply := dispatch(ctx, t, req)
		done <- result{exit, reply}
	}()

	pid := waitPid(t, pidFile)
	cancel()
	select {
	case r := <-done:
		if r.exit != conformance.ExitError {
			t.Fatalf("a cancelled payload must end in an error: exit %d, reply %s", r.exit, r.reply)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("dispatch did not return after cancel")
	}
	waitGone(t, pid, "sleep child")
}

// TestDispatch_ExitNineIsBusyDecline: a command that exits 9, even behind a
// wrapper layer, is the pre-accept busy decline: exit 9 and a body whose reason
// is command-busy and kind busy.
func TestDispatch_ExitNineIsBusyDecline(t *testing.T) {
	for name, argv := range map[string][]string{
		"direct":  {"/bin/sh", "-c", "exit 9"},
		"wrapped": {"/usr/bin/env", "W=1", "/bin/sh", "-c", "exit 9"},
	} {
		t.Run(name, func(t *testing.T) {
			req := request(t, eventWith(map[string]any{"id": "b1"}), "cmdrole", cmdConfig(false, argv...), true)
			exit, reply := dispatch(context.Background(), t, req)
			if exit != conformance.ExitBusy {
				t.Fatalf("exit = %d, want %d (busy); reply %s", exit, conformance.ExitBusy, reply)
			}
			m := decodeReply(t, reply)
			if m["reason"] != "command-busy" || m["kind"] != "busy" {
				t.Fatalf("decline body = %v, want reason command-busy kind busy", m)
			}
			if err := conformance.Check("handler.dispatch-reply", m); err != nil {
				t.Fatalf("decline body is not schema-conformant: %v", err)
			}
		})
	}
}

// TestDispatch_OtherFailureIsAnErrorWithStderrTail: a failing command that is
// not exit 9 is an error reply carrying the stderr tail, never a decline.
func TestDispatch_OtherFailureIsAnErrorWithStderrTail(t *testing.T) {
	cfg := cmdConfig(false, "/bin/sh", "-c", `echo 'pg-wrap[env]: boom' >&2; exit 2`)
	req := request(t, eventWith(map[string]any{"id": "b1"}), "cmdrole", cfg, true)
	exit, reply := dispatch(context.Background(), t, req)
	if exit != conformance.ExitError {
		t.Fatalf("exit = %d, want %d; reply %s", exit, conformance.ExitError, reply)
	}
	m := decodeReply(t, reply)
	msg, _ := m["error"].(string)
	if !strings.Contains(msg, "pg-wrap[env]: boom") || !strings.Contains(msg, "exit status 2") {
		t.Fatalf("error reply lacks the stderr tail or exit status: %q", msg)
	}
	if _, has := m["reason"]; has {
		t.Fatalf("a failure must not carry a decline reason: %v", m)
	}
}

// TestDispatch_SuccessReplyIsConformant: exit 0 from the argv is an inline
// outcome reply that echoes the request id, with no role in the way.
func TestDispatch_SuccessReplyIsConformant(t *testing.T) {
	req := request(t, eventWith(nil), "cmdrole", cmdConfig(false, "/bin/sh", "-c", ":"), true)
	exit, reply := dispatch(context.Background(), t, req)
	if exit != conformance.ExitOK {
		t.Fatalf("exit = %d; reply %s", exit, reply)
	}
	m := decodeReply(t, reply)
	if err := conformance.Check("handler.dispatch-reply", m); err != nil {
		t.Fatalf("reply %s is not schema-conformant: %v", reply, err)
	}
	if m["id"] != "req-1" {
		t.Fatalf("reply id = %v, want req-1", m["id"])
	}
	if _, ok := m["outcome"].(string); !ok {
		t.Fatalf("outcome must be a string: %v", m)
	}
}

// TestDispatch_RoleConfigIsDecodedStrictly: an unknown key, a wrong type and a
// missing command are each an error reply (exit 1) that names the problem, and
// the payload does not run.
func TestDispatch_RoleConfigIsDecodedStrictly(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	touch := []any{"/bin/sh", "-c", `: > "$1"`, "probe", ran}
	cases := map[string]struct {
		cfg  map[string]any
		want string
	}{
		"unknown top-level key": {map[string]any{"command": map[string]any{"argv": touch}, "bogus": true}, `unknown key "bogus"`},
		"unknown command key":   {map[string]any{"command": map[string]any{"argv": touch, "cwd": "/"}}, `unknown key "cwd"`},
		"wrong type":            {map[string]any{"command": map[string]any{"argv": touch}, "strictBaseEnv": "yes"}, "want a boolean"},
		"argv not an array":     {map[string]any{"command": map[string]any{"argv": "touch"}}, "want an array"},
		"no command":            {map[string]any{"strictBaseEnv": true}, "command: required"},
		"missing config":        {nil, "command: required"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			req := request(t, eventWith(map[string]any{"id": "b1"}), "cmdrole", c.cfg, true)
			exit, reply := dispatch(context.Background(), t, req)
			if exit != conformance.ExitError {
				t.Fatalf("exit = %d, want %d; reply %s", exit, conformance.ExitError, reply)
			}
			if msg, _ := decodeReply(t, reply)["error"].(string); !strings.Contains(msg, c.want) {
				t.Fatalf("error %q does not mention %q", msg, c.want)
			}
			if _, err := os.Stat(ran); err == nil {
				t.Fatal("the payload ran despite an invalid config")
			}
		})
	}
}

// TestDispatch_RequestWithoutUsableRoleIsUsageError: no role, a role with no name
// or a role that is not an object exits conformance.ExitUsage with an error
// reply; malformed JSON is an unexpected error, not usage.
func TestDispatch_RequestWithoutUsableRoleIsUsageError(t *testing.T) {
	ev := eventWith(nil)
	bodies := map[string]string{
		"no role":    request(t, ev, "", nil, false),
		"empty name": request(t, ev, "", cmdConfig(false, "/bin/sh", "-c", ":"), true),
	}
	for name, req := range bodies {
		t.Run(name, func(t *testing.T) {
			exit, reply := dispatch(context.Background(), t, req)
			if exit != conformance.ExitUsage {
				t.Fatalf("exit = %d, want %d; reply %s", exit, conformance.ExitUsage, reply)
			}
			if _, ok := decodeReply(t, reply)["error"].(string); !ok {
				t.Fatalf("want an error reply, got %s", reply)
			}
		})
	}
	if exit, _ := dispatch(context.Background(), t, "{not json"); exit != conformance.ExitError {
		t.Fatalf("malformed JSON exit = %d, want %d", exit, conformance.ExitError)
	}
}
