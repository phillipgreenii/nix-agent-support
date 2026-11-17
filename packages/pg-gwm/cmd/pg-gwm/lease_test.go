package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-gwm/internal/gitrun/gitruntest"
	"github.com/phillipgreenii/pg-gwm/internal/manager"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// cli runs the app in process against a hermetic repository, with standard
// output and error in temporary files so they can be real *os.File values.
type cli struct {
	t         *testing.T
	repo      *gitfixture.Repo
	a         *app
	out, errf *os.File
	tty       bool
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	r := newBaseRepo(t)
	c := &cli{t: t, repo: r}
	var err error
	if c.out, err = os.CreateTemp(t.TempDir(), "out"); err != nil {
		t.Fatal(err)
	}
	if c.errf, err = os.CreateTemp(t.TempDir(), "err"); err != nil {
		t.Fatal(err)
	}
	c.a = newApp(c.out, c.errf)
	c.a.git = gitruntest.New(t, r)
	c.a.isTerminal = func(*os.File) bool { return c.tty }
	return c
}

// run runs the command with --repo pointing at the fixture and returns its
// exit code and what it wrote.
func (c *cli) run(args ...string) (code int, stdout, stderr string) {
	c.t.Helper()
	for _, f := range []*os.File{c.out, c.errf} {
		if err := f.Truncate(0); err != nil {
			c.t.Fatal(err)
		}
		if _, err := f.Seek(0, 0); err != nil {
			c.t.Fatal(err)
		}
	}
	code = c.a.run(append(args, "--repo", c.repo.Dir))
	o, _ := os.ReadFile(c.out.Name())
	e, _ := os.ReadFile(c.errf.Name())
	return code, string(o), string(e)
}

func (c *cli) git(args ...string) string {
	c.t.Helper()
	out, err := c.repo.Client.Run(c.t.Context(), args...)
	if err != nil {
		c.t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return m
}

func TestParseInterleaved(t *testing.T) {
	fs := newFlagSet("t")
	name := fs.String("name", "", "")
	det := fs.Bool("detach", false, "")
	pos, err := parseInterleaved(fs, []string{"a", "--name", "n", "b", "--detach", "--", "--c", "-d"})
	if err != nil {
		t.Fatal(err)
	}
	if *name != "n" || !*det || strings.Join(pos, ",") != "a,b,--c,-d" {
		t.Errorf("name %q detach %v pos %v", *name, *det, pos)
	}
	if _, err := parseInterleaved(newFlagSet("t"), []string{"--nope"}); err == nil {
		t.Error("unknown flag accepted")
	}
	var _ flag.Value = kvFlag{}
}

func TestOutputFormats(t *testing.T) {
	c := newCLI(t)
	root := c.repo.Dir + ".worktrees"

	c.tty = true
	code, out, _ := c.run("lease", "--name", "one", "--owner", "me", "-b", "one")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"path:", "one", "leased", "me", "expires:", "branch:"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "schema_version") {
		t.Errorf("text output is JSON:\n%s", out)
	}

	c.tty = false
	code, out, _ = c.run("lease", "--name", "two", "-b", "two", "--metadata", "k=v")
	m := decode(t, out)
	if code != 0 || m["schema_version"] != float64(1) || m["name"] != "two" {
		t.Fatalf("pipe output: %d %v", code, m)
	}
	if md, _ := m["metadata"].(map[string]any); md["k"] != "v" {
		t.Errorf("metadata = %v", m["metadata"])
	}
	if !strings.HasPrefix(m["worktree_path"].(string), root) && !strings.Contains(m["worktree_path"].(string), ".worktrees") {
		t.Errorf("path = %v", m["worktree_path"])
	}

	if _, out, _ = c.run("lease", "--name", "three", "-b", "three", "--format=text"); !strings.Contains(out, "path:") {
		t.Errorf("--format=text on a pipe:\n%s", out)
	}
	c.tty = true
	if _, out, _ = c.run("lease", "--name", "four", "-b", "four", "--json"); decode(t, out)["schema_version"] != float64(1) {
		t.Errorf("--json on a terminal:\n%s", out)
	}
	if code, _, _ := c.run("lease", "--name", "x", "-b", "x", "--json", "--format=text"); code != 2 {
		t.Errorf("contradicting flags exit %d", code)
	}

	_, out, errs := c.run("lease", "--name", "five", "-b", "five", "--print=path")
	if !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 || !filepath.IsAbs(strings.TrimSpace(out)) || errs != "" {
		t.Errorf("--print=path stdout %q stderr %q", out, errs)
	}
	if _, err := os.Stat(strings.TrimSpace(out)); err != nil {
		t.Error(err)
	}

	// A warning goes to standard error and never to standard output.
	c.run("version")
	c.out.Truncate(0)
	c.errf.Truncate(0)
	c.a.warnf("careful %d", 1)
	o, _ := os.ReadFile(c.out.Name())
	e, _ := os.ReadFile(c.errf.Name())
	if len(o) != 0 || !strings.Contains(string(e), "warning: careful 1") {
		t.Errorf("warning: stdout %q stderr %q", o, e)
	}
}

