package unstick

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func init() {
	// Shared golden-update flag; defined once per test binary.
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite golden files in testdata/")
	}
}

func updateGolden() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

// checkGolden compares got with testdata/<name>, rewriting it under -update.
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if updateGolden() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update): %v", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("golden %s differs (run with -update to refresh)\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}

// row builders ------------------------------------------------------------

func crow(id string) Row { return Row{ID: id, Title: "t " + id, Status: StatusOpen} }

func blocks(r Row, on ...string) Row {
	for _, o := range on {
		r.Dependencies = append(r.Dependencies, Dep{IssueID: r.ID, DependsOnID: o, Type: DepBlocks})
	}
	return r
}

func childOf(r Row, parent string) Row {
	r.Dependencies = append(r.Dependencies, Dep{IssueID: r.ID, DependsOnID: parent, Type: DepParentChild})
	return r
}

func clusterOf(review []string, rows ...Row) [][]string {
	return Cluster(review, NewGraph(rows))
}

func sameBatch(batches [][]string, a, b string) bool {
	for _, bt := range batches {
		ha, hb := false, false
		for _, id := range bt {
			ha = ha || id == a
			hb = hb || id == b
		}
		if ha || hb {
			return ha && hb
		}
	}
	return false
}

// To observe LINKING (not packing), force the "pack" path with >= 10 beads:
// the pad beads are unlinked singletons with titles sorting after the
// subjects, so subjects share a batch only through links or lucky packing.
// Instead we test links directly via buildLinks.
func linked(review []string, rows ...Row) func(a, b string) bool {
	g := NewGraph(rows)
	l := buildLinks(review, g)
	comp := map[string]int{}
	for i, c := range l.components() {
		for _, id := range c {
			comp[id] = i
		}
	}
	return func(a, b string) bool { return comp[a] == comp[b] }
}

func TestLinkBlocksEdge(t *testing.T) {
	same := linked([]string{"a", "b", "c"}, blocks(crow("a"), "b"), crow("b"), crow("c"))
	if !same("a", "b") || same("a", "c") {
		t.Error("blocks edge between REVIEW beads must link exactly a-b")
	}
}

func TestLinkParentChild(t *testing.T) {
	same := linked([]string{"p", "k", "z"}, crow("p"), childOf(crow("k"), "p"), crow("z"))
	if !same("p", "k") || same("p", "z") {
		t.Error("parent-child must link")
	}
}

func TestLinkOpenOutsideHubLinksBothDirections(t *testing.T) {
	// r1 and r2 are both blocked by open outside hub h; r3 blocks h (other
	// direction); r4 is unrelated.
	rows := []Row{
		blocks(crow("r1"), "h"), blocks(crow("r2"), "h"), crow("r4"),
		blocks(crow("h"), "r3"), crow("r3"),
	}
	same := linked([]string{"r1", "r2", "r3", "r4"}, rows...)
	if !same("r1", "r2") || !same("r1", "r3") {
		t.Error("open outside hub must join REVIEW beads touching it in either direction")
	}
	if same("r1", "r4") {
		t.Error("unrelated bead must stay separate")
	}
}

func TestLinkOutsideHubViaParent(t *testing.T) {
	rows := []Row{childOf(crow("r1"), "h"), childOf(crow("r2"), "h"), crow("h")}
	if !linked([]string{"r1", "r2"}, rows...)("r1", "r2") {
		t.Error("shared open outside parent must link siblings")
	}
}

func TestLinkClosedOutsideHubNotLinked(t *testing.T) {
	h := crow("h")
	h.Status = StatusClosed
	rows := []Row{blocks(crow("r1"), "h"), blocks(crow("r2"), "h"), h}
	if linked([]string{"r1", "r2"}, rows...)("r1", "r2") {
		t.Error("closed outside bead must never be a hub")
	}
}

func TestLinkHubIsOneHopOnly(t *testing.T) {
	// r1 -> h1 -> h2 <- r2 : two outside hops must not link.
	rows := []Row{blocks(crow("r1"), "h1"), blocks(crow("h1"), "h2"), crow("h2"), blocks(crow("r2"), "h2")}
	// r2 touches h2 only, r1 touches h1 only.
	if linked([]string{"r1", "r2"}, rows...)("r1", "r2") {
		t.Error("two-hop outside path must not link")
	}
}

