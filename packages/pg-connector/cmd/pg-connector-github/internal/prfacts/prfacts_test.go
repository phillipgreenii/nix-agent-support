package prfacts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

var background = ratefeed.Meta{Class: governor.ClassKeepAlive, Purpose: governor.PurposeRefresh}

// fakeGH is the gh the metered Runner runs: it records every call and answers
// from reply.
type fakeGH struct {
	mu    sync.Mutex
	calls [][]string
	reply func(args []string) ([]byte, error)
}

func (g *fakeGH) Run(_ context.Context, args ...string) ([]byte, error) {
	g.mu.Lock()
	g.calls = append(g.calls, args)
	g.mu.Unlock()
	return g.reply(args)
}

func (g *fakeGH) RunStdin(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	return g.Run(ctx, args...)
}

func (g *fakeGH) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

// harness is the metered Provider over a fake gh and a real governor on a
// manual clock, with every booked spend recorded.
type harness struct {
	t      *testing.T
	gh     *fakeGH
	gov    *governor.Governor
	events []governor.SpendEvent
	prov   Reader
}

// Reader is both reads, which *github.Provider satisfies.
type Reader interface {
	FilesReader
	CommitsReader
}

func newHarness(t *testing.T, reply func(args []string) ([]byte, error)) *harness {
	t.Helper()
	h := &harness{t: t, gh: &fakeGH{reply: reply}}
	clk := governor.NewManualClock(t0)
	gov, err := governor.New(ratefeed.GovernorConfig(config.Defaults().Settings), governor.Options{
		Clock:   clk,
		OnSpend: func(e governor.SpendEvent) { h.events = append(h.events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.gov = gov
	h.prov = metered.NewProvider(&metered.Runner{Inner: h.gh, Governor: gov, Feed: ratefeed.New(gov, h.gh, clk)})
	return h
}

func (h *harness) files() *FilesFetcher     { return &FilesFetcher{Provider: h.prov} }
func (h *harness) commits() *CommitsFetcher { return &CommitsFetcher{Provider: h.prov} }

func (h *harness) graphqlSpent() int64 {
	h.t.Helper()
	bs, ok := h.gov.State().Bucket(governor.BucketGraphQL)
	if !ok {
		h.t.Fatal("no graphql bucket")
	}
	return bs.Spent
}

func target(head, base string) groups.Target {
	return groups.Target{
		NodeID: "PR_node_1",
		Ref:    entity.Ref{Owner: "phillipgreenii", Repo: "widgets", Number: 42},
		Stamps: groups.Stamps{Head: head, Base: base},
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// cursorOf returns the paging cursor of a graphql call ("" for the first page).
func cursorOf(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-f" && strings.HasPrefix(args[i+1], "after=") {
			return strings.TrimPrefix(args[i+1], "after=")
		}
	}
	return ""
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// pager serves a connection of total nodes, 100 a page, as GitHub does: the
// cursor of a page is "c<offset of the next page>". node renders node i;
// connection is the field name (files or commits).
func pager(connection string, total int, node func(i int) string) func([]string) ([]byte, error) {
	return func(args []string) ([]byte, error) {
		start := 0
		if c := cursorOf(args); c != "" {
			n, err := strconv.Atoi(strings.TrimPrefix(c, "c"))
			if err != nil {
				return nil, fmt.Errorf("bad cursor %q", c)
			}
			start = n
		}
		end := min(start+100, total)
		nodes := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			nodes = append(nodes, node(i))
		}
		next := fmt.Sprintf("c%d", end)
		return []byte(fmt.Sprintf(`{"data":{"rateLimit":{"cost":1,"remaining":4900,"resetAt":"2026-10-12T10:00:00Z"},`+
			`"repository":{"pullRequest":{%q:{"totalCount":%d,"pageInfo":{"hasNextPage":%t,"endCursor":%q},"nodes":[%s]}}}}}`,
			connection, total, end < total, next, strings.Join(nodes, ","))), nil
	}
}

func fileNode(i int) string {
	return fmt.Sprintf(`{"path":"src/file-%04d.go","additions":%d,"deletions":%d}`, i, i+1, i%7)
}

func commitNode(i int) string {
	return fmt.Sprintf(`{"commit":{"oid":"sha-%04d","messageHeadline":"change %d","authors":{"nodes":[{"user":{"login":"user-%d"}}]}}}`, i, i, i%3)
}

const notResolved = `{"data":{"rateLimit":{"cost":1,"remaining":4900,"resetAt":"2026-10-12T10:00:00Z"},"repository":{"pullRequest":null}}}`

func TestFetchers_Identity(t *testing.T) {
	for _, f := range []groups.Fetcher{&FilesFetcher{}, &CommitsFetcher{}} {
		if f.MaxBatch() != 1 {
			t.Errorf("%s: MaxBatch = %d, want 1", f.Group(), f.MaxBatch())
		}
		req := f.Spend(governor.ClassInteractive, 7)
		if req.Bucket != governor.BucketGraphQL || req.Class != governor.ClassInteractive || req.Cost != 1 {
			t.Errorf("%s: Spend = %+v", f.Group(), req)
		}
	}
	if (&FilesFetcher{}).Group() != groups.Files || (&FilesFetcher{}).Group() != "files" {
		t.Error("FilesFetcher.Group is not files")
	}
	if (&CommitsFetcher{}).Group() != groups.Commits || (&CommitsFetcher{}).Group() != "commits" {
		t.Error("CommitsFetcher.Group is not commits")
	}
}

func TestFilesFetch_PagesAndKeepsOrder(t *testing.T) {
	h := newHarness(t, pager("files", 250, fileNode))
	out := h.files().Fetch(context.Background(), background, []groups.Target{target("h1", "b1")})
	if len(out) != 1 || out[0].Err != nil {
		t.Fatalf("outcomes = %+v", out)
	}
	got, err := DecodeFiles(out[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 250 {
		t.Fatalf("%d files, want 250", len(got.Files))
	}
	for i, f := range got.Files {
		if f.Path != fmt.Sprintf("src/file-%04d.go", i) || f.Additions != i+1 || f.Deletions != i%7 {
			t.Fatalf("file %d = %+v", i, f)
		}
	}
	if h.gh.count() != 3 {
		t.Errorf("%d gh calls, want 3 pages", h.gh.count())
	}
	// The call names the PR it reads.
	first := h.gh.calls[0]
	for _, want := range []string{"owner=phillipgreenii", "name=widgets", "number=42"} {
		if !hasArg(first, want) {
			t.Errorf("first call %q lacks %s", strings.Join(first, " "), want)
		}
	}
}

func TestFilesFetch_EachPageIsAdmittedAndBooked(t *testing.T) {
	h := newHarness(t, pager("files", 250, fileNode))
	h.files().Fetch(context.Background(), background, []groups.Target{target("h1", "b1")})
	if len(h.events) != h.gh.count() || len(h.events) != 3 {
		t.Fatalf("%d booked spends for %d gh calls, want 3 and 3", len(h.events), h.gh.count())
	}
	for i, e := range h.events {
		if e.Bucket != governor.BucketGraphQL || e.Class != governor.ClassKeepAlive || e.Purpose != governor.PurposeRefresh ||
			e.Group != "files" || e.BatchSize != 1 || e.Cost != 1 {
			t.Errorf("spend %d = %+v", i, e)
		}
	}
	if got := h.graphqlSpent(); got != 3 {
		t.Errorf("graphql spent = %d, want 3", got)
	}
}

func TestFilesFetch_NoFilesStoresAnEmptyList(t *testing.T) {
	h := newHarness(t, pager("files", 0, fileNode))
	out := h.files().Fetch(context.Background(), background, []groups.Target{target("h1", "b1")})
	if out[0].Err != nil {
		t.Fatal(out[0].Err)
	}
	if string(out[0].Content) != `{"files":[]}` {
		t.Errorf("content = %s, want {\"files\":[]}", out[0].Content)
	}
	got, err := DecodeFiles(out[0].Content)
	if err != nil || got.Files == nil || len(got.Files) != 0 {
		t.Errorf("DecodeFiles = %+v, %v; want a non-nil empty list", got, err)
	}
}

func TestFilesFetch_StopsAtTheCap(t *testing.T) {
	h := newHarness(t, pager("files", 3200, fileNode))
	out := h.files().Fetch(context.Background(), background, []groups.Target{target("h1", "b1")})
	if out[0].Err != nil {
		t.Fatal(out[0].Err)
	}
	got, _ := DecodeFiles(out[0].Content)
	if len(got.Files) != 3000 {
		t.Errorf("%d files, want the cap of 3000", len(got.Files))
	}
	if h.gh.count() != 30 {
		t.Errorf("%d pages read, want 30 (3000 / 100) and no more", h.gh.count())
	}
}

func TestCommitsFetch_ConvertsAsTheOldCommitsDid(t *testing.T) {
	h := newHarness(t, func([]string) ([]byte, error) { return fixture(t, "commits_mixed.json"), nil })
	out := h.commits().Fetch(context.Background(), background, []groups.Target{target("h1", "b1")})
	if out[0].Err != nil {
		t.Fatal(out[0].Err)
	}
	got, err := DecodeCommits(out[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	type c struct{ sha, author, msg string }
	want := []c{
		{"1111111111111111111111111111111111111111", "phillipgreenii", "Add the widget"},
		{"2222222222222222222222222222222222222222", "", "Fix the widget with no linked account"},
		{"3333333333333333333333333333333333333333", "teammate", "Second author is not the commit's author"},
	}
	if len(got.Commits) != len(want) {
		t.Fatalf("commits = %+v", got.Commits)
	}
	for i, w := range want {
		g := got.Commits[i]
		if g.SHA != w.sha || g.Author != w.author || g.Message != w.msg {
			t.Errorf("commit %d = %+v, want %+v", i, g, w)
		}
	}
	// The stored bytes are the typed content's JSON, so the wire fields are exact.
	if !strings.HasPrefix(string(out[0].Content), `{"commits":[{"sha":"1111`) || !strings.Contains(string(out[0].Content), `"author":""`) {
		t.Errorf("content = %s", out[0].Content)
	}
}

func TestCommitsFetch_PagesBooksAndStopsAtTheCap(t *testing.T) {
	h := newHarness(t, pager("commits", 250, commitNode))
	out := h.commits().Fetch(context.Background(), background, []groups.Target{target("h1", "b1")})
	got, err := DecodeCommits(out[0].Content)
	if err != nil || len(got.Commits) != 250 || got.Commits[249].SHA != "sha-0249" || got.Commits[7].Author != "user-1" {
		t.Fatalf("commits = %d (%v)", len(got.Commits), err)
	}
	if h.gh.count() != 3 || len(h.events) != 3 {
		t.Errorf("%d calls and %d booked spends, want 3 and 3", h.gh.count(), len(h.events))
	}
	for _, e := range h.events {
		if e.Group != "commits" {
			t.Errorf("spend booked for group %q, want commits", e.Group)
		}
	}

	h = newHarness(t, pager("commits", 1200, commitNode))
	out = h.commits().Fetch(context.Background(), background, []groups.Target{target("h1", "b1")})
	got, _ = DecodeCommits(out[0].Content)
	if out[0].Err != nil || len(got.Commits) != 1000 || h.gh.count() != 10 {
		t.Errorf("capped read: %d commits in %d pages (err %v), want 1000 in 10", len(got.Commits), h.gh.count(), out[0].Err)
	}
}

func TestCommitsFetch_NoCommitsStoresAnEmptyList(t *testing.T) {
	h := newHarness(t, pager("commits", 0, commitNode))
	out := h.commits().Fetch(context.Background(), background, []groups.Target{target("h1", "b1")})
	if string(out[0].Content) != `{"commits":[]}` {
		t.Errorf("content = %s", out[0].Content)
	}
}

func TestFetch_OutcomeCarriesTheStampsFromBeforeTheCall(t *testing.T) {
	h := newHarness(t, pager("files", 5, fileNode))
	tg := target("head-old", "base-old")
	for _, f := range []groups.Fetcher{h.files(), h.commits()} {
		h.gh.reply = pager(f.Group(), 5, map[string]func(int) string{"files": fileNode, "commits": commitNode}[f.Group()])
		out := f.Fetch(context.Background(), background, []groups.Target{tg})
		if len(out) != 1 || out[0].Stamps != (groups.Stamps{Head: "head-old", Base: "base-old"}) {
			t.Errorf("%s: outcome = %+v", f.Group(), out)
		}
		if !out[0].FreshUntil.IsZero() {
			t.Errorf("%s: FreshUntil = %v, want zero (the engine schedules)", f.Group(), out[0].FreshUntil)
		}
	}
}

// A stored row is fresh only while its stamps equal the summary's, with no time
// bound for an open PR, and for terminal_ttl for a merged one.
func TestFetch_StampedFreshnessThroughTheRules(t *testing.T) {
	rules := groups.NewRules(config.Defaults().Settings)
	h := newHarness(t, pager("files", 3, fileNode))
	for _, group := range []string{groups.Files, groups.Commits} {
		var f groups.Fetcher = h.files()
		h.gh.reply = pager("files", 3, fileNode)
		if group == groups.Commits {
			f = h.commits()
			h.gh.reply = pager("commits", 3, commitNode)
		}
		for _, terminal := range []bool{false, true} {
			tg := target("head-1", "base-1")
			tg.Terminal = terminal
			o := f.Fetch(context.Background(), background, []groups.Target{tg})[0]
			if o.Err != nil {
				t.Fatal(o.Err)
			}
			fetchedAt := t0
			freshUntil, maxAgeAt := rules.Schedule(group, terminal, fetchedAt)
			row := store.GroupRow{
				Group: group, Content: o.Content, Fingerprint: o.Fingerprint, FetchedAt: fetchedAt,
				FreshUntil: freshUntil, MaxAgeAt: maxAgeAt, FetchedForHead: o.Stamps.Head, FetchedForBase: o.Stamps.Base,
			}
			name := fmt.Sprintf("%s terminal=%v", group, terminal)
			if terminal {
				if want := fetchedAt.Add(config.Defaults().Settings.TerminalTTL.Std()); !freshUntil.Equal(want) {
					t.Errorf("%s: fresh_until = %v, want %v", name, freshUntil, want)
				}
			} else if !freshUntil.IsZero() {
				t.Errorf("%s: fresh_until = %v, want zero for an open PR", name, freshUntil)
			}
			cur := groups.Stamps{Head: "head-1", Base: "base-1"}
			soon, much := t0.Add(time.Minute), t0.Add(1000*time.Hour)
			if !rules.Fresh(row, cur, soon).IsFresh() {
				t.Errorf("%s: not fresh with matching stamps", name)
			}
			if !terminal && !rules.Fresh(row, cur, much).IsFresh() {
				t.Errorf("%s: an open PR's row went stale by time", name)
			}
			if terminal && rules.Fresh(row, cur, much).IsFresh() {
				t.Errorf("%s: a terminal PR's row stayed fresh past terminal_ttl", name)
			}
			if rules.Fresh(row, groups.Stamps{Head: "head-2", Base: "base-1"}, soon).IsFresh() {
				t.Errorf("%s: fresh after the head moved", name)
			}
			if rules.Fresh(row, groups.Stamps{Head: "head-1", Base: "base-2"}, soon).IsFresh() {
				t.Errorf("%s: fresh after the base moved", name)
			}
		}
	}
}

func TestFetch_FingerprintIsTheHashOfTheStoredBytes(t *testing.T) {
	h := newHarness(t, pager("files", 5, fileNode))
	a := h.files().Fetch(context.Background(), background, []groups.Target{target("h", "b")})[0]
	b := h.files().Fetch(context.Background(), background, []groups.Target{target("h", "b")})[0]
	if a.Fingerprint != groups.Fingerprint(a.Content) || a.Fingerprint != b.Fingerprint {
		t.Errorf("fingerprints %q %q for the same list", a.Fingerprint, b.Fingerprint)
	}
	h.gh.reply = pager("files", 6, fileNode)
	c := h.files().Fetch(context.Background(), background, []groups.Target{target("h", "b")})[0]
	if c.Fingerprint == a.Fingerprint {
		t.Error("a different list kept the fingerprint")
	}
}

func TestFetch_NotFoundIsGone(t *testing.T) {
	for name, f := range map[string]func(h *harness) groups.Fetcher{
		"files":   func(h *harness) groups.Fetcher { return h.files() },
		"commits": func(h *harness) groups.Fetcher { return h.commits() },
	} {
		// A null pullRequest, and a REST-style 404 from gh, are both "gone".
		for answer, reply := range map[string]func([]string) ([]byte, error){
			"null pull request": func([]string) ([]byte, error) { return []byte(notResolved), nil },
			"gh could not resolve": func([]string) ([]byte, error) {
				return nil, errors.New("gh: Could not resolve to a PullRequest with the number of 42. (repository.pullRequest)")
			},
		} {
			h := newHarness(t, reply)
			o := f(h).Fetch(context.Background(), background, []groups.Target{target("h", "b")})[0]
			if !errors.Is(o.Err, groups.ErrGone) || o.Content != nil {
				t.Errorf("%s/%s: outcome = %+v, want ErrGone and no content", name, answer, o)
			}
		}
	}
}

func TestFetch_MidPageFailureReturnsTheErrorAndNoPartialContent(t *testing.T) {
	page := pager("files", 250, fileNode)
	h := newHarness(t, func(args []string) ([]byte, error) {
		if cursorOf(args) == "c100" {
			return nil, errors.New("gh: HTTP 422: validation failed")
		}
		return page(args)
	})
	o := h.files().Fetch(context.Background(), background, []groups.Target{target("h", "b")})[0]
	if o.Err == nil || !strings.Contains(o.Err.Error(), "HTTP 422") || errors.Is(o.Err, groups.ErrGone) {
		t.Errorf("err = %v, want the page's own error", o.Err)
	}
	if o.Content != nil || o.Fingerprint != "" {
		t.Errorf("a failed read carried content %q / %q", o.Content, o.Fingerprint)
	}
	if h.gh.count() != 2 {
		t.Errorf("%d pages read, want 2 (the failing page ends the read)", h.gh.count())
	}
	if o.Stamps != (groups.Stamps{Head: "h", Base: "b"}) {
		t.Errorf("stamps = %+v", o.Stamps)
	}
}

func TestFetch_GovernorRefusalIsReturnedUntouched(t *testing.T) {
	h := newHarness(t, pager("commits", 250, commitNode))
	// Room for exactly the first page of background spend.
	bs, _ := h.gov.State().Bucket(governor.BucketGraphQL)
	tk, err := h.gov.Admit(governor.Request{Bucket: governor.BucketGraphQL, Class: governor.ClassQuery, Cost: bs.BackgroundCap - 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := tk.Settle(governor.Spend{Purpose: governor.PurposeInteractive, Cost: bs.BackgroundCap - 1, Outcome: ratefeed.OutcomeOK}); err != nil {
		t.Fatal(err)
	}
	o := h.commits().Fetch(context.Background(), background, []groups.Target{target("h", "b")})[0]
	var refusal *governor.Refusal
	if !errors.As(o.Err, &refusal) || refusal.Kind != governor.RefusedBackgroundShare {
		t.Fatalf("err = %v, want the background-share refusal of the second page", o.Err)
	}
	if o.Content != nil {
		t.Errorf("a refused read carried content %q", o.Content)
	}
	if h.gh.count() != 1 {
		t.Errorf("gh ran %d times, want only the admitted page", h.gh.count())
	}
	if errors.Is(o.Err, groups.ErrGone) {
		t.Error("a refusal was reported as gone")
	}
}

func TestFetch_OneOutcomePerTargetInOrder(t *testing.T) {
	h := newHarness(t, func(args []string) ([]byte, error) {
		return pager("files", 2, fileNode)(args)
	})
	a, b := target("h-a", "b-a"), target("h-b", "b-b")
	b.Ref.Number = 43
	out := h.files().Fetch(context.Background(), background, []groups.Target{a, b})
	if len(out) != 2 || out[0].Stamps != a.Stamps || out[1].Stamps != b.Stamps {
		t.Fatalf("outcomes = %+v", out)
	}
	if !hasArg(h.gh.calls[0], "number=42") || !hasArg(h.gh.calls[1], "number=43") {
		t.Errorf("calls = %v", h.gh.calls)
	}
}

func TestDecode_RejectsNoContentAndGarbage(t *testing.T) {
	for name, in := range map[string][]byte{"nil": nil, "empty": {}, "garbage": []byte("nope")} {
		if _, err := DecodeFiles(in); err == nil {
			t.Errorf("DecodeFiles %s: no error", name)
		}
		if _, err := DecodeCommits(in); err == nil {
			t.Errorf("DecodeCommits %s: no error", name)
		}
	}
}

func TestDecode_NullListBecomesEmpty(t *testing.T) {
	f, err := DecodeFiles([]byte(`{"files":null}`))
	if err != nil || f.Files == nil {
		t.Errorf("DecodeFiles = %+v, %v", f, err)
	}
	c, err := DecodeCommits([]byte(`{}`))
	if err != nil || c.Commits == nil {
		t.Errorf("DecodeCommits = %+v, %v", c, err)
	}
}
