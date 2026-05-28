package nudger

import (
	"testing"
	"time"
)

func TestIntentKeyEquality(t *testing.T) {
	a := IntentKey{SessionID: "sid", Source: SourceManual}
	b := IntentKey{SessionID: "sid", Source: SourceManual}
	c := IntentKey{SessionID: "sid", Source: SourceDisrupted}
	if a != b {
		t.Error("IntentKey same fields should be equal")
	}
	if a == c {
		t.Error("IntentKey different source should not be equal")
	}
}

func TestSourceConstants(t *testing.T) {
	for _, s := range []Source{SourceWindowReset, SourceDisrupted, SourceManual} {
		if s == "" {
			t.Errorf("Source constant empty")
		}
	}
}

func TestNudgeIntentZeroValueIsUsable(t *testing.T) {
	var n NudgeIntent
	if !n.EmittedAt.IsZero() {
		t.Error("zero NudgeIntent EmittedAt should be zero time")
	}
	n.EmittedAt = time.Now()
	_ = n
}