func TestErrorOutput(t *testing.T) {
	c := newCLI(t)
	if code, _, e := c.run("lease", "--name", "held", "-b", "held"); code != 0 {
		t.Fatalf("setup: %d %s", code, e)
	}

	c.tty = false
	code, out, errs := c.run("lease", "--name", "held", "--detach")
	env := decode(t, out)
	body, _ := env["error"].(map[string]any)
	if code != 21 || env["schema_version"] != float64(1) || body["name"] != "name-held" || body["code"] != float64(21) {
		t.Fatalf("envelope: %d %v", code, env)
	}
	for _, k := range []string{"message", "hint", "details"} {
		if body[k] == nil || body[k] == "" {
			t.Errorf("envelope lacks %s: %v", k, body)
		}
	}
	if d, _ := body["details"].(map[string]any); d["lease_id"] == nil || d["status"] != "leased" {
		t.Errorf("details = %v", body["details"])
	}
	if errs != "" {
		t.Errorf("stderr on a JSON refusal: %q", errs)
	}

	c.tty = true
	code, out, errs = c.run("lease", "--name", "held", "--detach")
	if code != 21 || out != "" || !strings.Contains(errs, "pg-gwm: ") || !strings.Contains(errs, "hint: ") {
		t.Errorf("tty refusal: %d stdout %q stderr %q", code, out, errs)
	}

	// Under cd "$(pg-gwm lease ... --print=path)" a refusal prints nothing to
	// stdout in any format, so the cd fails instead of receiving JSON.
	for _, tty := range []bool{true, false} {
		c.tty = tty
		for _, extra := range [][]string{nil, {"--json"}, {"--format=json"}} {
			args := append([]string{"lease", "--name", "held", "--detach", "--print=path"}, extra...)
			code, out, errs = c.run(args...)
			if code != 21 || out != "" || !strings.Contains(errs, "pg-gwm: ") || !strings.Contains(errs, "hint: ") {
				t.Errorf("--print=path refusal (tty %v %v): %d stdout %q stderr %q", tty, extra, code, out, errs)
			}
		}
	}
}

func TestDirtyErrorListsAtMostTwenty(t *testing.T) {
	c := newCLI(t)
	var files []string
	for i := 0; i < 25; i++ {
		files = append(files, "f"+string(rune('a'+i)))
	}
	code := c.a.emitErr("text", false, manager.ErrDirty(files, nil))
	e, _ := os.ReadFile(c.errf.Name())
	if code != 23 || !strings.Contains(string(e), "... and 5 more") || strings.Contains(string(e), "fy") {
		t.Errorf("exit %d stderr:\n%s", code, e)
	}
}

func TestInterruptedIsNotARefusal(t *testing.T) {
	c := newCLI(t)
	if code, _, _ := c.run("lease", "--name", "a", "-b", "a"); code != 0 {
		t.Fatal(code)
	}
	c.a.emitErr("json", false, manager.ErrInterrupted("cut short"))
	if code := c.a.emitErr("json", false, manager.ErrInterrupted("cut short")); code != 130 {
		t.Errorf("exit %d", code)
	}
	snap, err := c.a.counters.Read()
	if err != nil {
		t.Fatal(err)
	}
	if n := snap.Repos[c.a.label].Refusals["interrupted"]; n != 0 {
		t.Errorf("interrupted counted %d times", n)
	}
}

func TestCommitIshDefaultsToRepoPath(t *testing.T) {
	c := newCLI(t)
	linked := filepath.Join(filepath.Dir(c.repo.Dir), "linked")
	c.git("worktree", "add", "-b", "X", linked)
	lc := gittest.ClientAt(t, c.repo, linked)
	if _, err := lc.Run(t.Context(), "commit", "--allow-empty", "-m", "ahead"); err != nil {
		t.Fatal(err)
	}
	xHead, err := lc.Run(t.Context(), "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	mainHead := c.git("rev-parse", "HEAD")
	if strings.TrimSpace(string(xHead)) == mainHead {
		t.Fatal("fixture: X is not ahead of main")
	}
	// Run from a directory that is not a repository, aimed at the linked
	// worktree with --repo.
	t.Chdir(t.TempDir())
	code := c.a.run([]string{"lease", "--repo", linked, "-b", "from-x", "--name", "fx"})
	o, _ := os.ReadFile(c.out.Name())
	m := decode(t, string(o))
	if code != 0 || m["head"] != strings.TrimSpace(string(xHead)) {
		t.Fatalf("exit %d head %v, want X's head %s", code, m["head"], xHead)
	}
}

func TestPathInNoRepositoryIsUsage(t *testing.T) {
	c := newCLI(t)
	code := c.a.run([]string{"lease", "--repo", t.TempDir(), "--name", "n", "--detach", "--json"})
	o, _ := os.ReadFile(c.out.Name())
	if code != 2 || decode(t, string(o))["error"].(map[string]any)["name"] != "usage" {
		t.Errorf("exit %d: %s", code, o)
	}
}

// snapshot is the state directory's bytes (minus the event log and the
// per-operation holds), the worktree registrations and the refs.
func snapshot(t *testing.T, c *cli) string {
	t.Helper()
	var b strings.Builder
	common := c.git("rev-parse", "--path-format=absolute", "--git-common-dir")
	dir := filepath.Join(common, "pg-gwm")
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(dir, p)
		if err != nil || d.IsDir() || strings.HasPrefix(rel, "events-") || strings.HasPrefix(rel, "ops"+string(filepath.Separator)) {
			return nil
		}
		data, _ := os.ReadFile(p)
		b.WriteString(rel + "\n" + string(data) + "\n")
		return nil
	})
	b.WriteString(c.git("worktree", "list", "--porcelain"))
	b.WriteString(c.git("for-each-ref"))
	return b.String()
}

