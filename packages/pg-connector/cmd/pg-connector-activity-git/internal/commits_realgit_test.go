package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

const (
	meEmail    = "me@example.test"
	otherEmail = "other@example.test"
)

// gitFixture builds real repositories from x/gittest (hermetic by
// construction: fixture root under t.TempDir(), fixture HOME, an allowlisted
// child environment that never inherits GIT_DIR/GIT_INDEX_FILE, discovery
// ceiling at the fixture root, hooks redirected to an empty directory). The
// methods keep the path-keyed shape the tests were written against; every
// path they accept is a repository this fixture created.
type gitFixture struct {
	t     *testing.T
	repos map[string]*gitfixture.Repo // by Dir
}

// newGitFixture pins this process's HOME to an empty directory (the code
// under test builds its git child environment from PATH and HOME only, so a
// pinned empty HOME is what keeps it off the developer's real git config).
// The fixture's own git commands never read this process's environment.
func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return &gitFixture{t: t, repos: map[string]*gitfixture.Repo{}}
}

// track registers repo, makes meEmail its configured author (the tests
// filter on it; commit overrides it per commit), and returns its resolved
// path.
func (f *gitFixture) track(repo *gitfixture.Repo) string {
	f.t.Helper()
	if _, err := repo.Client.Run(context.Background(), "config", "user.email", meEmail); err != nil {
		f.t.Fatalf("configure author of %s: %v", repo.Dir, err)
	}
	f.repos[repo.Dir] = repo
	return repo.Dir
}

func (f *gitFixture) repo(dir string) *gitfixture.Repo {
	f.t.Helper()
	repo, ok := f.repos[dir]
	if !ok {
		f.t.Fatalf("%s is not a repository created by this fixture", dir)
	}
	return repo
}

