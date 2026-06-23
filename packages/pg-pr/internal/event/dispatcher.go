// Package event is pg-pr's in-process event dispatcher. Handlers are called in
// registration order; a failure or panic in one is isolated so the rest run.
package event

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

// Handler reacts to one event. Returning an error is logged; it never blocks
// sibling handlers.
type Handler func(ctx context.Context, e store.Event) error

// Dispatcher fans an event out to registered handlers.
type Dispatcher struct {
	handlers []Handler
	log      *slog.Logger
}

// New returns an empty dispatcher logging to slog.Default.
func New() *Dispatcher { return &Dispatcher{log: slog.Default()} }

// WithLogger sets the logger (chainable).
func (d *Dispatcher) WithLogger(l *slog.Logger) *Dispatcher { d.log = l; return d }

// Register appends a handler.
func (d *Dispatcher) Register(h Handler) { d.handlers = append(d.handlers, h) }

// Dispatch calls every handler, recovering panics and logging errors. Its
// signature matches store.DispatchFunc so it can be passed to DB.RunOutbox.
func (d *Dispatcher) Dispatch(ctx context.Context, e store.Event) error {
	for i, h := range d.handlers {
		d.callOne(ctx, i, h, e)
	}
	return nil
}

func (d *Dispatcher) callOne(ctx context.Context, idx int, h Handler, e store.Event) {
	defer func() {
		if r := recover(); r != nil {
			d.log.Error("event handler panicked", "handler", idx, "type", e.Type, "panic", fmt.Sprint(r))
		}
	}()
	if err := h(ctx, e); err != nil {
		d.log.Warn("event handler error", "handler", idx, "type", e.Type, "err", err.Error())
	}
}
