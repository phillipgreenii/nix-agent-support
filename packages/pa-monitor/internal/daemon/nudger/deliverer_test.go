package nudger

import (
	"context"
	"errors"
	"testing"
)

// fakeDeliverer is a minimal Deliverer used only to confirm the interface
// shape compiles and is satisfiable by an external type.
type fakeDeliverer struct {
	pid  int
	text string
}

func (f *fakeDeliverer) Deliver(_ context.Context, pid int, text string) error {
	f.pid = pid
	f.text = text
	return nil
}

func TestDeliverer_InterfaceIsSatisfiable(t *testing.T) {
	var d Deliverer = &fakeDeliverer{}
	if err := d.Deliver(context.Background(), 123, "hi"); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
}

func TestErrNoBridge_IsANonNilSentinel(t *testing.T) {
	if ErrNoBridge == nil {
		t.Fatal("ErrNoBridge must be a non-nil sentinel error")
	}
	if !errors.Is(ErrNoBridge, ErrNoBridge) {
		t.Fatal("ErrNoBridge must satisfy errors.Is against itself")
	}
}
