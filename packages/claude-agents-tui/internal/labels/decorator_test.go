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
