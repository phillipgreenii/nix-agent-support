package executor

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

const (
	testRefspec = `pull/{{index .Item.Metadata "pr_number"}}/head`
	testRev     = `{{index .Item.Metadata "head_sha"}}`
)

func prefetchItem(sha string) item.Item {
	return item.Item{ID: "zr-pf", Metadata: map[string]any{"pr_number": float64(7), "head_sha": sha}}
}

// scriptGit is a scripted GitRun: it records every call (args joined by a
// space) and fails the first call whose joined args contain a key of failOn.
type scriptGit struct {
	mu     sync.Mutex
	calls  []string
	failOn map[string]error
	// local is the set of commitishes `rev-parse --verify` resolves.
	local map[string]bool
	// excludeFile is what `rev-parse --git-path info/exclude` prints.
	excludeFile string
}

func (g *scriptGit) run(_ context.Context, _ string, stdout io.Writer, args ...string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	joined := strings.Join(args, " ")
	g.calls = append(g.calls, joined)
	for k, err := range g.failOn {
		if strings.Contains(joined, k) {
			return err
		}
	}
	switch {
	case args[0] == "rev-parse" && args[1] == "--verify":
		if !g.local[strings.TrimSuffix(args[len(args)-1], "^{commit}")] {
			return errors.New("not a commit")
		}
	case args[0] == "rev-parse" && strings.Contains(joined, "--git-path"):
		_, _ = io.WriteString(stdout, g.excludeFile+"\n")
	case args[0] == "diff":
		_, _ = io.WriteString(stdout, "DIFF-OUTPUT "+args[len(args)-1]+"\n")
	}
	return nil
}