// git runs `git args...` in the fixture repository at dir. extraEnv
// (KEY=value pairs, e.g. pinned dates or an author) applies to this one
// invocation, on top of the library's hermetic environment.
func (f *gitFixture) git(dir string, extraEnv []string, args ...string) string {
	f.t.Helper()
	repo := f.repo(dir)
	client := repo.Client
	if len(extraEnv) > 0 {
		// gitclient has no per-call environment, so a one-off client over
		// the same repository carries it; the base options mirror
		// gitfixture's own (fixture HOME, fixture-root ceiling, no system
		// config), and the library's allowlist still excludes every GIT_*
		// variable of this process.
		opts := []gitclient.Option{
			gitclient.WithHome(filepath.Join(repo.Root(), "home")),
			gitclient.WithoutInherited("SSH_AUTH_SOCK"),
			gitclient.WithCeiling(repo.Root()),
			gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"),
		}
		for _, kv := range extraEnv {
			k, v, _ := strings.Cut(kv, "=")
			opts = append(opts, gitclient.WithEnv(k, v))
		}
		var err error
		client, err = gitclient.New(context.Background(), repo.Dir, opts...)
		if err != nil {
			f.t.Fatalf("git client for %s: %v", dir, err)
		}
	}
	out, err := client.Run(context.Background(), args...)
	if err != nil {
		f.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// newRepo creates an empty repo on branch main and returns its resolved path.
func (f *gitFixture) newRepo() string {
	f.t.Helper()
	return f.track(gittest.New(f.t, gitfixture.RepoOptions{}))
}

// clone clones the fixture repository at src into a new repository named
// name beside it (under src's fixture root), with src as its origin.
func (f *gitFixture) clone(src, name string) string {
	f.t.Helper()
	return f.track(gittest.Clone(f.t, f.repo(src), name))
}

// commit makes an empty commit with a pinned identity and pinned dates, and
// returns its sha.
func (f *gitFixture) commit(dir, email, authorDate, committerDate, subject string) string {
	f.t.Helper()
	f.git(dir, []string{
		"GIT_AUTHOR_EMAIL=" + email,
		"GIT_AUTHOR_DATE=" + authorDate,
		"GIT_COMMITTER_DATE=" + committerDate,
	}, "commit", "--allow-empty", "-m", subject)
	return f.git(dir, nil, "rev-parse", "HEAD")
}

// populatedRepo builds the shared fixture. Range used by the tests:
// since 2026-09-01Z, before 2026-10-01Z. Subject -> expectation (default,
// non-merge, with the range):
//
//	a-in-range              me, author in range (with a -07:00 offset)          in
//	other-author            another author, in range                            out
//	c-author-before-since   me, author before since, committer inside range     out (in with since omitted)
//	d-committer-after       me, author in range, committer after before          in
//	e-committer-before      me, author in range, committer before since         in
//	f-author-after-before   me, author after before                              out
//	side-commit             me, on a merged side branch                          in
//	m1-main-commit          me, on main alongside the side branch                in
//	merge-side              me, the merge commit                                 out unless include_merges
//	t-topic-only            me, only on a non-checked-out local branch           in
func populatedRepo(f *gitFixture) string {
	repo := f.newRepo()
	f.commit(repo, meEmail, "2026-09-05T10:00:00-07:00", "2026-09-05T10:00:00-07:00", "a-in-range")
	f.commit(repo, otherEmail, "2026-09-06T10:00:00+00:00", "2026-09-06T10:00:00+00:00", "other-author")
	f.commit(repo, meEmail, "2026-08-15T10:00:00+00:00", "2026-09-10T10:00:00+00:00", "c-author-before-since")
	f.commit(repo, meEmail, "2026-09-20T10:00:00+00:00", "2026-10-15T10:00:00+00:00", "d-committer-after")
	f.commit(repo, meEmail, "2026-09-25T10:00:00+00:00", "2026-08-20T10:00:00+00:00", "e-committer-before")
	f.commit(repo, meEmail, "2026-10-05T10:00:00+00:00", "2026-10-05T10:00:00+00:00", "f-author-after-before")

	f.git(repo, nil, "checkout", "-b", "side")
	f.commit(repo, meEmail, "2026-09-12T10:00:00+00:00", "2026-09-12T10:00:00+00:00", "side-commit")
	f.git(repo, nil, "checkout", "main")
	f.commit(repo, meEmail, "2026-09-13T10:00:00+00:00", "2026-09-13T10:00:00+00:00", "m1-main-commit")
	f.git(repo, []string{
		"GIT_AUTHOR_DATE=2026-09-14T10:00:00+00:00",
		"GIT_COMMITTER_DATE=2026-09-14T10:00:00+00:00",
	}, "merge", "--no-ff", "-m", "merge-side", "side")

	f.git(repo, nil, "checkout", "-b", "topic")
	f.commit(repo, meEmail, "2026-09-15T10:00:00+00:00", "2026-09-15T10:00:00+00:00", "t-topic-only")
	f.git(repo, nil, "checkout", "main")
	return repo
}

var (
	rangeSince  = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rangeBefore = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	pinnedNow   = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
)

func realBackend(stderr *bytes.Buffer) *Backend {
	b := New(Options{Stderr: stderr, Runner: NewExecRunner()})
	b.now = func() time.Time { return pinnedNow }
	return b
}

func listWith(t *testing.T, b *Backend, cfg Config, since, before time.Time) *schema.ActivityListResult {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := scriptout.WithConfig(context.Background(), raw)
	res, err := b.ListActivity(ctx, since, before)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	return res
}

func subjects(items []schema.ActivityItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Summary)
	}
	sort.Strings(out)
	return out
}

