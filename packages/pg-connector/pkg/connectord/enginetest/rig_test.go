package enginetest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/enginetest/vocab"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

const (
	idOne  = "id-1"
	idTwo  = "id-2"
	refOne = "Example-Org/Thing#1"
	refTwo = "Example-Org/Thing#2"
	// queryMine is the one configured query of the scenarios.
	queryMine = "mine"
	consumer  = "consumer-1"
)

func TestEveryOptionLeavesAValidSpec(t *testing.T) {
	cases := map[string][]Option{
		"the default":               nil,
		"entity kind beta":          {WithBeta()},
		"no membership rows":        {WithStoreRows(false)},
		"watched-only emission":     {WithEmission(registry.EmitWatchedOnly)},
		"delivery by watched":       {WithDelivery(registry.DeliverByWatched)},
		"ad-hoc watching":           {WithAdHocQueryWatch(true)},
		"all of them":               {WithBeta(), WithStoreRows(false), WithEmission(registry.EmitWatchedOnly), WithDelivery(registry.DeliverByWatched), WithAdHocQueryWatch(true)},
		"a spec function runs last": {WithStoreRows(false), WithSpec(func(s *registry.Spec) { s.Views = s.Views[:1] })},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if err := NewRegistration(opts...).Spec.Validate(); err != nil {
				t.Fatalf("Validate = %v", err)
			}
		})
	}
}

func TestOptionsOverrideTheirSpecFieldAndNothingElse(t *testing.T) {
	base := NewRegistration().Spec
	if base.Emission != registry.EmitAll || base.Delivery != registry.DeliverByMembership ||
		!base.Membership.StoreRows || base.AdHocQueryWatch {
		t.Fatalf("default policies = %+v", base)
	}
	if got := NewRegistration(WithEmission(registry.EmitWatchedOnly)).Spec.Emission; got != registry.EmitWatchedOnly {
		t.Errorf("Emission = %v", got)
	}
	if got := NewRegistration(WithDelivery(registry.DeliverByWatched)).Spec.Delivery; got != registry.DeliverByWatched {
		t.Errorf("Delivery = %v", got)
	}
	if !NewRegistration(WithAdHocQueryWatch(true)).Spec.AdHocQueryWatch {
		t.Error("AdHocQueryWatch was not set")
	}
	noRows := NewRegistration(WithStoreRows(false))
	if noRows.Spec.Membership.StoreRows || slices.Contains(noRows.Spec.Changes, noRows.Entered) || slices.Contains(noRows.Spec.Changes, noRows.Left) {
		t.Errorf("without membership rows the spec is %+v", noRows.Spec)
	}
	if beta, alpha := NewRegistration(WithBeta()), NewRegistration(); beta.Spec.EntityKind == alpha.Spec.EntityKind || beta.Entity != beta.Spec.EntityKind {
		t.Errorf("entity kinds: beta %q, alpha %q", beta.Spec.EntityKind, alpha.Spec.EntityKind)
	}
}

func TestTheRegistrationIsMadeOfTheVocabularyWordsOfTheRunningRegistration(t *testing.T) {
	reg := NewRegistration()
	want := map[string]string{
		reg.Entity: "alpha", reg.Retitled: "retitled", reg.Toggled: "toggled", reg.Tweaked: "tweaked",
		reg.Added: "added", reg.Removed: "gone", reg.Entered: "joined", reg.Left: "left",
		reg.ViewAll: "all", reg.ViewToggled: "flagged", reg.Extra: "extra",
	}
	for got, first := range want {
		if got != vocab.W(first) {
			t.Errorf("the word for %q is %q, want %q", first, got, vocab.W(first))
		}
	}
	if !vocab.IsSecond() && reg.Entity != "alpha" {
		t.Errorf("the first registration's entity kind is %q", reg.Entity)
	}
}

