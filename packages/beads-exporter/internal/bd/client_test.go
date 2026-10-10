package bd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/failure"
)

// guardRunner records every Cmd and replies with a canned Result. It is the
// "guard fake": a test that expects zero spawns asserts len(calls) == 0.
type guardRunner struct {
	mu    sync.Mutex
	calls []Cmd
	reply func(Cmd) (Result, error)
}

func (g *guardRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	g.mu.Lock()
	g.calls = append(g.calls, c)
	g.mu.Unlock()
	if g.reply == nil {
		return Result{Stdout: []byte(`{"data":[],"schema_version":1}`)}, nil
	}
	return g.reply(c)
}

func (g *guardRunner) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

const (
	testBD    = "/opt/test/bin/bd"
	testHome  = "/opt/test/home"
	testPath  = "/opt/test/bash/bin:/opt/test/coreutils/bin"
	okEnvJSON = `{"data":[],"schema_version":1}`
)

func newTestClient(t *testing.T, beadsDir string, r Runner) *Client {
	t.Helper()
	return NewClient(ClientConfig{
		BDPath:    testBD,
		BeadsDir:  beadsDir,
		Home:      testHome,
		ChildPath: testPath,
		Timeout:   5 * time.Second,
		Runner:    r,
	})
}

func TestArgvFlagTable(t *testing.T) {
	cases := []struct {
		name      string
		argv      []string
		wantN     bool
		wantExact []string
	}{
		{
			"list", ListArgv(ListOpts{}), true,
			[]string{"list", "-n", "0", "--json", "--readonly", "--sandbox"},
		},
		{
			"ready", ReadyArgv(nil), true,
			[]string{"ready", "-n", "0", "--json", "--readonly", "--sandbox"},
		},
		{
			"blocked", BlockedArgv(), false,
			[]string{"blocked", "--json", "--readonly", "--sandbox"},
		},
		{
			"count", CountByStatusArgv(), false,
			[]string{"count", "--by-status", "--json", "--readonly", "--sandbox"},
		},
		{
			"statuses", StatusesArgv(), false,
			[]string{"statuses", "--json", "--readonly", "--sandbox"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.argv, tc.wantExact) {
				t.Fatalf("argv = %v, want %v", tc.argv, tc.wantExact)
			}
			hasN := false
			for _, a := range tc.argv {
				if a == "-n" {
					hasN = true
				}
			}
			if hasN != tc.wantN {
				t.Fatalf("-n present = %v, want %v", hasN, tc.wantN)
			}
			for _, f := range []string{"--readonly", "--sandbox"} {
				found := false
				for _, a := range tc.argv {
					if a == f {
						found = true
					}
				}
				if !found {
					t.Fatalf("%s missing from %v", f, tc.argv)
				}
			}
		})
	}
}

func TestListArgvDateFilters(t *testing.T) {
	ts := time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("x", 3600))
	got := ListArgv(ListOpts{All: true, CreatedAfter: ts})
	want := []string{"list", "-n", "0", "--all", "--created-after", "2026-03-04T04:06:07Z", "--json", "--readonly", "--sandbox"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("created argv = %v, want %v", got, want)
	}
	got = ListArgv(ListOpts{All: true, ClosedAfter: ts})
	want = []string{"list", "-n", "0", "--all", "--closed-after", "2026-03-04T04:06:07Z", "--json", "--readonly", "--sandbox"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("closed argv = %v, want %v", got, want)
	}
	got = ListArgv(ListOpts{})
	for _, a := range got {
		if a == "--all" || a == "--created-after" || a == "--closed-after" {
			t.Fatalf("default list must not carry %s: %v", a, got)
		}
	}
}