func TestListActivity_RealRepo_AuthorDateRangeAndAuthor(t *testing.T) {
	f := newGitFixture(t)
	repo := populatedRepo(f)
	cfg := Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{repo}}

	res := listWith(t, realBackend(&bytes.Buffer{}), cfg, rangeSince, rangeBefore)
	want := []string{"a-in-range", "d-committer-after", "e-committer-before", "m1-main-commit", "side-commit", "t-topic-only"}
	if got := subjects(res.Items); !reflect.DeepEqual(got, want) {
		t.Errorf("subjects = %v, want %v", got, want)
	}
	if res.Truncated {
		t.Error("truncated must be false")
	}

	// Identical ids (and bytes) across two calls.
	again := listWith(t, realBackend(&bytes.Buffer{}), cfg, rangeSince, rangeBefore)
	if !reflect.DeepEqual(res, again) {
		t.Errorf("two calls differ:\nfirst  %+v\nsecond %+v", res, again)
	}
}

func TestListActivity_RealRepo_SinceOmittedAndIncludeMerges(t *testing.T) {
	f := newGitFixture(t)
	repo := populatedRepo(f)
	farFuture := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

	cfg := Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{repo}}
	res := listWith(t, realBackend(&bytes.Buffer{}), cfg, time.Time{}, farFuture)
	want := []string{
		"a-in-range", "c-author-before-since", "d-committer-after", "e-committer-before",
		"f-author-after-before", "m1-main-commit", "side-commit", "t-topic-only",
	}
	if got := subjects(res.Items); !reflect.DeepEqual(got, want) {
		t.Errorf("since omitted: subjects = %v, want %v", got, want)
	}

	cfg.IncludeMerges = true
	res = listWith(t, realBackend(&bytes.Buffer{}), cfg, rangeSince, rangeBefore)
	want = []string{"a-in-range", "d-committer-after", "e-committer-before", "m1-main-commit", "merge-side", "side-commit", "t-topic-only"}
	if got := subjects(res.Items); !reflect.DeepEqual(got, want) {
		t.Errorf("include_merges: subjects = %v, want %v", got, want)
	}
}

func TestListActivity_RealRepo_ItemShape(t *testing.T) {
	f := newGitFixture(t)
	repo := populatedRepo(f)
	cfg := Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{repo}}
	res := listWith(t, realBackend(&bytes.Buffer{}), cfg, rangeSince, rangeBefore)

	ident := fallbackIdent(repo) // the fixture has no origin remote
	var found bool
	for _, it := range res.Items {
		if _, err := time.Parse(time.RFC3339, it.OccurredAt); err != nil {
			t.Errorf("%s: occurred_at %q is not RFC3339: %v", it.Summary, it.OccurredAt, err)
		}
		var raw map[string]any
		if err := json.Unmarshal(it.Fields, &raw); err != nil {
			t.Fatalf("%s: fields: %v", it.Summary, err)
		}
		sha, _ := raw["sha"].(string)
		if it.Kind != "commit" || it.EntityType != "commit" {
			t.Errorf("%s: kind/entity_type = %q/%q", it.Summary, it.Kind, it.EntityType)
		}
		if want := ident + "@" + sha; it.EntityID != want || it.ID != want {
			t.Errorf("%s: id/entity_id = %q/%q, want %q", it.Summary, it.ID, it.EntityID, want)
		}
		if len(it.Labels) == 0 || it.Labels[0] != "repo:"+ident {
			t.Errorf("%s: labels = %v, want repo:%s first", it.Summary, it.Labels, ident)
		}
		if raw["repo_path"] != repo || raw["author_email"] != meEmail || len(sha) != 40 {
			t.Errorf("%s: fields = %v", it.Summary, raw)
		}
		if it.AsOf != "2026-10-06T12:00:00Z" || it.Stale {
			t.Errorf("%s: as_of/stale = %q/%v", it.Summary, it.AsOf, it.Stale)
		}
		if it.Summary == "a-in-range" {
			found = true
			if it.OccurredAt != "2026-09-05T10:00:00-07:00" {
				t.Errorf("occurred_at = %q, want the author date with its own offset", it.OccurredAt)
			}
		}
	}
	if !found {
		t.Fatal("a-in-range missing")
	}
}

