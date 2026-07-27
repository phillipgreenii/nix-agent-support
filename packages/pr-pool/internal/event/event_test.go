package event

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/item"
)

func TestFingerprintID_stableAcrossCalls(t *testing.T) {
	a := FingerprintID("feedback.ready", "zr-1")
	b := FingerprintID("feedback.ready", "zr-1")
	if a != b {
		t.Fatalf("fingerprint must be stable: %q != %q", a, b)
	}
	if FingerprintID("work.ready", "zr-1") == a {
		t.Fatal("different event type must yield a different fingerprint")
	}
	if FingerprintID("feedback.ready", "zr-2") == a {
		t.Fatal("different item id must yield a different fingerprint")
	}
}

func TestNewItemEvent_carriesItemAndFingerprint(t *testing.T) {
	it := item.Item{ID: "zr-9", Type: "task", Title: "t"}
	e := NewItemEvent("work.ready", "worker-source", it)
	if e.Type != "work.ready" || e.Item.ID != "zr-9" || e.Source != "worker-source" {
		t.Fatalf("event fields wrong: %+v", e)
	}
	if e.ID != FingerprintID("work.ready", "zr-9") {
		t.Fatalf("id must be the fingerprint, got %q", e.ID)
	}
	if !e.EmittedAt.IsZero() {
		t.Fatal("EmittedAt must be left zero for the bus to stamp")
	}
}

func TestEvent_Expired(t *testing.T) {
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	e := Event{EmittedAt: base}
	if e.Expired(base.Add(29*time.Minute), 30*time.Minute) {
		t.Fatal("must not be expired before ttl elapses")
	}
	if !e.Expired(base.Add(31*time.Minute), 30*time.Minute) {
		t.Fatal("must be expired after ttl elapses")
	}
	// Unstamped event is inside no window yet.
	if (Event{}).Expired(base, 30*time.Minute) {
		t.Fatal("unstamped event must not be considered expired")
	}
}

func TestAllOf_Complete(t *testing.T) {
	c := AllOf{Types: []string{"a", "b"}}
	if c.Complete([]Event{{Type: "a"}}) {
		t.Fatal("missing type b must be incomplete")
	}
	if !c.Complete([]Event{{Type: "a"}, {Type: "b"}}) {
		t.Fatal("both types present must be complete")
	}
	if (AllOf{}).Complete(nil) {
		t.Fatal("empty AllOf must never be complete")
	}
}

func TestCountOf_Complete(t *testing.T) {
	c := CountOf{N: 2}
	if c.Complete([]Event{{Type: "a"}}) {
		t.Fatal("1 < 2 must be incomplete")
	}
	if !c.Complete([]Event{{Type: "a"}, {Type: "b"}}) {
		t.Fatal("2 >= 2 must be complete")
	}
	if (CountOf{N: 0}).Complete([]Event{{Type: "a"}}) {
		t.Fatal("N=0 must never be complete")
	}
}
