package proc

import (
	"errors"
	"testing"
	"time"
)

func TestParseStartAndFormatRoundTrip(t *testing.T) {
	for _, s := range []string{"Fri Oct 30 15:50:54 2026", "Wed Oct  4 07:51:50 2026"} {
		got, ok := ParseStart(s)
		if !ok {
			t.Fatalf("%q did not parse", s)
		}
		if again, _ := ParseStart(FormatStart(got)); !again.Equal(got) {
			t.Errorf("%q: round trip %v != %v", s, again, got)
		}
	}
	for _, bad := range []string{"", "yesterday", "t1"} {
		if _, ok := ParseStart(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestADeadPidAndAReusedPidAreNotAlive(t *testing.T) {
	f := NewFake()
	start := time.Date(2026, 10, 30, 15, 50, 54, 0, time.UTC)
	f.Add(10, start, nil)
	ps := FormatStart(start)
	if !Alive(f, 10, ps) {
		t.Fatal("a live pid with a matching start time is dead")
	}
	if !Alive(f, 10, FormatStart(start.Add(time.Second))) {
		t.Fatal("a one second rounding difference must not make it dead")
	}
	if Alive(f, 10, FormatStart(start.Add(time.Hour))) {
		t.Fatal("a reused pid (a different start time) is alive")
	}
	if Alive(f, 11, ps) {
		t.Fatal("a pid that does not exist is alive")
	}
	if !Alive(f, 10, "") || !Alive(f, 10, "t1") {
		t.Fatal("an empty or unparseable procStart must fail open")
	}
}

func TestWatchOnAnExitedPidFailsWithGoneAndAWatchFiresOnce(t *testing.T) {
	f := NewFake()
	if _, err := f.Watch(5, func(time.Time) {}); !errors.Is(err, ErrGone) {
		t.Fatalf("watching a pid that never existed: %v", err)
	}
	f.Add(5, time.Now(), nil)
	n := 0
	stop, err := f.Watch(5, func(time.Time) { n++ })
	if err != nil {
		t.Fatal(err)
	}
	f.Exit(5, time.Now())
	f.Exit(5, time.Now())
	if n != 1 {
		t.Fatalf("the watch fired %d times", n)
	}
	stop()
	if _, err := f.Watch(5, func(time.Time) {}); !errors.Is(err, ErrGone) {
		t.Fatalf("watching the exited pid: %v", err)
	}
}

func TestHostOfReadsCmuxTmuxAndOtherBindings(t *testing.T) {
	if h := HostOf(map[string]string{"CMUX_SURFACE_ID": "s1", "CMUX_WORKSPACE_ID": "w1", "TERM_PROGRAM": "x", "TMUX": "/a,1,0"}); h.Kind != "cmux" || h.CmuxSurface != "s1" || h.CmuxWorkspace != "w1" || h.TmuxSocket != "" {
		t.Errorf("cmux %+v", h)
	}
	if h := HostOf(map[string]string{"TMUX": "/tmp/example-sock,99,0", "TMUX_PANE": "%3"}); h.Kind != "tmux" || h.TmuxSocket != "/tmp/example-sock" || h.TmuxPane != "%3" {
		t.Errorf("tmux %+v", h)
	}
	if a, b := HostOf(map[string]string{"TMUX": "/s1,1,0", "TMUX_PANE": "%0"}), HostOf(map[string]string{"TMUX": "/s2,1,0", "TMUX_PANE": "%0"}); a == b {
		t.Error("two panes with the same id on different sockets must differ")
	}
	if h := HostOf(map[string]string{"TERM_PROGRAM": "example-term"}); h.Kind != "other" || h.TermProgram != "example-term" {
		t.Errorf("other %+v", h)
	}
}

func TestParseProcArgs2ReadsTheEnvironmentAfterTheArguments(t *testing.T) {
	// argc=2, exec path, padding, argv[0], argv[1], then the environment.
	b := []byte{2, 0, 0, 0}
	b = append(b, []byte("/example/bin/claude\x00\x00\x00claude\x00--resume\x00A=1\x00CMUX_SURFACE_ID=s9\x00EMPTY=\x00\x00")...)
	env := parseProcArgs2(b)
	if env["A"] != "1" || env["CMUX_SURFACE_ID"] != "s9" || len(env) != 3 {
		t.Fatalf("env %v", env)
	}
	if len(parseProcArgs2(nil)) != 0 || len(parseProcArgs2([]byte{1, 0})) != 0 {
		t.Fatal("a short buffer must yield an empty environment")
	}
}
