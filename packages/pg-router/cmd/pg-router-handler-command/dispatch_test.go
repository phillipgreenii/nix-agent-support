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
func runEnvProbe(t *testing.T) (names []string, kv map[string]string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "env.txt")
	cfg := cmdConfig("/bin/sh", "-c", `env | sort > "$1"`, "probe", out)
	req := request(t, eventWith(map[string]any{"id": "b-1", "type": "task", "title": "a title"}), "cmdrole", cfg, true)
	exit, reply := dispatch(context.Background(), t, req)
	if exit != conformance.ExitOK {
		t.Fatalf("dispatch exit %d, reply %s", exit, reply)
	}
	return envNames(t, out)
}

// TestDispatch_StrictBaseEnvGolden pins the payload env by exact name: the
// minimal base env, the participant markers and the PG_ROUTER_* item context
// variables, and nothing else. SECRET_TOKEN and PATH_EXTRA exist ONLY in the
// handler's (daemon-side) env, so their absence proves an ambient variable
// does not reach a payload.
func TestDispatch_StrictBaseEnvGolden(t *testing.T) {
	setHandlerEnv(t)
	names, kv := runEnvProbe(t)
	want := []string{
		"BEADS_ACTOR", "BEADS_DIR", "COLORTERM", "HOME", "LANG", "LOGNAME", "PATH",
		"PG_ROUTER_DISPATCH_ID", "PG_ROUTER_EVENT_ID", "PG_ROUTER_ITEM_ID", "PG_ROUTER_ITEM_TYPE", "PG_ROUTER_ROLE", "PG_ROUTER_TEST_MODE",
		"TERM", "TMPDIR", "USER", "WORKSPACE_ROOT",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("strict env names = %v\nwant %v", names, want)
	}
	for k, v := range map[string]string{
		"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "PG_ROUTER_ROLE": "cmdrole",
		"PG_ROUTER_ITEM_ID": "b-1", "PG_ROUTER_ITEM_TYPE": "task", "PG_ROUTER_EVENT_ID": "evt-1", "PG_ROUTER_DISPATCH_ID": "req-1", "HOME": "/h", "BEADS_ACTOR": "a",
	} {
		if kv[k] != v {
			t.Errorf("%s = %q, want %q", k, kv[k], v)
		}
	}
	for _, only := range []string{"SECRET_TOKEN", "PATH_EXTRA"} {
		if _, leaked := kv[only]; leaked {
			t.Errorf("a variable present only in the handler's env reached the payload: %s", only)
		}
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
	cfg := cmdConfig("/usr/bin/env", "W=1", "/bin/sh", "-c", `printf '%s\n' "$#" "$1" > "$2"`, "probe", "{{.Item.Title}}", out)
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

// TestDispatch_RetryWindowedItemKeepsTheRouterID: the command adapter does NOT
// map a suffixed item id back to metadata.entity_id (unlike the ccpool adapter,
// bead pg2-xbppib): a command role such as a PR-change handler keys on the
// router's own id, so {{.BeadID}} stays the <entity>@<hex> the event carries.
func TestDispatch_RetryWindowedItemKeepsTheRouterID(t *testing.T) {
	out := filepath.Join(t.TempDir(), "id.txt")
	cfg := cmdConfig("/bin/sh", "-c", `printf '%s' "$1" > "$2"`, "probe", "{{.BeadID}}", out)
	payload := map[string]any{"id": "b-1@46d60fca149d", "metadata": map[string]any{"entity_id": "b-1"}}
	req := request(t, eventWith(payload), "cmdrole", cfg, true)
	if exit, reply := dispatch(context.Background(), t, req); exit != conformance.ExitOK {
		t.Fatalf("dispatch exit %d, reply %s", exit, reply)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), "b-1@46d60fca149d"; got != want {
		t.Fatalf("command saw BeadID %q, want the router's id %q", got, want)
	}
}

// waitPid reads the pid the payload's script wrote, killing it at cleanup.
func waitPid(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second) // a bound on a hang, not a delay: the loop returns on the condition
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
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
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
	cfg := cmdConfig("/bin/sh", "-c", `sleep 600 & echo $! > "`+pidFile+`"; wait`)
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
			req := request(t, eventWith(map[string]any{"id": "b1"}), "cmdrole", cmdConfig(argv...), true)
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
	cfg := cmdConfig("/bin/sh", "-c", `echo 'pg-wrap[env]: boom' >&2; exit 2`)
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
	req := request(t, eventWith(nil), "cmdrole", cmdConfig("/bin/sh", "-c", ":"), true)
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
		"removed switch":        {map[string]any{"command": map[string]any{"argv": touch}, "strictBaseEnv": true}, `unknown key "strictBaseEnv"`},
		"argv not an array":     {map[string]any{"command": map[string]any{"argv": "touch"}}, "want an array"},
		"no command":            {map[string]any{}, "command: required"},
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
		"empty name": request(t, ev, "", cmdConfig("/bin/sh", "-c", ":"), true),
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
