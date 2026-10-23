package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/wire"
	"github.com/phillipgreenii/pg-router/conformance"
)

// callArgs returns the argument list of the first recorded stub call whose first
// argument is sub (calls are separated by a "--" line, see ccpoolNewFails).
func callArgs(t *testing.T, log, sub string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range strings.Split(string(b), "--\n") {
		if args := strings.Split(strings.TrimSuffix(call, "\n"), "\n"); args[0] == sub {
			return args
		}
	}
	t.Fatalf("no `ccpool %s` call recorded:\n%s", sub, b)
	return nil
}

// In-flight sessions the OLD handler started MUST be adopted by this adapter
// across the cutover (ADR 0085), and the reverse: the external id format, the
// session naming and the dispatch metadata this adapter writes are pinned here
// to the old handler's (both are the shared library's, so this is the guard
// against anyone giving the adapter its own).
func TestDispatch_externalIDSessionNamingAndMetadataUnchanged(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("PATH", stubBin(t, map[string]string{"ccpool": ccpoolNewFails(log)})+":/usr/bin:/bin")
	name, cfg := workerRole("", "")
	if code, got := dispatchWith(t, name, cfg, "", defaultEvent); code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want ExitBusy (the stub's new fails); stdout=%q", code, got)
	}
	args := callArgs(t, log, "new")

	// <prefix><role>-<bead>-<stamp>, the stamp being 20060102T150405.000000000.
	if !regexp.MustCompile(`^pg-router-worker-zr-w-\d{8}T\d{6}\.\d{9}$`).MatchString(args[1]) {
		t.Errorf("external id = %q, want pg-router-worker-zr-w-<yyyymmddThhmmss.nnnnnnnnn>", args[1])
	}
	flagValues := func(flag string) []string {
		var out []string
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				out = append(out, args[i+1])
			}
		}
		sort.Strings(out)
		return out
	}
	if got := flagValues("--name"); len(got) != 1 || got[0] != "pg-router-worker-zr-w" {
		t.Errorf("--name = %q, want the stable per-bead-and-role display name pg-router-worker-zr-w", got)
	}
	var metaKeys []string
	for _, m := range flagValues("--meta") {
		metaKeys = append(metaKeys, strings.SplitN(m, "=", 2)[0])
	}
	want := []string{"pgrouter.bead", "pgrouter.event_id", "pgrouter.launched_at", "pgrouter.lease_until", "pgrouter.pool", "pgrouter.role"}
	if strings.Join(metaKeys, ",") != strings.Join(want, ",") {
		t.Errorf("--meta keys = %q, want %q", metaKeys, want)
	}
	meta := strings.Join(flagValues("--meta"), "\n")
	for _, frag := range []string{"pgrouter.bead=zr-w", "pgrouter.event_id=e-1", "pgrouter.pool=pg-router", "pgrouter.role=worker"} {
		if !strings.Contains(meta, frag) {
			t.Errorf("--meta lacks %q:\n%s", frag, meta)
		}
	}
	if got := strings.Join(flagValues("--label"), ","); got != "pgrouter.pool,pgrouter.role" {
		t.Errorf("--label = %q, want pgrouter.pool,pgrouter.role", got)
	}
}

// A dispatch outlives the daemon that spawned it across a restart (ADR 0085), so
// by the time it writes its one reply the read end of its stdout pipe is gone.
// These tests re-execute this test binary with such a pipe.
const (
	helperModeDispatch  = "dispatch"   // the real `dispatch` entry point
	helperModeBareWrite = "bare-write" // control: a reply write with no SIGPIPE handling
)

func runWithClosedStdoutPipe(t *testing.T, mode string) error {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{}
	env := adapterRunEnv + "=1"
	if mode == helperModeDispatch {
		args = []string{"dispatch"}
	} else {
		env = brokenPipeBareEnv + "=1"
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), env)
	cmd.Stdout = pw
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close() // the child now holds the only write end
	_ = pr.Close() // the "daemon" is gone: nobody can read the reply
	_, _ = io.WriteString(stdin, "this is not json")
	_ = stdin.Close()
	err = cmd.Wait()
	t.Logf("helper %s: err=%v stderr=%q", mode, err, stderr.String())
	return err
}

// brokenPipeBareEnv selects the control child (see TestMain's helper hook).
const brokenPipeBareEnv = "PG_ROUTER_HANDLER_CCPOOL_TEST_BARE_WRITE"

// runBareWrite is the control child: a reply write with no SIGPIPE handling.
func runBareWrite() int {
	_, _ = io.ReadAll(os.Stdin)
	wire.WriteReply(os.Stdout, map[string]any{"ok": true})
	return 0
}

// Control: without the handler's SIGPIPE handling, the reply write kills the
// process with SIGPIPE. This proves the closed pipe really is the failure mode
// and keeps the next test from passing vacuously.
func TestBrokenStdoutPipe_bareReplyWriteIsKilledBySIGPIPE(t *testing.T) {
	err := runWithClosedStdoutPipe(t, helperModeBareWrite)
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v, want an ExitError (the control write should die of SIGPIPE)", err)
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGPIPE {
		t.Fatalf("wait status = %v, want terminated by SIGPIPE", ee.Sys())
	}
}

// `dispatch` survives losing its reply: the write fails with EPIPE, is logged,
// and the process exits with its ordinary exit code instead of a signal.
func TestBrokenStdoutPipe_dispatchExitsNormally(t *testing.T) {
	err := runWithClosedStdoutPipe(t, helperModeDispatch)
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v, want exit status 1 (malformed request)", err)
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("no wait status: %v", ee.Sys())
	}
	if ws.Signaled() {
		t.Fatalf("dispatch was killed by %v: a lost reply must not be a signalled death", ws.Signal())
	}
	if ws.ExitStatus() != conformance.ExitError {
		t.Fatalf("exit status = %d, want %d (ExitError for the malformed request)", ws.ExitStatus(), conformance.ExitError)
	}
}

// The adapter as a process, over the real wire: a dispatch declined for an
// unusable pool exits 9 with the decline body, a validate replies on stdout, and
// a request without a role is a usage error.
func TestProcess_DispatchAndValidateOverTheRealWire(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	name, cfg := workerRole("", "")
	code, stdout := runAdapterProcess(t, []string{"dispatch"}, request(defaultEvent, name, cfg), "PATH=/usr/bin")
	if code != conformance.ExitBusy {
		t.Fatalf("dispatch: exit = %d, want %d; stdout %s", code, conformance.ExitBusy, stdout)
	}
	if reason := decodeReply(t, stdout)["reason"]; reason != "pool-unreadable" {
		t.Errorf("dispatch: reason = %v, want pool-unreadable", reason)
	}
	code, stdout = runAdapterProcess(t, []string{"validate", "--skip-pool-dirs"}, request("", name, cfg))
	if code != conformance.ExitOK || len(decodeReply(t, stdout)["problems"].([]any)) != 0 {
		t.Errorf("validate: exit = %d stdout %s, want 0 and no problems", code, stdout)
	}
	code, stdout = runAdapterProcess(t, []string{"dispatch"}, request(defaultEvent, "", nil))
	if code != conformance.ExitUsage {
		t.Errorf("dispatch without a role: exit = %d stdout %s, want %d", code, stdout, conformance.ExitUsage)
	}
}