func TestLinkIDMentionKnownIDs(t *testing.T) {
	a := crow("tc-mol-4prt")
	b := crow("tc-o14i5.3.7")
	b.Description = "see tc-mol-4prt for context"
	c := crow("tc-o14i5.3")
	c.Notes = "related: tc-o14i5.3.7."
	d := crow("tc-o14i5")
	d.Title = "unrelated mentions tc-o14i5x only"
	same := linked([]string{a.ID, b.ID, c.ID, d.ID}, a, b, c, d)
	if !same(a.ID, b.ID) {
		t.Error("multi-hyphen id mention must link")
	}
	if !same(b.ID, c.ID) {
		t.Error("dotted id mention (trailing full stop) must link")
	}
	if same(a.ID, d.ID) || same(c.ID, d.ID) {
		t.Error("prefix of a longer token must not link")
	}
}

func TestMentionsIDBoundaries(t *testing.T) {
	cases := []struct {
		text, id string
		want     bool
	}{
		{"tc-1", "tc-1", true},
		{"see tc-1.", "tc-1", true},
		{"(tc-1)", "tc-1", true},
		{"tc-10", "tc-1", false},
		{"xtc-1", "tc-1", false},
		{"tc-1.2", "tc-1", false},
		{"tc-1.2", "tc-1.2", true},
		{"tc-1x tc-1", "tc-1", true},
		{"", "tc-1", false},
		{"tc-1", "", false},
	}
	for _, c := range cases {
		if got := MentionsID(c.text, c.id); got != c.want {
			t.Errorf("MentionsID(%q,%q)=%v want %v", c.text, c.id, got, c.want)
		}
	}
}

func TestLinkIdenticalDeferUntil(t *testing.T) {
	a, b, c, d, e := crow("a"), crow("b"), crow("c"), crow("d"), crow("e")
	a.DeferUntil, b.DeferUntil = "2026-11-01T00:00:00Z", "2026-11-01T00:00:00Z"
	c.DeferUntil = "2026-11-02T00:00:00Z"
	same := linked([]string{"a", "b", "c", "d", "e"}, a, b, c, d, e) // d,e: empty defer_until
	if !same("a", "b") || same("a", "c") || same("d", "e") {
		t.Error("only identical NON-EMPTY defer_until links")
	}
}

func TestLinkLabelsNeverLink(t *testing.T) {
	a, b := crow("a"), crow("b")
	a.Labels, b.Labels = []string{"infra", "x"}, []string{"infra", "x"}
	if linked([]string{"a", "b"}, a, b)("a", "b") {
		t.Error("labels must never link")
	}
}

func TestLinkNonReviewEdgeIgnored(t *testing.T) {
	// edge to a closed bead and to an id absent from the export: no panic, no link.
	c := crow("c")
	c.Status = StatusClosed
	rows := []Row{blocks(crow("a"), "c", "ghost"), blocks(crow("b"), "c", "ghost"), c}
	if linked([]string{"a", "b"}, rows...)("a", "b") {
		t.Error("closed or absent shared neighbour must not link")
	}
}

// packing -------------------------------------------------------------------

func singles(n int) ([]string, []Row) {
	ids := make([]string, n)
	rows := make([]Row, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("s-%03d", i)
		rows[i] = crow(ids[i])
	}
	return ids, rows
}

func checkPacking(t *testing.T, review []string, batches [][]string) {
	t.Helper()
	n := len(review)
	seen := map[string]int{}
	for _, b := range batches {
		if len(b) == 0 {
			t.Errorf("empty batch in %v", batches)
		}
		if !sort.StringsAreSorted(b) {
			t.Errorf("batch not sorted: %v", b)
		}
		for _, id := range b {
			seen[id]++
		}
		if len(b) > MaxBatch {
			t.Errorf("n=%d: batch of %d exceeds %d", n, len(b), MaxBatch)
		}
		if n >= 20 && len(b) < MinBatch {
			t.Errorf("n=%d: batch of %d below %d", n, len(b), MinBatch)
		}
	}
	if len(seen) != n {
		t.Errorf("n=%d: %d distinct ids batched", n, len(seen))
	}
	for _, id := range review {
		if seen[id] != 1 {
			t.Errorf("id %s in %d batches", id, seen[id])
		}
	}
	if n < SingleBatchBelow && n > 0 && len(batches) != 1 {
		t.Errorf("n=%d must be one batch, got %d", n, len(batches))
	}
	if n == 0 && batches != nil {
		t.Errorf("n=0 must yield no batches")
	}
}

