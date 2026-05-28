package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeState_AtomicRoundTrip(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "runtime.json")
	s := RuntimeState{CaffeinateOn: true}
	if err := WriteRuntimeState(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRuntimeState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CaffeinateOn {
		t.Error("CaffeinateOn lost")
	}
}

func TestRuntimeState_MissingFileReturnsZero(t *testing.T) {
	got, err := ReadRuntimeState("/no/such/file")
	if err != nil {
		t.Fatalf("expected (zero, nil), got err %v", err)
	}
	if got.CaffeinateOn {
		t.Error("zero state should have CaffeinateOn=false")
	}
}

func TestRuntimeState_BadJSONReturnsZeroAndError(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "runtime.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRuntimeState(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if got.CaffeinateOn {
		t.Error("zero state expected on parse failure")
	}
}

func TestRuntimeStateNudgerRoundTrip(t *testing.T) {
	in := RuntimeState{
		CaffeinateOn:      true,
		AutoResumeEnabled: true,
		Nudger: NudgerState{
			PendingIntents: []PersistedIntent{
				{
					SessionID: "sid-1", Source: "manual", Text: "continue",
					EmittedAt: time.Date(2026, 5, 28, 14, 0, 0, 0, time.UTC),
				},
			},
			Sessions: map[string]NudgerSessionWatermarks{
				"sid-1": {
					LastNudgedAt:        time.Date(2026, 5, 28, 14, 1, 0, 0, time.UTC),
					LastDisruptNudgeAt:  time.Date(2026, 5, 28, 14, 1, 0, 0, time.UTC),
					LastDisruptNudgeFor: time.Date(2026, 5, 28, 14, 0, 50, 0, time.UTC),
					DisruptEscalated:    false,
				},
			},
			WindowResetFiredFor: time.Date(2026, 5, 28, 14, 0, 0, 0, time.UTC),
		},
	}
	path := t.TempDir() + "/runtime.json"
	if err := WriteRuntimeState(path, in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := ReadRuntimeState(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.AutoResumeEnabled != true {
		t.Error("AutoResumeEnabled mismatch")
	}
	if len(got.Nudger.PendingIntents) != 1 || got.Nudger.PendingIntents[0].SessionID != "sid-1" {
		t.Errorf("Nudger.PendingIntents = %+v", got.Nudger.PendingIntents)
	}
	if !got.Nudger.WindowResetFiredFor.Equal(in.Nudger.WindowResetFiredFor) {
		t.Errorf("WindowResetFiredFor mismatch: got %v want %v",
			got.Nudger.WindowResetFiredFor, in.Nudger.WindowResetFiredFor)
	}
}

func TestRuntimeStateOldFormatBackwardCompat(t *testing.T) {
	path := t.TempDir() + "/runtime.json"
	if err := os.WriteFile(path, []byte(`{"caffeinate_on":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRuntimeState(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.CaffeinateOn {
		t.Error("CaffeinateOn = false, want true (legacy file)")
	}
	if got.Nudger.PendingIntents != nil {
		t.Errorf("Nudger.PendingIntents = %v, want nil for legacy file", got.Nudger.PendingIntents)
	}
}
