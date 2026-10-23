// Package router is the daemon's request router: it takes one decoded call
// (the frame's instance, the unmodified scriptout Request and the client's
// deadline) and answers one scriptout Response.
//
// # Routing
//
// Handlers register under (instance, op) with Register; the socket server
// hands every call to Dispatch. In order, Dispatch
//
//  1. answers every op unavailable, with a message starting "migrating:", while
//     the gate says the store is migrating (SetMigrating);
//  2. checks the request's attached config block with the configured check and
//     answers invalid_argument when it fails (a block with no hash, or no
//     block, passes: the check decides, see Options.CheckConfig);
//  3. answers invalid_argument for an instance that is neither declared
//     (Options.Instances) nor has anything registered under it;
//  4. answers unknown_op for an op nobody registered under that instance (an
//     op not yet delivered is the same: unknown_op);
//  5. runs the handler with a context that carries the request's config block
//     (scriptout.WithConfig, as scriptout.ServeLoop does, so
//     scriptout.ConfigFromContext and scriptout.ListRangeFromContext work in
//     the handler) and, when the call states one, the call's deadline.
//
// Every error uses the seven-value scriptout taxonomy; the router adds none.
//
// # Deadline
//
// A call's deadline is put on the handler's context so a handler can stop
// early. The router never cancels or abandons a handler that outlives it:
// what the call answers at the deadline is the handler's concern, and
// whatever the handler returns is the answer. A call that states no deadline
// gets no deadline: the daemon never assumes one.
//
// # Annotations and middleware
//
// A handler attaches what it learned about the request (where it was served
// from, whether that was stale, how old) with Annotate, and the router
// measures the request wait itself. Use registers middleware that wraps the
// whole Dispatch and reads AnnotationsFrom after the handler returned: the
// event log and the telemetry read the same carrier instead of each inventing
// one.
package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// MigratingPrefix starts the message of the unavailable answer given while the
// store migrates.
const MigratingPrefix = "migrating:"

// Call is one decoded request.
type Call struct {
	// Instance is the registered instance name (any registered name routes).
	Instance string
	// Request is the unmodified scriptout Request.
	Request scriptout.Request
	// Deadline is the budget the client stated; zero means none.
	Deadline time.Duration
	// Received is when the daemon began serving the connection; the router
	// measures the request wait from it. Zero means "now".
	Received time.Time
	// ClientBuild is the client's build id.
	ClientBuild string
}

// Func is the shape of Dispatch and of what middleware wraps.
type Func func(ctx context.Context, c Call) scriptout.Response

// Middleware wraps a Func. The first Use is the outermost.
type Middleware func(next Func) Func