func (g *scriptGit) has(prefix string) bool {
	for _, c := range g.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func newScriptRun(t *testing.T, g *scriptGit) *ccpoolRun {
	t.Helper()
	g.excludeFile = filepath.Join(t.TempDir(), "info", "exclude")
	return &ccpoolRun{deps: Deps{Cfg: config.Config{}, GitRun: g.run}}
}

func prefetchRole(pf roles.PrefetchConfig) roles.Role {
	return roles.Role{Name: "review", Type: "ccpool", CCPool: &roles.CCPoolConfig{
		Isolation: roles.IsolationConfig{Prefetch: &pf},
	}}
}

const goodSHA = "0123456789abcdef0123456789abcdef01234567"

func TestPrefetch_localRevSkipsFetchChecksOutAndWritesDiff(t *testing.T) {
	g := &scriptGit{local: map[string]bool{goodSHA: true, "origin/main": true}}
	r := newScriptRun(t, g)
	wt := t.TempDir()
	pf := roles.PrefetchConfig{Refspec: testRefspec, Rev: testRev, DiffBase: "origin/main"}
	d := DispatchContext{Role: prefetchRole(pf), Item: prefetchItem(goodSHA)}

	info := r.prefetch(context.Background(), pf, d, wt)

	if !info.OK || info.Rev != goodSHA {
		t.Fatalf("info = %+v, want OK at %s", info, goodSHA)
	}
	if g.has("fetch") {
		t.Errorf("a locally present commit must not be fetched; calls=%v", g.calls)
	}
	if !g.has("checkout --detach --quiet " + goodSHA) {
		t.Errorf("worktree must be checked out detached at the rev; calls=%v", g.calls)
	}
	for _, f := range []string{info.DiffFile, info.NumstatFile} {
		b, err := os.ReadFile(f)
		if err != nil || !strings.Contains(string(b), "origin/main..."+goodSHA) {
			t.Errorf("%s = %q, %v; want the three-dot range", f, b, err)
		}
	}
	if got := loadPrefetchInfo(wt); got != info {
		t.Errorf("manifest = %+v, want %+v", got, info)
	}
	ex, _ := os.ReadFile(g.excludeFile)
	if !strings.Contains(string(ex), "/.pg-router/") {
		t.Errorf("info/exclude = %q, want the pre-fetch dir ignored", ex)
	}
}

func TestPrefetch_missingRevIsFetchedByRefspec(t *testing.T) {
	g := &scriptGit{local: map[string]bool{"origin/main": true}}
	// The commit becomes local once fetched.
	inner := g.run
	r := newScriptRun(t, g)
	r.deps.GitRun = func(ctx context.Context, dir string, out io.Writer, args ...string) error {
		err := inner(ctx, dir, out, args...)
		if args[0] == "fetch" {
			g.mu.Lock()
			g.local[goodSHA] = true
			g.mu.Unlock()
		}
		return err
	}
	pf := roles.PrefetchConfig{Remote: "upstream", Refspec: testRefspec, Rev: testRev}
	d := DispatchContext{Role: prefetchRole(pf), Item: prefetchItem(goodSHA)}

	info := r.prefetch(context.Background(), pf, d, t.TempDir())

	if !info.OK {
		t.Fatalf("info = %+v, want OK", info)
	}
	if !g.has("fetch --no-tags upstream pull/7/head") {
		t.Errorf("want a fetch of the rendered refspec from the configured remote; calls=%v", g.calls)
	}
	if info.DiffFile != "" || info.NumstatFile != "" {
		t.Errorf("no DiffBase configured, so no diff files; got %+v", info)
	}
}

func TestPrefetch_failuresAreSoftAndRecorded(t *testing.T) {
	tests := []struct {
		name  string
		g     *scriptGit
		pf    roles.PrefetchConfig
		sha   string
		noGit string // a git subcommand that must NOT have run
	}{
		{
			"fetch fails", &scriptGit{failOn: map[string]error{"fetch": errors.New("oauth command timed out")}},
			roles.PrefetchConfig{Refspec: testRefspec, Rev: testRev},
			goodSHA, "checkout",
		},
		{
			"not local and no refspec", &scriptGit{},
			roles.PrefetchConfig{Rev: testRev},
			goodSHA, "fetch",
		},
		{
			"fetched but still absent", &scriptGit{},
			roles.PrefetchConfig{Refspec: testRefspec, Rev: testRev},
			goodSHA, "checkout",
		},
		{
			"checkout fails", &scriptGit{local: map[string]bool{goodSHA: true}, failOn: map[string]error{"checkout": errors.New("would be overwritten")}},
			roles.PrefetchConfig{Refspec: testRefspec, Rev: testRev},
			goodSHA, "diff",
		},
		{
			"rev is an option, not a commit", &scriptGit{},
			roles.PrefetchConfig{Refspec: testRefspec, Rev: testRev},
			"--upload-pack=evil", "rev-parse --verify",
		},
		{
			"rev is not hex", &scriptGit{},
			roles.PrefetchConfig{Refspec: testRefspec, Rev: testRev},
			"main", "rev-parse --verify",
		},
		{
			"refspec renders as an option", &scriptGit{},
			roles.PrefetchConfig{Refspec: `-{{index .Item.Metadata "pr_number"}}`, Rev: testRev},
			goodSHA, "fetch",
		},
		{
			"remote renders with whitespace", &scriptGit{},
			roles.PrefetchConfig{Remote: "a b", Refspec: testRefspec, Rev: testRev},
			goodSHA, "fetch",
		},
		{
			"rev template references a missing key", &scriptGit{},
			roles.PrefetchConfig{Rev: `{{.Nope}}`},
			goodSHA, "rev-parse --verify",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newScriptRun(t, tc.g)
			wt := t.TempDir()
			d := DispatchContext{Role: prefetchRole(tc.pf), Item: prefetchItem(tc.sha)}

			info := r.prefetch(context.Background(), tc.pf, d, wt)

			if info.OK {
				t.Fatalf("info = %+v, want OK=false", info)
			}
			if tc.noGit != "" && tc.g.has(tc.noGit) {
				t.Errorf("%q must not run after this failure; calls=%v", tc.noGit, tc.g.calls)
			}
			// The failure is recorded, so a later render sees OK=false, not stale data.
			if got := loadPrefetchInfo(wt); got.OK {
				t.Errorf("manifest = %+v, want OK=false", got)
			}
		})
	}
}

func TestPrefetch_unresolvableBaseKeepsCheckoutDropsDiff(t *testing.T) {
	g := &scriptGit{local: map[string]bool{goodSHA: true}}
	r := newScriptRun(t, g)
	pf := roles.PrefetchConfig{Rev: testRev, DiffBase: "origin/gone"}
	d := DispatchContext{Role: prefetchRole(pf), Item: prefetchItem(goodSHA)}

	info := r.prefetch(context.Background(), pf, d, t.TempDir())

	if !info.OK || info.DiffFile != "" || info.NumstatFile != "" {
		t.Fatalf("info = %+v, want OK with no diff files", info)
	}
	if g.has("diff") {
		t.Errorf("no diff may run against an unresolvable base; calls=%v", g.calls)
	}
}

