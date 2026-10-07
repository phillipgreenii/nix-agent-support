package eventlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestTruncate(t *testing.T) {
	long := strings.Repeat("a", 400)
	if got := Truncate(long, 300); len(got) != 303 || !strings.HasSuffix(got, "...") {
		t.Errorf("Truncate ascii len = %d", len(got))
	}
	if got := Truncate("short", 300); got != "short" {
		t.Errorf("Truncate short = %q", got)
	}
	multi := strings.Repeat("é", 200) // 2 bytes each
	got := Truncate(multi, 301)       // 301 lands mid-rune
	if !strings.HasSuffix(got, "...") {
		t.Fatal("missing ellipsis")
	}
	if body := strings.TrimSuffix(got, "..."); strings.ContainsRune(body, '�') {
		t.Errorf("Truncate split a rune: %q", body)
	}
}

func TestLevelForCode(t *testing.T) {
	for code, want := range map[string]string{
		"": "info", "unauthenticated": "error", "unavailable": "error",
		"not_found": "warn", "invalid_argument": "warn", "unknown_op": "warn",
		"version_mismatch": "warn", "query_not_recognized": "info",
	} {
		if got := LevelForCode(code); got != want {
			t.Errorf("LevelForCode(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestNewBase_SuccessAndFailure(t *testing.T) {
	start := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	end := start.Add(250 * time.Millisecond)

	ok := NewBase("svc", "show", "v1", start, end, nil)
	if ok.Level != LevelInfo || ok.Msg != "show ok" || ok.ErrorCode != "" || ok.Error != "" {
		t.Errorf("success base = %+v", ok)
	}
	if ok.Time != "2026-10-03T12:00:00.250Z" || ok.DurationMS != 250 || ok.PID != os.Getpid() {
		t.Errorf("success base time/duration/pid = %q/%d/%d", ok.Time, ok.DurationMS, ok.PID)
	}
	if ok.Service != "svc" || ok.Version != "v1" || ok.Op != "show" {
		t.Errorf("success base identity = %+v", ok)
	}

	bad := NewBase("svc", "list", "", start, end, scriptout.WrapError(scriptout.ErrUnauthenticated, "no token"))
	if bad.Level != LevelError || bad.ErrorCode != "unauthenticated" || bad.Msg != "list failed: unauthenticated" {
		t.Errorf("failure base = %+v", bad)
	}
	if !strings.Contains(bad.Error, "no token") {
		t.Errorf("error message not carried: %q", bad.Error)
	}

	// An unwrapped error falls into the wire taxonomy's catch-all, exactly as
	// the error envelope does.
	if got := NewBase("svc", "x", "", start, end, errors.New("plain")).ErrorCode; got != "unavailable" {
		t.Errorf("plain error code = %q, want unavailable", got)
	}

	long := NewBase("svc", "x", "", start, end, errors.New(strings.Repeat("e", 5000)))
	if len(long.Error) > MaxErrorBytes+len(elisionMarker(5000)) {
		t.Errorf("error not truncated: %d bytes", len(long.Error))
	}
}

// A long error (a gh command line carrying a large GraphQL query, followed by
// the subprocess's stderr) must keep its TAIL: the stderr is the diagnostic,
// and head-only truncation at 300 bytes lost it entirely (bead pg2-daktd).
func TestNewBase_LongErrorKeepsHeadAndStderrTail(t *testing.T) {
	start := time.Unix(100, 0)
	end := start.Add(time.Second)
	msg := "gh api graphql -F query=" + strings.Repeat("{ field }", 400) +
		": exit status 1: error connecting to api.github.com: check your internet connection"
	b := NewBase("svc", "list", "", start, end, errors.New(msg))
	if !strings.HasPrefix(b.Error, "gh api graphql -F query=") {
		t.Errorf("head lost: %q", b.Error[:40])
	}
	if !strings.HasSuffix(b.Error, "error connecting to api.github.com: check your internet connection") {
		t.Errorf("stderr tail lost: ...%q", b.Error[len(b.Error)-120:])
	}
	if !strings.Contains(b.Error, "bytes elided") {
		t.Errorf("no elision marker: %q", b.Error)
	}
	if len(b.Error) > MaxErrorBytes+len(elisionMarker(len(msg))) {
		t.Errorf("error exceeds bound: %d bytes", len(b.Error))
	}
}

func TestTruncateHeadTail(t *testing.T) {
	if got := TruncateHeadTail("short", 100, 20); got != "short" {
		t.Errorf("short = %q", got)
	}
	long := strings.Repeat("h", 50) + strings.Repeat("m", 500) + strings.Repeat("t", 50)
	got := TruncateHeadTail(long, 100, 30)
	if !strings.HasPrefix(got, strings.Repeat("h", 30)) || !strings.HasSuffix(got, strings.Repeat("t", 50)) {
		t.Errorf("head/tail not preserved: %q", got)
	}
	if strings.Contains(got, "mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm") {
		t.Errorf("middle not elided: %q", got)
	}
	// Both cut points must land on rune boundaries so the line stays valid UTF-8.
	multi := strings.Repeat("é", 300)
	for _, head := range []int{29, 30, 31} {
		out := TruncateHeadTail(multi, 101, head)
		if !utf8.ValidString(out) {
			t.Errorf("head=%d produced invalid UTF-8: %q", head, out)
		}
	}
}

func TestWrap_AroundRunsPerCallAndPassesResultThrough(t *testing.T) {
	type ctxKey struct{}
	var calls []string
	inner := func(ctx context.Context, args json.RawMessage) (any, error) {
		calls = append(calls, "inner:"+ctx.Value(ctxKey{}).(string))
		return "res", errors.New("boom")
	}
	table := scriptout.DispatchTable{
		"list": scriptout.OpHandler{SchemaVersion: 7, Handle: inner},
		"show": scriptout.OpHandler{SchemaVersion: 7, Handle: inner},
	}
	step := 0
	now := func() time.Time { step++; return time.Unix(int64(step), 0) }
	var finished []string
	wrapped := Wrap(table, now, func(ctx context.Context, op string) (context.Context, Finish) {
		return context.WithValue(ctx, ctxKey{}, op), func(start, end time.Time, err error) {
			finished = append(finished, op+":"+end.Sub(start).String()+":"+err.Error())
		}
	})

	if len(wrapped) != 2 || wrapped["list"].SchemaVersion != 7 {
		t.Fatalf("table shape changed: %+v", wrapped)
	}
	res, err := wrapped["list"].Handle(context.Background(), nil)
	if res != "res" || err == nil || err.Error() != "boom" {
		t.Errorf("result/error altered: %v, %v", res, err)
	}
	if strings.Join(calls, ",") != "inner:list" {
		t.Errorf("around ctx not seen by handler: %v", calls)
	}
	if len(finished) != 1 || finished[0] != "list:1s:boom" {
		t.Errorf("finish = %v", finished)
	}
}

type progressRow struct {
	op, phase string
	start     time.Time
	at        time.Time
}

// TestWrapProgress_StartThenHeartbeatsThenFinish proves bead pg2-5dyz2's
// in-flight rows: a start row before the handler runs, heartbeats while it is
// still running, no heartbeat after the handler returned, and the result and
// error passed through untouched.
func TestWrapProgress_StartThenHeartbeatsThenFinish(t *testing.T) {
	var mu sync.Mutex
	var rows []progressRow
	var order []string
	progress := func(op string, args json.RawMessage, phase string, start, now time.Time) {
		mu.Lock()
		defer mu.Unlock()
		rows = append(rows, progressRow{op: op, phase: phase, start: start, at: now})
		order = append(order, phase)
		if string(args) != `{"q":1}` {
			t.Errorf("args = %s", args)
		}
	}
	release := make(chan struct{})
	table := scriptout.DispatchTable{"list": {SchemaVersion: 3, Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
		<-release
		return "res", errors.New("boom")
	}}}
	wrapped := WrapProgress(table, time.Now, func(ctx context.Context, op string) (context.Context, Finish) {
		return ctx, func(start, end time.Time, err error) {
			mu.Lock()
			order = append(order, "finish")
			mu.Unlock()
		}
	}, progress, 10*time.Millisecond)

	type out struct {
		res any
		err error
	}
	done := make(chan out)
	go func() {
		res, err := wrapped["list"].Handle(context.Background(), json.RawMessage(`{"q":1}`))
		done <- out{res, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(rows)
		mu.Unlock()
		if n >= 3 { // start + at least two heartbeats
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d in-flight rows after 5s", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	o := <-done
	if o.res != "res" || o.err == nil || o.err.Error() != "boom" {
		t.Errorf("result/error altered: %v, %v", o.res, o.err)
	}

	mu.Lock()
	defer mu.Unlock()
	if rows[0].phase != PhaseStart || !rows[0].at.Equal(rows[0].start) {
		t.Errorf("first row = %+v, want a start row with elapsed 0", rows[0])
	}
	for _, r := range rows[1:] {
		if r.phase != PhaseHeartbeat || r.op != "list" || !r.at.After(r.start) {
			t.Errorf("row = %+v, want a later heartbeat", r)
		}
	}
	if order[len(order)-1] != "finish" {
		t.Errorf("a heartbeat was written after the final row: %v", order)
	}
}

func TestWrapProgress_NilProgressIsPlainWrap(t *testing.T) {
	table := scriptout.DispatchTable{"list": {Handle: func(context.Context, json.RawMessage) (any, error) { return 1, nil }}}
	finished := 0
	wrapped := WrapProgress(table, time.Now, func(ctx context.Context, op string) (context.Context, Finish) {
		return ctx, func(time.Time, time.Time, error) { finished++ }
	}, nil, 0)
	if res, err := wrapped["list"].Handle(context.Background(), nil); res != 1 || err != nil || finished != 1 {
		t.Errorf("res=%v err=%v finished=%d", res, err, finished)
	}
}

func TestNewProgressEvent_ShapeAndFields(t *testing.T) {
	start := time.Date(2026, 10, 6, 14, 9, 20, 0, time.UTC)
	ev := NewProgressEvent("svc", "list", "v1", PhaseHeartbeat, json.RawMessage(`{ "q": "x" }`), start, start.Add(10*time.Second))
	line, err := Line(ev)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"level": "info", "msg": "list still running", "service": "svc", "op": "list",
		"phase": "heartbeat", "elapsed_ms": float64(10000), "args": `{"q":"x"}`,
		"time": "2026-10-06T14:09:30.000Z",
	} {
		if m[k] != want {
			t.Errorf("%s = %v, want %v", k, m[k], want)
		}
	}
	// In-flight rows must not look like finished calls to a latency or
	// error-rate query.
	for _, k := range []string{"duration_ms", "error_code", "error"} {
		if _, ok := m[k]; ok {
			t.Errorf("progress row carries %s", k)
		}
	}
	if got := NewProgressEvent("svc", "list", "", PhaseStart, nil, start, start); got.Msg != "list started" || got.Args != "{}" {
		t.Errorf("start row = %+v", got)
	}
}

func TestLine_OneNewlineTerminatedLineNoHTMLEscape(t *testing.T) {
	line, err := Line(Base{Time: "t", Level: "info", Msg: "a<b&c"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(line, []byte("\n")) || bytes.Count(line, []byte("\n")) != 1 {
		t.Fatalf("not exactly one newline-terminated line: %q", line)
	}
	if !bytes.Contains(line, []byte("a<b&c")) {
		t.Errorf("HTML escaped: %s", line)
	}
}

func TestAppend_RotatesPastMaxKeepingOneCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	line := []byte(strings.Repeat("x", 49) + "\n") // 50 bytes
	const max = 120

	for i := 0; i < 3; i++ { // 150 bytes: crosses max on the 3rd append (check is pre-append)
		if err := Append(path, line, max); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path + ".1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rotated too early (150 > 120 only counts at the next append): %v", err)
	}
	if err := Append(path, line, max); err != nil { // file is 150 > 120: rotate, then append
		t.Fatal(err)
	}
	rot, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("no rotated copy: %v", err)
	}
	if len(rot) != 150 {
		t.Errorf("rotated copy = %d bytes, want 150", len(rot))
	}
	cur, _ := os.ReadFile(path)
	if len(cur) != 50 {
		t.Errorf("fresh log = %d bytes, want 50", len(cur))
	}

	// A second rotation REPLACES .1 rather than accumulating more copies.
	for i := 0; i < 3; i++ {
		_ = Append(path, line, max)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		switch e.Name() {
		case FileName, FileName + ".1", FileName + ".lock":
		default:
			t.Errorf("unexpected file %q after repeated rotation", e.Name())
		}
	}
}

func TestAppend_CreatesPrivateFileAndDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", FileName)
	if err := Append(path, []byte("x\n"), MaxBytes); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode = %v, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
	}
}

func TestAppend_RefusesToFollowASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, FileName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Append(link, []byte("x\n"), MaxBytes); err == nil {
		t.Error("Append followed a symlink")
	}
	if b, _ := os.ReadFile(target); len(b) != 0 {
		t.Errorf("symlink target was written: %q", b)
	}
}

func TestAppend_ConcurrentWritersNeverInterleaveLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				WriteBestEffort(path, 1<<30, Base{Time: "t", Level: "info", Msg: strings.Repeat("m", 200), Op: "list"})
			}
		}()
	}
	wg.Wait()
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 400 {
		t.Fatalf("lines = %d, want 400", len(lines))
	}
	for i, l := range lines {
		var b Base
		if err := json.Unmarshal([]byte(l), &b); err != nil {
			t.Fatalf("line %d corrupt: %v", i, err)
		}
	}
}

func TestWriteBestEffort_FailureIsSwallowedAndDefaultsMax(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Parent "directory" is a regular file: MkdirAll fails. Must not panic.
	WriteBestEffort(filepath.Join(blocker, FileName), 0, Base{Msg: "x"})
	// An unencodable value is dropped, not panicked on.
	WriteBestEffort(filepath.Join(dir, FileName), 0, make(chan int))
	if _, err := os.Stat(filepath.Join(dir, FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unencodable value created a file: %v", err)
	}
}

func TestRotatedAndLockFilesDoNotMatchJSONLGlob(t *testing.T) {
	// A logSources glob is ${XDG_STATE_HOME}/<name>/*.jsonl; the rotated copy
	// and the lock must stay outside it or Loki would re-ingest rotated lines.
	for _, n := range []string{FileName + ".1", FileName + ".lock"} {
		if ok, _ := filepath.Match("*.jsonl", n); ok {
			t.Errorf("%q matches *.jsonl", n)
		}
	}
	if ok, _ := filepath.Match("*.jsonl", FileName); !ok {
		t.Errorf("%q does not match *.jsonl", FileName)
	}
}

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestPathAndResolve(t *testing.T) {
	const ev, svc = "SOME_EVENTS_FILE", "pg-connector-demo"
	cases := []struct {
		name        string
		env         map[string]string
		wantPath    string
		wantResolve string
	}{
		{"explicit override", map[string]string{ev: "/x/e.jsonl", "XDG_STATE_HOME": "/s"}, "/x/e.jsonl", "/x/e.jsonl"},
		{"xdg state home", map[string]string{"XDG_STATE_HOME": "/s"}, "/s/pg-connector-demo/events.jsonl", "/s/pg-connector-demo/events.jsonl"},
		{"home fallback", map[string]string{"HOME": "/h"}, "/h/.local/state/pg-connector-demo/events.jsonl", "/h/.local/state/pg-connector-demo/events.jsonl"},
		{"nothing", map[string]string{}, "", ""},
		{"off disables", map[string]string{ev: "off", "XDG_STATE_HOME": "/s"}, "off", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Path(env(c.env), ev, svc); got != c.wantPath {
				t.Errorf("Path = %q, want %q", got, c.wantPath)
			}
			if got := Resolve(env(c.env), ev, svc); got != c.wantResolve {
				t.Errorf("Resolve = %q, want %q", got, c.wantResolve)
			}
		})
	}
}