func TestListActivity_RealRepo_ClonesDedupeAndTopicOnlyOnce(t *testing.T) {
	f := newGitFixture(t)
	repo := populatedRepo(f)

	cloneA, cloneB := f.clone(repo, "clone-a"), f.clone(repo, "clone-b")
	identA, identB := repoIdent(context.Background(), NewExecRunner(), cloneA), repoIdent(context.Background(), NewExecRunner(), cloneB)
	if identA != identB || strings.HasPrefix(identA, filepath.Base(cloneA)) {
		t.Fatalf("clones must share an origin-derived ident: %q vs %q", identA, identB)
	}

	cfg := Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{cloneA, cloneB, repo, repo}}
	res := listWith(t, realBackend(&bytes.Buffer{}), cfg, rangeSince, rangeBefore)

	seen := map[string]bool{}
	byPath := map[string]int{}
	for _, it := range res.Items {
		if seen[it.ID] {
			t.Errorf("duplicate id %q", it.ID)
		}
		seen[it.ID] = true
		var raw struct {
			RepoPath string `json:"repo_path"`
		}
		_ = json.Unmarshal(it.Fields, &raw)
		byPath[raw.RepoPath]++
	}
	// Clones only carry main (the topic branch is remote-tracking there);
	// cloneB's items all collide with cloneA's and lose, and the repeated
	// fixture path adds nothing.
	if byPath[cloneB] != 0 {
		t.Errorf("cloneB contributed %d items; first configured path must win", byPath[cloneB])
	}
	if byPath[cloneA] != 5 {
		t.Errorf("cloneA contributed %d items, want 5 (main only)", byPath[cloneA])
	}
	if byPath[repo] != 6 {
		t.Errorf("fixture contributed %d items, want 6 (including the topic-only commit once)", byPath[repo])
	}
}

func TestListActivity_RealRepo_SkippedPathsLogOneLineEach(t *testing.T) {
	f := newGitFixture(t)
	repo := populatedRepo(f)
	notRepo := t.TempDir()
	missing := filepath.Join(t.TempDir(), "gone")
	aFile := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(aFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	cfg := Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{missing, notRepo, aFile, repo}}
	res := listWith(t, realBackend(&stderr), cfg, rangeSince, rangeBefore)
	if len(res.Items) != 6 {
		t.Errorf("items = %d, want 6 (the valid repo is still read)", len(res.Items))
	}
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stderr has %d lines, want 3:\n%s", len(lines), stderr.String())
	}
	for i, p := range []string{missing, notRepo, aFile} {
		if !strings.Contains(lines[i], p) {
			t.Errorf("line %d = %q, want it to name %q", i, lines[i], p)
		}
	}
}

func TestListActivity_RealRepo_EmptyRepoIsZeroItemsNotSkipped(t *testing.T) {
	f := newGitFixture(t)
	repo := f.newRepo()
	var stderr bytes.Buffer
	cfg := Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{repo}}
	res := listWith(t, realBackend(&stderr), cfg, rangeSince, rangeBefore)
	if len(res.Items) != 0 || stderr.Len() != 0 {
		t.Errorf("items = %d, stderr = %q; want zero items and no skip line", len(res.Items), stderr.String())
	}
}