func TestPrefetch_failedDiffLeavesNoPartialFile(t *testing.T) {
	// Fail only the plain diff (its args end "--no-color <range>"); the
	// numstat call has "--numstat" between them and still succeeds.
	g := &scriptGit{
		local:  map[string]bool{goodSHA: true, "origin/main": true},
		failOn: map[string]error{"--no-color origin/main": errors.New("killed")},
	}
	r := newScriptRun(t, g)
	pf := roles.PrefetchConfig{Rev: testRev, DiffBase: "origin/main"}
	d := DispatchContext{Role: prefetchRole(pf), Item: prefetchItem(goodSHA)}
	wt := t.TempDir()

	info := r.prefetch(context.Background(), pf, d, wt)

	if !info.OK || info.DiffFile != "" || info.NumstatFile == "" {
		t.Fatalf("info = %+v, want OK, numstat kept, diff not advertised", info)
	}
	if _, err := os.Stat(filepath.Join(wt, prefetchDirName, prefetchDiffName)); !os.IsNotExist(err) {
		t.Errorf("a failed diff must leave no partial file; stat err = %v", err)
	}
}

func TestPrefetch_excludeFailureWritesNothingInWorktree(t *testing.T) {
	g := &scriptGit{
		local:  map[string]bool{goodSHA: true},
		failOn: map[string]error{"--git-path": errors.New("not a repository")},
	}
	r := newScriptRun(t, g)
	pf := roles.PrefetchConfig{Rev: testRev}
	d := DispatchContext{Role: prefetchRole(pf), Item: prefetchItem(goodSHA)}
	wt := t.TempDir()

	info := r.prefetch(context.Background(), pf, d, wt)

	if info.OK {
		t.Fatalf("info = %+v, want OK=false when the output dir cannot be ignored", info)
	}
	if _, err := os.Stat(filepath.Join(wt, prefetchDirName)); !os.IsNotExist(err) {
		t.Errorf("an un-ignored output dir would strand the worktree on removal; stat err = %v", err)
	}
}

