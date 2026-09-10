// config_context.go: threads a Request's own Config member (see
// envelope.go) onto the context.Context an op handler receives, rather
// than widening OpHandler.Handle's own signature (bead pg2-2j5ac.28.1,
// bead pg2-2j5ac.28.1) [freedom boundary: "exact Go method signatures ... are the
// implementer's choice"]. Widening Handle itself would touch every
// capability's dispatch table — including ci and scm, which this packet
// never gives a Config-consuming op — for a value only pr/issue's own
// "list" op actually reads. Context is this package's existing vehicle
// for a value every handler transitively receives without changing its
// own signature (serveLoop already threads a DefaultExecTimeout deadline
// this same way).
package scriptout

import (
	"context"
	"encoding/json"
)

// configContextKey is an unexported type so no other package can collide
// with, or directly set, this context key.
type configContextKey struct{}

// WithConfig returns a context carrying config — the request's own
// opaque, per-backend config block (nil when the request carried none).
// serveLoop calls this once per request, before invoking the matched
// dispatch-table entry's Handle.
func WithConfig(ctx context.Context, config json.RawMessage) context.Context {
	return context.WithValue(ctx, configContextKey{}, config)
}

// ConfigFromContext returns the config threaded onto ctx by WithConfig, or
// nil if none was ever set — the ordinary case for every op handler this
// packet does not touch (ci, scm, auth_status, capabilities, and every
// pr/issue op other than list), and for any pre-existing test that builds
// its own ctx via context.Background() without ever calling WithConfig.
func ConfigFromContext(ctx context.Context) json.RawMessage {
	v, _ := ctx.Value(configContextKey{}).(json.RawMessage)
	return v
}
