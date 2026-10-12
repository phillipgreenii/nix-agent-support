package router

import (
	"context"
	"sync"
	"time"
)

// Annotations is what a request's handler (and the router) learned about it,
// for the event log and the telemetry to read.
type Annotations struct {
	// ServedFrom names where the answer came from ("store", "origin", ...);
	// empty when the handler said nothing.
	ServedFrom string
	// Stale reports that the answer was served past its freshness.
	Stale bool
	// AgeSeconds is the age of the data served; zero when not applicable.
	AgeSeconds int64
	// Wait is how long the request waited before its handler ran: the router
	// measures it (from the call's Received to the handler's start) unless the
	// handler set its own with Annotate (for example a wait on the governor).
	Wait time.Duration
}

type carrierKey struct{}

type carrier struct {
	mu      sync.Mutex
	a       Annotations
	waitSet bool
}

func withCarrier(ctx context.Context) context.Context {
	return context.WithValue(ctx, carrierKey{}, &carrier{})
}

// carrierOf returns ctx's carrier; a context that never went through Dispatch
// gets a throwaway one, so Annotate is always safe to call.
func carrierOf(ctx context.Context) *carrier {
	if c, ok := ctx.Value(carrierKey{}).(*carrier); ok {
		return c
	}
	return &carrier{}
}

func (c *carrier) setWaitIfUnset(w time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.waitSet {
		c.a.Wait = w
		c.waitSet = true
	}
}

// Annotate records what a handler learned about the request on ctx's carrier:
// where the answer was served from, whether it was stale, its age in seconds
// and the request wait. The last call wins for every field; a wait of zero
// leaves the router's own measurement in place, a non-zero wait replaces it.
// Calling it on a context that did not come from Dispatch is a no-op.
func Annotate(ctx context.Context, servedFrom string, stale bool, ageSeconds int64, wait time.Duration) {
	c := carrierOf(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.a.ServedFrom = servedFrom
	c.a.Stale = stale
	c.a.AgeSeconds = ageSeconds
	if wait != 0 {
		c.a.Wait = wait
		c.waitSet = true
	}
}

// AnnotationsFrom returns what has been annotated on ctx so far; middleware
// calls it after the wrapped Func returned.
func AnnotationsFrom(ctx context.Context) Annotations {
	c := carrierOf(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.a
}
