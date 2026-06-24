package ccpool

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

var _ Runner = (*CLIRunner)(nil)

func newSpy() (*CLIRunner, *[][]string, func(out []byte)) {
	var got [][]string
	var canned []byte
	cli := NewCLIRunner(config.Default())
	cli.run = func(_ context.Context, args []string) ([]byte, []byte, error) {
		got = append(got, args)
		return canned, nil, nil
	}
	setOut := func(out []byte) { canned = out }
	return cli, &got, setOut
}

func TestEnsure_argv(t *testing.T) {
	cli, got, _ := newSpy()
	err := cli.Ensure(context.Background(), "pr-pool-worker-zr-1-20260616T010203", "pr-pool-worker-zr-1", "/repo",
		map[string]string{"WORKSPACE_ROOT": "/repo", "BEADS_ACTOR": "pgii-pool__worker", "BEADS_DIR": "/repo/.beads"})
	if err != nil {
		t.Fatal(err)
	}
	// addressed by external_id; display name passed via --name; env keys sorted.
	// Default() sets Autonomous=true, so --autonomous must appear at the end.
	want := []string{
		"new", "pr-pool-worker-zr-1-20260616T010203", "--cwd", "/repo",
		"--name", "pr-pool-worker-zr-1",
		"--env", "BEADS_ACTOR=pgii-pool__worker",
		"--env", "BEADS_DIR=/repo/.beads",
		"--env", "WORKSPACE_ROOT=/repo",
		"--permission-mode", "dontAsk",
		"--allowed-tools", config.Default().AllowedTools,
		"--effort", "max",
		"--autonomous",
	}
	if !reflect.DeepEqual((*got)[0], want) {
		t.Errorf("argv =\n %v\nwant\n %v", (*got)[0], want)
	}
}

func TestEnsure_argv_withModel_noPermissionMode_noName(t *testing.T) {
	var got [][]string
	cfg := config.Default()
	cfg.Model = "claude-opus-4-8"
	cfg.PermissionMode = ""
	cfg.AllowedTools = ""
	cfg.Effort = "high"
	cfg.Autonomous = false // isolate this test to model/effort; not testing autonomous here
	cli := NewCLIRunner(cfg)
	cli.run = func(_ context.Context, args []string) ([]byte, []byte, error) {
		got = append(got, args)
		return nil, nil, nil
	}
	// empty name => no --name flag emitted.
	if err := cli.Ensure(context.Background(), "s", "", "/r", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"new", "s", "--cwd", "/r", "--effort", "high", "--model", "claude-opus-4-8"}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("argv = %v, want %v", got[0], want)
	}
}

func TestEnsure_argv_includesAutonomous(t *testing.T) {
	cli, got, _ := newSpy() // config.Default() => Autonomous true, PermissionMode dontAsk, Effort max
	if err := cli.Ensure(context.Background(), "s", "", "/r", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"new", "s", "--cwd", "/r",
		"--permission-mode", "dontAsk",
		"--allowed-tools", config.Default().AllowedTools,
		"--effort", "max",
		"--autonomous",
	}
	if !reflect.DeepEqual((*got)[0], want) {
		t.Errorf("argv =\n %v\nwant\n %v", (*got)[0], want)
	}
}

func TestEnsure_argv_omitsAutonomousWhenDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Autonomous = false
	var got [][]string
	cli := NewCLIRunner(cfg)
	cli.run = func(_ context.Context, args []string) ([]byte, []byte, error) {
		got = append(got, args)
		return nil, nil, nil
	}
	if err := cli.Ensure(context.Background(), "s", "", "/r", nil); err != nil {
		t.Fatal(err)
	}
	for _, a := range got[0] {
		if a == "--autonomous" {
			t.Fatalf("--autonomous must be omitted when Autonomous=false; argv = %v", got[0])
		}
	}
}

