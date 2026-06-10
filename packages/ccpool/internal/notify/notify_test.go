package notify

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNone_isNoOp(t *testing.T) {
	if err := (None{}).Notify(Event{Name: "a", State: "failed"}); err != nil {
		t.Errorf("None.Notify = %v, want nil", err)
	}
}

func TestExec_runsCommandWithEnv(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "fired")
	// a tiny shell command that records the env it received
	ex := Exec{Argv: []string{"sh", "-c", "printf '%s %s' \"$CCPOOL_NAME\" \"$CCPOOL_STATE\" > " + out}}
	if err := ex.Notify(Event{Name: "alpha", UUID: "u", State: "needs_input", CWD: "/x"}); err != nil {
		t.Fatalf("Exec.Notify: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != "alpha needs_input" {
		t.Errorf("got %q, want 'alpha needs_input'", string(b))
	}
}

func TestFromConfig_selectsAdapter(t *testing.T) {
	if _, ok := FromConfig("none", "").(None); !ok {
		t.Error("none → None")
	}
	if _, ok := FromConfig("exec", "sh -c true").(Exec); !ok {
		t.Error("exec → Exec")
	}
	// unknown falls back to None (never error on a misconfig).
	if _, ok := FromConfig("bogus", "").(None); !ok {
		t.Error("unknown adapter should fall back to None")
	}
}

func TestShouldNotify_edgeAndMembership(t *testing.T) {
	on := []string{"needs_input", "failed"}
	if !ShouldNotify(on, "working", "needs_input") {
		t.Error("working→needs_input should notify")
	}
	if ShouldNotify(on, "needs_input", "needs_input") {
		t.Error("needs_input→needs_input (no edge) should NOT notify")
	}
	if ShouldNotify(on, "working", "done") {
		t.Error("done is not in On; should NOT notify")
	}
}
