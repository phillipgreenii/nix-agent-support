package query

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// confirmRig is a rig with its scheduler running and a confirmer over the real
// summary service and store.
func confirmRig(t *testing.T) (*rig, Confirmer) {
	t.Helper()
	r := newRig(t)
	r.run(t)
	return r, NewConfirmer(r.svc, r.st)
}

func (r *rig) alias(t *testing.T, m mem) {
	t.Helper()
	ref, err := entity.NewRef(m.repo, m.n)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.id.Put(context.Background(), m.id, ref); err != nil {
		t.Fatal(err)
	}
}

func TestNewConfirmer_AnswersTheStoredStateAfterOneInteractiveRead(t *testing.T) {
	for _, state := range []string{"OPEN", "MERGED", "CLOSED"} {
		t.Run(state, func(t *testing.T) {
			r, c := confirmRig(t)
			r.alias(t, memA)
			r.fetch.states = map[string]string{memA.id: state}
			got, err := c.Confirm(context.Background(), memA.id, "Acme-Org/Widget#3")
			if err != nil || got.Gone || got.State != state {
				t.Fatalf("confirmation = %+v err = %v, want state %s", got, err, state)
			}
			if f := r.fetch.fetched(); len(f) != 1 || f[0] != memA.id {
				t.Errorf("origin reads = %v, want exactly one for %s", f, memA.id)
			}
		})
	}
}

func TestNewConfirmer_ReadsEvenAFreshSummary(t *testing.T) {
	r, c := confirmRig(t)
	r.alias(t, memA)
	if _, err := c.Confirm(context.Background(), memA.id, "x#1"); err != nil {
		t.Fatal(err)
	}
	r.fetch.states = map[string]string{memA.id: "MERGED"}
	got, err := c.Confirm(context.Background(), memA.id, "x#1") // still inside fresh_until
	if err != nil || got.State != "MERGED" {
		t.Errorf("confirmation = %+v err = %v, want the second read's state", got, err)
	}
	if n := len(r.fetch.fetched()); n != 2 {
		t.Errorf("origin reads = %d, want one per confirmation", n)
	}
}

func TestNewConfirmer_AnswersGoneForATombstonedEntity(t *testing.T) {
	r, c := confirmRig(t)
	// The fetcher answers a node id it does not know as a gone PR.
	r.alias(t, mem{"PR_gone", "Acme-Org/Widget", 99})
	got, err := c.Confirm(context.Background(), "PR_gone", "Acme-Org/Widget#99")
	if err != nil || !got.Gone {
		t.Fatalf("confirmation = %+v err = %v, want Gone", got, err)
	}
	ent, ok, err := r.st.GetEntity(context.Background(), entity.KindPR, "PR_gone")
	if err != nil || !ok || ent.TombstonedAt.IsZero() {
		t.Errorf("entity = %+v ok=%v err=%v, want it tombstoned by the read", ent, ok, err)
	}
}

func TestNewConfirmer_AnErrorForARefusedRead(t *testing.T) {
	r, c := confirmRig(t)
	r.alias(t, memA)
	r.clk.Advance(2 * time.Minute)
	if err := r.gov.Record(governor.Spend{
		Bucket: governor.BucketGraphQL, Class: governor.ClassInteractive,
		Cost: int64(r.cfg.GraphQLPointsPerHour), Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := c.Confirm(context.Background(), memA.id, "Acme-Org/Widget#3")
	var refusal *governor.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want the governor's refusal visible through the wrap", err)
	}
	if len(r.fetch.fetched()) != 0 {
		t.Error("a refused read must not reach the origin")
	}
}

// fakeReader answers InteractiveFresh with a fixed result.
type fakeReader struct{ res []summary.InteractiveResult }

func (f fakeReader) InteractiveFresh(context.Context, []string) []summary.InteractiveResult {
	return f.res
}

// fakeStoreReader answers the two reads from fixed rows.
type fakeStoreReader struct {
	ent   store.EntityRow
	entOK bool
	grp   store.GroupRow
	grpOK bool
	err   error
}

func (f fakeStoreReader) GetEntity(context.Context, string, string) (store.EntityRow, bool, error) {
	return f.ent, f.entOK, f.err
}

func (f fakeStoreReader) GetGroup(context.Context, string, string, string) (store.GroupRow, bool, error) {
	return f.grp, f.grpOK, f.err
}

func TestNewConfirmer_EveryOtherFailureOfTheReadIsAnError(t *testing.T) {
	good := store.GroupRow{Content: []byte(`{"state":"OPEN"}`)}
	cases := []struct {
		name string
		rd   InteractiveReader
		st   StoreReader
		want string
	}{
		{"a pending read", fakeReader{[]summary.InteractiveResult{{Pending: true}}}, fakeStoreReader{grp: good, grpOK: true}, "deadline"},
		{"a failed read", fakeReader{[]summary.InteractiveResult{{Err: errors.New("origin failed")}}}, fakeStoreReader{grp: good, grpOK: true}, "origin failed"},
		{"no result", fakeReader{}, fakeStoreReader{grp: good, grpOK: true}, "0 results"},
		{"no stored summary", fakeReader{[]summary.InteractiveResult{{Fresh: true}}}, fakeStoreReader{}, "no summary"},
		{"an undecodable summary", fakeReader{[]summary.InteractiveResult{{Fresh: true}}}, fakeStoreReader{grp: store.GroupRow{Content: []byte("{")}, grpOK: true}, "decode"},
		{"a store failure", fakeReader{[]summary.InteractiveResult{{Fresh: true}}}, fakeStoreReader{err: errors.New("disk")}, "disk"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NewConfirmer(c.rd, c.st).Confirm(context.Background(), "PR_a", "Acme-Org/Widget#3")
			if err == nil || !strings.Contains(err.Error(), c.want) || got != (Confirmation{}) {
				t.Errorf("confirmation = %+v err = %v, want an error naming %q", got, err, c.want)
			}
		})
	}
}

func TestNewConfirmer_ATombstoneWinsOverAStoredState(t *testing.T) {
	st := fakeStoreReader{
		ent: store.EntityRow{TombstonedAt: t0}, entOK: true,
		grp: store.GroupRow{Content: []byte(`{"state":"OPEN"}`)}, grpOK: true,
	}
	got, err := NewConfirmer(fakeReader{[]summary.InteractiveResult{{Fresh: true}}}, st).Confirm(context.Background(), "PR_a", "x#1")
	if err != nil || !got.Gone || got.State != "" {
		t.Errorf("confirmation = %+v err = %v, want Gone alone", got, err)
	}
}
