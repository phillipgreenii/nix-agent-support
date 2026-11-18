package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var (
	p  = params{threshold: 30 * time.Minute, maxGap: 3 * time.Minute, renotify: time.Hour}
	t0 = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
)

// drive runs the pure step over minute ticks.
func drive(s state, start time.Time, n int, samples bool) (state, time.Time, []action) {
	var acts []action
	now := start
	for i := 0; i < n; i++ {
		now = now.Add(time.Minute)
		var a action
		s, a = step(s, now, samples, p)
		acts = append(acts, a)
	}
	return s, now, acts
}

func count(acts []action, a action) int {
	n := 0
	for _, x := range acts {
		if x == a {
			n++
		}
	}
	return n
}

func TestPagesAfterThirtyAwakeMinutesOnce(t *testing.T) {
	s, _ := step(state{}, t0, false, p) // first tick, no baseline
	_, _, acts := drive(s, t0, 45, false)
	if got := count(acts, actPage); got != 1 {
		t.Fatalf("pages = %d, want 1", got)
	}
	if acts[28] != actNone || acts[29] != actPage {
		t.Fatalf("page must land on the 30th minute, got %v", acts[27:31])
	}
}

func TestSleepGapAddsNothing(t *testing.T) {
	s, now, _ := drive(state{LastRun: t0}, t0, 20, false)
	now = now.Add(6 * time.Hour) // laptop asleep
	s, a := step(s, now, false, p)
	if a != actNone || s.Silence != 20*time.Minute {
		t.Fatalf("sleep must add zero: silence=%v act=%v", s.Silence, a)
	}
	_, _, acts := drive(s, now, 9, false) // 29 awake minutes total
	if count(acts, actPage) != 0 {
		t.Fatal("paged although only 29 awake minutes elapsed")
	}
}

func TestSamplesResetAndClear(t *testing.T) {
	s, now, acts := drive(state{LastRun: t0}, t0, 31, false)
	if count(acts, actPage) != 1 {
		t.Fatal("setup: expected a page")
	}
	s, a := step(s, now.Add(time.Minute), true, p)
	if a != actClear || s.Silence != 0 || !s.PagedAt.IsZero() {
		t.Fatalf("recovery: %+v %v", s, a)
	}
}

func TestRenotify(t *testing.T) {
	_, _, acts := drive(state{LastRun: t0}, t0, 30+61, false)
	if got := count(acts, actPage); got != 2 {
		t.Fatalf("pages = %d, want 2 (initial plus renotify)", got)
	}
}

func TestBackwardsClockAddsNothing(t *testing.T) {
	s, _ := step(state{LastRun: t0, Silence: time.Minute}, t0.Add(-time.Hour), false, p)
	if s.Silence != time.Minute {
		t.Fatalf("silence = %v", s.Silence)
	}
}

func TestRunOncePagesAndRecovers(t *testing.T) {
	dir := t.TempDir()
	now := t0
	samples := false
	var msgs []string
	d := deps{
		now:      func() time.Time { return now },
		probe:    func() bool { return samples },
		notify:   func(m string) error { msgs = append(msgs, m); return nil },
		stateDir: dir,
	}
	for i := 0; i < 35; i++ {
		now = now.Add(time.Minute)
		if err := runOnce(d, p); err != nil {
			t.Fatal(err)
		}
	}
	if len(msgs) != 1 {
		t.Fatalf("notifications = %d, want 1", len(msgs))
	}
	if _, err := os.Stat(filepath.Join(dir, "alert")); err != nil {
		t.Fatal("alert file missing")
	}
	samples = true
	now = now.Add(time.Minute)
	if err := runOnce(d, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "alert")); err == nil {
		t.Fatal("alert file not cleared")
	}
}

func TestRunOnceQuietAcrossSleep(t *testing.T) {
	dir := t.TempDir()
	now := t0
	paged := 0
	d := deps{
		now: func() time.Time { return now }, probe: func() bool { return false },
		notify: func(string) error { paged++; return nil }, stateDir: dir,
	}
	tick := func(n int, step time.Duration) {
		for i := 0; i < n; i++ {
			now = now.Add(step)
			if err := runOnce(d, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	tick(20, time.Minute)
	tick(1, 8*time.Hour) // sleep
	tick(9, time.Minute)
	if paged != 0 {
		t.Fatalf("paged %d times across sleep", paged)
	}
	tick(2, time.Minute)
	if paged != 1 {
		t.Fatalf("paged = %d after 31 awake minutes, want 1", paged)
	}
}

func TestProbe(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"present", 200, `{"data":{"result":[{"value":[1,"3"]}]}}`, true},
		{"empty", 200, `{"data":{"result":[]}}`, false},
		{"zero", 200, `{"data":{"result":[{"value":[1,"0"]}]}}`, false},
		{"500", 500, `{}`, false},
		{"garbage", 200, `nope`, false},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body))
		}))
		if got := promHasSamples(srv.URL, time.Second); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
		srv.Close()
	}
	if promHasSamples("http://127.0.0.1:1", time.Second) {
		t.Error("refused connection must read as no samples")
	}
}