func TestEnsure_allowedTools(t *testing.T) {
	// Non-default allowlist is forwarded verbatim, positioned after --permission-mode.
	var got [][]string
	cfg := config.Default()
	cfg.PermissionMode = "dontAsk"
	cfg.AllowedTools = "Read,Bash(git *)"
	cfg.Effort = ""
	cfg.Autonomous = false // isolate this test to allowlist positioning
	cli := NewCLIRunner(cfg)
	cli.run = func(_ context.Context, args []string) ([]byte, []byte, error) {
		got = append(got, args)
		return nil, nil, nil
	}
	if err := cli.Ensure(context.Background(), "s", "", "/r", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"new", "s", "--cwd", "/r", "--permission-mode", "dontAsk", "--allowed-tools", "Read,Bash(git *)"}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("argv = %v, want %v", got[0], want)
	}
}

func TestEnsure_allowedToolsEmptyOmitsFlag(t *testing.T) {
	var got [][]string
	cfg := config.Default()
	cfg.PermissionMode = ""
	cfg.AllowedTools = "" // empty => no --allowed-tools flag
	cfg.Effort = ""
	cfg.Autonomous = false // isolate this test to allowedTools behavior
	cli := NewCLIRunner(cfg)
	cli.run = func(_ context.Context, args []string) ([]byte, []byte, error) {
		got = append(got, args)
		return nil, nil, nil
	}
	if err := cli.Ensure(context.Background(), "s", "", "/r", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"new", "s", "--cwd", "/r"}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("argv = %v, want %v (empty AllowedTools must omit the flag)", got[0], want)
	}
}

func TestSend_modes(t *testing.T) {
	cases := []struct {
		mode SendMode
		flag string
	}{
		{ModeNoWait, "--no-wait"},
		{ModeInterrupt, "--interrupt"},
		{ModeQueue, "--queue-message"},
	}
	for _, tc := range cases {
		cli, got, _ := newSpy()
		cli.ConfirmIngest = 0 // isolate the mode→flag mapping from the ingestion guard
		if err := cli.Send(context.Background(), "s", "hello world", tc.mode); err != nil {
			t.Fatal(err)
		}
		want := []string{"reply", "s", "hello world", tc.flag}
		if !reflect.DeepEqual((*got)[0], want) {
			t.Errorf("mode %d argv = %v, want %v", tc.mode, (*got)[0], want)
		}
	}
}

// pg2-yukh #1: a worker's initial fire-and-forget nudge (ModeNoWait) must forward
// --confirm-ingest <dur> when a confirm window is configured, so ccpool can detect
// a dropped prompt and exit 7. A queued budget message (ModeQueue) must NOT carry
// it (the model is already mid-turn by then).
func TestSend_forwardsConfirmIngest(t *testing.T) {
	cli, got, _ := newSpy()
	cli.ConfirmIngest = 90 * time.Second
	if err := cli.Send(context.Background(), "zr-1", "do it", ModeNoWait); err != nil {
		t.Fatal(err)
	}
	want := []string{"reply", "zr-1", "do it", "--no-wait", "--confirm-ingest", "1m30s"}
	if !reflect.DeepEqual((*got)[0], want) {
		t.Errorf("no-wait argv = %v, want %v", (*got)[0], want)
	}
}

func TestSend_confirmIngestOnlyForNoWait(t *testing.T) {
	cli, got, _ := newSpy()
	cli.ConfirmIngest = 90 * time.Second
	if err := cli.Send(context.Background(), "zr-1", "do it", ModeQueue); err != nil {
		t.Fatal(err)
	}
	want := []string{"reply", "zr-1", "do it", "--queue-message"}
	if !reflect.DeepEqual((*got)[0], want) {
		t.Errorf("queue argv = %v, want %v (no --confirm-ingest on a queued message)", (*got)[0], want)
	}
}

func TestSend_zeroConfirmIngestOmitsFlag(t *testing.T) {
	cli, got, _ := newSpy()
	cli.ConfirmIngest = 0 // a zero window must omit the flag entirely
	if err := cli.Send(context.Background(), "zr-1", "do it", ModeNoWait); err != nil {
		t.Fatal(err)
	}
	want := []string{"reply", "zr-1", "do it", "--no-wait"}
	if !reflect.DeepEqual((*got)[0], want) {
		t.Errorf("zero window argv = %v, want %v (no flag)", (*got)[0], want)
	}
}

