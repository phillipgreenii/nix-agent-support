package workitem

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/view"
)

func load(t *testing.T, name string) *view.View {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

var (
	ctxFixCI = Context{HeadSHA: "9f3c1e2"}
	ctxConf  = Context{HeadSHA: "9f3c1e2", Branch: "feature/retry", Base: "main", BaseSHA: "b45e000"}
)

func TestEntityRefFromView(t *testing.T) {
	e := EntityRefFrom(load(t, "items_open.json"))
	if e != (EntityRef{Type: "pr", ID: "acme/widgets#42", NodeID: "PR_node_42"}) {
		t.Fatalf("%+v", e)
	}
}

func TestIndexFindsAnchorAndEveryKind(t *testing.T) {
	ix := BuildIndex(load(t, "items_open.json"))
	a, ok := ix.Anchor()
	if !ok || a.ID != "bd-1" || a.Kind != KindAnchor || !a.Open() || a.DedupKey != "pr:acme/widgets#42:anchor" {
		t.Fatalf("anchor: %+v %v", a, ok)
	}
	for _, tc := range []struct {
		k   Kind
		c   Context
		id  string
		len int
	}{
		{KindReviewPR, Context{}, "bd-2", 1},
		{KindFixCI, ctxFixCI, "bd-3", 1},
		{KindResolveConflict, ctxConf, "bd-4", 1},
		{KindProcessFeedback, Context{Digest: "d1"}, "bd-5", 1},
	} {
		it, ok := ix.Find(tc.k, tc.c)
		if !ok || it.ID != tc.id || it.Kind != tc.k {
			t.Errorf("Find(%s) = %+v %v", tc.k, it, ok)
		}
		if got := ix.ByKind(tc.k); len(got) != tc.len {
			t.Errorf("ByKind(%s) = %d items", tc.k, len(got))
		}
	}
	// the linked pr is not a work item.
	total := 0
	for _, k := range Kinds() {
		total += len(ix.ByKind(k))
	}
	if total != 5 {
		t.Fatalf("work items = %d, want 5", total)
	}
	fix, _ := ix.Find(KindFixCI, ctxFixCI)
	if fix.Metadata["failing_builds"] != "101:1" || !fix.HasLabel("worker-ready") || fix.HasLabel("human") {
		t.Fatalf("fix-ci item: %+v", fix)
	}
}

func TestIndexNoneForDifferentContext(t *testing.T) {
	ix := BuildIndex(load(t, "items_open.json"))
	cases := []struct {
		name string
		k    Kind
		c    Context
	}{
		{"new head for fix-ci", KindFixCI, Context{HeadSHA: "newhead"}},
		{"different base sha", KindResolveConflict, Context{HeadSHA: "9f3c1e2", Branch: "feature/retry", Base: "main", BaseSHA: "other"}},
		{"different head commit", KindResolveConflict, Context{HeadSHA: "x", Branch: "feature/retry", Base: "main", BaseSHA: "b45e000"}},
		{"different branch", KindResolveConflict, Context{HeadSHA: "9f3c1e2", Branch: "other", Base: "main", BaseSHA: "b45e000"}},
		{"different base", KindResolveConflict, Context{HeadSHA: "9f3c1e2", Branch: "feature/retry", Base: "dev", BaseSHA: "b45e000"}},
		{"uncovered digest", KindProcessFeedback, Context{Digest: "d9"}},
		{"empty digest", KindProcessFeedback, Context{}},
	}
	for _, tc := range cases {
		if it, ok := ix.Find(tc.k, tc.c); ok {
			t.Errorf("%s: found %+v", tc.name, it)
		}
	}
}

func TestClosedItemsAreFoundHoweverClosed(t *testing.T) {
	ix := BuildIndex(load(t, "closed_three_ways.json"))
	for _, tc := range []struct {
		k  Kind
		c  Context
		id string
	}{
		{KindReviewPR, Context{}, "bd-10"},
		{KindFixCI, ctxFixCI, "bd-11"},
		{KindResolveConflict, ctxConf, "bd-12"},
	} {
		it, ok := ix.Find(tc.k, tc.c)
		if !ok || it.ID != tc.id {
			t.Fatalf("Find(%s) = %+v %v", tc.k, it, ok)
		}
		if it.Open() || it.State != "closed" {
			t.Errorf("%s: should be closed: %+v", tc.k, it)
		}
	}
	// a different context is still none for the closed items.
	if _, ok := ix.Find(KindFixCI, Context{HeadSHA: "newhead"}); ok {
		t.Error("closed fix-ci matched a new head")
	}
	// nothing is parked: the human label on the CLOSED fix-ci does not count.
	if ix.OpenHumanParked(KindFixCI) {
		t.Error("closed human item counted as parked")
	}
}

func TestOpenStateSemantics(t *testing.T) {
	ix := BuildIndex(load(t, "items_open.json"))
	it, ok := ix.Find(KindProcessFeedback, Context{Digest: "d1"})
	if !ok || it.State != "in_progress" || it.Assignee != "worker-1" || !it.Open() {
		t.Fatalf("claimed item: %+v", it)
	}
	for state, want := range map[string]bool{"open": true, "in_progress": true, "blocked": true, "": true, "closed": false} {
		if got := (Item{State: state}).Open(); got != want {
			t.Errorf("Open(%q) = %v", state, got)
		}
	}
}

func TestCoveredCommentsUnionOverOpenAndClosedCycles(t *testing.T) {
	ix := BuildIndex(load(t, "feedback_cycles.json"))
	got := ix.CoveredComments()
	var ids []string
	for id := range got {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if want := []string{"c1", "c2", "c3", "c4"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("covered = %v, want %v", ids, want)
	}
	// only process-feedback items contribute.
	if got := BuildIndex(load(t, "items_open.json")).CoveredComments(); !got["c1"] || !got["c2"] || len(got) != 2 {
		t.Fatalf("items_open covered = %v", got)
	}
	if got := BuildIndex(load(t, "closed_three_ways.json")).CoveredComments(); len(got) != 0 {
		t.Fatalf("non-feedback fixture covered = %v", got)
	}
}

func TestPreCutoverFeedbackCycle(t *testing.T) {
	for _, name := range []string{"precutover_feedback.json", "precutover_feedback_closed.json"} {
		ix := BuildIndex(load(t, name))
		items := ix.ByKind(KindProcessFeedback)
		if len(items) != 1 || items[0].Digest() != "abc123" || items[0].DedupKey != "" {
			t.Fatalf("%s: %+v", name, items)
		}
		it, ok := ix.Find(KindProcessFeedback, Context{Digest: "abc123"})
		if !ok || it.ID != "bd-30" {
			t.Fatalf("%s: Find by label digest = %+v %v", name, it, ok)
		}
		if _, ok := ix.Find(KindProcessFeedback, Context{Digest: "other"}); ok {
			t.Fatalf("%s: found for another digest", name)
		}
		if len(ix.CoveredComments()) != 0 {
			t.Fatalf("%s: covered must stay the union of covered_comments only", name)
		}
	}
}

func TestDigestFromLabel(t *testing.T) {
	if got := (Item{Labels: []string{"mine", "fbsum:xyz"}}).Digest(); got != "xyz" {
		t.Fatal(got)
	}
	if got := (Item{Labels: []string{"mine"}}).Digest(); got != "" {
		t.Fatal(got)
	}
}

func TestOpenHumanParkedCountsAsExisting(t *testing.T) {
	ix := BuildIndex(load(t, "human_parked.json"))
	if !ix.OpenHumanParked(KindFixCI) {
		t.Fatal("open human fix-ci not parked")
	}
	if ix.OpenHumanParked(KindReviewPR) {
		t.Fatal("closed human review-pr counted as parked")
	}
	if ix.OpenHumanParked(KindResolveConflict) {
		t.Fatal("no resolve-conflict item exists")
	}
	if it, ok := ix.Find(KindFixCI, ctxFixCI); !ok || it.ID != "bd-40" {
		t.Fatalf("parked item must be found as existing: %+v %v", it, ok)
	}
	if ix2 := BuildIndex(load(t, "items_open.json")); ix2.OpenHumanParked(KindFixCI) {
		t.Fatal("non-human item parked")
	}
}

func TestAdoptableItems(t *testing.T) {
	ix := BuildIndex(load(t, "adoptable.json"))
	got := map[string]Adoptable{}
	var order []string
	for _, a := range ix.Adoptable() {
		got[a.Item.ID] = a
		order = append(order, a.Item.ID)
	}
	want := map[string]struct {
		kind Kind
		by   string
	}{
		"bd-50": {KindAnchor, "anchor-prefix"},
		"bd-51": {KindReviewPR, "title"},
		"bd-52": {KindProcessFeedback, "node_id"},
		"bd-58": {KindReviewPR, "title"}, // malformed dedup_key == absent
	}
	if len(got) != len(want) {
		t.Fatalf("adoptable = %v", order)
	}
	for id, w := range want {
		a, ok := got[id]
		if !ok || a.Kind != w.kind || a.MatchedBy != w.by || a.Item.Kind != w.kind {
			t.Errorf("%s: %+v (%v), want %+v", id, a, ok, w)
		}
	}
	if !reflect.DeepEqual(order, []string{"bd-50", "bd-51", "bd-52", "bd-58"}) {
		t.Errorf("adoptable not in link order: %v", order)
	}
	for _, id := range []string{"bd-53", "bd-54", "bd-55", "bd-56", "bd-57"} {
		if _, ok := got[id]; ok {
			t.Errorf("%s must not be adoptable", id)
		}
	}
	// adopted items count as existing work.
	if a, ok := ix.Anchor(); !ok || a.ID != "bd-50" {
		t.Errorf("adopted anchor: %+v %v", a, ok)
	}
	if it, ok := ix.Find(KindProcessFeedback, Context{Digest: "zzz"}); !ok || it.ID != "bd-52" {
		t.Errorf("adopted feedback by label digest: %+v %v", it, ok)
	}
	if it, ok := ix.Find(KindReviewPR, Context{}); !ok || it.ID != "bd-56" {
		t.Errorf("keyed review-pr must be found first: %+v %v", it, ok)
	}
	// the keyed item and nothing else is the keyed review-pr.
	if n := len(ix.ByKind(KindReviewPR)); n != 3 {
		t.Errorf("review-pr items = %d", n)
	}
}

func TestKeylessNonMatchingAreNotIndexed(t *testing.T) {
	ix := BuildIndex(load(t, "adoptable.json"))
	for _, k := range Kinds() {
		for _, it := range ix.ByKind(k) {
			switch it.ID {
			case "bd-53", "bd-54", "bd-55", "bd-57":
				t.Errorf("%s indexed as %s", it.ID, k)
			}
		}
	}
}

func TestNodeIDKeyFormIsEqualIdentity(t *testing.T) {
	ix := BuildIndex(load(t, "items_node_id_keys.json"))
	if a, ok := ix.Anchor(); !ok || a.ID != "bd-1" {
		t.Fatalf("anchor under node form: %+v %v", a, ok)
	}
	if it, ok := ix.Find(KindFixCI, ctxFixCI); !ok || it.ID != "bd-3" || it.Open() {
		t.Fatalf("fix-ci under node form: %+v %v", it, ok)
	}
	// a rename leaves the old id form behind; the link still scopes it to this entity.
	if it, ok := ix.Find(KindReviewPR, Context{}); !ok || it.ID != "bd-9" {
		t.Fatalf("renamed id form: %+v %v", it, ok)
	}
}

func TestBuildIndexEdgeCases(t *testing.T) {
	ix := BuildIndex(&view.View{Type: "pr", ID: "x/y#1"})
	if _, ok := ix.Anchor(); ok {
		t.Fatal("anchor in empty view")
	}
	if len(ix.Adoptable()) != 0 || len(ix.CoveredComments()) != 0 || ix.OpenHumanParked(KindFixCI) {
		t.Fatal("empty view should yield empty answers")
	}
	if _, ok := ix.Find(KindReviewPR, Context{}); ok {
		t.Fatal("find in empty view")
	}
	var nilIx *Index
	if len(nilIx.ByKind(KindFixCI)) != 0 {
		t.Fatal("nil index")
	}
	if BuildIndex(nil) == nil {
		t.Fatal("BuildIndex(nil) must return an empty index")
	}
}

func TestMalformedMetadataIsEmptyNeverAPanic(t *testing.T) {
	v := &view.View{Type: "pr", ID: "x/y#1", Links: []view.Link{
		{Type: "issue", ID: "a", State: "open", Title: "process-feedback: x/y#1", Metadata: map[string]string{"covered_comments": ",,  ,", "dedup_key": "pr:x/y#1:process-feedback:d"}},
		{Type: "issue", ID: "b", State: "open", Title: "fix-ci: x/y#1", Metadata: map[string]string{"dedup_key": "pr:x/y#1:fix-ci", "head_sha": ""}},
		{Type: "issue", ID: "c", State: "open", Title: "resolve-conflict: x/y#1", Metadata: nil},
	}}
	ix := BuildIndex(v)
	if len(ix.CoveredComments()) != 0 {
		t.Fatal("covered from garbage")
	}
	if len(ix.ByKind(KindFixCI)) != 1 || len(ix.ByKind(KindResolveConflict)) != 1 {
		t.Fatal("items missing")
	}
	// keyless resolve-conflict without metadata matches no context.
	if _, ok := ix.Find(KindResolveConflict, Context{}); ok {
		t.Fatal("keyless item with empty metadata matched an empty context")
	}
}

func TestPackageNeverReadsOrStoresACloser(t *testing.T) {
	banned := []string{"closed_by", "closedby", "closer"}
	check := func(name string) {
		l := strings.ToLower(name)
		for _, b := range banned {
			if strings.Contains(l, b) {
				t.Errorf("exported API name %q mentions a closer", name)
			}
		}
	}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(Item{}), reflect.TypeOf(Adoptable{}), reflect.TypeOf(KindContract{}),
		reflect.TypeOf(EntityRef{}), reflect.TypeOf(Context{}), reflect.TypeOf(&Index{}),
	} {
		for i := 0; i < typ.NumMethod(); i++ {
			check(typ.Method(i).Name)
		}
		if typ.Kind() == reflect.Struct {
			for i := 0; i < typ.NumField(); i++ {
				check(typ.Field(i).Name)
			}
		}
	}
	for _, k := range Kinds() {
		for _, key := range ContractFor(k).MetadataKeys {
			check(key)
		}
	}
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join("testdata", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		s := strings.ToLower(string(b))
		for _, bn := range banned {
			if strings.Contains(s, bn) {
				t.Errorf("fixture %s mentions %q", e.Name(), bn)
			}
		}
	}
}