func TestPackingSizes(t *testing.T) {
	for _, n := range []int{0, 1, 9, 10, 15, 16, 19, 20, 31, 106} {
		ids, rows := singles(n)
		got := Cluster(ids, NewGraph(rows))
		checkPacking(t, ids, got)
		if n == 10 || n == 15 {
			if len(got) != 1 {
				t.Errorf("n=%d: want 1 batch, got %d", n, len(got))
			}
		}
		if n == 16 {
			if len(got) != 2 || len(got[0]) != 8 || len(got[1]) != 8 {
				t.Errorf("n=16 cannot satisfy 10..15 twice; want even 8+8, got %v", sizes(got))
			}
		}
		if n == 106 && len(got) != 8 {
			t.Errorf("n=106: want ceil(106/15)=8 batches, got %d (%v)", len(got), sizes(got))
		}
	}
}

func sizes(b [][]string) []int {
	out := make([]int, len(b))
	for i := range b {
		out[i] = len(b[i])
	}
	return out
}

func TestPackingKeepsComponentsWhole(t *testing.T) {
	// Two chains of 8 plus 6 singletons: n=22, K=2 -> 8+8 comps placed whole,
	// singletons water-fill to 11/11.
	var rows []Row
	var ids []string
	for _, p := range []string{"a", "b"} {
		for i := 0; i < 8; i++ {
			r := crow(fmt.Sprintf("%s%d", p, i))
			if i > 0 {
				r = blocks(r, fmt.Sprintf("%s%d", p, i-1))
			}
			rows = append(rows, r)
			ids = append(ids, r.ID)
		}
	}
	for i := 0; i < 6; i++ {
		r := crow(fmt.Sprintf("z%d", i))
		rows = append(rows, r)
		ids = append(ids, r.ID)
	}
	got := Cluster(ids, NewGraph(rows))
	checkPacking(t, ids, got)
	if !sameBatch(got, "a0", "a7") || !sameBatch(got, "b0", "b7") || sameBatch(got, "a0", "b0") {
		t.Errorf("components must stay whole and apart here: %v", got)
	}
}

func TestPackingOversizeComponentBFSSplit(t *testing.T) {
	// Star: root "c00" blocked-by nothing; c01..c19 each blocked by c00 -> BFS
	// order from lowest id is c00 then neighbours in id order. 20 members.
	var rows []Row
	var ids []string
	rows = append(rows, crow("c00"))
	ids = append(ids, "c00")
	for i := 1; i < 20; i++ {
		id := fmt.Sprintf("c%02d", i)
		rows = append(rows, blocks(crow(id), "c00"))
		ids = append(ids, id)
	}
	// plus a long path hanging off c19 so BFS order differs from id order:
	// 20 star members + nothing else. First chunk = first 15 in BFS order.
	got := Cluster(ids, NewGraph(rows))
	checkPacking(t, ids, got)
	if len(got) != 2 || len(got[0])+len(got[1]) != 20 {
		t.Fatalf("want 2 batches of 20 total, got %v", sizes(got))
	}
}

func TestPackingBFSChunkOrder(t *testing.T) {
	// Path graph with ids deliberately NOT in path order: BFS from the lowest
	// id walks the path, so the first chunk must be the path prefix, not the
	// 15 lowest ids.
	const n = 17
	order := make([]string, n) // path order
	for i := range order {
		order[i] = fmt.Sprintf("n%02d", (i*7)%n) // permutation: gcd(7,17)=1
	}
	var rows []Row
	for i, id := range order {
		r := crow(id)
		if i > 0 {
			r = blocks(r, order[i-1])
		}
		rows = append(rows, r)
	}
	g := NewGraph(rows)
	l := buildLinks(order, g)
	comp := l.components()
	if len(comp) != 1 || len(comp[0]) != n {
		t.Fatalf("want one component of %d, got %v", n, comp)
	}
	bfs := l.bfsOrder(comp[0])
	low := comp[0][0]
	if bfs[0] != low {
		t.Fatalf("BFS must start at lowest id %s, got %s", low, bfs[0])
	}
	// On a path, BFS order from position p visits outward: distance sequence
	// must be non-decreasing.
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	prev := 0
	for _, id := range bfs {
		d := pos[id] - pos[low]
		if d < 0 {
			d = -d
		}
		if d < prev {
			t.Fatalf("BFS distance went backwards at %s: %v", id, bfs)
		}
		prev = d
	}
	// Packing the oversize component: 17 total -> K=2, chunks 15+2 -> repaired
	// to 9+8, all members present.
	got := Cluster(order, g)
	checkPacking(t, order, got)
}

