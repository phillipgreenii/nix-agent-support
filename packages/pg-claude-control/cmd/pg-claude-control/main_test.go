package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

const (
	startA = "Wed Oct 14 07:51:50 2026"
	startB = "Thu Oct 15 09:00:00 2026"
	uuid1  = "9806c8aa-1111-4222-8333-444455556666"
	uuid2  = "9806c8aa-1111-4222-8333-777788889999"
)

// world is a hermetic stand-in for the machine: a registry directory, a state directory, a set
// of live pids with their start times (answered through a cmdruntest.Fake for ps), and a record
// of every signal sent.
type world struct {
	t       *testing.T
	dir     string
	mu      sync.Mutex
	started map[int]string // live pid -> its real start time
	sent    []string
	out     bytes.Buffer
	errb    bytes.Buffer
	stdin   string
	env     map[string]string
}

func newWorld(t *testing.T) *world {
	dir := t.TempDir()
	w := &world{t: t, dir: dir, started: map[int]string{}, env: map[string]string{
		"CLAUDE_CONFIG_DIR":           filepath.Join(dir, "claude"),
		"PG_CLAUDE_CONTROL_STATE_DIR": filepath.Join(dir, "state"),
	}}
	must(t, os.MkdirAll(filepath.Join(dir, "claude", "sessions"), 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "state", "inbox"), 0o700))
	return w
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// claude registers a running Claude: a registry file, and the process in the live table.
func (w *world) claude(pid int, session, name, procStart string) {
	w.t.Helper()
	b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": session, "name": name, "procStart": procStart, "cwd": "/example/project"})
	must(w.t, os.WriteFile(filepath.Join(w.dir, "claude", "sessions", fmt.Sprint(pid)+".json"), b, 0o600))
	w.mu.Lock()
	w.started[pid] = procStart
	w.mu.Unlock()
}

func (w *world) die(pid int) {
	w.mu.Lock()
	delete(w.started, pid)
	w.mu.Unlock()
}

func (w *world) signals() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.sent...)
}

func (w *world) app() *app {
	fake := cmdruntest.New(w.t)
	fake.On(cmdruntest.MatchName("ps")).Respond(func(c cmdrun.Call) cmdrun.Result {
		var pid int
		_, _ = fmt.Sscan(c.Args[len(c.Args)-1], &pid)
		w.mu.Lock()
		defer w.mu.Unlock()
		if s, ok := w.started[pid]; ok {
			return cmdruntest.Out(s + "\n")
		}
		return cmdruntest.Fail(1, "")
	})
	return &app{
		out: &w.out, errw: &w.errb, stdin: strings.NewReader(w.stdin),
		getenv: func(k string) string { return w.env[k] },
		runner: fake.Runner(),
		kill: func(pid int, sig syscall.Signal) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			if sig == 0 {
				if _, ok := w.started[pid]; ok {
					return nil
				}
				return syscall.ESRCH
			}
			w.sent = append(w.sent, fmt.Sprintf("%d:%d", pid, sig))
			return nil
		},
		poll: 10 * time.Millisecond,
	}
}

func (w *world) run(args ...string) int {
	w.out.Reset()
	w.errb.Reset()
	return w.app().run(args)
}

func (w *world) fifoPath(pid int) string {
	return filepath.Join(w.dir, "state", "inbox", fmt.Sprint(pid)+".fifo")
}

// inbox plays the mod for one pid: a real FIFO opened read-write (as the listener does), every
// line delivered on the returned channel, and a reply written when the line carries reply=.
func (w *world) inbox(pid int, respond func(line string) string) chan string {
	w.t.Helper()
	p := w.fifoPath(pid)
	must(w.t, syscall.Mkfifo(p, 0o600))
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	must(w.t, err)
	lines := make(chan string, 64)
	w.t.Cleanup(func() { _ = f.Close() })
	go func() {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			lines <- line
			if respond == nil {
				continue
			}
			for _, tok := range strings.Fields(line) {
				if path, ok := strings.CutPrefix(tok, "reply="); ok {
					body := respond(line)
					if body != "" {
						_ = os.WriteFile(path+".tmp", []byte(body), 0o600)
						_ = os.Rename(path+".tmp", path)
					}
				}
			}
		}
	}()
	return lines
}

