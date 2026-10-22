package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// shAdds are the variables /bin/sh itself adds to what it was started with.
var shAdds = map[string]bool{"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true}

// envNames reads an `env | sort` dump and returns its variable names, sorted,
// minus what /bin/sh adds itself.
func envNames(t *testing.T, path string) (names []string, kv map[string]string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("probe wrote no env file: %v", err)
	}
	kv = map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		name, val, ok := strings.Cut(line, "=")
		if !ok || shAdds[name] {
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
func runEnvProbe(t *testing.T, strict bool) (names []string, kv map[string]string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "env.txt")
	role := roles.Role{
		Name: "cmdrole", Type: "command", StrictBaseEnv: strict,
		Command: &roles.CommandConfig{Argv: []string{"/bin/sh", "-c", `env | sort > "$1"`, "probe", out}},
	}
	d := DispatchContext{Role: role, Item: item.Item{ID: "b-1", Type: "task", Title: "a title"}}
	if _, err := (commandExecutor{}).Dispatch(context.Background(), d, Deps{}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	return envNames(t, out)
}

// clearAmbientEnv removes every variable from the test process's environment
// (restored at cleanup). A probe that dumps `env | sort` sees one line per
// variable only when no value spans lines; the nix build sandbox exports
// multi-line ones (buildPhase, preBuild, ...) that would read as phantom names.
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

// TestCommandPayloadEnv_StrictGolden pins the strict payload env by exact name:
// the minimal base env, the participant markers, and the three item-context
// variables, and nothing else.
func TestCommandPayloadEnv_StrictGolden(t *testing.T) {
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
}

// TestCommandPayloadEnv_NotStrictInherits: with strictBaseEnv off the payload
// inherits the handler's env plus the three PG_ROUTER_* variables.
func TestCommandPayloadEnv_NotStrictInherits(t *testing.T) {
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

// TestCommandPayloadEnv_Contract pins commandPayloadEnv's two returns.
func TestCommandPayloadEnv_Contract(t *testing.T) {
	setHandlerEnv(t)
	it := item.Item{ID: "b-1", Type: "task", Title: "untrusted"}

	env, base := commandPayloadEnv(roles.Role{Name: "r", StrictBaseEnv: true}, it)
	if !sort.StringsAreSorted(base) || len(base) != len(env) {
		t.Fatalf("strict baseNames must be the sorted names of env; base=%v env=%v", base, env)
	}
	for _, kv := range env {
		if strings.Contains(kv, "untrusted") || strings.HasPrefix(kv, "SECRET_TOKEN=") {
			t.Errorf("strict env leaked %q", kv)
		}
	}

	env, base = commandPayloadEnv(roles.Role{Name: "r"}, it)
	if base != nil {
		t.Errorf("non-strict baseNames = %v, want nil", base)
	}
	if !reflect.DeepEqual(env[len(env)-3:], []string{"PG_ROUTER_ITEM_ID=b-1", "PG_ROUTER_ITEM_TYPE=task", "PG_ROUTER_ROLE=r"}) {
		t.Errorf("non-strict env must end with the item context; tail = %v", env[len(env)-3:])
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
	role := roles.Role{Name: "cmdrole", Type: "command", Command: &roles.CommandConfig{Argv: argv}}
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
	role := roles.Role{Name: "cmdrole", Type: "command", Command: &roles.CommandConfig{
		Argv: []string{"/usr/bin/env", "W=1", "/bin/sh", "-c", `echo 'pg-wrap[env]: boom' >&2; exit 2`},
	}}
	_, err := (commandExecutor{}).Dispatch(context.Background(), DispatchContext{Role: role, Item: item.Item{ID: "b1"}}, Deps{})
	if err == nil || errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "pg-wrap[env]: boom") {
		t.Fatalf("want a non-busy error with the stderr tail, got %v", err)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("exit code 2 not reachable: %v", err)
	}
	role.Command.Argv = []string{"/usr/bin/env", "W=1", "/bin/sh", "-c", "exit 9"}
	_, err = (commandExecutor{}).Dispatch(context.Background(), DispatchContext{Role: role, Item: item.Item{ID: "b1"}}, Deps{})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("exit 9 through a wrapper must be ErrBusy; err = %v", err)
	}
}
