package notice

import (
	"errors"
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestHandlersAreExactlyTheTwoNotices(t *testing.T) {
	r := newRig(t)
	hs := Handlers(r.deps)
	got := make([]string, 0, len(hs))
	for op, h := range hs {
		got = append(got, op)
		if h.SchemaVersion != schema.IssueSchemaVersion || h.Handle == nil {
			t.Errorf("%s: schema version %d, handle nil %v", op, h.SchemaVersion, h.Handle == nil)
		}
	}
	slices.Sort(got)
	want := []string{"invalidate", "write_begin"}
	if !slices.Equal(got, want) {
		t.Fatalf("handlers = %v, want %v", got, want)
	}
	ops := slices.Clone(Ops)
	slices.Sort(ops)
	if !slices.Equal(ops, want) {
		t.Fatalf("Ops = %v, want %v", ops, want)
	}
}

func TestMalformedArgumentsAreInvalidArgument(t *testing.T) {
	r := newRig(t)
	cases := []struct{ op, args string }{
		{"write_begin", `{"id":"not a key"}`},
		{"write_begin", `{"id":"A-"}`},
		{"write_begin", `{}`},
		{"write_begin", `null`},
		{"write_begin", `{"id":"A-1","instance":"x"}`},
		{"write_begin", `{"id":7}`},
		{"invalidate", `{"id":"nope"}`},
		{"invalidate", `{}`},
		{"invalidate", `{"id":7}`},
	}
	for _, c := range cases {
		res, err := r.call(c.op, c.args)
		if !errors.Is(err, scriptout.ErrInvalidArgument) || res != nil {
			t.Errorf("%s %s = %v, %v; want invalid_argument", c.op, c.args, res, err)
		}
	}
	if r.table.Held("not a key") || len(r.timers.timers) != 0 {
		t.Fatal("a rejected notice took a hold")
	}
	if r.origin.count() != 0 {
		t.Fatal("a rejected notice called the origin")
	}
}

func TestHandlersWithoutWiringFailLoudlyNotWithAPanic(t *testing.T) {
	h := Handlers(Deps{})
	for op, args := range map[string]string{"write_begin": `{"id":"A-1"}`, "invalidate": `{"id":"A-1"}`} {
		if _, err := h[op].Handle(t.Context(), []byte(args)); err == nil {
			t.Errorf("%s answered without wiring", op)
		}
	}
}

func TestWriteBeginBumpsBothGroupsAndHoldsForTheLease(t *testing.T) {
	r := newRig(t)
	r.seed(keyA, oldText)
	sum0, det0 := r.generation(keyA, groups.Summary), r.generation(keyA, groups.Detail)

	r.mustCall("write_begin", `{"id":"a-1"}`) // any case

	if g := r.generation(keyA, groups.Summary); g != sum0+1 {
		t.Errorf("summary generation = %d, want %d", g, sum0+1)
	}
	if g := r.generation(keyA, groups.Detail); g != det0+1 {
		t.Errorf("detail generation = %d, want %d", g, det0+1)
	}
	if !r.table.Held(keyA) {
		t.Fatal("the issue is not held")
	}
	if d := r.timers.last().d; d != lease {
		t.Errorf("hold timer = %v, want the lease %v", d, lease)
	}
	if r.origin.count() != 0 {
		t.Error("write_begin called the origin")
	}
	if _, ok := r.queued(keyA, groups.Summary); ok {
		t.Error("write_begin queued a re-pull")
	}
}

func TestWriteBeginOnAnInstanceHoldsTheNameAndBumpsNothing(t *testing.T) {
	r := newRig(t)
	r.seed(keyA, oldText)
	sum0, det0 := r.generation(keyA, groups.Summary), r.generation(keyA, groups.Detail)

	r.mustCall("write_begin", `{"instance":"work-1"}`)

	if !r.table.Held(instanceKey("work-1")) {
		t.Fatal("the instance name is not held")
	}
	if r.table.Held(keyA) {
		t.Error("an instance hold held an issue")
	}
	if r.generation(keyA, groups.Summary) != sum0 || r.generation(keyA, groups.Detail) != det0 {
		t.Error("an instance hold bumped a generation")
	}
	if r.origin.count() != 0 {
		t.Error("write_begin called the origin")
	}
	// The instance hold is released by an invalidate naming it, and expires
	// without touching a group.
	r.mustCall("invalidate", `{"instance":"work-1"}`)
	if r.table.Held(instanceKey("work-1")) {
		t.Error("invalidate did not release the instance hold")
	}
	r.mustCall("write_begin", `{"instance":"work-2"}`)
	r.timers.fire(r.timers.last())
	if r.table.Held(instanceKey("work-2")) {
		t.Error("the instance hold outlived its lease")
	}
	if r.generation(keyA, groups.Summary) != sum0 || len(r.tableErrs()) != 0 {
		t.Errorf("an instance expiry bumped a generation or failed: %v", r.tableErrs())
	}
}

func TestWriteBeginOnAnUnknownIssueAnswersNullWithoutAnOriginCall(t *testing.T) {
	r := newRig(t)
	r.mustCall("write_begin", `{"id":"`+keyNew+`"}`)
	if !r.table.Held(keyNew) {
		t.Error("the unknown issue is not held")
	}
	if r.origin.count() != 0 {
		t.Error("write_begin called the origin")
	}
	if _, ok := r.group(keyNew, groups.Summary); ok {
		t.Error("write_begin created a summary row for an issue never fetched")
	}
}

func TestInvalidateReleasesBumpsAgainAndQueuesOneRePullOfEachGroup(t *testing.T) {
	r := newRig(t)
	r.seed(keyA, oldText)
	r.mustCall("write_begin", `{"id":"`+keyA+`"}`)
	sum1, det1 := r.generation(keyA, groups.Summary), r.generation(keyA, groups.Detail)
	hold := r.timers.last()

	r.mustCall("invalidate", `{"id":"`+keyA+`"}`)

	if r.table.Held(keyA) {
		t.Error("invalidate left the hold in force")
	}
	if !hold.stopped {
		t.Error("invalidate left the expiry timer running")
	}
	if g := r.generation(keyA, groups.Summary); g != sum1+1 {
		t.Errorf("summary generation = %d, want %d", g, sum1+1)
	}
	if g := r.generation(keyA, groups.Detail); g != det1+1 {
		t.Errorf("detail generation = %d, want %d", g, det1+1)
	}
	for _, g := range []string{groups.Summary, groups.Detail} {
		if c, ok := r.queued(keyA, g); !ok || c != governor.ClassInvalidated {
			t.Errorf("%s re-pull queued=%v class=%v, want queued at class %v", g, ok, c, governor.ClassInvalidated)
		}
	}
	if r.queryRuns() != 0 {
		t.Error("invalidate of a known issue re-ran the queries")
	}
	if r.origin.count() != 0 {
		t.Error("invalidate called the origin")
	}
	if st := r.sched.State(); st.Classes[int(governor.ClassInvalidated)-1].Depth != 2 {
		t.Errorf("class 2 depth = %d, want exactly 2 (one per group)", st.Classes[int(governor.ClassInvalidated)-1].Depth)
	}
}

func TestInvalidateOfAnUnknownIssueQueuesItsTrackingFetchAndRerunsTheQueries(t *testing.T) {
	r := newRig(t)
	r.mustCall("write_begin", `{"instance":"x"}`)
	r.mustCall("invalidate", `{"id":"`+keyNew+`"}`)

	if c, ok := r.queued(keyNew, groups.Summary); !ok || c != governor.ClassInvalidated {
		t.Errorf("summary fetch queued=%v class=%v", ok, c)
	}
	if _, ok := r.queued(keyNew, groups.Detail); ok {
		t.Error("an unknown issue got a detail re-pull")
	}
	if n := r.queryRuns(); n != 1 {
		t.Errorf("queries re-run %d times, want once", n)
	}
	if r.origin.count() != 0 {
		t.Error("invalidate called the origin")
	}
}

func TestInvalidateWithoutAHoldStillBumpsAndQueues(t *testing.T) {
	r := newRig(t)
	r.seed(keyA, oldText)
	g0 := r.generation(keyA, groups.Summary)
	r.mustCall("invalidate", `{"id":"`+keyA+`"}`)
	if g := r.generation(keyA, groups.Summary); g != g0+1 {
		t.Errorf("summary generation = %d, want %d", g, g0+1)
	}
	if _, ok := r.queued(keyA, groups.Summary); !ok {
		t.Error("no re-pull queued")
	}
}
