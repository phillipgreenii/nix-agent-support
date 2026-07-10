package labels

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDecorator_RejectsNonNixStorePath(t *testing.T) {
	_, err := NewDecorator(DecoratorConfig{
		Name:    "evil",
		Command: "/tmp/whatever",
	})
	if err == nil {
		t.Fatal("expected rejection of non-/nix/store path")
	}
}

func TestDecorator_RejectsTraversalThroughNixStorePrefix(t *testing.T) {
	cases := []string{
		"/nix/store/../etc/passwd",
		"/nix/store/abc/../../../etc/passwd",
		"./nix/store/x",
		"nix/store/x",
		"",
	}
	for _, c := range cases {
		_, err := NewDecorator(DecoratorConfig{Name: "evil", Command: c})
		if err == nil {
			t.Errorf("expected rejection of %q", c)
		}
	}
}

func TestDecorator_AcceptsNixStorePath(t *testing.T) {
	_, err := NewDecorator(DecoratorConfig{
		Name:    "ok",
		Command: "/nix/store/abc-defaults/bin/decorator",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecorator_RoundTripJSON(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-decorator")
	script := "#!/bin/sh\necho '{\"labels\":{\"workspace.scope\":\"zr\",\"agent.role\":\"reviewer\"}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newDecoratorRaw("fake", bin, 2000)
	got := d.Detect(Session{ID: "s1"})
	if got["workspace.scope"] != "zr" || got["agent.role"] != "reviewer" {
		t.Errorf("got %+v", got)
	}
}

func TestDecorator_TimeoutReturnsNil(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "slow-decorator")
	script := "#!/bin/sh\nsleep 5\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newDecoratorRaw("slow", bin, 100) // 100ms < 5s
	if got := d.Detect(Session{ID: "s1"}); got != nil {
		t.Errorf("expected nil on timeout, got %+v", got)
	}
}

func TestDecorator_NonZeroExitReturnsNil(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fail-decorator")
	script := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newDecoratorRaw("fail", bin, 1000)
	if got := d.Detect(Session{ID: "s1"}); got != nil {
		t.Errorf("expected nil on non-zero exit, got %+v", got)
	}
}

// TestDecorator_DetectOK_SuccessReportsOK confirms DetectOK returns ok=true on
// a successful run — even when the decorator legitimately produces no labels.
// The label cache must treat a successful-empty result as cacheable (distinct
// from a failure, which must NOT be cached).
func TestDecorator_DetectOK_SuccessReportsOK(t *testing.T) {
	dir := t.TempDir()

	full := filepath.Join(dir, "full")
	if err := os.WriteFile(full, []byte("#!/bin/sh\necho '{\"labels\":{\"workspace.scope\":\"zr\"}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	set, ok := newDecoratorRaw("full", full, 2000).DetectOK(Session{ID: "s1"})
	if !ok {
		t.Fatal("expected ok=true on success")
	}
	if set["workspace.scope"] != "zr" {
		t.Errorf("got %+v", set)
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("#!/bin/sh\necho '{\"labels\":{}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	set, ok = newDecoratorRaw("empty", empty, 2000).DetectOK(Session{ID: "s1"})
	if !ok {
		t.Fatal("expected ok=true on successful-empty result")
	}
	if len(set) != 0 {
		t.Errorf("expected empty labels, got %+v", set)
	}
}

// TestDecorator_DetectOK_FailureReportsNotOK confirms DetectOK returns
// ok=false on a transient failure (non-zero exit / timeout), so callers can
// decline to cache the (wrong) empty result and retry next tick.
func TestDecorator_DetectOK_FailureReportsNotOK(t *testing.T) {
	dir := t.TempDir()

	fail := filepath.Join(dir, "fail")
	if err := os.WriteFile(fail, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if set, ok := newDecoratorRaw("fail", fail, 1000).DetectOK(Session{ID: "s1"}); ok || set != nil {
		t.Errorf("expected (nil,false) on non-zero exit, got (%+v,%v)", set, ok)
	}

	slow := filepath.Join(dir, "slow")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if set, ok := newDecoratorRaw("slow", slow, 100).DetectOK(Session{ID: "s1"}); ok || set != nil {
		t.Errorf("expected (nil,false) on timeout, got (%+v,%v)", set, ok)
	}
}

// TestDecorator_SatisfiesFailableDetector is a compile-time assertion that the
// concrete *Decorator implements the FailableDetector interface the daemon's
// label cache depends on.
func TestDecorator_SatisfiesFailableDetector(t *testing.T) {
	var _ FailableDetector = (*Decorator)(nil)
}
