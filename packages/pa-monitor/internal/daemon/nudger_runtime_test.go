package daemon

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
)

func TestWatermarkStoreUpdateAndRead(t *testing.T) {
	path := t.TempDir() + "/runtime.json"
	w, err := NewWatermarkStore(path)
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
	w, _ := NewWatermarkStore(path)
	w.RecordSent("sid-1", []nudger.Source{nudger.SourceManual}, "", false)
	wm := w.SessionWatermark("sid-1")
	if !wm.LastNudgedAt.IsZero() {
		t.Error("RecordSent should not touch watermarks; UpdateWatermarks does")
	}
}

func TestWatermarkStorePersistsToDisk(t *testing.T) {
	path := t.TempDir() + "/runtime.json"
	w, _ := NewWatermarkStore(path)
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	w.UpdateWatermarks("sid-1", now, nil, false)
	// Reload from disk; watermark must survive.
	w2, err := NewWatermarkStore(path)
	if err != nil {
		t.Fatal(err)
	}
	wm := w2.SessionWatermark("sid-1")
	if !wm.LastNudgedAt.Equal(now) {
		t.Errorf("after reload: LastNudgedAt = %v, want %v", wm.LastNudgedAt, now)
	}
}
