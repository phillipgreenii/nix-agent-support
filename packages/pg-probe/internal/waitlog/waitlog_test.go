package waitlog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

func newRoot(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pgw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func opts(root, path, re string) Options {
	return Options{
		Path: path, Pattern: regexp.MustCompile(re), Roots: []string{root},
		Timeout: 2 * time.Second, Poll: 5 * time.Millisecond, Lines: 10,
	}
}

func TestWait_MatchesAnExistingTailLine(t *testing.T) {
	root := newRoot(t)
	p := filepath.Join(root, "a.log")
	write(t, p, "one\nready now\nthree\n")
	got, err := Wait(context.Background(), opts(root, p, `^ready`))
	if err != nil || got != "ready now" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestWait_ReturnsOnTheFirstMatchOnly(t *testing.T) {
	root := newRoot(t)
	p := filepath.Join(root, "a.log")
	write(t, p, "hit 1\nhit 2\n")
	got, err := Wait(context.Background(), opts(root, p, `hit`))
	if err != nil || got != "hit 1" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestWait_ReturnsALineAppendedLater(t *testing.T) {
	root := newRoot(t)
	p := filepath.Join(root, "a.log")
	write(t, p, "boot\n")
	go func() {
		time.Sleep(50 * time.Millisecond)
		appendTo(t, p, "noise\nlistening on :1\n")
	}()
	got, err := Wait(context.Background(), opts(root, p, `listening`))
	if err != nil || got != "listening on :1" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestWait_TimesOutWithErrTimeout(t *testing.T) {
	root := newRoot(t)
	p := filepath.Join(root, "a.log")
	write(t, p, "nothing here\n")
	o := opts(root, p, `never`)
	o.Timeout = 80 * time.Millisecond
	start := time.Now()
	_, err := Wait(context.Background(), o)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if d := time.Since(start); d < 60*time.Millisecond || d > time.Second {
		t.Errorf("timed out after %v, want about 80ms", d)
	}
}

func TestWait_NewOnlyIgnoresHistory(t *testing.T) {
	root := newRoot(t)
	p := filepath.Join(root, "a.log")
	write(t, p, "stale ready\n")
	o := opts(root, p, `ready`)
	o.Lines = 0
	o.Timeout = 80 * time.Millisecond
	if _, err := Wait(context.Background(), o); !errors.Is(err, ErrTimeout) {
		t.Fatalf("history line matched with Lines=0: %v", err)
	}
}

func TestWait_LinesLimitsHistory(t *testing.T) {
	root := newRoot(t)
	p := filepath.Join(root, "a.log")
	write(t, p, "target\nb\nc\nd\n")
	o := opts(root, p, `target`)
	o.Lines = 2 // only c and d
	o.Timeout = 80 * time.Millisecond
	if _, err := Wait(context.Background(), o); !errors.Is(err, ErrTimeout) {
		t.Fatalf("a line outside the last 2 matched: %v", err)
	}
	o.FromStart = true
	o.Timeout = 2 * time.Second
	if got, err := Wait(context.Background(), o); err != nil || got != "target" {
		t.Fatalf("FromStart: got %q, %v", got, err)
	}
}

func TestWait_PartialLineWaitsForItsNewline(t *testing.T) {
	root := newRoot(t)
	p := filepath.Join(root, "a.log")
	write(t, p, "")
	o := opts(root, p, `^done$`)
	go func() {
		time.Sleep(30 * time.Millisecond)
		appendTo(t, p, "do")
		time.Sleep(60 * time.Millisecond)
		appendTo(t, p, "ne\n")
	}()
	if got, err := Wait(context.Background(), o); err != nil || got != "done" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestWait_UnterminatedFinalLineIsNotAMatch(t *testing.T) {
	root := newRoot(t)
	p := filepath.Join(root, "a.log")
	write(t, p, "ready") // no newline
	o := opts(root, p, `ready`)
	o.Timeout = 80 * time.Millisecond
	if _, err := Wait(context.Background(), o); !errors.Is(err, ErrTimeout) {
		t.Fatalf("an unterminated line matched: %v", err)
	}
}

func TestWait_SurvivesTruncation(t *testing.T) {
	root := newRoot(t)
	p := filepath.Join(root, "a.log")
	write(t, p, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n")
	o := opts(root, p, `fresh`)
	o.Lines = 0
	go func() {
		time.Sleep(40 * time.Millisecond)
		write(t, p, "fresh\n") // same inode, shorter than the read offset
	}()
	if got, err := Wait(context.Background(), o); err != nil || got != "fresh" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestWait_SurvivesRotation(t *testing.T) {
	root := newRoot(t)
	p := filepath.Join(root, "a.log")
	write(t, p, "old\n")
	o := opts(root, p, `after rotation`)
	o.Lines = 0
	go func() {
		time.Sleep(40 * time.Millisecond)
		if err := os.Rename(p, p+".1"); err != nil {
			t.Error(err)
			return
		}
		write(t, p, "after rotation\n")
	}()
	if got, err := Wait(context.Background(), o); err != nil || got != "after rotation" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestWait_ContextCancel(t *testing.T) {
	root := newRoot(t)
	p := filepath.Join(root, "a.log")
	write(t, p, "x\n")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(40 * time.Millisecond); cancel() }()
	o := opts(root, p, `never`)
	o.Timeout = 5 * time.Second
	if _, err := Wait(ctx, o); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestWait_StripsCarriageReturn(t *testing.T) {
	root := newRoot(t)
	p := filepath.Join(root, "a.log")
	write(t, p, "ready\r\n")
	if got, err := Wait(context.Background(), opts(root, p, `^ready$`)); err != nil || got != "ready" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestWait_ValidatesTimeoutAndLines(t *testing.T) {
	root := newRoot(t)
	p := filepath.Join(root, "a.log")
	write(t, p, "x\n")
	for _, tc := range []struct {
		name string
		mut  func(*Options)
	}{
		{"zero timeout", func(o *Options) { o.Timeout = 0 }},
		{"negative timeout", func(o *Options) { o.Timeout = -time.Second }},
		{"over cap", func(o *Options) { o.Timeout = MaxTimeout + time.Second }},
		{"negative lines", func(o *Options) { o.Lines = -1 }},
	} {
		o := opts(root, p, `x`)
		tc.mut(&o)
		var ref *RefusedError
		if _, err := Wait(context.Background(), o); !errors.As(err, &ref) {
			t.Errorf("%s: want RefusedError, got %v", tc.name, err)
		}
	}
}

func TestResolveUnderRoots_Refusals(t *testing.T) {
	root := newRoot(t)
	outside := newRoot(t)
	inside := filepath.Join(root, "ok.log")
	write(t, inside, "x\n")
	secret := filepath.Join(outside, "secret")
	write(t, secret, "token\n")

	if _, err := ResolveUnderRoots(inside, []string{root}); err != nil {
		t.Fatalf("positive control: %v", err)
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "sub")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	sibling := root + "-sibling"
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sibling) })
	write(t, filepath.Join(sibling, "f"), "x\n")

	for name, p := range map[string]string{
		"outside the roots":        secret,
		"symlink escaping a root":  link,
		"dotdot escape":            filepath.Join(root, "..", filepath.Base(outside), "secret"),
		"a directory":              dir,
		"a fifo":                   fifo,
		"the root itself":          root,
		"a prefix-sharing sibling": filepath.Join(sibling, "f"),
		"a relative path":          "ok.log",
	} {
		var ref *RefusedError
		if _, err := ResolveUnderRoots(p, []string{root}); !errors.As(err, &ref) {
			t.Errorf("%s (%s): want RefusedError, got %v", name, p, err)
		}
	}

	if _, err := ResolveUnderRoots(filepath.Join(root, "absent"), []string{root}); err == nil || strings.Contains(err.Error(), "refused") {
		t.Errorf("missing file: want a plain not-found error, got %v", err)
	}
	if _, err := ResolveUnderRoots(inside, nil); err == nil {
		t.Error("no roots: want a refusal")
	}
}
