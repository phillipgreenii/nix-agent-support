package labels

import (
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

// TestDecorator_SatisfiesFailableDetector is a compile-time assertion that the
// concrete *Decorator implements the FailableDetector interface the daemon's
// label cache depends on.
func TestDecorator_SatisfiesFailableDetector(t *testing.T) {
	var _ FailableDetector = (*Decorator)(nil)
}