// TestListActivity_RealRepo_LeakedGitDirDoesNotRedirect is the pg2-67h4y
// class: a GIT_DIR in the process environment pointing at a DIFFERENT repo
// must not change what is read.
func TestListActivity_RealRepo_LeakedGitDirDoesNotRedirect(t *testing.T) {
	f := newGitFixture(t)
	repo := populatedRepo(f)
	decoy := f.newRepo()
	f.commit(decoy, meEmail, "2026-09-07T10:00:00+00:00", "2026-09-07T10:00:00+00:00", "decoy-commit")

	cfg := Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{repo}}
	baseline := listWith(t, realBackend(&bytes.Buffer{}), cfg, rangeSince, rangeBefore)

	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))
	t.Setenv("GIT_WORK_TREE", decoy)
	leaked := listWith(t, realBackend(&bytes.Buffer{}), cfg, rangeSince, rangeBefore)

	if !reflect.DeepEqual(baseline, leaked) {
		t.Errorf("leaked GIT_DIR changed the result:\nbaseline %v\nleaked   %v", subjects(baseline.Items), subjects(leaked.Items))
	}
	for _, s := range subjects(leaked.Items) {
		if s == "decoy-commit" {
			t.Error("read the decoy repository")
		}
	}

	// And directly through the runner: the child resolves the -C repo.
	out, err := NewExecRunner().Run(context.Background(), repo, "rev-parse", "--git-dir")
	if err != nil {
		t.Fatal(err)
	}
	if out != ".git" {
		t.Errorf("rev-parse --git-dir = %q, want .git (the -C repo, not the leaked one)", out)
	}
}

// TestListActivity_RealRepo_Conformance runs the shared list_activity
// conformance case over the populated fixture, through the real dispatch
// table with the host's config block injected.
func TestListActivity_RealRepo_Conformance(t *testing.T) {
	f := newGitFixture(t)
	repo := populatedRepo(f)
	cfg, _ := json.Marshal(Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{repo}})
	backend := configBackend{
		inner:  conformance.TableBackend{Table: activity.NewDispatchTable(realBackend(&bytes.Buffer{}))},
		config: cfg,
	}
	for _, since := range []time.Time{rangeSince, {}} {
		results := conformance.RunListActivityCase(context.Background(), backend, since, rangeBefore)
		if len(results) == 0 {
			t.Fatal("conformance returned no sub-cases")
		}
		for _, r := range results {
			if r.Err != nil {
				t.Errorf("%s (since=%v): %v", r.Name, since, r.Err)
			}
		}
	}
}

// configBackend adds the host's per-backend config block to every request.
type configBackend struct {
	inner  conformance.Backend
	config json.RawMessage
}

func (c configBackend) Invoke(ctx context.Context, request []byte) ([]byte, int, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, 0, err
	}
	req["config"] = c.config
	out, err := json.Marshal(req)
	if err != nil {
		return nil, 0, err
	}
	return c.inner.Invoke(ctx, out)
}