func TestSend_notIngestedExit7(t *testing.T) {
	cli := NewCLIRunner(config.Default())
	cli.ConfirmIngest = 90 * time.Second
	cli.run = func(_ context.Context, _ []string) ([]byte, []byte, error) {
		return nil, []byte("prompt not ingested"), &fakeExit{code: 7}
	}
	err := cli.Send(context.Background(), "zr-1", "do it", ModeNoWait)
	if !errors.Is(err, ErrPromptNotIngested) {
		t.Errorf("exit 7 should map to ErrPromptNotIngested, got %v", err)
	}
	if !IsNotIngested(err) {
		t.Errorf("IsNotIngested must report true for a code-7 send error; got %v", err)
	}
}

func TestCancelCloseList_argv(t *testing.T) {
	cli, got, setOut := newSpy()
	_ = cli.Cancel(context.Background(), "s")
	_ = cli.Close(context.Background(), "s", true)
	setOut([]byte(`[{"external_id":"pr-pool-worker-zr-1-20260616T010203","name":"pr-pool-worker-zr-1","claude_session_id":"u-1","state":"working","live":true,"transcript_path":"/t/x.jsonl"}]`))
	sessions, err := cli.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual((*got)[0], []string{"cancel", "s"}) {
		t.Errorf("cancel argv = %v", (*got)[0])
	}
	if !reflect.DeepEqual((*got)[1], []string{"close", "s", "--purge"}) {
		t.Errorf("close argv = %v", (*got)[1])
	}
	if !reflect.DeepEqual((*got)[2], []string{"list", "--all", "--json"}) {
		t.Errorf("list argv = %v", (*got)[2])
	}
	if len(sessions) != 1 || sessions[0].ExternalID != "pr-pool-worker-zr-1-20260616T010203" ||
		sessions[0].Name != "pr-pool-worker-zr-1" || sessions[0].ClaudeSessionID != "u-1" ||
		sessions[0].State != StateWorking || !sessions[0].Live ||
		sessions[0].TranscriptPath != "/t/x.jsonl" {
		t.Errorf("parsed session = %+v", sessions)
	}
}

// Close without purge omits the --purge flag.
func TestClose_noPurge_argv(t *testing.T) {
	cli, got, _ := newSpy()
	_ = cli.Close(context.Background(), "s", false)
	if !reflect.DeepEqual((*got)[0], []string{"close", "s"}) {
		t.Errorf("close (no purge) argv = %v", (*got)[0])
	}
}

// ccpool now emits idle/errored (was done/failed); pr-pool's parse must match.
func TestList_parsesIdleErroredStates(t *testing.T) {
	cli, _, setOut := newSpy()
	setOut([]byte(`[{"external_id":"a","state":"idle","live":false},{"external_id":"b","state":"errored","live":false}]`))
	got, err := cli.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].State != StateIdle || got[1].State != StateErrored {
		t.Errorf("parsed states = %+v (want idle, errored)", got)
	}
}

func TestCancel_unconfirmedExit6(t *testing.T) {
	cli := NewCLIRunner(config.Default())
	cli.run = func(_ context.Context, args []string) ([]byte, []byte, error) {
		return nil, []byte("cancel may not have landed"), &fakeExit{code: 6}
	}
	err := cli.Cancel(context.Background(), "s")
	if !errors.Is(err, ErrCancelUnconfirmed) {
		t.Errorf("exit 6 should map to ErrCancelUnconfirmed, got %v", err)
	}
}

func TestCancel_otherErrorNotUnconfirmed(t *testing.T) {
	cli := NewCLIRunner(config.Default())
	cli.run = func(_ context.Context, args []string) ([]byte, []byte, error) { return nil, nil, &fakeExit{code: 1} }
	err := cli.Cancel(context.Background(), "s")
	if err == nil || errors.Is(err, ErrCancelUnconfirmed) {
		t.Errorf("exit 1 must not be ErrCancelUnconfirmed, got %v", err)
	}
}