// Options configure a Router.
type Options struct {
	// CheckConfig inspects the request's attached config block and returns a
	// non-nil error when it must be refused (a different config hash); the
	// router answers invalid_argument with the error's text. nil accepts
	// every block. A block with no hash, or no block at all, MUST pass: the
	// conformance probes send none.
	CheckConfig func(block json.RawMessage) error
	// Instances declares the instance names that exist even while nothing is
	// registered under them (the registered instance names), so an
	// op not yet delivered answers unknown_op rather than an unknown
	// instance. Registering under a name also makes it known.
	Instances []string
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

type routeKey struct{ instance, op string }

// Router routes calls to registered handlers.
type Router struct {
	opts      Options
	mu        sync.RWMutex
	routes    map[routeKey]scriptout.OpHandler
	instances map[string]bool
	mws       []Middleware
	// migrating is nil while the gate is closed, else the message to answer.
	migrating atomic.Pointer[string]
}

// New returns an empty Router.
func New(opts Options) *Router {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	r := &Router{opts: opts, routes: map[routeKey]scriptout.OpHandler{}, instances: map[string]bool{}}
	for _, name := range opts.Instances {
		r.instances[name] = true
	}
	return r
}

// Register installs h for op under instance. It panics on an empty name, a nil
// handler or a second registration of the same (instance, op): a duplicate is a
// startup-time wiring bug, never a runtime condition.
func (r *Router) Register(instance, op string, h scriptout.OpHandler) {
	if instance == "" || op == "" || h.Handle == nil {
		panic(fmt.Sprintf("router: Register(%q, %q): instance, op and handler are required", instance, op))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := routeKey{instance, op}
	if _, dup := r.routes[k]; dup {
		panic(fmt.Sprintf("router: Register(%q, %q): already registered", instance, op))
	}
	r.routes[k] = h
	r.instances[instance] = true
}

// Ops returns the sorted ops registered under instance.
func (r *Router) Ops(instance string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var ops []string
	for k := range r.routes {
		if k.instance == instance {
			ops = append(ops, k.op)
		}
	}
	sort.Strings(ops)
	return ops
}

// Use appends middleware around Dispatch. It is for startup wiring: call it
// before the first Dispatch.
func (r *Router) Use(mw Middleware) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mws = append(r.mws, mw)
}

// SetMigrating opens (true) or closes (false) the migration gate. While open
// every op is answered unavailable with a "migrating:" message; detail, when
// non-empty, is appended to it ("store migration to schema 3 in progress").
func (r *Router) SetMigrating(on bool, detail string) {
	if !on {
		r.migrating.Store(nil)
		return
	}
	msg := MigratingPrefix + " the store is being migrated; retry shortly"
	if detail != "" {
		msg = MigratingPrefix + " " + detail
	}
	r.migrating.Store(&msg)
}

// Migrating reports whether the gate is open.
func (r *Router) Migrating() bool { return r.migrating.Load() != nil }

// Dispatch answers c. It always returns a Response: a failure is an error body
// in the closed taxonomy. The context carries the annotation carrier before any
// middleware runs.
func (r *Router) Dispatch(ctx context.Context, c Call) scriptout.Response {
	if c.Received.IsZero() {
		c.Received = r.opts.Now()
	}
	ctx = withCarrier(ctx)
	r.mu.RLock()
	mws := append([]Middleware(nil), r.mws...)
	r.mu.RUnlock()
	f := Func(r.dispatch)
	for i := len(mws) - 1; i >= 0; i-- {
		f = mws[i](f)
	}
	return f(ctx, c)
}

func (r *Router) dispatch(ctx context.Context, c Call) scriptout.Response {
	if msg := r.migrating.Load(); msg != nil {
		return scriptout.Response{ProtocolVersion: scriptout.ProtocolVersion, Error: &scriptout.Error{Code: "unavailable", Message: *msg}}
	}
	if r.opts.CheckConfig != nil {
		if err := r.opts.CheckConfig(c.Request.Config); err != nil {
			return *scriptout.ErrorResponse(scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error()))
		}
	}
	r.mu.RLock()
	known := r.instances[c.Instance]
	h, ok := r.routes[routeKey{c.Instance, c.Request.Op}]
	r.mu.RUnlock()
	if !known {
		return *scriptout.ErrorResponse(scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("unknown instance %q", c.Instance)))
	}
	if !ok {
		return *scriptout.ErrorResponse(scriptout.WrapError(scriptout.ErrUnknownOp, fmt.Sprintf("unknown op %q", c.Request.Op)))
	}

	hctx := scriptout.WithConfig(ctx, c.Request.Config)
	if c.Deadline > 0 {
		var cancel context.CancelFunc
		hctx, cancel = context.WithTimeout(hctx, c.Deadline)
		defer cancel()
	}
	start := r.opts.Now()
	carrierOf(hctx).setWaitIfUnset(start.Sub(c.Received))

	result, err := h.Handle(hctx, c.Request.Args)
	if err != nil {
		if c.Deadline > 0 && errors.Is(hctx.Err(), context.DeadlineExceeded) {
			err = scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf(
				"deadline exceeded after %s (limit %s) in op=%s args=%s: %v",
				r.opts.Now().Sub(start).Round(time.Millisecond), c.Deadline, c.Request.Op, scriptout.SummarizeArgs(c.Request.Args), err,
			))
		}
		resp := scriptout.ErrorResponse(err)
		resp.SchemaVersion = h.SchemaVersion
		return *resp
	}
	raw, err := json.Marshal(result)
	if err != nil {
		resp := scriptout.ErrorResponse(fmt.Errorf("router: marshal result for op %q: %w", c.Request.Op, err))
		resp.SchemaVersion = h.SchemaVersion
		return *resp
	}
	return scriptout.Response{ProtocolVersion: scriptout.ProtocolVersion, SchemaVersion: h.SchemaVersion, Result: raw}
}
