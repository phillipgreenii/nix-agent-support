package summary

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func sample() Summary {
	return Summary{
		NodeID: "PR_1", Repo: "Acme-Org/Widget", Number: 7, Title: "Add the widget", URL: "u", State: "OPEN",
		UpdatedAt: "2026-10-12T08:00:00Z", Mergeable: "MERGEABLE", Author: "phillipgreenii",
		Labels: []string{"b", "a"}, LabelCount: 2, CommentCount: 3, ReviewCount: 1, ReviewThreadCount: 2,
		HeadRefOid: "head-1", HeadRefName: "feature", BaseRefName: "main", BaseRefOid: "base-1", ChecksState: "SUCCESS",
	}
}

func newPersister(t *testing.T) (*Persister, *store.Store, *governor.ManualClock) {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)
	return &Persister{Store: st, Identity: entity.NewIdentity(st, clk), TTL: 60 * time.Second, Clock: clk}, st, clk
}

func TestFingerprint_ChangesOnTheListedSignals(t *testing.T) {
	base := sample().Fingerprint()
	for name, mutate := range map[string]func(*Summary){
		"comment count":       func(s *Summary) { s.CommentCount++ },
		"review count":        func(s *Summary) { s.ReviewCount++ },
		"review thread count": func(s *Summary) { s.ReviewThreadCount++ },
		"updatedAt":           func(s *Summary) { s.UpdatedAt = "2026-10-12T08:00:01Z" },
		"head":                func(s *Summary) { s.HeadRefOid = "head-2" },
		"base":                func(s *Summary) { s.BaseRefOid = "base-2" },
		"state":               func(s *Summary) { s.State = "MERGED" },
		"mergeable":           func(s *Summary) { s.Mergeable = "CONFLICTING" },
		"title":               func(s *Summary) { s.Title = "other" },
	} {
		s := sample()
		mutate(&s)
		if s.Fingerprint() == base {
			t.Errorf("fingerprint did not change on %s", name)
		}
	}
	// Label order is not a change; the receiver is not modified by hashing.
	s := sample()
	s.Labels = []string{"a", "b"}
	if s.Fingerprint() != base {
		t.Error("a reordered label list must not change the fingerprint")
	}
	o := sample()
	_ = o.Fingerprint()
	if o.Labels[0] != "b" {
		t.Error("Fingerprint must not reorder the caller's labels")
	}
}

func TestPersist_StoresTheGroupWithTimesHeadAndBase(t *testing.T) {
	ctx := context.Background()
	p, st, clk := newPersister(t)
	got, err := p.Persist(ctx, sample())
	if err != nil {
		t.Fatal(err)
	}
	if !got.First || got.PrevFingerprint != "" || got.Fingerprint != got.Summary.Fingerprint() || got.MergeabilityChanged || got.Rename != nil {
		t.Errorf("Stored = %+v", got)
	}
	g, ok, err := st.GetGroup(ctx, entity.KindPR, "PR_1", GroupName)
	if err != nil || !ok {
		t.Fatalf("group: ok=%v err=%v", ok, err)
	}
	if g.FetchedForHead != "head-1" || g.FetchedForBase != "base-1" || !g.FetchedAt.Equal(t0) ||
		!g.FreshUntil.Equal(t0.Add(60*time.Second)) || g.Fingerprint != got.Fingerprint || g.Generation != 1 {
		t.Errorf("group row = %+v", g)
	}
	var content Summary
	if err := json.Unmarshal(g.Content, &content); err != nil || content.Title != "Add the widget" || content.CommentCount != 3 {
		t.Errorf("content = %+v err %v", content, err)
	}
	e, ok, _ := st.GetEntity(ctx, entity.KindPR, "PR_1")
	if !ok || e.LastKnownMergeable != "MERGEABLE" {
		t.Errorf("entity = %+v ok=%v", e, ok)
	}
	if a, ok, _ := st.AliasByNodeID(ctx, "PR_1"); !ok || a.Ref != "Acme-Org/Widget#7" {
		t.Errorf("alias = %+v ok=%v", a, ok)
	}

	// A refetch later moves fetched_at and fresh_until but not the fingerprint.
	clk.Advance(45 * time.Second)
	again, err := p.Persist(ctx, sample())
	if err != nil {
		t.Fatal(err)
	}
	g2, _, _ := st.GetGroup(ctx, entity.KindPR, "PR_1", GroupName)
	if again.Fingerprint != got.Fingerprint || again.PrevFingerprint != got.Fingerprint || again.First {
		t.Errorf("Stored on refetch = %+v", again)
	}
	if !g2.FetchedAt.Equal(t0.Add(45*time.Second)) || !g2.FreshUntil.Equal(t0.Add(105*time.Second)) || g2.Generation != 2 {
		t.Errorf("group row after refetch = %+v", g2)
	}
}

