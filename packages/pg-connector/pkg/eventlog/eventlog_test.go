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

	long := NewBase("svc", "x", "", start, end, errors.New(strings.Repeat("e", 1000)))
	if len(long.Error) > MaxErrorBytes+3 {
		t.Errorf("error not truncated: %d bytes", len(long.Error))
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
