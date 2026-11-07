package watcher

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	canReplay bool
	uuid      string
	started   bool
	replay    bool
	since     uint64
	onHint    func(Hint)
}

func (f *fakeBackend) Start(_ []string, since uint64, replay bool, onHint func(Hint)) error {
	f.started, f.since, f.replay, f.onHint = true, since, replay, onHint
	return nil
}
func (f *fakeBackend) Stop()                    {}
func (f *fakeBackend) CanReplay() bool          { return f.canReplay }
func (f *fakeBackend) VolumeUUID(string) string { return f.uuid }

type fakeCheckpoint struct {
	mu sync.Mutex
	st State
}

func (c *fakeCheckpoint) Load() (State, error) { c.mu.Lock(); defer c.mu.Unlock(); return c.st, nil }

func (c *fakeCheckpoint) Save(s State) error { c.mu.Lock(); defer c.mu.Unlock(); c.st = s; return nil }
func (c *fakeCheckpoint) get() State         { c.mu.Lock(); defer c.mu.Unlock(); return c.st }

type sinkLog struct {
	mu sync.Mutex
	ev []Event
}

func (s *sinkLog) add(e Event) { s.mu.Lock(); s.ev = append(s.ev, e); s.mu.Unlock() }
func (s *sinkLog) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p []string
	for _, e := range s.ev {
		p = append(p, e.Path)
	}
	sort.Strings(p)
	return p
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestWatcher(t *testing.T, cfg Config) (*Watcher, *sinkLog) {
	t.Helper()
	sink := &sinkLog{}
	cfg.Sink = sink.add
	w, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return w, sink
}

func TestDecide(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	good := State{EventID: 42, EventTime: now.Add(-5 * time.Minute), VolumeUUID: "example-uuid"}
	tests := []struct {
		name      string
		st        State
		uuid      string
		canReplay bool
		wantRep   bool
		wantGap   bool
	}{
		{"replay when recent and same volume", good, "example-uuid", true, true, false},
		{"gap at the limit sweeps", State{EventID: 42, EventTime: now.Add(-10 * time.Minute), VolumeUUID: "example-uuid"}, "example-uuid", true, false, true},
		{"long gap sweeps", State{EventID: 42, EventTime: now.Add(-time.Hour), VolumeUUID: "example-uuid"}, "example-uuid", true, false, true},
		{"volume changed sweeps", good, "example-other", true, false, true},
		{"unknown volume sweeps", good, "", true, false, true},
		{"clock went backwards sweeps", State{EventID: 42, EventTime: now.Add(time.Hour), VolumeUUID: "example-uuid"}, "example-uuid", true, false, true},
		{"no checkpoint sweeps without a gap", State{}, "example-uuid", true, false, false},
		{"no history source sweeps without a gap", good, "example-uuid", false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.st, tc.uuid, now, 10*time.Minute, tc.canReplay)
			if d.Replay != tc.wantRep || d.Gap != tc.wantGap {
				t.Fatalf("Decide = %+v, want replay=%v gap=%v", d, tc.wantRep, tc.wantGap)
			}
			if d.Replay && d.SinceID != 42 {
				t.Fatalf("SinceID = %d, want 42", d.SinceID)
			}
		})
	}
}

func TestWatcherRescanRecoversMissedEvent(t *testing.T) {
	root := t.TempDir()
	fb := &fakeBackend{}
	w, sink := newTestWatcher(t, Config{Roots: []string{root}, backend: fb, SweepInterval: time.Hour})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	eventually(t, "initial sweep", func() bool { return !w.LastSweep().IsZero() })

	missed := filepath.Join(root, "example-project", "example-session.jsonl")
	write(t, missed, "{}\n") // the backend emits no hint: the event is missed
	if got := sink.paths(); len(got) != 0 {
		t.Fatalf("sink saw %v before Rescan", got)
	}
	if err := w.Rescan(); err != nil {
		t.Fatal(err)
	}
	got := sink.paths()
	if len(got) != 1 || got[0] != missed {
		t.Fatalf("Rescan reported %v, want [%s]", got, missed)
	}
	if sink.ev[0].Info.Size != 3 || sink.ev[0].Info.Ino == 0 || sink.ev[0].Info.ModTime.IsZero() {
		t.Fatalf("Rescan identity incomplete: %+v", sink.ev[0].Info)
	}
}