func TestReadyArgvPassesQueueArgsBeforeLimit(t *testing.T) {
	got := ReadyArgv([]string{"--priority", "1"})
	want := []string{"ready", "--priority", "1", "-n", "0", "--json", "--readonly", "--sandbox"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
}

func TestClientCommandShape(t *testing.T) {
	dir := t.TempDir()
	r := &guardRunner{}
	c := newTestClient(t, dir, r)
	t.Setenv("AMBIENT_LEAK", "ambient")
	t.Setenv("BEADS_DIR", "/ambient/other")
	t.Setenv("PATH", "/ambient/bin:/usr/bin")
	t.Setenv("HOME", "/ambient/home")

	if _, err := c.List(context.Background(), ListOpts{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if r.count() != 1 {
		t.Fatalf("calls = %d, want 1", r.count())
	}
	cmd := r.calls[0]
	if cmd.Path != testBD {
		t.Fatalf("Path = %q, want absolute %q", cmd.Path, testBD)
	}
	wantEnv := []string{
		"HOME=" + testHome,
		"PATH=" + testPath,
		"BEADS_DIR=" + dir,
		"BD_JSON_ENVELOPE=1",
		"BEADS_DOLT_AUTO_START=0",
		"BD_BACKUP_ENABLED=0",
	}
	if !reflect.DeepEqual(cmd.Env, wantEnv) {
		t.Fatalf("Env = %v, want exactly %v", cmd.Env, wantEnv)
	}
	for _, kv := range cmd.Env {
		if strings.Contains(kv, "ambient") {
			t.Fatalf("ambient value leaked into child env: %q", kv)
		}
	}
}

func TestChildEnvIsExactlyTheReassertedSet(t *testing.T) {
	got := ChildEnv("/h", "/p", "/b")
	want := []string{"HOME=/h", "PATH=/p", "BEADS_DIR=/b", "BD_JSON_ENVELOPE=1", "BEADS_DOLT_AUTO_START=0", "BD_BACKUP_ENABLED=0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChildEnv = %v, want %v", got, want)
	}
	for _, kv := range got {
		if strings.HasPrefix(kv, "GIT") {
			t.Fatalf("unexpected git variable %q", kv)
		}
	}
}

func TestEveryMethodUsesItsSubcommand(t *testing.T) {
	r := &guardRunner{reply: func(c Cmd) (Result, error) {
		switch c.Args[0] {
		case "count":
			return Result{Stdout: []byte(`{"data":{"total":1,"groups":[{"group":"closed","count":1}]},"schema_version":1}`)}, nil
		case "statuses":
			return Result{Stdout: []byte(`{"data":{"built_in_statuses":[{"name":"open"}]},"schema_version":1}`)}, nil
		}
		return Result{Stdout: []byte(okEnvJSON)}, nil
	}}
	c := newTestClient(t, t.TempDir(), r)
	ctx := context.Background()
	if _, err := c.List(ctx, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ready(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Blocked(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CountByStatus(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Statuses(ctx); err != nil {
		t.Fatal(err)
	}
	var subs []string
	for _, c := range r.calls {
		subs = append(subs, c.Args[0])
	}
	if !reflect.DeepEqual(subs, []string{"list", "ready", "blocked", "count", "statuses"}) {
		t.Fatalf("subcommands = %v", subs)
	}
}

func reasonOfCall(t *testing.T, reply func(Cmd) (Result, error)) failure.Reason {
	t.Helper()
	c := newTestClient(t, t.TempDir(), &guardRunner{reply: reply})
	_, err := c.List(context.Background(), ListOpts{})
	if err == nil {
		t.Fatal("expected an error")
	}
	return failure.ReasonOf(err)
}

func TestEnvelopeFailures(t *testing.T) {
	out := func(s string) func(Cmd) (Result, error) {
		return func(Cmd) (Result, error) { return Result{Stdout: []byte(s)}, nil }
	}
	cases := []struct {
		name  string
		reply func(Cmd) (Result, error)
		want  failure.Reason
	}{
		{"bare array", out(`[{"id":"a"}]`), failure.SchemaSkew},
		{"bare array after whitespace", out("  \n[]"), failure.SchemaSkew},
		{"missing schema_version", out(`{"data":[]}`), failure.SchemaSkew},
		{"unknown schema_version", out(`{"data":[],"schema_version":2}`), failure.SchemaSkew},
		{"zero schema_version", out(`{"data":[],"schema_version":0}`), failure.SchemaSkew},
		{"not json", out(`oops`), failure.ParseError},
		{"empty", out(``), failure.ParseError},
		{"blank", out("  \n "), failure.ParseError},
		{"missing data", out(`{"schema_version":1}`), failure.ParseError},
		{"data wrong shape", out(`{"data":{"error":"x"},"schema_version":1}`), failure.ParseError},
		{"bad timestamp", out(`{"data":[{"id":"a","created_at":"yesterday"}],"schema_version":1}`), failure.ParseError},
		{"non-zero exit", func(Cmd) (Result, error) {
			return Result{ExitCode: 1, Stderr: []byte("boom")}, nil
		}, failure.BDError},
		{"spawn failure", func(Cmd) (Result, error) { return Result{}, errors.New("no such file") }, failure.BDError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reasonOfCall(t, tc.reply); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecodesFields(t *testing.T) {
	body := `{"data":[{"id":"alpha-1","status":"in_progress","priority":0,"issue_type":"task","assignee":"someone",
"created_at":"2026-01-02T03:04:05Z","started_at":"2026-01-02T04:00:00.123456Z","defer_until":"2099-01-01T00:00:00Z",
"updated_at":"2026-01-03T00:00:00Z","labels":["x","y"],"is_template":true}],"schema_version":1}`
	c := newTestClient(t, t.TempDir(), &guardRunner{reply: func(Cmd) (Result, error) {
		return Result{Stdout: []byte(body)}, nil
	}})
	got, err := c.List(context.Background(), ListOpts{})
	if err != nil || len(got) != 1 {
		t.Fatalf("List = %v, %v", got, err)
	}
	b := got[0]
	if b.ID != "alpha-1" || b.Status != "in_progress" || b.Priority != 0 || b.IssueType != "task" || b.Assignee != "someone" {
		t.Fatalf("scalar fields: %+v", b)
	}
	if b.CreatedAt == nil || !b.CreatedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("created_at: %v", b.CreatedAt)
	}
	if b.StartedAt == nil || b.StartedAt.Nanosecond() != 123456000 {
		t.Fatalf("started_at: %v", b.StartedAt)
	}
	if b.DeferUntil == nil || b.DeferUntil.Year() != 2099 || b.UpdatedAt == nil {
		t.Fatalf("defer_until/updated_at: %+v", b)
	}
	if !reflect.DeepEqual(b.Labels, []string{"x", "y"}) || !b.IsTemplate {
		t.Fatalf("labels/is_template: %+v", b)
	}
}

func TestCountAndStatusesDecode(t *testing.T) {
	c := newTestClient(t, t.TempDir(), &guardRunner{reply: func(cmd Cmd) (Result, error) {
		if cmd.Args[0] == "count" {
			return Result{Stdout: []byte(`{"data":{"total":5,"groups":[{"group":"closed","count":2},{"group":"open","count":3}]},"schema_version":1}`)}, nil
		}
		return Result{Stdout: []byte(`{"data":{"built_in_statuses":[{"name":"open"},{"name":"closed"}],"custom_statuses":[{"name":"review","category":"active"}]},"schema_version":1}`)}, nil
	}})
	counts, err := c.CountByStatus(context.Background())
	if err != nil || counts["closed"] != 2 || counts["open"] != 3 || len(counts) != 2 {
		t.Fatalf("counts = %v, %v", counts, err)
	}
	names, err := c.Statuses(context.Background())
	if err != nil || !reflect.DeepEqual(names, []string{"open", "closed", "review"}) {
		t.Fatalf("statuses = %v, %v", names, err)
	}
}

func TestTimeoutReason(t *testing.T) {
	r := &guardRunner{reply: func(Cmd) (Result, error) { return Result{}, nil }}
	blocking := runnerFunc(func(ctx context.Context, c Cmd) (Result, error) {
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	_ = r
	c := NewClient(ClientConfig{BDPath: testBD, BeadsDir: t.TempDir(), Home: testHome, ChildPath: testPath, Timeout: 30 * time.Millisecond, Runner: blocking})
	_, err := c.List(context.Background(), ListOpts{})
	if got := failure.ReasonOf(err); got != failure.Timeout {
		t.Fatalf("reason = %q (%v), want timeout", got, err)
	}
}

// blockUntilDeadline simulates a bd call starved by the host: it returns only
// when the per-call deadline expires.
func blockUntilDeadline(ctx context.Context) (Result, error) {
	<-ctx.Done()
	return Result{}, ctx.Err()
}

func TestTimedOutCallIsRetriedOnceAndRecovers(t *testing.T) {
	var (
		mu        sync.Mutex
		attempts  int
		deadlines []bool
	)
	r := runnerFunc(func(ctx context.Context, c Cmd) (Result, error) {
		mu.Lock()
		attempts++
		n := attempts
		_, has := ctx.Deadline()
		deadlines = append(deadlines, has)
		mu.Unlock()
		if n == 1 {
			return blockUntilDeadline(ctx)
		}
		return Result{Stdout: []byte(okEnvJSON)}, nil
	})
	var logs bytes.Buffer
	c := NewClient(ClientConfig{
		BDPath: testBD, BeadsDir: t.TempDir(), Home: testHome, ChildPath: testPath,
		Timeout: 30 * time.Millisecond, Runner: r,
		Log: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if _, err := c.List(context.Background(), ListOpts{}); err != nil {
		t.Fatalf("a call that times out once must recover on the retry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if !deadlines[0] || !deadlines[1] {
		t.Fatalf("every attempt needs its own deadline: %v", deadlines)
	}
	if !strings.Contains(logs.String(), "bd call timed out; retrying") {
		t.Fatalf("the retry must be logged, got %q", logs.String())
	}
}

// A bd that really hangs must still surface as reason=timeout (so
// beads_exporter_up goes to 0 and the alert path is intact), after exactly two
// attempts and no more.
func TestPersistentTimeoutStillFailsAsTimeoutAfterTwoAttempts(t *testing.T) {
	var attempts int
	r := runnerFunc(func(ctx context.Context, c Cmd) (Result, error) {
		attempts++
		return blockUntilDeadline(ctx)
	})
	c := NewClient(ClientConfig{BDPath: testBD, BeadsDir: t.TempDir(), Home: testHome, ChildPath: testPath, Timeout: 20 * time.Millisecond, Runner: r})
	_, err := c.List(context.Background(), ListOpts{})
	if got := failure.ReasonOf(err); got != failure.Timeout {
		t.Fatalf("reason = %q (%v), want timeout", got, err)
	}
	if attempts != timeoutAttempts || timeoutAttempts != 2 {
		t.Fatalf("attempts = %d (timeoutAttempts %d), want exactly 2", attempts, timeoutAttempts)
	}
}

// Only a timeout is retried: a real bd failure is final on the first attempt.
func TestNonTimeoutFailuresAreNotRetried(t *testing.T) {
	cases := map[string]func(Cmd) (Result, error){
		"exit code":   func(Cmd) (Result, error) { return Result{ExitCode: 1, Stderr: []byte("boom")}, nil },
		"spawn error": func(Cmd) (Result, error) { return Result{}, errors.New("exec: no such file") },
		"schema skew": func(Cmd) (Result, error) { return Result{Stdout: []byte(`[]`)}, nil },
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			g := &guardRunner{reply: reply}
			c := newTestClient(t, t.TempDir(), g)
			if _, err := c.List(context.Background(), ListOpts{}); err == nil {
				t.Fatal("want an error")
			}
			if g.count() != 1 {
				t.Fatalf("spawns = %d, want 1 (no retry)", g.count())
			}
		})
	}
}

// A cancelled parent (shutdown) is neither a timeout nor retried.
func TestParentCancellationDuringACallIsNotRetried(t *testing.T) {
	var attempts int
	ctx, cancel := context.WithCancel(context.Background())
	r := runnerFunc(func(rc context.Context, c Cmd) (Result, error) {
		attempts++
		cancel()
		return blockUntilDeadline(rc)
	})
	c := NewClient(ClientConfig{BDPath: testBD, BeadsDir: t.TempDir(), Home: testHome, ChildPath: testPath, Timeout: time.Minute, Runner: r})
	_, err := c.List(ctx, ListOpts{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestParentCancellationIsNotAFailure(t *testing.T) {
	blocking := runnerFunc(func(ctx context.Context, c Cmd) (Result, error) {
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	c := NewClient(ClientConfig{BDPath: testBD, BeadsDir: t.TempDir(), Home: testHome, ChildPath: testPath, Timeout: time.Minute, Runner: blocking})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.List(ctx, ListOpts{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	var fe *failure.Error
	if errors.As(err, &fe) {
		t.Fatalf("cancellation must not be a classified failure: %v", err)
	}
}

type runnerFunc func(ctx context.Context, c Cmd) (Result, error)

func (f runnerFunc) Run(ctx context.Context, c Cmd) (Result, error) { return f(ctx, c) }

func TestStaleGuard(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		stale bool
	}{
		{"file", func(t *testing.T, dir string) { mustWrite(t, filepath.Join(dir, "issues.jsonl"), "x") }, true},
		{"empty file", func(t *testing.T, dir string) { mustWrite(t, filepath.Join(dir, "issues.jsonl"), "") }, true},
		{"directory", func(t *testing.T, dir string) { mustMkdir(t, filepath.Join(dir, "issues.jsonl")) }, true},
		{"symlink", func(t *testing.T, dir string) {
			target := filepath.Join(dir, "elsewhere")
			mustWrite(t, target, "x")
			mustSymlink(t, target, filepath.Join(dir, "issues.jsonl"))
		}, true},
		{"dangling symlink", func(t *testing.T, dir string) {
			mustSymlink(t, filepath.Join(dir, "does-not-exist"), filepath.Join(dir, "issues.jsonl"))
		}, true},
		{"disabled suffix ignored", func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, "issues.jsonl.disabled-2026"), "x")
		}, false},
		{"other jsonl ignored", func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, "interactions.jsonl"), "x")
		}, false},
		{"clean", func(t *testing.T, dir string) {}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			r := &guardRunner{}
			c := newTestClient(t, dir, r)
			ctx := context.Background()
			calls := []func() error{
				func() error { _, err := c.List(ctx, ListOpts{}); return err },
				func() error { _, err := c.Ready(ctx, nil); return err },
				func() error { _, err := c.Blocked(ctx); return err },
			}
			for i, call := range calls {
				err := call()
				if tc.stale {
					if got := failure.ReasonOf(err); err == nil || got != failure.StaleIssuesJSONL {
						t.Fatalf("call %d: err = %v, want stale_issues_jsonl", i, err)
					}
				} else if err != nil {
					t.Fatalf("call %d: unexpected error %v", i, err)
				}
			}
			if tc.stale && r.count() != 0 {
				t.Fatalf("stale db spawned bd %d times, want zero", r.count())
			}
			if !tc.stale && r.count() != 3 {
				t.Fatalf("clean db spawned bd %d times, want 3", r.count())
			}
		})
	}
}

func TestStaleGuardRecovers(t *testing.T) {
	dir := t.TempDir()
	r := &guardRunner{}
	c := newTestClient(t, dir, r)
	stale := filepath.Join(dir, "issues.jsonl")
	mustWrite(t, stale, "x")
	if _, err := c.List(context.Background(), ListOpts{}); failure.ReasonOf(err) != failure.StaleIssuesJSONL {
		t.Fatalf("want stale, got %v", err)
	}
	if err := os.Remove(stale); err != nil {
		t.Fatal(err)
	}
	if _, err := c.List(context.Background(), ListOpts{}); err != nil {
		t.Fatalf("after removal: %v", err)
	}
	if r.count() != 1 {
		t.Fatalf("calls = %d, want 1 (only the recovered cycle)", r.count())
	}
}

func TestCheckStaleUnreadableDirectory(t *testing.T) {
	// A beads dir that is itself missing is not stale: lstat reports not-exist
	// and bd (not the guard) reports the real problem.
	if err := CheckStale(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("missing dir: %v", err)
	}
	// A path component that is a regular file makes lstat fail with ENOTDIR,
	// which is neither stale nor not-exist: it must surface as bd_error.
	dir := t.TempDir()
	file := filepath.Join(dir, "plain")
	mustWrite(t, file, "x")
	err := CheckStale(file)
	if err == nil || failure.ReasonOf(err) != failure.BDError {
		t.Fatalf("err = %v, want bd_error", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestSignalKilledExitIsABDError(t *testing.T) {
	// A child killed by a signal reports exit code -1.
	r := &guardRunner{reply: func(Cmd) (Result, error) { return Result{ExitCode: -1}, nil }}
	_, err := newTestClient(t, t.TempDir(), r).List(context.Background(), ListOpts{})
	if failure.ReasonOf(err) != failure.BDError || err == nil {
		t.Fatalf("err = %v, want bd_error", err)
	}
}

func TestStderrTailKeepsTheEndOfLongOutput(t *testing.T) {
	long := strings.Repeat("A", 500) + "THE-END"
	r := &guardRunner{reply: func(Cmd) (Result, error) { return Result{ExitCode: 3, Stderr: []byte("  " + long + "\n")}, nil }}
	_, err := newTestClient(t, t.TempDir(), r).List(context.Background(), ListOpts{})
	if err == nil || !strings.Contains(err.Error(), "THE-END") {
		t.Fatalf("err = %v, want the end of stderr", err)
	}
	if strings.Count(err.Error(), "A") != 300-len("THE-END") {
		t.Fatalf("stderr excerpt is not the last 300 bytes: %q", err.Error())
	}
	short := &guardRunner{reply: func(Cmd) (Result, error) { return Result{ExitCode: 3, Stderr: []byte("short message")}, nil }}
	_, err = newTestClient(t, t.TempDir(), short).List(context.Background(), ListOpts{})
	if err == nil || !strings.Contains(err.Error(), "short message") {
		t.Fatalf("err = %v", err)
	}
}

func TestNonObjectNonArrayJSONIsAParseError(t *testing.T) {
	for _, body := range []string{`"text"`, `42`, `true`} {
		got := reasonOfCall(t, func(Cmd) (Result, error) { return Result{Stdout: []byte(body)}, nil })
		if got != failure.ParseError {
			t.Errorf("body %s: reason = %s, want parse_error", body, got)
		}
	}
}

func TestZeroOrNegativeTimeoutMeansNoTimeout(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		c := NewClient(ClientConfig{
			BDPath: testBD, BeadsDir: t.TempDir(), Home: testHome, ChildPath: testPath, Timeout: d,
			Runner: runnerFunc(func(ctx context.Context, _ Cmd) (Result, error) {
				if _, has := ctx.Deadline(); has {
					t.Errorf("timeout %v: a deadline was set", d)
				}
				return Result{Stdout: []byte(okEnvJSON)}, nil
			}),
		})
		if _, err := c.List(context.Background(), ListOpts{}); err != nil {
			t.Fatalf("timeout %v: %v", d, err)
		}
	}
	// A positive timeout does set one.
	c := NewClient(ClientConfig{
		BDPath: testBD, BeadsDir: t.TempDir(), Home: testHome, ChildPath: testPath, Timeout: time.Minute,
		Runner: runnerFunc(func(ctx context.Context, _ Cmd) (Result, error) {
			if _, has := ctx.Deadline(); !has {
				t.Error("no deadline for a positive timeout")
			}
			return Result{Stdout: []byte(okEnvJSON)}, nil
		}),
	})
	if _, err := c.List(context.Background(), ListOpts{}); err != nil {
		t.Fatal(err)
	}
}

func TestCountAndStatusesPropagateFailures(t *testing.T) {
	bad := map[string]func(Cmd) (Result, error){
		"exit":   func(Cmd) (Result, error) { return Result{ExitCode: 2}, nil },
		"parse":  func(Cmd) (Result, error) { return Result{Stdout: []byte("oops")}, nil },
		"skew":   func(Cmd) (Result, error) { return Result{Stdout: []byte(`[]`)}, nil },
		"shape":  func(Cmd) (Result, error) { return Result{Stdout: []byte(`{"data":"x","schema_version":1}`)}, nil },
		"nodata": func(Cmd) (Result, error) { return Result{Stdout: []byte(`{"schema_version":1}`)}, nil },
	}
	want := map[string]failure.Reason{"exit": failure.BDError, "parse": failure.ParseError, "skew": failure.SchemaSkew, "shape": failure.ParseError, "nodata": failure.ParseError}
	for name, reply := range bad {
		c := newTestClient(t, t.TempDir(), &guardRunner{reply: reply})
		counts, err := c.CountByStatus(context.Background())
		if err == nil || counts != nil || failure.ReasonOf(err) != want[name] {
			t.Errorf("count %s: counts=%v err=%v", name, counts, err)
		}
		names, err := c.Statuses(context.Background())
		if err == nil || names != nil || failure.ReasonOf(err) != want[name] {
			t.Errorf("statuses %s: names=%v err=%v", name, names, err)
		}
	}
	// Both also refuse to run on a stale store.
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "issues.jsonl"), "x")
	c := newTestClient(t, dir, &guardRunner{})
	if _, err := c.CountByStatus(context.Background()); failure.ReasonOf(err) != failure.StaleIssuesJSONL {
		t.Errorf("count on stale store: %v", err)
	}
	if _, err := c.Statuses(context.Background()); failure.ReasonOf(err) != failure.StaleIssuesJSONL {
		t.Errorf("statuses on stale store: %v", err)
	}
}

func TestStatusesWithNoCustomStatuses(t *testing.T) {
	c := newTestClient(t, t.TempDir(), &guardRunner{reply: func(Cmd) (Result, error) {
		return Result{Stdout: []byte(`{"data":{"built_in_statuses":[{"name":"a"},{"name":"b"}]},"schema_version":1}`)}, nil
	}})
	names, err := c.Statuses(context.Background())
	if err != nil || !reflect.DeepEqual(names, []string{"a", "b"}) {
		t.Fatalf("names = %v, err = %v", names, err)
	}
}