// TestTheRigRunsTheEngineTheQueryRunnerAndTheFeedTogether is the scenario the
// later packets build on: a query run records an arrival, an engine change
// reaches the feed, and a departure follows.
func TestTheRigRunsTheEngineTheQueryRunnerAndTheFeedTogether(t *testing.T) {
	r := NewRig(t)
	reg := r.Reg
	r.Settings.Define(queryMine, "expression-one")
	r.Seed(idOne, Content{Ref: refOne, Stamp: "s1", Title: "A title", At: Start})

	// The first complete run is the baseline: it appends no arrival row.
	r.Lister.Script(queryMine, true, query.Member{ID: idOne, Ref: refOne})
	row := r.RunQuery(queryMine)
	if !slices.Equal(row.IDs, []string{refOne}) || row.BaselineAt.IsZero() {
		t.Fatalf("stored row = %+v, want %s and a baseline", row, refOne)
	}
	WantNoRows(t, r.Rows())
	if calls := r.Tracker.Calls(); len(calls) != 1 || !slices.Equal(calls[0], []string{refOne}) {
		t.Errorf("tracked = %v", calls)
	}

	// The cursor starts at the tail; a change of the entity reaches it.
	WantNoRows(t, r.Poll(reg.ViewAll, consumer, queryMine).Rows)
	r.Clock.Advance(time.Minute)
	if err := r.ChangePrimary(idOne, governor.PurposeRefresh, func(c *Content) { c.Title = "Another title" }); err != nil {
		t.Fatal(err)
	}
	res := r.Poll(reg.ViewAll, consumer, queryMine)
	WantRowKinds(t, res.Rows, []string{reg.Retitled})
	WantRefs(t, res.Rows, refOne)
	WantFieldNames(t, res.Rows[0], "title")
	r.Ack(reg.ViewAll, consumer, queryMine, res.NextSeq)

	// The flag view delivers only rows that carry the toggled kind.
	WantNoRows(t, r.Poll(reg.ViewToggled, "consumer-2", queryMine).Rows)
	if err := r.ChangePrimary(idOne, governor.PurposeRefresh, func(c *Content) { c.Open = true }); err != nil {
		t.Fatal(err)
	}
	WantRowKinds(t, r.Poll(reg.ViewToggled, "consumer-2", queryMine).Rows, []string{reg.Toggled})

	// A second member arrives, then the list drops it: the arrival and the
	// departure are rows of the query's scope.
	r.Clock.Advance(time.Hour)
	r.Lister.Script(queryMine, true, query.Member{ID: idOne, Ref: refOne}, query.Member{ID: idTwo, Ref: refTwo})
	r.RunQuery(queryMine)
	r.Lister.Script(queryMine, true, query.Member{ID: idOne, Ref: refOne})
	r.RunQuery(queryMine)
	res = r.Poll(reg.ViewAll, consumer, queryMine)
	var arrivals, departures []string
	for _, row := range res.Rows {
		switch {
		case slices.Contains(row.Kinds, reg.Entered):
			arrivals = append(arrivals, row.ID)
		case slices.Contains(row.Kinds, reg.Left):
			departures = append(departures, row.ID)
		}
	}
	// refOne's toggled row is after the acknowledged position too.
	if !slices.Equal(arrivals, []string{refTwo}) || !slices.Equal(departures, []string{refTwo}) {
		t.Errorf("arrivals = %v, departures = %v; rows %v", arrivals, departures, Seqs(res.Rows))
	}
	// A poll re-runs a stale query too, so there are at least the three runs above.
	if calls := r.Lister.Calls(); len(calls) < 3 || calls[0].Key.Name != queryMine || !slices.Equal(calls[0].Exprs, []string{"expression-one"}) {
		t.Errorf("list calls = %+v", calls)
	}
}