func next(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case l := <-ch:
		return l
	case <-time.After(3 * time.Second):
		t.Fatal("no request line arrived")
		return ""
	}
}

func none(t *testing.T, ch chan string) {
	t.Helper()
	select {
	case l := <-ch:
		t.Fatalf("unexpected request line: %q", l)
	case <-time.After(150 * time.Millisecond):
	}
}

func okReply(string) string { return `{"ok":true,"result":{"text":"draft","cursor":5}}` + "\n" }

func TestDefaults(t *testing.T) {
	if defaultWait != 3*time.Second || defaultTimeout != 10*time.Second {
		t.Fatalf("defaults: wait %v timeout %v", defaultWait, defaultTimeout)
	}
}

func TestSubmitWritesOneLine_Exit0(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	ch := w.inbox(4242, nil)
	w.stdin = "hello there\n"
	if rc := w.run("submit", "4242"); rc != 0 {
		t.Fatalf("rc=%d err=%s", rc, w.errb.String())
	}
	line := next(t, ch)
	if !strings.HasPrefix(line, "id=") || !strings.HasSuffix(line, " submit hello there") {
		t.Fatalf("line %q", line)
	}
	if !strings.Contains(w.out.String(), "sent; effect not confirmed") {
		t.Fatalf("out %q", w.out.String())
	}
}

func TestExitCodes(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	w.claude(4300, uuid2, "dup", startA)
	w.claude(4301, "9806c8aa-1111-4222-8333-000000000001", "dup", startA)
	w.claude(4400, "9806c8aa-1111-4222-8333-000000000002", "nomod", startA)
	w.claude(4500, "9806c8aa-1111-4222-8333-000000000003", "deaf", startA)
	must(t, syscall.Mkfifo(w.fifoPath(4500), 0o600)) // a FIFO nobody reads: ENXIO
	ch := w.inbox(4242, nil)

	cases := []struct {
		name  string
		stdin string
		args  []string
		want  int
		err   string
	}{
		{"usage: no args", "", nil, 2, ""},
		{"usage: unknown command", "", []string{"frobnicate"}, 2, "usage"},
		{"usage: slash text", "/exit", []string{"submit", "4242"}, 2, "starting with /"},
		{"usage: empty text", "", []string{"submit", "4242"}, 2, "needs the text"},
		{"usage: run not allowed", "", []string{"run", "4242", "model"}, 2, "allowlist"},
		{"usage: missing target", "", []string{"interrupt"}, 2, "usage"},
		{"usage: bad flag", "", []string{"interrupt", "4242", "--nope"}, 2, "usage"},
		{"usage: signal and escalate", "", []string{"stop", "4242", "--signal", "--escalate", "1s"}, 2, "exclude"},
		{"not found: unknown pid", "x", []string{"submit", "999"}, exitNoProcess, "pid 999 is not a running Claude"},
		{"not found: unknown name", "x", []string{"submit", "ghost"}, exitNoProcess, "no running Claude"},
		{"unavailable: no inbox", "x", []string{"submit", "4400", "--wait", "120ms"}, exitNoControlMod, "waiting for inbox"},
		{"busy: nobody reading", "x", []string{"submit", "4500"}, exitNotAccepting, "stop pid:4500 --escalate"},
		{"ambiguous: name", "x", []string{"submit", "dup"}, exitAmbiguous, "pid 4300"},
		{"ambiguous: name lists both", "x", []string{"submit", "name:dup"}, exitAmbiguous, "pid 4301"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w.stdin = c.stdin
			if rc := w.run(c.args...); rc != c.want {
				t.Fatalf("rc=%d want %d; err=%s", rc, c.want, w.errb.String())
			}
			if !strings.Contains(w.errb.String(), c.err) {
				t.Fatalf("stderr %q lacks %q", w.errb.String(), c.err)
			}
			if c.want != 0 && !strings.Contains(w.errb.String(), "error: ") && c.want != 2 {
				t.Fatalf("no error: line in %q", w.errb.String())
			}
		})
	}
	none(t, ch) // not one failure wrote to the live inbox
}