func TestPackingShortTailRebalance(t *testing.T) {
	// A 15-member component plus 5 singletons: K=2 -> 15 + 5 -> rebalanced
	// (component split) to 10 + 10.
	var rows []Row
	var ids []string
	for i := 0; i < 15; i++ {
		r := crow(fmt.Sprintf("k%02d", i))
		if i > 0 {
			r = blocks(r, fmt.Sprintf("k%02d", i-1))
		}
		rows = append(rows, r)
		ids = append(ids, r.ID)
	}
	for i := 0; i < 5; i++ {
		r := crow(fmt.Sprintf("s%d", i))
		rows = append(rows, r)
		ids = append(ids, r.ID)
	}
	got := Cluster(ids, NewGraph(rows))
	checkPacking(t, ids, got)
	if s := sizes(got); !reflect.DeepEqual(s, []int{10, 10}) {
		t.Errorf("want 10+10 after rebalance, got %v", s)
	}
}

func TestPackingShortBatchMerged(t *testing.T) {
	// Four components of 9: K=3 and none fits a fourth into 6 spare, so a
	// fourth batch opens (9) and is merged away, giving 12/12/12 w/o splits
	// needed beyond the dissolved batch.
	var rows []Row
	var ids []string
	for _, p := range []string{"a", "b", "c", "d"} {
		for i := 0; i < 9; i++ {
			r := crow(fmt.Sprintf("%s%d", p, i))
			if i > 0 {
				r = blocks(r, fmt.Sprintf("%s%d", p, i-1))
			}
			rows = append(rows, r)
			ids = append(ids, r.ID)
		}
	}
	got := Cluster(ids, NewGraph(rows))
	checkPacking(t, ids, got)
	if len(got) != 3 {
		t.Errorf("want 3 batches, got %v", sizes(got))
	}
}

func TestSingletonOrderLabelThenTitleByteWiseNoMutation(t *testing.T) {
	var rows []Row
	var ids []string
	mk := func(id, title string, labels ...string) {
		r := Row{ID: id, Title: title, Status: StatusOpen, Labels: labels}
		rows = append(rows, r)
		ids = append(ids, id)
	}
	mk("x01", "Zeta", "b", "a") // sorted copy first label "a"
	mk("x02", "alpha", "a")     // "a", title lower-case sorts after "Zeta" bytewise
	mk("x03", "beta", "c")
	mk("x04", "beta")  // no labels sorts first
	mk("x05", "Ärger") // non-ASCII: bytewise after ASCII
	for i := 6; i <= 12; i++ {
		mk(fmt.Sprintf("x%02d", i), "zz", "zzz")
	}
	g := NewGraph(rows)
	before := append([]string(nil), rows[0].Labels...)
	order := []string{"x01", "x02", "x03", "x04", "x05"}
	sortSingletons(order, g)
	// no label first ("beta" < "Ärger" bytewise: 0xC3 > 'b'), then label a
	// ("Zeta" < "alpha" bytewise), then label c.
	if want := []string{"x04", "x05", "x01", "x02", "x03"}; !reflect.DeepEqual(order, want) {
		t.Errorf("singleton order %v want %v", order, want)
	}
	if !reflect.DeepEqual(rows[0].Labels, before) || rows[0].Labels[0] != "b" {
		t.Error("input labels must not be mutated")
	}
	checkPacking(t, ids, Cluster(ids, g))
}

func TestClusterDoesNotMutateInput(t *testing.T) {
	ids, rows := singles(25)
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
	cp := append([]string(nil), ids...)
	Cluster(ids, NewGraph(rows))
	if !reflect.DeepEqual(ids, cp) {
		t.Error("review slice mutated")
	}
}

func TestClusterDuplicateReviewIDsCollapsed(t *testing.T) {
	got := clusterOf([]string{"a", "a", "b"}, crow("a"), crow("b"))
	if !reflect.DeepEqual(got, [][]string{{"a", "b"}}) {
		t.Errorf("got %v", got)
	}
}