func TestValidatePrefetch(t *testing.T) {
	ok := roles.PrefetchConfig{Rev: testRev, Refspec: testRefspec, DiffBase: "origin/main", Timeout: "90s"}
	if err := ValidatePrefetch(ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := map[string]roles.PrefetchConfig{
		"no rev":         {},
		"bad rev tmpl":   {Rev: "{{"},
		"bad refspec":    {Rev: testRev, Refspec: "{{"},
		"bad timeout":    {Rev: testRev, Timeout: "soon"},
		"zero timeout":   {Rev: testRev, Timeout: "0s"},
		"bad base tmpl":  {Rev: testRev, DiffBase: "{{.X"},
		"bad remote tpl": {Rev: testRev, Remote: "{{"},
	}
	for name, pf := range bad {
		if err := ValidatePrefetch(pf); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// Real git, local remote only (no network): the full fetch -> detached
// checkout -> diff path, and the property the design rests on, that the
// pre-fetch files do not make the worktree unremovable.
func TestPrefetch_realGit(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx := context.Background()

	src := gittest.New(t, gitfixture.RepoOptions{Suite: "prefetch-src"})
	if _, err := src.Commit(ctx, "base", map[string]string{"a.txt": "one\n"}); err != nil {
		t.Fatal(err)
	}
	origin, err := src.AddBareRemote(ctx, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Client.Run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	prSHA, err := src.Commit(ctx, "pr", map[string]string{"a.txt": "one\ntwo\n", "b.txt": "new\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Client.Run(ctx, "push", "origin", "HEAD:refs/pull/7/head"); err != nil {
		t.Fatal(err)
	}

	work := gittest.New(t, gitfixture.RepoOptions{Suite: "prefetch-work"})
	for _, args := range [][]string{
		{"remote", "add", "origin", origin.Dir},
		{"fetch", "origin", "main:refs/remotes/origin/main"},
		{"checkout", "-B", "main", "refs/remotes/origin/main"},
	} {
		if _, err := work.Client.Run(ctx, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if _, err := work.Client.Run(ctx, "worktree", "add", "--detach", wt, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := work.Client.Run(ctx, "cat-file", "-e", prSHA+"^{commit}"); err == nil {
		t.Fatal("fixture bug: the PR commit must not be local before the pre-fetch")
	}

	r := &ccpoolRun{deps: Deps{Cfg: config.Config{}}} // production gitRun
	pf := roles.PrefetchConfig{Refspec: testRefspec, Rev: testRev, DiffBase: "origin/main"}
	d := DispatchContext{Role: prefetchRole(pf), Item: prefetchItem(prSHA)}
	info := r.prefetch(ctx, pf, d, wt)

	if !info.OK || info.Rev != prSHA {
		t.Fatalf("info = %+v, want OK at %s", info, prSHA)
	}
	head, err := r.deps.gitRunOut(ctx, wt, "rev-parse", "HEAD")
	if err != nil || head != prSHA {
		t.Errorf("worktree HEAD = %q, %v; want %s", head, err, prSHA)
	}
	numstat, _ := os.ReadFile(info.NumstatFile)
	if !strings.Contains(string(numstat), "a.txt") || !strings.Contains(string(numstat), "b.txt") {
		t.Errorf("numstat = %q, want both changed files", numstat)
	}
	diff, _ := os.ReadFile(info.DiffFile)
	if !strings.Contains(string(diff), "+two") || !strings.Contains(string(diff), "+new") {
		t.Errorf("diff = %q, want the PR's added lines", diff)
	}
	if st, _ := r.deps.gitRunOut(ctx, wt, "status", "--porcelain"); st != "" {
		t.Errorf("worktree must read as clean (pre-fetch dir ignored); status = %q", st)
	}
	// A rerun (redispatch of a reused worktree) is idempotent.
	if again := r.prefetch(ctx, pf, d, wt); !again.OK {
		t.Errorf("rerun = %+v, want OK", again)
	}
	// The point of the exclude entry: a NON-force removal still works.
	if _, err := work.Client.Run(ctx, "worktree", "remove", wt); err != nil {
		t.Errorf("non-force worktree remove refused after pre-fetch: %v", err)
	}
}

// gitRunOut is a test helper: stdout of one git call, trimmed.
func (d Deps) gitRunOut(ctx context.Context, dir string, args ...string) (string, error) {
	var b strings.Builder
	err := d.gitRun()(ctx, dir, &b, args...)
	return strings.TrimSpace(b.String()), err
}

// Dispatch wiring: the pre-fetch runs after isolation and before the session
// launches, and the rendered nudge carries its outcome.
func TestDispatch_prefetchRunsBeforeLaunchAndFeedsPrompt(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	body := `rev={{.Prefetch.Rev}} ok={{.Prefetch.OK}} diff={{.Prefetch.DiffFile}} numstat={{.Prefetch.NumstatFile}}`
	pf := roles.PrefetchConfig{Rev: testRev, DiffBase: "origin/main"}
	role := reviewRole(cfg)
	role.CCPool.PromptBody = body
	role.CCPool.Prompt = mustParsePrompt("review", body)
	role.CCPool.Isolation = roles.IsolationConfig{Prefetch: &pf}

	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-pf": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-review-zr-pf", Live: true, State: ccpool.StateWorking}}}}
	g := &scriptGit{local: map[string]bool{goodSHA: true, "origin/main": true}}
	deps := newExec(cc, bd, cfg).deps
	g.excludeFile = filepath.Join(t.TempDir(), "info", "exclude")
	deps.GitRun = g.run
	deps.ExternalID = "pg-router-review-zr-pf"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	// The worktree dir must exist for the manifest, as it does once git created it.
	wt := filepath.Join(cfg.WorktreeDir, "zr-pf")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}

	d := DispatchContext{Role: role, Item: prefetchItem(goodSHA)}
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if len(cc.SentText) != 1 {
		t.Fatalf("want one nudge, got %d", len(cc.SentText))
	}
	want := "rev=" + goodSHA + " ok=true diff=" + filepath.Join(wt, ".pg-router", "diff.patch") +
		" numstat=" + filepath.Join(wt, ".pg-router", "numstat.txt")
	if !strings.Contains(cc.SentText[0], want) {
		t.Errorf("nudge = %q, want it to contain %q", cc.SentText[0], want)
	}
	if !g.has("checkout --detach --quiet " + goodSHA) {
		t.Errorf("checkout must have run before launch; calls=%v", g.calls)
	}
}

func TestRenderNudge_prefetchFieldsEmptyWithoutPrefetchBlock(t *testing.T) {
	cfg := fastCfg()
	body := `[{{.Prefetch.OK}}|{{.Prefetch.DiffFile}}]`
	role := reviewRole(cfg)
	role.CCPool.PromptBody = body
	role.CCPool.Prompt = mustParsePrompt("review", body)
	r := newExec(&dtest.FakeCC{}, &dtest.ScriptBD{}, cfg)

	got := r.renderNudge(role.CCPool, DispatchContext{Role: role, Item: item.Item{ID: "x"}}, t.TempDir())

	if !strings.Contains(got, "[false|]") {
		t.Errorf("nudge = %q, want the zero PrefetchInfo", got)
	}
}