func TestExit4MessageNamesStopAndMod(t *testing.T) {
	w := newWorld(t)
	w.claude(4400, uuid1, "nomod", startA)
	w.stdin = "x"
	if rc := w.run("submit", "4400", "--wait", "100ms"); rc != exitNoControlMod {
		t.Fatalf("rc=%d", rc)
	}
	e := w.errb.String()
	for _, want := range []string{"error: no_control_mod", "waiting for inbox...", "stop pid:4400 --signal", "enable the pg-claude-control mod"} {
		if !strings.Contains(e, want) {
			t.Fatalf("stderr %q lacks %q", e, want)
		}
	}
}

func TestExit4RetriesUntilTheFifoAppears(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	var ch chan string
	a := w.app()
	sleeps := 0
	// the inbox appears, complete with its reader, during the second retry pause: no wall clock
	a.sleep = func(time.Duration) {
		sleeps++
		if sleeps == 2 {
			ch = w.inbox(4242, nil)
		}
	}
	a.stdin = strings.NewReader("late")
	if rc := a.run([]string{"submit", "4242", "--wait", "3s"}); rc != 0 {
		t.Fatalf("rc=%d err=%s", rc, w.errb.String())
	}
	if sleeps != 2 || strings.Count(w.errb.String(), "waiting for inbox...") != 1 {
		t.Fatalf("sleeps=%d stderr=%q", sleeps, w.errb.String())
	}
	if l := next(t, ch); !strings.HasSuffix(l, " submit late") {
		t.Fatalf("line %q", l)
	}
}

func TestNoControlModWaitIsCountedInRetrySteps(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	a := w.app()
	var total time.Duration
	a.sleep = func(d time.Duration) { total += d }
	a.stdin = strings.NewReader("x")
	if rc := a.run([]string{"submit", "4242"}); rc != exitNoControlMod {
		t.Fatalf("rc=%d", rc)
	}
	if total != defaultWait {
		t.Fatalf("slept %v, want %v", total, defaultWait)
	}
}

func TestFifoWithoutReaderIsExit5NotRetried(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	must(t, syscall.Mkfifo(w.fifoPath(4242), 0o600))
	a := w.app()
	a.sleep = func(time.Duration) { t.Fatal("ENXIO must not be retried") }
	a.stdin = strings.NewReader("x")
	if rc := a.run([]string{"submit", "4242"}); rc != exitNotAccepting {
		t.Fatalf("rc=%d", rc)
	}
}

