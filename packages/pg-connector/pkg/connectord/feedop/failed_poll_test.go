package feedop

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// failOnceRenderer fails its first Row call, standing in for a changes op that
// ran out its call budget while building the answer, then renders normally.
type failOnceRenderer struct {
	fakeRenderer
	failed bool
}

func (r *failOnceRenderer) Row(ctx context.Context, row store.ChangeRow) (json.RawMessage, error) {
	if !r.failed {
		r.failed = true
		return nil, context.DeadlineExceeded
	}
	return r.fakeRenderer.Row(ctx, row)
}

// ackedSeq reads the consumer's stored position straight from the store, so a
// test asserts the cursor itself and not only what the next answer happens to say.
func (r *rig) ackedSeq(consumer string) int64 {
	r.t.Helper()
	row, ok, err := r.st.GetConsumer(context.Background(), consumer)
	if err != nil || !ok {
		r.t.Fatalf("consumer %q: row=%+v exists=%v err=%v", consumer, row, ok, err)
	}
	return row.AckedSeq
}

// A changes poll that fails (for example at its deadline under host load) never
// moves the consumer's position: only changes_ack does. The stored position is
// the same after the failed poll, and the next poll, with no acknowledgement in
// between, delivers the same row again, so a timed-out tick loses nothing.
func TestAFailedChangesPollLeavesThePositionWhereItWasAndTheNextPollRedelivers(t *testing.T) {
	r := newRig(t, viewAll, &failOnceRenderer{})
	r.changes(`{"consumer":"c","query":"mine"}`) // first poll places the cursor at the tail
	tail := r.ackedSeq("c")
	seq := r.add(refA, 1, "s1", []string{kindContent})
	if seq <= tail {
		t.Fatalf("appended row seq %d is not past the tail %d", seq, tail)
	}

	if _, err := r.call("changes", `{"consumer":"c","query":"mine"}`); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("poll with a failing render = %v, want the deadline error", err)
	}
	if got := r.ackedSeq("c"); got != tail {
		t.Fatalf("stored position after the failed poll = %d, want it unmoved at %d", got, tail)
	}
	res := r.changes(`{"consumer":"c","query":"mine"}`)
	if len(res.Changes) != 1 || res.Changes[0].Seq != seq {
		t.Fatalf("poll after the failed one = %+v, want the same row (seq %d) redelivered", res.Changes, seq)
	}
	if got := r.ackedSeq("c"); got != tail {
		t.Fatalf("a successful poll moved the stored position to %d; only changes_ack may (want %d)", got, tail)
	}
}

// A poll whose own context is already past its deadline (the daemon's call
// budget ran out before the feed read anything) fails and leaves the stored
// position alone; the next poll under a live context delivers the row.
func TestAChangesPollWhoseDeadlineHasPassedFailsAndLeavesThePositionWhereItWas(t *testing.T) {
	r := newRig(t, viewAll, nil)
	r.changes(`{"consumer":"c","query":"mine"}`)
	tail := r.ackedSeq("c")
	seq := r.add(refA, 1, "s1", []string{kindContent})

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := r.ops["changes"](ctx, json.RawMessage(`{"consumer":"c","query":"mine"}`)); err == nil {
		t.Fatal("poll under an expired context succeeded, want an error")
	}
	if got := r.ackedSeq("c"); got != tail {
		t.Fatalf("stored position after the expired-context poll = %d, want it unmoved at %d", got, tail)
	}
	res := r.changes(`{"consumer":"c","query":"mine"}`)
	if len(res.Changes) != 1 || res.Changes[0].Seq != seq {
		t.Fatalf("poll after the expired one = %+v, want the row (seq %d) delivered", res.Changes, seq)
	}
}
