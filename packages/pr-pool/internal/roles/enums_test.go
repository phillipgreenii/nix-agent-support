package roles

import "testing"

func TestCompletion_unmarshalText(t *testing.T) {
	var c Completion
	if err := c.UnmarshalText([]byte("close-or-handback")); err != nil || c != CloseOrHandback {
		t.Fatalf("valid completion failed: c=%q err=%v", c, err)
	}
	if err := c.UnmarshalText([]byte("bogus")); err == nil {
		t.Fatal("invalid completion must error at decode")
	}
}

func TestFailureAction_unmarshalText(t *testing.T) {
	var a FailureAction
	if err := a.UnmarshalText([]byte("add-human")); err != nil || a != AddHuman {
		t.Fatalf("valid failure action failed: %v", err)
	}
	if err := a.UnmarshalText([]byte("nuke")); err == nil {
		t.Fatal("invalid failure action must error")
	}
}

func TestDispatchFailAction_unmarshalText(t *testing.T) {
	var d DispatchFailAction
	if err := d.UnmarshalText([]byte("leave")); err != nil || d != DispatchLeave {
		t.Fatalf("valid dispatch-fail failed: %v", err)
	}
	if err := d.UnmarshalText([]byte("x")); err == nil {
		t.Fatal("invalid dispatch-fail must error")
	}
}
