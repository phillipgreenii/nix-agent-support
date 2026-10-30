package notify

import (
	"bytes"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/pg-rescue/internal/notifyclient/fakedaemon"
	"github.com/phillipgreenii/pg-rescue/internal/orphan"
	"github.com/phillipgreenii/pg-rescue/internal/tmpldata"
)

func TestSanitize(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		max  int
		want string
	}{
		"plain":                   {"hello world", 256, "hello world"},
		"empty":                   {"", 256, ""},
		"quotes and backslash":    {`a "b" \ c`, 256, `a "b" \ c`},
		"newline tab to space":    {"a\nb\tc\r\nd", 256, "a b c  d"},
		"line separators":         {"a\u2028b\u2029c", 256, "a b c"},
		"C0 removed":              {"a\x00b\x01c\x1bd", 256, "abcd"},
		"DEL and C1 removed":      {"a\x7fb\u0085c\u009fd", 256, "abcd"},
		"surrounding space":       {"  \n x \t ", 256, "x"},
		"invalid utf8":            {"a\xffb", 256, "a\ufffdb"},
		"unicode kept":            {"héllo 日本語 \U0001F600", 256, "héllo 日本語 \U0001F600"},
		"emoji sequence kept":     {"\U0001F468\u200d\U0001F469", 256, "\U0001F468\u200d\U0001F469"},
		"exactly at the cap":      {strings.Repeat("x", 256), 256, strings.Repeat("x", 256)},
		"one over the cap":        {strings.Repeat("x", 257), 256, strings.Repeat("x", 256)},
		"multibyte at the cap":    {strings.Repeat("é", 128), 256, strings.Repeat("é", 128)},
		"multibyte over the cap":  {strings.Repeat("é", 129), 256, strings.Repeat("é", 128)},
		"cut never splits a rune": {"a" + strings.Repeat("日", 100), 256, "a" + strings.Repeat("日", 85)},
		"200 runes exceed 256 B":  {strings.Repeat("日", 200), 256, strings.Repeat("日", 85)},
		"cap after stripping":     {strings.Repeat("\x01", 500) + "ok", 256, "ok"},
		"cut never ends in space": {strings.Repeat("x", 255) + " yyy", 256, strings.Repeat("x", 255)},
	} {
		if got := Sanitize(tc.in, tc.max); got != tc.want {
			t.Errorf("%s: Sanitize(%q, %d) = %q, want %q", name, tc.in, tc.max, got, tc.want)
		}
		if got := Sanitize(tc.in, tc.max); len(got) > tc.max || !utf8.ValidString(got) {
			t.Errorf("%s: %q breaks the byte cap or UTF-8", name, got)
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

// When the wrapper is gone, the in-flight connection is closed (the fake
// daemon sees EOF while it still holds its reply) and the handler exits
// through the orphan helper (orphan.ExitCode) without waiting for the reply.
func TestOrphanClosesTheInFlightPostAndExitsViaTheHelper(t *testing.T) {
	srv := fakedaemon.Start(t, fakedaemon.Options{Hang: true})
	report := filepath.Join("..", "..", "testdata", "reports", "first-attempt.json")

	var ppid atomic.Int64
	ppid.Store(100)
	exits := &exitLog{}
	rt := Runtime{
		Tmpl: tmpldata.DefaultEnv(),
		Getenv: func(k string) string {
			return map[string]string{"PG_RESCUE_REPORT": report, "PG_OSX_NOTIFIER_SOCKET": srv.Path}[k]
		},
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
	for len(srv.Requests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(srv.Requests()) == 0 {
		t.Fatal("the fake daemon never received the post")
	}
	ppid.Store(1) // the wrapper is gone

	select {
	case code := <-done:
		// The post was cancelled, so Run reports a failed post. In production
		// the helper's os.Exit has already ended the process before this return.
		if code != ExitFailed {
			t.Errorf("Run = %d after its post was cancelled, want %d", code, ExitFailed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the orphan cleanup: the post was not cancelled")
	}
	select {
	case <-srv.Closed():
	case <-time.After(5 * time.Second):
		t.Error("the fake daemon never saw the connection close while holding its reply")
	}
	if got := exits.snapshot(); len(got) != 1 || got[0] != orphan.ExitCode {
		t.Errorf("orphan exit codes = %v, want [%d]", got, orphan.ExitCode)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

// RS-12: a daemon that never replies is bounded by the client deadline (the
// test injects a short one) and the handler exits 1, not 2.
func TestAHungDaemonIsBoundedAndExits1(t *testing.T) {
	srv := fakedaemon.Start(t, fakedaemon.Options{Hang: true})
	rt := Runtime{
		Tmpl:        tmpldata.DefaultEnv(),
		PostTimeout: 150 * time.Millisecond,
		Getenv: func(k string) string {
			return map[string]string{
				"PG_RESCUE_REPORT":       filepath.Join("..", "..", "testdata", "reports", "first-attempt.json"),
				"PG_OSX_NOTIFIER_SOCKET": srv.Path,
			}[k]
		},
	}
	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := Run(rt, nil, &stdout, &stderr)
	if code != ExitFailed || stdout.Len() != 0 || time.Since(start) > 5*time.Second {
		t.Errorf("code=%d stdout=%q after %v stderr=%q", code, stdout.String(), time.Since(start), stderr.String())
	}
}

func TestDefaultRuntimeWatchesForOrphaning(t *testing.T) {
	rt := DefaultRuntime()
	if rt.Watch == nil {
		t.Fatal("DefaultRuntime has no orphan watch: the handler would outlive a killed wrapper")
	}
	rt.Watch(func() { t.Error("cleanup ran although the parent is alive") })()
}
