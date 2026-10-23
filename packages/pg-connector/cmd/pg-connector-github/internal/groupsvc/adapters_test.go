package groupsvc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	sharedsvc "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
)

// recordingFetcher is a groups.Fetcher that records what it was given and
// answers each target with an outcome naming it.
type recordingFetcher struct {
	call    ratefeed.Meta
	targets []groups.Target
	short   bool
}

func (f *recordingFetcher) Group() string { return groups.Detail }
func (f *recordingFetcher) MaxBatch() int { return 7 }
func (f *recordingFetcher) Spend(c governor.Class, n int) governor.Request {
	return governor.Request{Bucket: governor.BucketGraphQL, Class: c, Cost: int64(n)}
}

func (f *recordingFetcher) Fetch(_ context.Context, call ratefeed.Meta, targets []groups.Target) []groups.Outcome {
	f.call, f.targets = call, targets
	n := len(targets)
	if f.short {
		n--
	}
	out := make([]groups.Outcome, n)
	for i := range out {
		out[i] = groups.Outcome{
			Content: []byte(targets[i].NodeID), Fingerprint: "fp-" + targets[i].NodeID,
			Stamps:     groups.Stamps{Head: "h-" + targets[i].NodeID, Base: "b-" + targets[i].NodeID},
			FreshUntil: time.Unix(int64(i+1), 0), LastError: "le", Cost: int64(i + 5),
		}
	}
	return out
}

func TestTheFetcherAdapterConvertsTargetsAndOutcomesBothWays(t *testing.T) {
	f := &recordingFetcher{}
	a := fetcherAdapter{f: f}
	if a.Group() != groups.Detail || a.MaxBatch() != 7 {
		t.Errorf("Group/MaxBatch = %q/%d", a.Group(), a.MaxBatch())
	}
	if got := a.Spend(governor.ClassQuery, 3); got.Class != governor.ClassQuery || got.Cost != 3 {
		t.Errorf("Spend = %+v", got)
	}
	call := sharedsvc.CallMeta{Class: governor.ClassFill, Purpose: governor.PurposeFill, Group: groups.Detail, BatchSize: 2}
	out := a.Fetch(context.Background(), call, []sharedsvc.Target{
		{
			ID: "PR_1", Ref: "Acme-Org/Widget#41", Stamp: "head1", Stamp2: "base1", Terminal: true,
			Attrs: map[string]string{groups.AttrHeadRef: "feature/x", groups.AttrState: "MERGED"},
		},
		{ID: "PR_2", Ref: "acme-org/widget#not-a-number"},
		{ID: "PR_3"},
	})
	if f.call != (ratefeed.Meta{Class: governor.ClassFill, Purpose: governor.PurposeFill, Group: groups.Detail, BatchSize: 2}) {
		t.Errorf("call = %+v", f.call)
	}
	if len(f.targets) != 2 {
		t.Fatalf("fetcher saw %d targets, want the two valid ones", len(f.targets))
	}
	want := groups.Target{
		NodeID: "PR_1", Ref: entity.Ref{Owner: "Acme-Org", Repo: "Widget", Number: 41},
		Stamps: groups.Stamps{Head: "head1", Base: "base1"}, HeadRefName: "feature/x", State: "MERGED", Terminal: true,
	}
	if f.targets[0] != want {
		t.Errorf("target = %+v, want %+v", f.targets[0], want)
	}
	if f.targets[1].NodeID != "PR_3" || f.targets[1].Ref != (entity.Ref{}) {
		t.Errorf("an empty reference must become the zero Ref: %+v", f.targets[1])
	}
	if len(out) != 3 {
		t.Fatalf("%d outcomes, want one per target", len(out))
	}
	if o := out[0]; string(o.Content) != "PR_1" || o.Fingerprint != "fp-PR_1" || o.Stamp != "h-PR_1" || o.Stamp2 != "b-PR_1" ||
		!o.FreshUntil.Equal(time.Unix(1, 0)) || o.LastError != "le" || o.Cost != 5 || o.Err != nil {
		t.Errorf("outcome 0 = %+v", o)
	}
	if o := out[2]; string(o.Content) != "PR_3" || o.Cost != 6 {
		t.Errorf("outcome 2 = %+v, want the second fetched target's", o)
	}
	if o := out[1]; o.Content != nil || o.Err == nil || !strings.HasPrefix(o.Err.Error(), "groupsvc: PR_2: ") {
		t.Errorf("an invalid non-empty reference must be that target's Outcome.Err (groupsvc: <node>: <err>): %+v", o)
	}
}

