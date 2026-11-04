package obs

import (
	"context"
	"sync/atomic"
	"time"
)

// ReqTrace follows one socket request. The listener creates it and puts it in
// the request context; the read pool adds the time its statements and
// transactions spent running in SQLite, so a timed-out request can say how much
// of its life went to waiting (for a read slot or for the handler's own work)
// and how much to executing.
type ReqTrace struct {
	ID     string
	execNS atomic.Int64
}

type traceKey struct{}

// WithTrace returns ctx carrying t.
func WithTrace(ctx context.Context, t *ReqTrace) context.Context {
	return context.WithValue(ctx, traceKey{}, t)
}

// TraceFrom returns the request trace in ctx, nil when there is none.
func TraceFrom(ctx context.Context) *ReqTrace {
	t, _ := ctx.Value(traceKey{}).(*ReqTrace)
	return t
}

// AddExec adds d of database execution time; it is safe for concurrent use and
// for a nil receiver.
func (t *ReqTrace) AddExec(d time.Duration) {
	if t != nil && d > 0 {
		t.execNS.Add(int64(d))
	}
}

// Exec is the database execution time added so far.
func (t *ReqTrace) Exec() time.Duration {
	if t == nil {
		return 0
	}
	return time.Duration(t.execNS.Load())
}
