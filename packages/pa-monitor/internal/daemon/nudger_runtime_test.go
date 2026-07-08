package daemon

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
)

func TestClassifySendFailure(t *testing.T) {
	cases := []struct{ err, want string }{
		{"no cmux surface found for pid 123", "no_surface"},
		{"cmux send-key failed: exit status 1", "send_key"},
		{"cmux --json top enumerate failed", "enumerate"},
		{"context deadline exceeded", "timeout"},
		// A cmux subprocess that exceeds its context deadline is SIGKILLed by
		// exec.CommandContext, which surfaces as "signal: killed" — the dominant
		// real-world timeout signature (see cache/signal-errors.log). It is the
		// same root cause as "context deadline exceeded" and must classify as a
		// timeout, not fall through to "other" (pg2-il6j). A plain `cmux send`
		// timeout carries no path keyword, so it is the case that regressed.
		{"cmux send: signal: killed", "timeout"},
		// A send-key timeout keeps its more-specific path label; the send-key
		// keyword is matched before the timeout signature, and that ordering is
		// intentional (the path is more actionable than the generic timeout).
		{"cmux send-key: signal: killed", "send_key"},
		// An enumerate timeout likewise keeps its path label.
		{"cmux enumerate: cmux --json top --processes: signal: killed", "enumerate"},
		{"dial unix: connection refused", "connection"},
		{"", "unknown"},
		{"something totally unexpected", "other"},
	}
	for _, tc := range cases {
		if got := classifySendFailure(tc.err); got != tc.want {
			t.Errorf("classifySendFailure(%q) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// TestSendFailureCounterAttrs_Bounded guards the metric-cardinality fix: the
// send_failures_total counter must carry only bounded labels — never the raw
// per-session id or the full error string, which would explode series count.
func TestSendFailureCounterAttrs_Bounded(t *testing.T) {
	attrs := sendFailureCounterAttrs(string(transcript.ErrServerError), "no cmux surface found for pid 99999")
	if _, ok := attrs["session_id"]; ok {
		t.Error("counter attrs must not include session_id (unbounded)")
	}
	if _, ok := attrs["error"]; ok {
		t.Error("counter attrs must not include the full error text (unbounded)")
	}
	if attrs["reason"] != "no_surface" {
		t.Errorf("counter attrs reason = %q, want no_surface", attrs["reason"])
	}
	if attrs["error_kind"] != string(transcript.ErrServerError) {
		t.Errorf("counter attrs error_kind = %q, want %q", attrs["error_kind"], transcript.ErrServerError)
	}
}

func TestJoinSources_StableSort(t *testing.T) {
	cases := []struct {
		in   []nudger.Source
		want string
	}{
		{[]nudger.Source{nudger.SourceManual}, "manual"},
		{[]nudger.Source{nudger.SourceDisrupted, nudger.SourceManual}, "disrupted,manual"},
		{[]nudger.Source{nudger.SourceManual, nudger.SourceDisrupted}, "disrupted,manual"},
		{[]nudger.Source{nudger.SourceWindowReset, nudger.SourceDisrupted, nudger.SourceManual}, "disrupted,manual,window_reset"},
		{nil, ""},
	}
	for _, tc := range cases {
		got := joinSources(tc.in)
		if got != tc.want {
			t.Errorf("joinSources(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWatermarkStore_RecordSuppressed_NilEmitter(t *testing.T) {
	// With nil emitter, RecordSuppressed must not panic.
	path := t.TempDir() + "/runtime.json"
	w, _ := NewWatermarkStore(path, nil)
	w.RecordSuppressed("sid-1", []nudger.Source{nudger.SourceDisrupted}, "session_active")
}

func TestWatermarkStore_RecordSent_NilEmitter(t *testing.T) {
	path := t.TempDir() + "/runtime.json"
	w, _ := NewWatermarkStore(path, nil)
	w.RecordSent("sid-1", []nudger.Source{nudger.SourceManual}, "server_error", true)
	// No watermark side-effect expected.
	wm := w.SessionWatermark("sid-1")
	if !wm.LastNudgedAt.IsZero() {
		t.Error("RecordSent should not touch watermarks")
	}
}

func TestWatermarkStoreUpdateAndRead(t *testing.T) {
	path := t.TempDir() + "/runtime.json"
	w, err := NewWatermarkStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	cause := &transcript.ErrorRecord{Kind: transcript.ErrUnknown, At: now.Add(-1 * time.Minute)}
	w.UpdateWatermarks("sid-1", now, []nudger.Source{nudger.SourceDisrupted}, cause, false)
	w.SetWindowResetFiredFor(now.Add(-5 * time.Minute))

	wm := w.SessionWatermark("sid-1")
	if !wm.LastNudgedAt.Equal(now) {
		t.Errorf("LastNudgedAt = %v, want %v", wm.LastNudgedAt, now)
	}
	if !wm.LastDisruptNudgeFor.Equal(cause.At) {
		t.Errorf("LastDisruptNudgeFor = %v, want %v", wm.LastDisruptNudgeFor, cause.At)
	}
	if !w.WindowResetFiredFor().Equal(now.Add(-5 * time.Minute)) {
		t.Errorf("WindowResetFiredFor = %v, want %v",
			w.WindowResetFiredFor(), now.Add(-5*time.Minute))
	}
}

func TestWatermarkStoreRecordersAreNoOpOnPersistence(t *testing.T) {
	// RecordSuppressed/RecordSent shouldn't modify watermarks.
	path := t.TempDir() + "/runtime.json"
	w, _ := NewWatermarkStore(path, nil)
	w.RecordSent("sid-1", []nudger.Source{nudger.SourceManual}, "", false)
	wm := w.SessionWatermark("sid-1")
	if !wm.LastNudgedAt.IsZero() {
		t.Error("RecordSent should not touch watermarks; UpdateWatermarks does")
	}
}

func TestWatermarkStorePersistsToDisk(t *testing.T) {
	path := t.TempDir() + "/runtime.json"
	w, _ := NewWatermarkStore(path, nil)
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	w.UpdateWatermarks("sid-1", now, nil, nil, false)
	// Reload from disk; watermark must survive.
	w2, err := NewWatermarkStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	wm := w2.SessionWatermark("sid-1")
	if !wm.LastNudgedAt.Equal(now) {
		t.Errorf("after reload: LastNudgedAt = %v, want %v", wm.LastNudgedAt, now)
	}
}

// TestWatermarkStoreLimitPauseFiredForRoundTrip verifies the limit-pause
// once-per-window latch (bead pg2-2z7k) survives a daemon restart: without
// persistence a restart mid-window would re-nudge (the loaded zero latch is
// After-beaten by any real reset), reintroducing the restart-burst the latch
// exists to prevent. Exercises the Recorder path (AdvanceLimitPauseFiredFor).
func TestWatermarkStoreLimitPauseFiredForRoundTrip(t *testing.T) {
	path := t.TempDir() + "/runtime.json"
	w, _ := NewWatermarkStore(path, nil)
	reset := time.Date(2026, 7, 8, 17, 0, 0, 0, time.UTC)
	w.AdvanceLimitPauseFiredFor(reset)
	if !w.LimitPauseFiredFor().Equal(reset) {
		t.Fatalf("LimitPauseFiredFor = %v, want %v", w.LimitPauseFiredFor(), reset)
	}
	// Reload from disk; the latch must survive so a restart does not re-nudge.
	w2, err := NewWatermarkStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !w2.LimitPauseFiredFor().Equal(reset) {
		t.Errorf("after reload: LimitPauseFiredFor = %v, want %v", w2.LimitPauseFiredFor(), reset)
	}
}

// TestWatermarkStoreLastNudgeSourcesRoundTrip verifies that the sources passed
// to UpdateWatermarks survive a disk round-trip in sorted order so the details
// panel renders a stable "via: [...]" line across daemon restarts.
func TestWatermarkStoreLastNudgeSourcesRoundTrip(t *testing.T) {
	path := t.TempDir() + "/runtime.json"
	w, _ := NewWatermarkStore(path, nil)
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	// Order intentionally reversed so we exercise the sort.
	in := []nudger.Source{nudger.SourceManual, nudger.SourceDisrupted}
	w.UpdateWatermarks("sid-1", now, in, nil, false)

	wm := w.SessionWatermark("sid-1")
	if got, want := wm.LastNudgeSources, []string{"disrupted", "manual"}; !equalStrSlice(got, want) {
		t.Errorf("LastNudgeSources = %v, want %v", got, want)
	}
	// Reload; should still be sorted.
	w2, _ := NewWatermarkStore(path, nil)
	wm2 := w2.SessionWatermark("sid-1")
	if got, want := wm2.LastNudgeSources, []string{"disrupted", "manual"}; !equalStrSlice(got, want) {
		t.Errorf("after reload: LastNudgeSources = %v, want %v", got, want)
	}
}

func equalStrSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
