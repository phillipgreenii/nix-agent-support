package event

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

func TestDispatchIsolatesHandlerFailures(t *testing.T) {
	var calls []string
	d := New()
	d.Register(func(ctx context.Context, e store.Event) error {
		calls = append(calls, "A")
		return errors.New("boom") // must not stop B
	})
	d.Register(func(ctx context.Context, e store.Event) error {
		calls = append(calls, "B")
		return nil
	})
	d.Register(func(ctx context.Context, e store.Event) error {
		panic("kaboom") // must be recovered, must not stop the others
	})
	d.Register(func(ctx context.Context, e store.Event) error {
		calls = append(calls, "D")
		return nil
	})

	if err := d.Dispatch(context.Background(), store.Event{Type: store.EventPROpened}); err != nil {
		t.Fatalf("Dispatch returned unexpected error: %v", err)
	}

	want := []string{"A", "B", "D"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want all non-panicking handlers to run %v", calls, want)
	}
}