func randomWorld(seed int64) ([]string, []Row) {
	rng := rand.New(rand.NewSource(seed))
	n := 1 + rng.Intn(120)
	m := rng.Intn(n + 1) // outside beads
	var rows []Row
	var review []string
	all := make([]string, 0, n+m)
	for i := 0; i < n+m; i++ {
		all = append(all, fmt.Sprintf("p-%03d", i))
	}
	labels := []string{"a", "b", "c"}
	for i, id := range all {
		r := crow(id)
		if i >= n && rng.Intn(3) == 0 {
			r.Status = StatusClosed
		}
		if rng.Intn(4) == 0 {
			r.Labels = []string{labels[rng.Intn(3)]}
		}
		if rng.Intn(10) == 0 {
			r.DeferUntil = fmt.Sprintf("2026-11-%02dT00:00:00Z", 1+rng.Intn(3))
		}
		for e := rng.Intn(3); e > 0; e-- {
			o := all[rng.Intn(len(all))]
			if o == id {
				continue
			}
			typ := DepBlocks
			if rng.Intn(4) == 0 {
				typ = DepParentChild
			}
			r.Dependencies = append(r.Dependencies, Dep{IssueID: id, DependsOnID: o, Type: typ})
		}
		if rng.Intn(15) == 0 {
			r.Description = "mentions " + all[rng.Intn(len(all))]
		}
		rows = append(rows, r)
		if i < n {
			review = append(review, id)
		}
	}
	rng.Shuffle(len(review), func(i, j int) { review[i], review[j] = review[j], review[i] })
	return review, rows
}

func TestClusterProperty(t *testing.T) {
	for seed := int64(1); seed <= 200; seed++ {
		review, rows := randomWorld(seed)
		g := NewGraph(rows)
		got := Cluster(review, g)
		checkPacking(t, review, got)
		again := Cluster(review, NewGraph(rows))
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("seed %d: nondeterministic", seed)
		}
		// Input order must not matter.
		rev := append([]string(nil), review...)
		sort.Strings(rev)
		if !reflect.DeepEqual(got, Cluster(rev, g)) {
			t.Fatalf("seed %d: depends on input order", seed)
		}
	}
}

func TestClusterGolden(t *testing.T) {
	// 31 beads: two linked groups (a chain via blocks, a parent with children
	// plus an open-hub pair), mentions, defer clusters, labelled singletons.
	var rows []Row
	var ids []string
	add := func(r Row) { rows = append(rows, r); ids = append(ids, r.ID) }
	for i := 0; i < 6; i++ {
		r := crow(fmt.Sprintf("g-chain-%d", i))
		if i > 0 {
			r = blocks(r, fmt.Sprintf("g-chain-%d", i-1))
		}
		add(r)
	}
	add(crow("g-parent"))
	for i := 0; i < 4; i++ {
		add(childOf(crow(fmt.Sprintf("g-kid-%d", i)), "g-parent"))
	}
	rows = append(rows, crow("g-hub")) // open outside bead
	add(blocks(crow("g-h1"), "g-hub"))
	add(blocks(crow("g-h2"), "g-hub"))
	m := crow("g-mention")
	m.Description = "follow-up of g-chain-0."
	add(m)
	for i := 0; i < 2; i++ {
		r := crow(fmt.Sprintf("g-def-%d", i))
		r.DeferUntil = "2026-12-01T00:00:00Z"
		add(r)
	}
	for i := 0; i < 12; i++ {
		r := crow(fmt.Sprintf("g-solo-%02d", i))
		r.Labels = []string{[]string{"net", "app", "ops"}[i%3]}
		r.Title = fmt.Sprintf("solo %d", (i*5)%12)
		add(r)
	}
	g := NewGraph(rows)
	batches := Cluster(ids, g)
	checkPacking(t, ids, batches)
	var sb strings.Builder
	for i, b := range batches {
		fmt.Fprintf(&sb, "%s (%d): %s\n", BatchName(i), len(b), strings.Join(b, " "))
	}
	checkGolden(t, "cluster/batches.golden", []byte(sb.String()))
}

func TestBatchName(t *testing.T) {
	if BatchName(0) != "B01" || BatchName(8) != "B09" || BatchName(11) != "B12" {
		t.Error("names")
	}
}