func TestAFetchOfAGroupSettlesAPendingRowAskedForByTheContent(t *testing.T) {
	r := NewRig(t)
	reg := r.Reg
	r.Seed(idOne, Content{Ref: refOne, Stamp: "s1", Title: "A title", At: Start})
	r.SeedGroup(idOne, reg.Extra, "one")

	// The change asks for the extra group: nothing is appended until it is fetched.
	r.Clock.Advance(time.Minute)
	if err := r.ChangePrimary(idOne, governor.PurposeRefresh, func(c *Content) {
		c.Title, c.Await = "Another title", []string{reg.Extra}
	}); err != nil {
		t.Fatal(err)
	}
	WantNoRows(t, r.Rows())
	if err := r.RefreshGroup(idOne, reg.Extra, "two", r.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	rows := r.Rows()
	WantRowKinds(t, rows, []string{reg.Retitled, reg.Tweaked})
	WantFieldNames(t, rows[0], reg.Extra, "title")
}

func TestScriptedHooksReplaceTheDefaultClassification(t *testing.T) {
	r := NewRig(t)
	reg := r.Reg
	r.Classifier.OnPrimary = func(prev, next Content) (engine.Result, []string, error) {
		return engine.Result{Kinds: []string{reg.Toggled}}, nil, nil
	}
	r.Seed(idOne, Content{Ref: refOne, Stamp: "s1"})
	r.Clock.Advance(time.Minute)
	if err := r.ChangePrimary(idOne, governor.PurposeRefresh, func(c *Content) { c.Stamp = "s2" }); err != nil {
		t.Fatal(err)
	}
	WantRowKinds(t, r.Rows(), []string{reg.Toggled})
}

func TestAClassifierErrorFailsTheChange(t *testing.T) {
	r := NewRig(t)
	boom := errors.New("scripted failure")
	r.Classifier.OnPrimary = func(Content, Content) (engine.Result, []string, error) { return engine.Result{}, nil, boom }
	r.Seed(idOne, Content{Ref: refOne})
	if err := r.ChangePrimary(idOne, governor.PurposeRefresh, func(c *Content) { c.Title = "x" }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the scripted failure", err)
	}
}

func TestSettingsWatchedAnswersFromTheMapTheTestFills(t *testing.T) {
	r := NewRig(t, WithRegistration(WithEmission(registry.EmitWatchedOnly), WithDelivery(registry.DeliverByWatched)))
	r.Settings.Define(queryMine, "e")
	r.Settings.Define("other-query", "f")
	if r.Settings.Watched(queryMine) || r.Queries.Watched(queryMine) {
		t.Fatal("a query is watched before the test says so")
	}
	r.Settings.SetWatched(queryMine, true)
	if !r.Settings.Watched(queryMine) || !r.Queries.Watched(queryMine) || r.Queries.Watched("other-query") {
		t.Error("Watched does not follow the map")
	}
	r.Lister.Script(queryMine, true, query.Member{ID: idOne, Ref: refOne}, query.Member{ID: idTwo, Ref: refTwo})
	r.Lister.Script("other-query", true, query.Member{ID: "id-3", Ref: "Example-Org/Thing#3"})
	r.RunQuery(queryMine)
	r.RunQuery("other-query")
	refs, err := r.Queries.WatchedRefs(context.Background())
	if err != nil || !slices.Equal(refs, []string{refOne, refTwo}) {
		t.Fatalf("WatchedRefs = %v, %v; want the members of the watched query only", refs, err)
	}
	if r.Reg.Spec.Delivery != registry.DeliverByWatched || r.Reg.Spec.Emission != registry.EmitWatchedOnly {
		t.Errorf("spec = %+v", r.Reg.Spec)
	}
}

func TestSettingsAnswerNamesTimesAndExpressions(t *testing.T) {
	s := NewSettings()
	s.Define("b-query", "x", "y")
	s.Define("a-query", "z")
	s.SetTTL("a-query", 5*time.Second)
	if names := s.Names(); !slices.Equal(names, []string{"a-query", "b-query"}) {
		t.Errorf("Names = %v, want them sorted", names)
	}
	if e, ok := s.Expr("b-query"); !ok || !slices.Equal(e, []string{"x", "y"}) {
		t.Errorf("Expr = %v, %v", e, ok)
	}
	if _, ok := s.Expr("c-query"); ok {
		t.Error("an undefined query has expressions")
	}
	if got := s.TTL(query.Key{Name: "a-query"}); got != 5*time.Second {
		t.Errorf("TTL = %s", got)
	}
	if got := s.TTL(query.Key{Name: "b-query"}); got != s.DefaultTTL {
		t.Errorf("TTL of a query with none = %s", got)
	}
	if got := s.TTL(query.Key{Name: "a-query", Args: map[string]string{"k": "v"}}); got != s.AdHocTTL {
		t.Errorf("TTL of an ad-hoc key = %s", got)
	}
}

func TestAliasesImplementTheReverseLookupAndAFoldedResolve(t *testing.T) {
	a := NewAliases(NewRegistration().Spec.FoldRef)
	ctx := context.Background()
	if _, ok, err := a.Ref(ctx, idOne); ok || err != nil {
		t.Fatalf("Ref of an unknown id = %v, %v", ok, err)
	}
	if err := a.Put(ctx, idOne, refOne); err != nil {
		t.Fatal(err)
	}
	if ref, ok, err := a.Ref(ctx, idOne); !ok || err != nil || ref != refOne {
		t.Errorf("Ref = %q, %v, %v", ref, ok, err)
	}
	got, err := a.Resolve(ctx, []string{"example-org/thing#1", refTwo})
	if err != nil {
		t.Fatal(err)
	}
	if al := got["example-org/thing#1"]; al.ID != idOne || al.CanonicalRef != refOne {
		t.Errorf("resolved = %+v", got)
	}
	if _, ok := got[refTwo]; ok {
		t.Error("an unknown reference resolved")
	}
	// A rename moves the reference of the same id.
	if err := a.Put(ctx, idOne, refTwo); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Resolve(ctx, []string{refTwo}); got[refTwo].ID != idOne {
		t.Errorf("after the rename: %+v", got)
	}
}

func TestListerFailsAndAuthRefusesAsScripted(t *testing.T) {
	r := NewRig(t)
	r.Settings.Define(queryMine, "e")
	boom := errors.New("scripted listing failure")
	r.Lister.Fail(queryMine, boom)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := r.Runner.Fetch(ctx, query.Key{Name: queryMine}); !errors.Is(err, boom) {
		t.Fatalf("Fetch = %v, want the scripted failure", err)
	}
	r.Lister.Script(queryMine, true)
	denied := errors.New("scripted sign-in refusal")
	r.Auth.Set(denied)
	if _, err := r.Runner.Fetch(ctx, query.Key{Name: queryMine}); !errors.Is(err, denied) {
		t.Fatalf("Fetch while unauthenticated = %v", err)
	}
	r.Auth.Set(nil)
	r.RunQuery(queryMine)
}

func TestTheConfirmerReportsTheScriptedFindingForADeparture(t *testing.T) {
	c := NewConfirmer()
	c.Script(idOne, query.Confirmation{Gone: true})
	ctx := context.Background()
	if got, err := c.Confirm(ctx, idOne, refOne); err != nil || !got.Gone {
		t.Errorf("scripted = %+v, %v", got, err)
	}
	if got, err := c.Confirm(ctx, idTwo, refTwo); err != nil || got.Gone || got.State != c.State {
		t.Errorf("unscripted = %+v, %v", got, err)
	}
	c.Err = errors.New("scripted read failure")
	if _, err := c.Confirm(ctx, idTwo, refTwo); err == nil {
		t.Error("a scripted failure was swallowed")
	}
}

func TestAssertionsFailOnAMismatchAndPassOtherwise(t *testing.T) {
	row := func(seq int64, ref string, kinds ...string) store.ChangeRow {
		return store.ChangeRow{Seq: seq, ID: ref, Kinds: kinds}
	}
	rows := []store.ChangeRow{row(1, refOne, "k1"), row(2, refTwo, "k2", "k3")}
	failing := map[string]func(tb testing.TB){
		"WantSeqs":     func(tb testing.TB) { WantSeqs(tb, rows, 1, 3) },
		"WantSeqs len": func(tb testing.TB) { WantSeqs(tb, rows, 1) },
		"WantKinds":    func(tb testing.TB) { WantKinds(tb, rows[1].Kinds, "k2") },
		"WantRowKinds": func(tb testing.TB) { WantRowKinds(tb, rows, []string{"k1"}, []string{"k2"}) },
		"WantRowKinds len": func(tb testing.TB) {
			WantRowKinds(tb, rows, []string{"k1"})
		},
		"WantRefs":   func(tb testing.TB) { WantRefs(tb, rows, refOne) },
		"WantNoRows": func(tb testing.TB) { WantNoRows(tb, rows) },
	}
	for name, call := range failing {
		t.Run("fails "+name, func(t *testing.T) {
			if !fails(call) {
				t.Error("the assertion passed on a mismatch")
			}
		})
	}
	passing := func(tb testing.TB) {
		WantSeqs(tb, rows, 1, 2)
		WantKinds(tb, rows[1].Kinds, "k2", "k3")
		WantRowKinds(tb, rows, []string{"k1"}, []string{"k2", "k3"})
		WantRefs(tb, rows, refOne, refTwo)
		WantNoRows(tb, nil)
	}
	if fails(passing) {
		t.Error("an assertion failed on a match")
	}
}

// recorder is a testing.TB that records a failure instead of failing; a Fatalf
// stops the call as testing.TB's does.
type recorder struct {
	testing.TB
	failed bool
}

type stopped struct{}

func (r *recorder) Helper()                      {}
func (r *recorder) Errorf(string, ...any)        { r.failed = true }
func (r *recorder) Fatalf(f string, args ...any) { r.failed = true; panic(stopped{}) }
func (r *recorder) Fatal(args ...any)            { r.failed = true; panic(stopped{}) }

// fails runs call over a recorder and reports whether it failed.
func fails(call func(tb testing.TB)) (failed bool) {
	rec := &recorder{}
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(stopped); !ok {
				panic(p)
			}
		}
		failed = rec.failed
	}()
	call(rec)
	return
}
