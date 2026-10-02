package notify

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/orphan"
	"github.com/phillipgreenii/pg-rescue/internal/tmpldata"
)

func TestSanitize(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"plain":                   {"hello world", "hello world"},
		"empty":                   {"", ""},
		"quotes and backslash":    {`a "b" \ c`, `a "b" \ c`},
		"newline tab to space":    {"a\nb\tc\r\nd", "a b c  d"},
		"line separators":         {"a b c", "a b c"},
		"C0 removed":              {"a\x00b\x01c\x1bd", "abcd"},
		"DEL and C1 removed":      {"a\x7fb\u0085c\u009fd", "abcd"},
		"surrounding space":       {"  \n x \t ", "x"},
		"invalid utf8":            {"a\xffb", "a�b"},
		"unicode kept":            {"héllo 日本語 \U0001F600", "héllo 日本語 \U0001F600"},
		"emoji sequence kept":     {"\U0001F468‍\U0001F469", "\U0001F468‍\U0001F469"},
		"exactly at the cap":      {strings.Repeat("x", 200), strings.Repeat("x", 200)},
		"one over the cap":        {strings.Repeat("x", 201), strings.Repeat("x", 200)},
		"cap counts characters":   {strings.Repeat("日", 250), strings.Repeat("日", 200)},
		"cap after stripping":     {strings.Repeat("\x01", 500) + "ok", "ok"},
		"cut never ends in space": {strings.Repeat("x", 199) + " yyy", strings.Repeat("x", 199)},
	} {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("%s: Sanitize(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
}

func TestScriptIsFixedAndReadsEveryValueFromArgv(t *testing.T) {
	text := strings.Join(Script, "\n")
	for _, want := range []string{"item 1 of argv", "item 2 of argv", "item 3 of argv", "on run argv", "end run"} {
		if !strings.Contains(text, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	for _, bad := range []string{"%", "{{", "do shell script"} {
		if strings.Contains(text, bad) {
			t.Errorf("script contains %q: it must be fixed text with no placeholders", bad)
		}
	}
}

// exitLog records what the orphan helper's Exit was asked to do.
type exitLog struct {
	mu    sync.Mutex
	codes []int
}

func (e *exitLog) Exit(c int) { e.mu.Lock(); e.codes = append(e.codes, c); e.mu.Unlock() }
func (e *exitLog) snapshot() []int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]int(nil), e.codes...)
}

// When the wrapper is gone, the handler kills the osascript it is waiting on
// and exits through the orphan helper (orphan.ExitCode).
func TestOrphanKillsOsascriptAndExitsViaTheHelper(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	script := "#!/bin/sh\n: > '" + started + "'\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "osascript"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PG_RESCUE_REPORT", filepath.Join("..", "..", "testdata", "reports", "first-attempt.json"))

	var ppid atomic.Int64
	ppid.Store(100)
	exits := &exitLog{}
	rt := Runtime{
		Tmpl: tmpldata.DefaultEnv(),
		Watch: func(cleanup func()) func() {
			return orphan.WatchWith(orphan.Options{
				Interval: 10 * time.Millisecond,
				Getppid:  func() int { return int(ppid.Load()) },
				Cleanup:  cleanup,
				Exit:     exits.Exit,
			})
		},
	}

	done := make(chan int, 1)
	var stdout, stderr bytes.Buffer
	go func() { done <- Run(rt, nil, &stdout, &stderr) }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatal("the fake osascript never started")
	}
	ppid.Store(1) // the wrapper is gone

	select {
	case code := <-done:
		// osascript was killed, so Run reports a failed post. In production the
		// helper's os.Exit has already ended the process before this return.
		if code != ExitFailed {
			t.Errorf("Run = %d after its osascript was killed, want %d", code, ExitFailed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the orphan cleanup: osascript was not killed")
	}
	if got := exits.snapshot(); len(got) != 1 || got[0] != orphan.ExitCode {
		t.Errorf("orphan exit codes = %v, want [%d]", got, orphan.ExitCode)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

func TestDefaultRuntimeWatchesForOrphaning(t *testing.T) {
	rt := DefaultRuntime()
	if rt.Watch == nil {
		t.Fatal("DefaultRuntime has no orphan watch: the handler would outlive a killed wrapper")
	}
	rt.Watch(func() { t.Error("cleanup ran although the parent is alive") })()
}