func TestWatcherWakeFiltersThenLstats(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "example-a.jsonl")
	skip := filepath.Join(root, "example-b.tmp")
	write(t, keep, "x")
	write(t, skip, "x")
	w, sink := newTestWatcher(t, Config{
		Roots:  []string{root},
		Filter: func(p string) bool { return strings.HasSuffix(p, ".jsonl") },
	})
	w.Wake(skip)
	w.Wake(root) // a directory: nothing to report
	w.Wake(keep)
	gone := filepath.Join(root, "example-gone.jsonl")
	w.Wake(gone)
	if len(sink.ev) != 2 || sink.ev[0].Path != keep || sink.ev[0].Gone {
		t.Fatalf("events = %+v", sink.ev)
	}
	if sink.ev[1].Path != gone || !sink.ev[1].Gone {
		t.Fatalf("missing path not reported Gone: %+v", sink.ev[1])
	}
}

func TestWatcherFilterAppliesToSweep(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "example-a.jsonl"), "x")
	write(t, filepath.Join(root, "example-b.tmp"), "x")
	var swept []FileInfo
	w, sink := newTestWatcher(t, Config{
		Roots:   []string{root, filepath.Join(root, "example-missing-ends")},
		Filter:  func(p string) bool { return strings.HasSuffix(p, ".jsonl") },
		OnSweep: func(f []FileInfo) { swept = f },
	})
	if err := w.Rescan(); err != nil {
		t.Fatal(err)
	}
	if len(swept) != 1 || len(sink.ev) != 1 {
		t.Fatalf("swept=%v events=%v", swept, sink.ev)
	}
}

func TestWatcherHintsAndMustScan(t *testing.T) {
	root := t.TempDir()
	cp := &fakeCheckpoint{}
	fb := &fakeBackend{canReplay: true, uuid: "example-uuid"}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	w, sink := newTestWatcher(t, Config{
		Roots: []string{root}, backend: fb, Checkpoint: cp,
		SweepInterval: time.Hour, Now: func() time.Time { return now },
	})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	eventually(t, "initial sweep", func() bool { return !cp.get().LastSweep.IsZero() })
	if fb.replay {
		t.Fatal("first start replayed with no checkpoint")
	}

	seen := filepath.Join(root, "example-seen.jsonl")
	write(t, seen, "x")
	fb.onHint(Hint{Path: seen, EventID: 7})
	eventually(t, "hinted path", func() bool { return len(sink.paths()) == 1 })
	if got := cp.get().EventID; got != 7 {
		t.Fatalf("checkpoint EventID = %d, want 7", got)
	}

	before := len(sink.paths())
	late := filepath.Join(root, "example-late.jsonl")
	write(t, late, "x") // only a sweep can find this
	fb.onHint(Hint{MustScan: true, EventID: 8})
	eventually(t, "MustScanSubDirs sweep", func() bool { return len(sink.paths()) > before })
	found := false
	for _, p := range sink.paths() {
		found = found || p == late
	}
	if !found {
		t.Fatalf("MustScan sweep missed %s: %v", late, sink.paths())
	}
}

func TestWatcherStartReplaysOrSweeps(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	recent := State{EventID: 99, EventTime: now.Add(-time.Minute), VolumeUUID: "example-uuid"}

	t.Run("replays a short gap without sweeping", func(t *testing.T) {
		fb := &fakeBackend{canReplay: true, uuid: "example-uuid"}
		w, _ := newTestWatcher(t, Config{
			Roots: []string{t.TempDir()}, backend: fb, Checkpoint: &fakeCheckpoint{st: recent},
			SweepInterval: time.Hour, Now: func() time.Time { return now },
		})
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = w.Close() }()
		if !fb.replay || fb.since != 99 {
			t.Fatalf("backend started replay=%v since=%d", fb.replay, fb.since)
		}
		if !w.LastSweep().IsZero() {
			t.Fatal("a replay start swept")
		}
	})

	t.Run("sweeps and logs fsevents_gap when the volume changed", func(t *testing.T) {
		var logs bytes.Buffer
		fb := &fakeBackend{canReplay: true, uuid: "example-other"}
		w, _ := newTestWatcher(t, Config{
			Roots: []string{t.TempDir()}, backend: fb, Checkpoint: &fakeCheckpoint{st: recent},
			SweepInterval: time.Hour, Now: func() time.Time { return now },
			Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		})
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = w.Close() }()
		eventually(t, "gap sweep", func() bool { return !w.LastSweep().IsZero() })
		if fb.replay {
			t.Fatal("replayed across a volume change")
		}
		if !strings.Contains(logs.String(), "fsevents_gap") {
			t.Fatalf("no fsevents_gap in log: %s", logs.String())
		}
	})
}

