package sync

import "context"

// WriteTally reports what one Sync call wrote, so the pipeline's per-run
// record (bead pg2-dpml1) can say whether the run wrote the anchor bead and
// why, without Sync's signature changing. It is carried in the context
// handed to Sync; a Sync call with no tally in its context records nothing.
type WriteTally struct {
	anchorWrites int
	anchorCause  string
}

// AnchorWritten reports whether Sync applied at least one anchor bead write
// (a create or an update). A plan-mode run never writes, so it is false.
func (t *WriteTally) AnchorWritten() bool { return t != nil && t.anchorWrites > 0 }

// AnchorCause is the cause of the last anchor write (the same vocabulary as
// the anchor-write log's cause field), or "" when none was written.
func (t *WriteTally) AnchorCause() string {
	if t == nil {
		return ""
	}
	return t.anchorCause
}

type writeTallyKey struct{}

// WithWriteTally returns a context carrying a fresh WriteTally and that tally.
func WithWriteTally(ctx context.Context) (context.Context, *WriteTally) {
	t := &WriteTally{}
	return context.WithValue(ctx, writeTallyKey{}, t), t
}

// RecordAnchorWrite notes one applied anchor write on the tally carried by
// ctx, if any. Syncer calls it next to its anchor-write log line; a test
// double standing in for the Syncer MAY call it to simulate a write.
func RecordAnchorWrite(ctx context.Context, cause string) {
	if t, ok := ctx.Value(writeTallyKey{}).(*WriteTally); ok {
		t.anchorWrites++
		t.anchorCause = cause
	}
}