func TestRefusalChangesNothing(t *testing.T) {
	c := newCLI(t)
	main := c.git("branch", "--show-current")
	if code, _, e := c.run("lease", "--name", "held", "-b", "held"); code != 0 {
		t.Fatalf("setup: %d %s", code, e)
	}
	_, dout, _ := c.run("lease", "--name", "dirty", "-b", "dirty", "--json")
	if err := os.WriteFile(filepath.Join(decode(t, dout)["worktree_path"].(string), "unsaved.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		code int
	}{
		{"renew usage", []string{"renew", "--name", "held", "--ttl", "none"}, 2},
		{"renew zero ttl", []string{"renew", "--name", "held", "--ttl", "0s"}, 2},
		{"renew no key", []string{"renew"}, 2},
		{"renew not-found", []string{"renew", "--name", "nobody"}, 4},
		{"renew id not-found", []string{"renew", "l-nope000000"}, 4},
		{"release not-found", []string{"release", "--name", "nobody"}, 4},
		{"release dirty", []string{"release", "--name", "dirty"}, 23},
		{"expire not-found", []string{"expire", "--name", "nobody"}, 4},
		{"info not-found", []string{"info", "nobody"}, 4},
		{"info no key", []string{"info"}, 2},
		{"usage", []string{"lease"}, 2},
		{"name-held", []string{"lease", "--name", "held", "--detach"}, 21},
		{"branch-in-use", []string{"lease", main}, 22},
		{"ref-unresolvable", []string{"lease", "-b", "nb", "no-such-ref"}, 26},
		{"usage", []string{"lease", "-b", "held"}, 2},
		{"usage", []string{"lease", "-b", "pg-gwm/x"}, 2},
	}
	for _, tc := range cases {
		before := snapshot(t, c)
		code, _, _ := c.run(tc.args...)
		if code != tc.code {
			t.Errorf("%v: exit %d, want %d", tc.args, code, tc.code)
		}
		if after := snapshot(t, c); after != before {
			t.Errorf("%v changed something:\n--- before\n%s\n--- after\n%s", tc.args, before, after)
		}
	}
	snap, err := c.a.counters.Read()
	if err != nil {
		t.Fatal(err)
	}
	rc := snap.Repos[c.a.label]
	if rc == nil || rc.Refusals["name-held"] != 1 || rc.Refusals["branch-in-use"] != 1 || rc.Refusals["ref-unresolvable"] != 1 || rc.Refusals["usage"] < 3 {
		t.Errorf("counters.json refusals = %+v", rc)
	}
}

func TestMaxReachedAndLowDiskRefuseCleanly(t *testing.T) {
	c := newCLI(t)
	if code, _, e := c.run("lease", "--name", "prime", "--detach"); code != 0 {
		t.Fatalf("setup: %d %s", code, e)
	}
	main, err := filepath.EvalSymlinks(c.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "pg-gwm", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, cfg string
		code      int
	}{
		{"max-reached", "max = 1\n", 20},
		{"low-disk", "max = 5\nfree-space-floor = 9000000000000000000\n", 29},
	} {
		cfg := "[repos.\"" + main + "\"]\n" + tc.cfg
		if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, c)
		code, out, _ := c.run("lease", "--name", "next", "--detach", "--json")
		if code != tc.code {
			t.Errorf("%s: exit %d: %s", tc.name, code, out)
		}
		if e, _ := decode(t, out)["error"].(map[string]any); e["name"] != tc.name {
			t.Errorf("%s: envelope %s", tc.name, out)
		}
		if snapshot(t, c) != before {
			t.Errorf("%s refusal changed something", tc.name)
		}
	}
}

func TestCompletionScripts(t *testing.T) {
	c := newCLI(t)
	for _, shell := range []string{"bash", "zsh"} {
		code := c.a.run([]string{"completion", shell})
		o, _ := os.ReadFile(c.out.Name())
		out := string(o)
		c.out.Truncate(0)
		c.out.Seek(0, 0)
		if code != 0 {
			t.Fatalf("%s: exit %d", shell, code)
		}
		for _, want := range []string{"lease", "--or-renew", "--print", "--detach", "completion"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s script lacks %q", shell, want)
			}
		}
	}
	if code := c.a.run([]string{"completion", "fish"}); code != 2 {
		t.Errorf("unknown shell exit %d", code)
	}
	if code := c.a.run([]string{"completion"}); code != 2 {
		t.Errorf("missing shell exit %d", code)
	}
}