func TestExit5FullBuffer(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	p := w.fifoPath(4242)
	must(t, syscall.Mkfifo(p, 0o600))
	rd, err := os.OpenFile(p, os.O_RDWR, 0) // a reader that never reads
	must(t, err)
	defer func() { _ = rd.Close() }()
	fd, err := syscall.Open(p, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
	must(t, err)
	chunk := []byte(strings.Repeat("x", 511) + "\n")
	for {
		if _, err := syscall.Write(fd, chunk); err != nil {
			break
		}
	}
	_ = syscall.Close(fd)
	w.stdin = "x"
	if rc := w.run("submit", "4242"); rc != exitNotAccepting {
		t.Fatalf("rc=%d err=%s", rc, w.errb.String())
	}
}

func TestExit6RefusedAndRepliesCarryResults(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	ch := w.inbox(4242, func(line string) string {
		if strings.Contains(line, " read") {
			return okReply(line)
		}
		return `{"ok":false,"reason":"command not in run.allow: compact"}` + "\n"
	})
	if rc := w.run("run", "--reply", "4242", "compact"); rc != exitRefused {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(w.errb.String(), "command not in run.allow") || !strings.Contains(w.errb.String(), "refused_by_mod") {
		t.Fatalf("stderr %q", w.errb.String())
	}
	if l := next(t, ch); !strings.Contains(l, " run compact ") || !strings.Contains(l, "reply=") {
		t.Fatalf("line %q", l)
	}
	if rc := w.run("read", "4242", "--json"); rc != 0 {
		t.Fatalf("rc=%d err=%s", rc, w.errb.String())
	}
	var got struct {
		Confirmed bool
		Result    struct {
			Text string
		}
	}
	must(t, json.Unmarshal(w.out.Bytes(), &got))
	if !got.Confirmed || got.Result.Text != "draft" {
		t.Fatalf("out %s", w.out.String())
	}
	if rc := w.run("read", "4242"); rc != 0 || strings.TrimSpace(w.out.String()) != "draft" {
		t.Fatalf("rc=%d out %q", rc, w.out.String())
	}
}

func TestExit8NoReply(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	w.inbox(4242, nil)
	if rc := w.run("read", "4242", "--timeout", "150ms", "--json"); rc != exitNoReply {
		t.Fatalf("rc=%d", rc)
	}
	var e struct {
		Error string
		Exit  int
	}
	must(t, json.Unmarshal(w.errb.Bytes(), &e))
	if e.Error != "no_reply" || e.Exit != exitNoReply {
		t.Fatalf("err %s", w.errb.String())
	}
}

func TestExit1Unexpected(t *testing.T) {
	w := newWorld(t)
	// the sessions "directory" is a file: an unexpected error, not "no such process"
	must(t, os.RemoveAll(filepath.Join(w.dir, "claude", "sessions")))
	must(t, os.WriteFile(filepath.Join(w.dir, "claude", "sessions"), []byte("x"), 0o600))
	w.stdin = "x"
	if rc := w.run("submit", "1234"); rc != 1 {
		t.Fatalf("rc=%d err=%s", rc, w.errb.String())
	}
	if !strings.Contains(w.errb.String(), "error: internal") {
		t.Fatalf("stderr %q", w.errb.String())
	}
}

func TestTargetReadings(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	w.claude(4243, uuid2, "4242", startA) // a name that looks like a pid
	ch42 := w.inbox(4242, nil)
	ch43 := w.inbox(4243, nil)
	send := func(target string) {
		w.stdin = "t"
		if rc := w.run("submit", target); rc != 0 {
			t.Fatalf("%s: rc=%d err=%s", target, rc, w.errb.String())
		}
	}
	send("4242") // bare number is a pid
	next(t, ch42)
	send("name:4242") // forced name
	next(t, ch43)
	send("pid:4243")
	next(t, ch43)
	send(uuid1) // UUID shape is a session id
	next(t, ch42)
	send("session:" + uuid2)
	next(t, ch43)
	send("example-one") // anything else is a name
	next(t, ch42)
	w.stdin = "t"
	if rc := w.run("submit", "name:"+uuid1); rc != exitNoProcess { // forced name: no Claude is named that
		t.Fatalf("rc=%d", rc)
	}
}

func TestReusedPidNothingWrittenOrSignalled(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	ch := w.inbox(4242, nil)
	w.mu.Lock()
	w.started[4242] = startB // the pid now belongs to another process
	w.mu.Unlock()
	w.stdin = "x"
	for _, args := range [][]string{{"submit", "4242"}, {"interrupt", "4242"}, {"stop", "4242", "--signal"}, {"stop", "4242"}, {"run", "4242", "exit"}} {
		if rc := w.run(args...); rc != exitNoProcess {
			t.Fatalf("%v: rc=%d err=%s", args, rc, w.errb.String())
		}
	}
	none(t, ch)
	if s := w.signals(); len(s) != 0 {
		t.Fatalf("signalled %v", s)
	}
}

func TestDeadPid(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	w.die(4242)
	w.stdin = "x"
	if rc := w.run("submit", "4242"); rc != exitNoProcess {
		t.Fatalf("rc=%d", rc)
	}
}

func TestRegistryChangedBeforeWrite(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	ch := w.inbox(4242, nil)
	a := w.app()
	e, ce := a.resolve("4242")
	if ce != nil {
		t.Fatal(ce)
	}
	w.claude(4242, uuid1, "example-one", startB) // the registry file is replaced under us
	if _, ce := a.deliver(e, filepath.Join(w.dir, "state"), req{verb: "interrupt"}, opts{wait: time.Millisecond}); ce == nil || ce.name != errNoProcess {
		t.Fatalf("ce=%v", ce)
	}
	none(t, ch)
}

func TestSubmitPayloadForLongAndMultilineText(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	ch := w.inbox(4242, nil)
	for _, text := range []string{strings.Repeat("word ", 200), "line one\nline two"} {
		w.stdin = text
		if rc := w.run("submit", "4242"); rc != 0 {
			t.Fatalf("rc=%d err=%s", rc, w.errb.String())
		}
		line := next(t, ch)
		if len(line)+1 > maxLine {
			t.Fatalf("line is %d bytes", len(line)+1)
		}
		i := strings.Index(line, "payload=")
		if i < 0 {
			t.Fatalf("no payload in %q", line)
		}
		got, err := os.ReadFile(line[i+len("payload="):])
		must(t, err)
		if string(got) != text {
			t.Fatalf("payload %q", got)
		}
		if !strings.HasPrefix(line[i+len("payload="):], filepath.Join(w.dir, "state", "inbox", "payloads")+"/") {
			t.Fatalf("payload outside payloads/: %q", line)
		}
	}
}

func TestRunAndInterruptLines(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	ch := w.inbox(4242, nil)
	if rc := w.run("run", "4242", "/compact", "focus", "on", "tests"); rc != 0 {
		t.Fatalf("rc=%d err=%s", rc, w.errb.String())
	}
	if l := next(t, ch); !strings.HasSuffix(l, " run compact focus on tests") {
		t.Fatalf("line %q", l)
	}
	if rc := w.run("interrupt", "4242"); rc != 0 {
		t.Fatal(rc)
	}
	if l := next(t, ch); !strings.HasSuffix(l, " interrupt") {
		t.Fatalf("line %q", l)
	}
	w.env["PG_CLAUDE_CONTROL_RUN_ALLOW"] = "status"
	if rc := w.run("run", "4242", "compact"); rc != 2 {
		t.Fatalf("rc=%d: the allowlist comes from the shared environment variable", rc)
	}
	if rc := w.run("run", "4242", "status"); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
}

func TestStopSignalWorksWithoutTheMod(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA) // no inbox at all
	if rc := w.run("stop", "4242", "--signal"); rc != 0 {
		t.Fatalf("rc=%d err=%s", rc, w.errb.String())
	}
	if s := w.signals(); len(s) != 1 || s[0] != fmt.Sprintf("4242:%d", syscall.SIGTERM) {
		t.Fatalf("signals %v", s)
	}
}

func TestStopWithoutModSuggestsSignal(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	if rc := w.run("stop", "4242", "--wait", "50ms"); rc != exitNoControlMod {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(w.errb.String(), "stop pid:4242 --signal") {
		t.Fatalf("stderr %q", w.errb.String())
	}
}

func TestStopInterruptsThenExitsAndStopsWhenTheProcessDies(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	ch := w.inbox(4242, func(string) string {
		go func() { time.Sleep(50 * time.Millisecond); w.die(4242) }()
		return `{"ok":true}` + "\n"
	})
	if rc := w.run("stop", "4242", "--escalate", "5s"); rc != 0 {
		t.Fatalf("rc=%d err=%s", rc, w.errb.String())
	}
	if l := next(t, ch); !strings.HasSuffix(l, " interrupt") {
		t.Fatalf("first line %q", l)
	}
	if l := next(t, ch); !strings.Contains(l, " run exit ") {
		t.Fatalf("second line %q", l)
	}
	if s := w.signals(); len(s) != 0 {
		t.Fatalf("signalled %v", s)
	}
}

func TestStopEscalatesToSIGTERMNeverSIGKILL(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	w.inbox(4242, func(string) string { return `{"ok":true}` + "\n" }) // the Claude stays alive
	start := time.Now()
	if rc := w.run("stop", "4242", "--escalate", "200ms"); rc != 0 {
		t.Fatalf("rc=%d err=%s", rc, w.errb.String())
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatal("escalated before the duration")
	}
	if s := w.signals(); len(s) != 1 || s[0] != fmt.Sprintf("4242:%d", syscall.SIGTERM) {
		t.Fatalf("signals %v", s)
	}
}

func TestStopReportsSessionStayedWhenTheClaudeStaysAlive(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	w.inbox(4242, func(string) string { return `{"ok":true}` + "\n" }) // exit asked, Claude stays
	if rc := w.run("stop", "4242", "--timeout", "150ms"); rc != exitStayed {
		t.Fatalf("rc=%d err=%s", rc, w.errb.String())
	}
	for _, want := range []string{"error: session_stayed", "stop pid:4242 --signal"} {
		if !strings.Contains(w.errb.String(), want) {
			t.Fatalf("stderr %q lacks %q", w.errb.String(), want)
		}
	}
	if s := w.signals(); len(s) != 0 {
		t.Fatalf("signalled %v", s)
	}
}

func TestStopPrintsTheModsRefusal(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	w.inbox(4242, func(string) string { return `{"ok":false,"reason":"command not in run.allow: exit"}` + "\n" })
	if rc := w.run("stop", "4242", "--escalate", "5s"); rc != exitRefused {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(w.errb.String(), "command not in run.allow: exit") {
		t.Fatalf("stderr %q", w.errb.String())
	}
	if s := w.signals(); len(s) != 0 {
		t.Fatalf("signalled %v", s)
	}
}

func TestList(t *testing.T) {
	w := newWorld(t)
	w.claude(4242, uuid1, "example-one", startA)
	w.claude(4300, uuid2, "example-two", startA)
	w.claude(4400, "9806c8aa-1111-4222-8333-000000000002", "gone", startA)
	w.die(4400)
	w.inbox(4242, nil)
	if rc := w.run("list"); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	want := "4242\t" + uuid1 + "\texample-one\tinbox=yes\n4300\t" + uuid2 + "\texample-two\tinbox=no\n"
	if w.out.String() != want {
		t.Fatalf("out %q", w.out.String())
	}
	if rc := w.run("list", "--json"); rc != 0 {
		t.Fatal(rc)
	}
	var rows []struct {
		PID   int
		Inbox bool
	}
	must(t, json.Unmarshal(w.out.Bytes(), &rows))
	if len(rows) != 2 || !rows[0].Inbox || rows[1].Inbox {
		t.Fatalf("rows %+v", rows)
	}
}

func TestJSONErrorShape(t *testing.T) {
	w := newWorld(t)
	w.claude(4300, uuid2, "dup", startA)
	w.claude(4301, uuid1, "dup", startA)
	w.stdin = "x"
	if rc := w.run("submit", "dup", "--json"); rc != exitAmbiguous {
		t.Fatalf("rc=%d", rc)
	}
	var e struct {
		Error   string
		Exit    int
		Message string
	}
	must(t, json.Unmarshal(w.errb.Bytes(), &e))
	if e.Error != "ambiguous_target" || e.Exit != exitAmbiguous || strings.Count(e.Message, "\npid ") != 2 {
		t.Fatalf("err %+v", e)
	}
}

func TestStateDirResolution(t *testing.T) {
	a := &app{getenv: func(k string) string {
		return map[string]string{"XDG_STATE_HOME": "/example/xdg", "HOME": "/example/home"}[k]
	}}
	if d, _ := a.stateDir(); d != "/example/xdg/pg-claude-control" {
		t.Fatal(d)
	}
	a = &app{getenv: func(k string) string { return map[string]string{"HOME": "/example/home"}[k] }}
	if d, _ := a.stateDir(); d != "/example/home/.local/state/pg-claude-control" {
		t.Fatal(d)
	}
}

// The wire values are a contract with callers (ccpool, scripts): pin the literals.
func TestExitCodeValues(t *testing.T) {
	for name, c := range map[string][2]int{
		"no_such_process":   {exitFor[errNoProcess], 4},
		"no_control_mod":    {exitFor[errNoControlMod], 3},
		"mod_not_accepting": {exitFor[errNotAccepting], 9},
		"no_reply":          {exitFor[errNoReply], 6},
		"session_stayed":    {exitFor[errStayed], 20},
		"refused_by_mod":    {exitFor[errRefused], 21},
		"ambiguous_target":  {exitFor[errAmbiguous], 22},
	} {
		if c[0] != c[1] {
			t.Errorf("%s exits %d; want %d", name, c[0], c[1])
		}
	}
}
