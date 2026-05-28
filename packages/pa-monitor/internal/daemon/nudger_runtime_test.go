package daemon

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
)

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
	w.UpdateWatermarks("sid-1", now, cause, false)
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
	w.UpdateWatermarks("sid-1", now, nil, false)
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