func TestTheFetcherAdapterPassesErrorsThroughAndTruncatesAShortAnswer(t *testing.T) {
	gone := fetcherAdapter{f: fetcherFunc(func(ts []groups.Target) []groups.Outcome {
		out := make([]groups.Outcome, len(ts))
		for i := range out {
			out[i].Err = groups.ErrGone
		}
		return out
	})}
	out := gone.Fetch(context.Background(), sharedsvc.CallMeta{}, []sharedsvc.Target{{ID: "PR_1", Ref: "a/b#1"}})
	if len(out) != 1 || !errors.Is(out[0].Err, sharedsvc.ErrGone) {
		t.Errorf("outcome = %+v, want the shared ErrGone", out)
	}

	short := fetcherAdapter{f: &recordingFetcher{short: true}}
	out = short.Fetch(context.Background(), sharedsvc.CallMeta{}, []sharedsvc.Target{{ID: "PR_1", Ref: "a/b#1"}, {ID: "PR_2", Ref: "a/b#2"}})
	if len(out) != 1 {
		t.Errorf("a fetcher that answered one target of two must yield one outcome, got %d", len(out))
	}
	if none := short.Fetch(context.Background(), sharedsvc.CallMeta{}, []sharedsvc.Target{{ID: "PR_9", Ref: "bad"}}); len(none) != 1 || none[0].Err == nil {
		t.Errorf("only invalid targets: %+v, want one error outcome and no fetch", none)
	}
}

type fetcherFunc func([]groups.Target) []groups.Outcome

func (fetcherFunc) Group() string                              { return groups.Detail }
func (fetcherFunc) MaxBatch() int                              { return 1 }
func (fetcherFunc) Spend(governor.Class, int) governor.Request { return governor.Request{} }
func (f fetcherFunc) Fetch(_ context.Context, _ ratefeed.Meta, ts []groups.Target) []groups.Outcome {
	return f(ts)
}

type stubResolver struct {
	call ratefeed.Meta
	refs []entity.Ref
	res  []entity.Resolution
	err  error
}

func (s *stubResolver) Resolve(_ context.Context, call ratefeed.Meta, refs []entity.Ref) ([]entity.Resolution, int64, error) {
	s.call, s.refs = call, refs
	return s.res, 9, s.err
}

func TestTheRefResolverAdapterConvertsMetaReferencesAndResolutions(t *testing.T) {
	ref := entity.Ref{Owner: "Acme-Org", Repo: "Widget", Number: 41}
	old := entity.Ref{Owner: "acme-org", Repo: "gadget", Number: 41}
	stub := &stubResolver{res: []entity.Resolution{
		{Requested: ref, NodeID: "PR_1", Ref: ref, Found: true, FromStore: true, Renamed: true, OldRef: old},
		{Requested: entity.Ref{Owner: "a", Repo: "b", Number: 2}},
	}}
	got, cost, err := refResolver{r: stub}.Resolve(context.Background(),
		sharedsvc.CallMeta{Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive},
		[]string{"Acme-Org/Widget#41", "a/b#2"})
	if err != nil || cost != 9 {
		t.Fatalf("Resolve = (%v, %d, %v)", got, cost, err)
	}
	if stub.call != (ratefeed.Meta{Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive}) || len(stub.refs) != 2 || stub.refs[0] != ref {
		t.Errorf("resolver was called with %+v %+v", stub.call, stub.refs)
	}
	want := sharedsvc.Resolution{
		Requested: "Acme-Org/Widget#41", NodeID: "PR_1", Ref: "Acme-Org/Widget#41",
		Found: true, FromStore: true, Renamed: true, OldRef: "acme-org/gadget#41",
	}
	if got[0] != want {
		t.Errorf("resolution 0 = %+v, want %+v", got[0], want)
	}
	if got[1].Found || got[1].Ref != "" || got[1].OldRef != "" || got[1].Requested != "a/b#2" {
		t.Errorf("resolution 1 = %+v, want a not-found one with empty references", got[1])
	}

	if _, _, err := (refResolver{r: stub}).Resolve(context.Background(), sharedsvc.CallMeta{}, []string{"nonsense"}); err == nil {
		t.Error("a reference that does not parse must be an error")
	}
	stub.err = errors.New("boom")
	if _, _, err := (refResolver{r: stub}).Resolve(context.Background(), sharedsvc.CallMeta{}, []string{"a/b#2"}); err == nil || err.Error() != "boom" {
		t.Errorf("a resolver error must come through unchanged, got %v", err)
	}
}

func TestParseIDTellsAWireReferenceFromANodeID(t *testing.T) {
	for _, tc := range []struct {
		id    string
		isRef bool
		bad   bool
	}{
		{"PR_kwDOabc123", false, false},
		{"acme-org/widget#41", true, false},
		{"acme-org/widget#0", true, true},
		{"acme-org#41", true, true},
		{"", false, true},
		{"has blank", false, true},
		{"has/slash", false, true},
	} {
		wire, isRef, err := parseID(tc.id)
		if wire != tc.id || isRef != tc.isRef || (err != nil) != tc.bad {
			t.Errorf("parseID(%q) = (%q, %v, %v), want isRef %v bad %v", tc.id, wire, isRef, err, tc.isRef, tc.bad)
		}
	}
}
