package pipeline

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The post-commit steps of a hydration (propagation records, the
// interpretation row, the derived links) are ONE Unit of Work with the entity,
// its change row and the feed marker. These tests fail each step once at its
// seam, assert that nothing of the failed call is left, then call again
// without injection and assert the outcome equals that of a control entity
// processed without any injection.

// uowScenario is a pipeline holding an observed issue i-1 linked to PR #7
// (which has a row), plus the call that applies the next change: the issue
// closes and its link moves from #7 to #8, so the call writes a propagation
// record to #7, rewrites the interpretation and replaces the derived link.
type uowScenario struct {
	p *Pipeline
	// step makes the one call under test.
	step func() error
	// feed is true when step ingests a feed row (so the marker moves).
	feed bool
}

func uowRunScenario(t *testing.T) uowScenario {
	t.Helper()
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	seedPR(t, p, ecRepo+"#7")
	res = ecIssue(t, "closed", "me", "2026-09-16T01:00:00Z", workMeta("8"))
	return uowScenario{p: p, step: func() error {
		_, err := p.RunEntityChange(context.Background(), "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
		return err
	}}
}

func uowFeedScenario(t *testing.T) uowScenario {
	t.Helper()
	p, g := frPipeline(t)
	g.result = ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	frIngest(t, p, frRow(1, "i-1", "added"))
	seedPR(t, p, ecRepo+"#7")
	g.result = ecIssue(t, "closed", "me", "2026-09-16T01:00:00Z", workMeta("8"))
	return uowScenario{p: p, feed: true, step: func() error {
		_, err := p.IngestFeedRow(context.Background(), frRow(2, "i-1", "status_changed"))
		return err
	}}
}

// uowState is everything the Unit of Work covers, without sequence numbers.
type uowState struct {
	Version  int64
	Issue    []string // "kinds|origin|version" per change row of the issue
	PR       []string // the same for the linked PR
	Interp   store.Interpretation
	Links    []string
	Marker   store.FeedMarker
	HasMark  bool
	Failures int
}

func uowCapture(t *testing.T, p *Pipeline) uowState {
	t.Helper()
	ctx := context.Background()
	var st uowState
	e, found, err := p.store.GetEntity(ctx, ecRepo, "issue", "i-1")
	if err != nil || !found {
		t.Fatalf("GetEntity = (%v, %v)", found, err)
	}
	st.Version = e.Version
	rows := func(typ, id string) []string {
		var out []string
		for _, r := range p.changeRows(t, typ, id) {
			out = append(out, fmt.Sprintf("%v|%s|%d", r.Kinds, r.Origin, r.Version))
		}
		return out
	}
	st.Issue, st.PR = rows("issue", "i-1"), rows("pr", ecRepo+"#7")
	if st.Interp, _, err = p.store.GetInterpretation(ctx, ecRepo, "issue", "i-1"); err != nil {
		t.Fatal(err)
	}
	links, err := p.store.ListXrefLinksFrom(ctx, ecRepo, "issue", "i-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		st.Links = append(st.Links, fmt.Sprintf("%s|%s|%s|%s", l.ToType, l.ToID, l.Origin, l.Relation))
	}
	if m, ok, err := p.store.FeedMarker(ctx, frInstance); err != nil {
		t.Fatal(err)
	} else if ok {
		st.Marker, st.HasMark = m, true
	}
	fails, err := p.store.ListIngestFailures(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	st.Failures = len(fails)
	return st
}

func TestUnitOfWork_FailedPostCommitStepLeavesNothingAndTheNextCallRepairs(t *testing.T) {
	boom := errors.New("injected post-commit failure")
	injections := []struct {
		name    string
		install func(p *Pipeline, fail func() error)
	}{
		{"before the steps (beforeFinish)", func(p *Pipeline, fail func() error) { p.seams.beforeFinish = fail }},
		{"propagation records (appendLocalRecords)", func(p *Pipeline, fail func() error) { p.seams.appendLocalRecords = fail }},
		{"interpretation row", func(p *Pipeline, fail func() error) { p.seams.beforeInterpretationWrite = fail }},
		{"derived links", func(p *Pipeline, fail func() error) { p.seams.beforeLinkReplace = fail }},
	}
	paths := []struct {
		name  string
		build func(*testing.T) uowScenario
	}{
		{"RunEntityChange", uowRunScenario},
		{"IngestFeedRow", uowFeedScenario},
	}
	for _, path := range paths {
		for _, inj := range injections {
			t.Run(path.name+"/"+inj.name, func(t *testing.T) {
				control := path.build(t)
				if err := control.step(); err != nil {
					t.Fatalf("control step: %v", err)
				}
				want := uowCapture(t, control.p)

				sc := path.build(t)
				before := uowCapture(t, sc.p)
				if reflect.DeepEqual(before, want) {
					t.Fatalf("the step changes nothing, so the test proves nothing: %+v", before)
				}
				fired := 0
				inj.install(sc.p, func() error {
					fired++
					if fired > 1 {
						return nil
					}
					return boom
				})

				if err := sc.step(); !errors.Is(err, boom) {
					t.Fatalf("injected call err = %v, want the injected failure", err)
				}
				// Atomicity: a failure leaves none of the call's writes, and the
				// feed marker did not advance.
				if got := uowCapture(t, sc.p); !reflect.DeepEqual(got, before) {
					t.Fatalf("a failed post-commit step left partial writes:\nbefore %+v\nafter  %+v", before, got)
				}

				// The next poll (the same call, no injection) repairs the row.
				if err := sc.step(); err != nil {
					t.Fatalf("repair call: %v", err)
				}
				got := uowCapture(t, sc.p)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("repaired state differs from the uninjected control:\nwant %+v\ngot  %+v", want, got)
				}
				if got.Version != before.Version+1 {
					t.Fatalf("version = %d, want one bump from %d", got.Version, before.Version)
				}
				if sc.feed && (!got.HasMark || got.Marker.Seq != 2 || before.Marker.Seq != 1) {
					t.Fatalf("marker before=%+v after=%+v, want it advanced from 1 to 2 exactly once", before.Marker, got.Marker)
				}
			})
		}
	}
}
