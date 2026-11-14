package nudger

import (
	"context"
	"sync"
	"testing"
)

// fakeDeliverer is a Deliverer test double. Beyond confirming the interface
// shape is satisfiable by an external type, it lets dispatcher tests drive
// Deliver's outcomes (nil / a generic error) via err.
type fakeDeliverer struct {
	mu   sync.Mutex
	pid  int
	text string
	err  error
}

func (f *fakeDeliverer) Deliver(_ context.Context, pid int, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pid = pid
	f.text = text
	return f.err
}

func TestDeliverer_InterfaceIsSatisfiable(t *testing.T) {
	var d Deliverer = &fakeDeliverer{}
	if err := d.Deliver(context.Background(), 123, "hi"); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
}
