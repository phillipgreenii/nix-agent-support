package executor

import (
	"context"
	"errors"
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

	"github.com/phillipgreenii/pg-router/internal/handlers/item"
	"github.com/phillipgreenii/pg-router/internal/handlers/query"
	"github.com/phillipgreenii/pg-router/internal/handlers/roles"
)

// shAdds are the variables /bin/sh itself adds to what it was started with.
var shAdds = map[string]bool{"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true}

var envIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// envNames reads an `env | sort` dump and returns its variable names, sorted,
// minus what /bin/sh adds itself and any line whose name is not a valid
// identifier (the continuation lines of a multi-line variable value, as the
// nix sandbox environment has).
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

// runEnvProbe dispatches a real command role whose payload dumps `env | sort`
// to a temp file, and returns the variables it saw.
func runEnvProbe(t *testing.T) (names []string, kv map[string]string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "env.txt")
	role := roles.Role{
		Name:    "cmdrole",
		Command: &roles.CommandConfig{Argv: []string{"/bin/sh", "-c", `env | sort > "$1"`, "probe", out}},
	}
	d := DispatchContext{Role: role, Item: item.Item{ID: "b-1", Type: "task", Title: "a title"}}
	if _, err := (commandExecutor{}).Dispatch(context.Background(), d, Deps{}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	return envNames(t, out)
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

// TestCommandPayloadEnv_StrictGolden pins the payload env by exact name: the
// minimal base env, the participant markers, and the three item-context
// variables, and nothing else. SECRET_TOKEN and PATH_EXTRA exist only in the
// handler's own env, so their absence proves an ambient variable never reaches
// a payload.
func TestCommandPayloadEnv_StrictGolden(t *testing.T) {
	setHandlerEnv(t)
	names, kv := runEnvProbe(t)
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
	for _, only := range []string{"SECRET_TOKEN", "PATH_EXTRA"} {
		if _, leaked := kv[only]; leaked {
			t.Errorf("a variable present only in the handler's env reached the payload: %s", only)
		}
	}
}

// TestCommandPayloadEnv_Contract pins commandPayloadEnv's two returns.
func TestCommandPayloadEnv_Contract(t *testing.T) {
	setHandlerEnv(t)
	it := item.Item{ID: "b-1", Type: "task", Title: "untrusted"}

	env, base := commandPayloadEnv(roles.Role{Name: "r"}, it, "", "")
	if !sort.StringsAreSorted(base) || len(base) != len(env) {
		t.Fatalf("strict baseNames must be the sorted names of env; base=%v env=%v", base, env)
	}
	for _, kv := range env {
		if strings.Contains(kv, "untrusted") || strings.HasPrefix(kv, "SECRET_TOKEN=") {
			t.Errorf("strict env leaked %q", kv)
		}
	}
}

// TestCommandDispatch_TemplatedArgvIsOneLiteralArgument: a templated argv
// element built from an untrusted item title reaches the wrapper as ONE literal
// argument; nothing is shell-parsed.
func TestCommandDispatch_TemplatedArgvIsOneLiteralArgument(t *testing.T) {
	dir := t.TempDir()
	pwned := filepath.Join(dir, "pwned")
	out := filepath.Join(dir, "args.txt")
	title := "x; touch " + pwned + " $(id)"
	// /usr/bin/env stands in for a wrapper prefix; the probe counts its args.
	argv := []string{"/usr/bin/env", "W=1", "/bin/sh", "-c", `printf '%s\n' "$#" "$1" > "$2"`, "probe", "{{.Item.Title}}", out}
	role := roles.Role{Name: "cmdrole", Command: &roles.CommandConfig{Argv: argv}}
	d := DispatchContext{Role: role, Item: item.Item{ID: "b-1", Type: "task", Title: title}}
	if _, err := (commandExecutor{}).Dispatch(context.Background(), d, Deps{}); err != nil {
		t.Fatalf("dispatch: %v", err)
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

// TestCommandDispatch_CancelKillsTheWholeGroup: cancelling the context ends the
// payload's whole process group, so a non-exec child (sleep behind sh) does not
// orphan.
func TestCommandDispatch_CancelKillsTheWholeGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	script := `sleep 600 & echo $! > "` + pidFile + `"; wait`
	d := DispatchContext{Role: shCommandRole(script), Item: item.Item{ID: "b1"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (commandExecutor{}).Dispatch(ctx, d, Deps{})
		done <- err
	}()

	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for pid == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the payload never wrote its child's pid")
		}
		if b, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				pid = n
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled payload must end in an error")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Dispatch did not return after cancel")
	}
	// The sleep is the shell's child, signalled through the group; poll for the
	// kernel to finish reaping it.
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("sleep child %d survived the cancel (orphaned)", pid)
		}
	}
}

// TestCommandDispatch_FailingWrapperIsAcceptedFailure: a failing wrapper layer
// that is not exit 9 is an ordinary error carrying the stderr tail, and a layer
// that exits 9 reads as busy.
func TestCommandDispatch_FailingWrapperIsAcceptedFailure(t *testing.T) {
	role := roles.Role{Name: "cmdrole", Command: &roles.CommandConfig{
		Argv: []string{"/usr/bin/env", "W=1", "/bin/sh", "-c", `echo 'pg-wrap[env]: boom' >&2; exit 2`},
	}}
	_, err := (commandExecutor{}).Dispatch(context.Background(), DispatchContext{Role: role, Item: item.Item{ID: "b1"}}, Deps{})
	if err == nil || errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "pg-wrap[env]: boom") {
		t.Fatalf("want a non-busy error with the stderr tail, got %v", err)
	}
	if code, _, ok := query.ExitDetail(err); !ok || code != 2 {
		t.Fatalf("exit code 2 not reachable: %v", err)
	}
	role.Command.Argv = []string{"/usr/bin/env", "W=1", "/bin/sh", "-c", "exit 9"}
	_, err = (commandExecutor{}).Dispatch(context.Background(), DispatchContext{Role: role, Item: item.Item{ID: "b1"}}, Deps{})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("exit 9 through a wrapper must be ErrBusy; err = %v", err)
	}
}