func TestList_parsesCWD(t *testing.T) {
	cli, _, setOut := newSpy()
	setOut([]byte(`[{"name":"s","state":"working","live":true,"transcript_path":"/t.jsonl","cwd":"/wt/repo-pr1"}]`))
	got, err := cli.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].CWD != "/wt/repo-pr1" {
		t.Errorf("CWD = %q, want /wt/repo-pr1", got[0].CWD)
	}
}

// pg2-x6ef: stderr noise emitted alongside the list JSON must not reach the
// parser. run returns stdout and stderr separately; List reads stdout only.
func TestList_stderrDoesNotCorruptJSON(t *testing.T) {
	cli := NewCLIRunner(config.Default())
	cli.run = func(_ context.Context, _ []string) ([]byte, []byte, error) {
		return []byte(`[{"name":"s","state":"working","live":true}]`), []byte("WARN: deprecated flag\n"), nil
	}
	got, err := cli.List(context.Background())
	if err != nil {
		t.Fatalf("stderr must not corrupt list --json: %v", err)
	}
	if len(got) != 1 || got[0].Name != "s" {
		t.Errorf("parsed = %+v, want one session named s", got)
	}
}

// pg2-x6ef: execCmd must capture stdout and stderr into SEPARATE buffers so a
// command that writes to both yields clean stdout and surfaces stderr on error.
func TestExecCmd_separatesStreams(t *testing.T) {
	stdout, stderr, err := execCmd(context.Background(), "sh", []string{"-c", "printf '[]'; echo noise 1>&2; exit 7"})
	if string(stdout) != "[]" {
		t.Errorf("stdout = %q, want %q", stdout, "[]")
	}
	if !strings.Contains(string(stderr), "noise") {
		t.Errorf("stderr = %q, want it to contain %q", stderr, "noise")
	}
	var ec exitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 7 {
		t.Errorf("err = %v, want exit-status 7", err)
	}
}

func TestExecCmd_success(t *testing.T) {
	stdout, stderr, err := execCmd(context.Background(), "sh", []string{"-c", "printf 'ok'"})
	if err != nil || string(stdout) != "ok" || len(stderr) != 0 {
		t.Errorf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
}

// pg2-yy42: execCmd uses exec.CommandContext, so a cancelled/expired ctx kills
// the child instead of hanging the orchestrator/watchdog.
func TestExecCmd_honorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := execCmd(ctx, "sh", []string{"-c", "sleep 5"})
	if err == nil {
		t.Fatal("a sleeping command under an expired ctx must error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("ctx cancellation did not kill the child promptly: took %s", elapsed)
	}
}

// review follow-up: a per-call timeout must name the timeout in the error, not
// leak a bare "context deadline exceeded".
func TestCCpool_timeoutNamesTheTimeout(t *testing.T) {
	cli := NewCLIRunner(config.Default())
	cli.run = func(ctx context.Context, _ []string) ([]byte, []byte, error) {
		<-ctx.Done() // a wedged ccpool that only returns once the deadline fires
		return nil, nil, ctx.Err()
	}
	_, err := cli.ccpool(context.Background(), 10*time.Millisecond, "list", "--all", "--json")
	if err == nil || !strings.Contains(err.Error(), "timed out after") {
		t.Fatalf("timeout should be named in the error, got %v", err)
	}
}

// review follow-up: long positionals (the full reply prompt) must be elided in
// error messages so the real diagnostic isn't buried.
func TestArgSummary_elidesLongArgs(t *testing.T) {
	long := strings.Repeat("x", 5000)
	got := argSummary([]string{"reply", "sess", long, "--queue-message"})
	if strings.Contains(got, long) {
		t.Errorf("long arg must be elided; got %q", got)
	}
	if !strings.Contains(got, "reply sess ") || !strings.Contains(got, "--queue-message") {
		t.Errorf("short args must be preserved; got %q", got)
	}
	if !strings.Contains(got, "<5000 bytes>") {
		t.Errorf("elision should show the byte count; got %q", got)
	}
}

// fakeExit implements the bits of *exec.ExitError errors.As + ExitCode() need.
type fakeExit struct{ code int }

func (e *fakeExit) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e *fakeExit) ExitCode() int { return e.code }