func TestPersist_FingerprintChangesOnACommentCountChangeNotOnFetchedAt(t *testing.T) {
	ctx := context.Background()
	p, _, clk := newPersister(t)
	first, _ := p.Persist(ctx, sample())
	clk.Advance(time.Hour)
	same, _ := p.Persist(ctx, sample())
	if same.Fingerprint != first.Fingerprint {
		t.Error("only fetched_at moved: the fingerprint must not change")
	}
	more := sample()
	more.CommentCount++
	changed, err := p.Persist(ctx, more)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Fingerprint == first.Fingerprint || changed.PrevFingerprint != first.Fingerprint {
		t.Errorf("comment count change: %+v", changed)
	}
}

func TestPersist_MergeabilityIsCarriedAcrossUnknown(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPersister(t)
	observe := func(m string, mutate func(*Summary)) Stored {
		s := sample()
		s.Mergeable = m
		if mutate != nil {
			mutate(&s)
		}
		got, err := p.Persist(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	a := observe("MERGEABLE", nil)
	u := observe("UNKNOWN", nil)
	if u.Summary.Mergeable != "MERGEABLE" || u.MergeabilityChanged || u.Fingerprint != a.Fingerprint {
		t.Errorf("UNKNOWN after MERGEABLE = %+v; want the carried value, no change, the same fingerprint", u)
	}
	c := observe("CONFLICTING", nil)
	if !c.MergeabilityChanged || c.PrevMergeable != "MERGEABLE" || c.Summary.Mergeable != "CONFLICTING" {
		t.Errorf("CONFLICTING = %+v; want exactly one change from MERGEABLE", c)
	}
	if c2 := observe("CONFLICTING", nil); c2.MergeabilityChanged {
		t.Error("the same known value again is no change")
	}
	g, _, _ := st.GetGroup(ctx, entity.KindPR, "PR_1", GroupName)
	var content Summary
	_ = json.Unmarshal(g.Content, &content)
	if content.Mergeable != "CONFLICTING" {
		t.Errorf("stored mergeable = %q", content.Mergeable)
	}

	// A head change clears the carried value: the next UNKNOWN stays UNKNOWN.
	h := observe("UNKNOWN", func(s *Summary) { s.HeadRefOid = "head-2" })
	if h.Summary.Mergeable != "UNKNOWN" || h.MergeabilityChanged {
		t.Errorf("UNKNOWN on a new head = %+v", h)
	}
	if e, _, _ := st.GetEntity(ctx, entity.KindPR, "PR_1"); e.LastKnownMergeable != "" {
		t.Errorf("carried value after a head change = %q, want cleared", e.LastKnownMergeable)
	}
	// The first known value for the new head is not a change from the old head's.
	if k := observe("MERGEABLE", func(s *Summary) { s.HeadRefOid = "head-2" }); k.MergeabilityChanged {
		t.Errorf("first known value on the new head reported a change: %+v", k)
	}
	// A base change does the same.
	b := observe("UNKNOWN", func(s *Summary) { s.HeadRefOid = "head-2"; s.BaseRefOid = "base-2" })
	if b.Summary.Mergeable != "UNKNOWN" || b.MergeabilityChanged {
		t.Errorf("UNKNOWN on a new base = %+v", b)
	}
}

func TestPersist_RenameIsReportedAndKeepsTheEntity(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPersister(t)
	if _, err := p.Persist(ctx, sample()); err != nil {
		t.Fatal(err)
	}
	s := sample()
	s.Repo = "Acme-Org/Gadget"
	got, err := p.Persist(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rename == nil || !got.Rename.Renamed || got.Rename.Old.String() != "Acme-Org/Widget#7" || got.Rename.Ref.String() != "Acme-Org/Gadget#7" {
		t.Errorf("Rename = %+v", got.Rename)
	}
	if a, _, _ := st.AliasByNodeID(ctx, "PR_1"); a.Ref != "Acme-Org/Gadget#7" {
		t.Errorf("alias = %+v", a)
	}
	if es, _ := st.ListEntities(ctx, entity.KindPR); len(es) != 1 {
		t.Errorf("a rename must keep one entity, got %d", len(es))
	}
}

func TestPersist_PreservesEntityBookkeepingAndSkipsGone(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPersister(t)
	if err := st.PutEntity(ctx, store.EntityRow{Kind: entity.KindPR, ID: "PR_1", Version: 4, KeepAlive: true, LastAccess: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Persist(ctx, sample()); err != nil {
		t.Fatal(err)
	}
	e, _, _ := st.GetEntity(ctx, entity.KindPR, "PR_1")
	if e.Version != 4 || !e.KeepAlive || !e.LastAccess.Equal(t0) || e.LastKnownMergeable != "MERGEABLE" {
		t.Errorf("entity = %+v", e)
	}
	got, err := p.Persist(ctx, Summary{NodeID: "PR_gone", Gone: true})
	if err != nil || !got.Summary.Gone {
		t.Fatalf("Persist(gone) = %+v, %v", got, err)
	}
	if _, ok, _ := st.GetEntity(ctx, entity.KindPR, "PR_gone"); ok {
		t.Error("a Gone summary must not create an entity")
	}
}