// TestListActivity_RealRepo_SearchPathDiscovery builds, directly under one
// search path: two git clones sharing an origin remote, a worktree checkout of
// the first clone (its .git is a file, sharing the clone's repo_ident), a
// worktree checkout of a separate repository (its .git is a file too), and a
// plain directory; plus a repo nested two levels down and a nonexistent
// search path.
func TestListActivity_RealRepo_SearchPathDiscovery(t *testing.T) {
	f := newGitFixture(t)
	origin := populatedRepo(f)
	other := populatedRepo(f)

	// The search path is the fixture root of a bare remote that carries
	// origin's branches: a bare repository has no .git entry, so discovery
	// skips it, and the clones below land beside it.
	bare, err := f.repo(origin).AddBareRemote(context.Background(), "origin")
	if err != nil {
		t.Fatal(err)
	}
	f.git(origin, nil, "push", "origin", "--all")
	search := bare.Root()

	cloneA := gittest.Clone(t, bare, "a-clone")
	f.track(cloneA)
	cloneB := gittest.Clone(t, bare, "b-clone")
	f.track(cloneB)
	siblingWT := filepath.Join(search, "c-sibling-worktree")
	f.git(cloneA.Dir, nil, "worktree", "add", "-b", "wt-branch", siblingWT)
	foreignWT := filepath.Join(search, "d-foreign-worktree")
	f.git(other, nil, "worktree", "add", "-b", "wt-foreign", foreignWT)
	mkdirAll(t, filepath.Join(search, "e-plain"))
	// A repository two levels down: its fixture root is the group directory.
	nestedRepo, err := gitfixture.NewRepo(context.Background(), filepath.Join(search, "f-group"),
		gitfixture.RepoOptions{Suite: "nested", Name: "nested"})
	if err != nil {
		t.Fatal(err)
	}
	nested := f.track(nestedRepo)
	f.commit(nested, meEmail, "2026-09-17T10:00:00+00:00", "2026-09-17T10:00:00+00:00", "nested-commit")

	for _, wt := range []string{siblingWT, foreignWT} {
		if st, err := os.Stat(filepath.Join(wt, ".git")); err != nil || !st.Mode().IsRegular() {
			t.Fatalf("%s: the worktree checkout's .git must be a file: %v", wt, err)
		}
	}

	missing := filepath.Join(t.TempDir(), "gone")
	var stderr bytes.Buffer
	cfg := Config{
		AuthorEmails:    []string{meEmail},
		RepoSearchPaths: []string{missing, search},
	}
	res := listWith(t, realBackend(&stderr), cfg, rangeSince, rangeBefore)

	seen := map[string]bool{}
	byPath := map[string]int{}
	for _, it := range res.Items {
		if seen[it.ID] {
			t.Errorf("duplicate id %q", it.ID)
		}
		seen[it.ID] = true
		var raw struct {
			RepoPath string    `json:"repo_path"`
			Refs     *[]string `json:"refs"`
		}
		_ = json.Unmarshal(it.Fields, &raw)
		byPath[raw.RepoPath]++
		if it.Summary == "nested-commit" {
			t.Error("a repo two levels down must not be discovered")
		}
		if len(it.Labels) == 0 || !strings.HasPrefix(it.Labels[0], "repo:") {
			t.Errorf("%s: labels = %v, want repo: first", it.Summary, it.Labels)
		}
		if raw.Refs == nil || !strings.Contains(string(it.Fields), `"insertions"`) || !strings.Contains(string(it.Fields), `"deletions"`) {
			t.Errorf("%s: fields lack refs or line counts: %s", it.Summary, it.Fields)
		}
		if it.Summary == "t-topic-only" && raw.RepoPath == foreignWT {
			if !reflect.DeepEqual(it.Labels[1:], []string{"branch:topic"}) {
				t.Errorf("t-topic-only labels = %v, want a branch:topic label", it.Labels)
			}
		}
	}
	// cloneA is read first; cloneB and the sibling worktree share its
	// repo_ident and shas, so everything they hold collides on id and loses.
	// The foreign worktree has its own ident and contributes its own commits.
	for path, want := range map[string]int{cloneA.Dir: 5, cloneB.Dir: 0, siblingWT: 0, foreignWT: 6} {
		if byPath[path] != want {
			t.Errorf("%s contributed %d items, want %d", path, byPath[path], want)
		}
	}

	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], missing) {
		t.Errorf("stderr = %q, want exactly one line naming the missing search path", stderr.String())
	}
}

// TestListActivity_RealRepo_SearchPathSharedWithRepoPaths: a repo named both
// in repo_paths and found under a search path is read once.
func TestListActivity_RealRepo_SearchPathSharedWithRepoPaths(t *testing.T) {
	f := newGitFixture(t)
	repo := f.newRepo()
	search := f.repo(repo).Root() // the repo sits directly under its fixture root
	f.commit(repo, meEmail, "2026-09-05T10:00:00+00:00", "2026-09-05T10:00:00+00:00", "only-commit")

	cfg := Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{repo}, RepoSearchPaths: []string{search}}
	res := listWith(t, realBackend(&bytes.Buffer{}), cfg, rangeSince, rangeBefore)
	if got := subjects(res.Items); !reflect.DeepEqual(got, []string{"only-commit"}) {
		t.Errorf("subjects = %v, want a single only-commit", got)
	}
}
