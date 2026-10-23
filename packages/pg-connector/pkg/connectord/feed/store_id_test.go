package feed

import (
	"context"
	"testing"
)

// PollResult.StoreID and AckResult.StoreID carry the store's identity on every
// outcome of a poll and an ack (H10b), the first poll of a new cursor and a Reset
// included.
func TestPollAndAckResultsCarryTheStoreIdentity(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	want, err := r.st.Identity(ctx)
	if err != nil || want == "" {
		t.Fatalf("store identity = %q, %v", want, err)
	}
	first, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c", Scope: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	r.plain(refA)
	second, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c", Scope: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	reset, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c", Scope: "mine", Reset: true})
	if err != nil {
		t.Fatal(err)
	}
	acked, err := r.f.Ack(ctx, AckRequest{View: viewAll, Consumer: "c", Scope: "mine", NextSeq: r.head()})
	if err != nil {
		t.Fatal(err)
	}
	noop, err := r.f.Ack(ctx, AckRequest{View: viewAll, Consumer: "c", Scope: "mine", NextSeq: 0})
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{
		"first poll": first.StoreID, "second poll": second.StoreID, "reset": reset.StoreID,
		"ack": acked.StoreID, "backward ack": noop.StoreID,
	} {
		if got != want {
			t.Errorf("%s StoreID = %q, want %q", name, got, want)
		}
	}
}