func TestWatcherPeriodicSweep(t *testing.T) {
	root := t.TempDir()
	w, sink := newTestWatcher(t, Config{Roots: []string{root}, backend: &fakeBackend{}, SweepInterval: 20 * time.Millisecond})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	write(t, filepath.Join(root, "example-p.jsonl"), "x")
	eventually(t, "periodic sweep to report the file", func() bool { return len(sink.paths()) > 0 })
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Config{Roots: []string{"x"}}); err == nil {
		t.Fatal("expected error without Sink")
	}
	if _, err := New(Config{Sink: func(Event) {}}); err == nil {
		t.Fatal("expected error without roots")
	}
}

// SweepTimer wraps the whole sweep: the walk, OnSweep and the per-file events
// all happen inside the timed step, and its error is the sweep's.
func TestSweepTimerWrapsTheWalkAndTheCallback(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "example-project", "example-session.jsonl"), "{}\n")
	inStep, timed := false, 0
	var onSweepIn, filterIn bool
	w, _ := newTestWatcher(t, Config{
		Roots: []string{root}, backend: &fakeBackend{}, SweepInterval: time.Hour, DisableEvents: true,
		Filter:  func(string) bool { filterIn = filterIn || inStep; return true },
		OnSweep: func([]FileInfo) { onSweepIn = inStep },
		SweepTimer: func(step func() error) error {
			timed++
			inStep = true
			defer func() { inStep = false }()
			return step()
		},
	})
	if err := w.Rescan(); err != nil {
		t.Fatal(err)
	}
	if timed != 1 || !filterIn || !onSweepIn {
		t.Fatalf("timed=%d walk inside=%v OnSweep inside=%v", timed, filterIn, onSweepIn)
	}
}

// A sweep calls OnSweep and the sinks without the watcher lock, so a hint
// (Wake) during a blocked sweep sink returns promptly, and so does LastSweep.
func TestWakeDuringBlockedSweepSinkReturnsPromptly(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "example-a.jsonl")
	write(t, a, "x")
	inSink := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var hints []string
	var hmu sync.Mutex
	w, err := New(Config{
		Roots: []string{root}, SweepInterval: time.Hour, DisableEvents: true,
		Sink: func(e Event) {
			if e.Sweep {
				once.Do(func() { close(inSink) })
				<-release
				return
			}
			hmu.Lock()
			hints = append(hints, e.Path)
			hmu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	swept := make(chan error, 1)
	go func() { swept <- w.Rescan() }()
	<-inSink
	woke := make(chan struct{})
	go func() { w.Wake(a); _ = w.LastSweep(); close(woke) }()
	select {
	case <-woke:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("Wake blocked behind a sweep sink")
	}
	close(release)
	if err := <-swept; err != nil {
		t.Fatal(err)
	}
	hmu.Lock()
	defer hmu.Unlock()
	if len(hints) != 1 {
		t.Fatalf("hint events = %v", hints)
	}
}

// OnSweep also runs without the watcher lock.
func TestOnSweepRunsWithoutTheWatcherLock(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "example-a.jsonl"), "x")
	var w *Watcher
	ok := make(chan struct{})
	var err error
	w, err = New(Config{
		Roots: []string{root}, SweepInterval: time.Hour, DisableEvents: true,
		Sink: func(Event) {},
		OnSweep: func([]FileInfo) {
			go func() { _ = w.LastSweep(); close(ok) }()
			select {
			case <-ok:
			case <-time.After(5 * time.Second):
				t.Error("LastSweep blocked inside OnSweep")
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Rescan(); err != nil {
		t.Fatal(err)
	}
}

// The periodic sweep runs on the injected ticker, not on SweepInterval.
func TestWatcherSweepsOnInjectedTicker(t *testing.T) {
	root := t.TempDir()
	tick := make(chan time.Time)
	w, sink := newTestWatcher(t, Config{Roots: []string{root}, backend: &fakeBackend{}, SweepInterval: time.Hour, Ticker: tick})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	eventually(t, "the start sweep to finish", func() bool { return !w.LastSweep().IsZero() })
	write(t, filepath.Join(root, "example-p.jsonl"), "x")
	time.Sleep(50 * time.Millisecond)
	if n := len(sink.paths()); n != 0 {
		t.Fatalf("swept %d paths before the ticker fired", n)
	}
	tick <- time.Now()
	eventually(t, "the injected tick to sweep", func() bool { return len(sink.paths()) > 0 })
}
