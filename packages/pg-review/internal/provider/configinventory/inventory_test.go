package configinventory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/provider/ccaudit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/schema"
)

const (
	since = "2026-02-01T00:00:00Z"
	until = "2026-02-15T00:00:00Z"
)

var oldTime = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// copyTree copies the synthetic tree, keeping symlinks as symlinks, and sets
// every file's modification time to oldTime.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, out)
		case d.IsDir():
			return os.MkdirAll(out, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			return err
		}
		return os.Chtimes(out, oldTime, oldTime)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeOld(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
}

type env struct {
	root, home, userDir string
}

func newEnv(t *testing.T) env {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	copyTree(t, filepath.Join("..", "..", "..", "testdata", "provider-config-inventory", "tree"), root)
	var big strings.Builder
	for i := 0; i < 520; i++ {
		fmt.Fprintf(&big, "synthetic line %d\n", i)
	}
	writeOld(t, filepath.Join(root, "home", ".claude", "skills", "bulky", "SKILL.md"), "---\nname: bulky\n---\n"+big.String())
	writeOld(t, filepath.Join(root, "proj-a", ".claude", "memory", "big.md"), strings.Repeat("a synthetic memory line\n", 250))
	return env{root: root, home: filepath.Join(root, "home"), userDir: filepath.Join(root, "home", ".claude")}
}

func fakeExec(t *testing.T, args *[][]string) ccaudit.Exec {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "provider-config-inventory", "skill-calls.json"))
	if err != nil {
		t.Fatal(err)
	}
	return func(_ context.Context, a []string) ([]byte, error) {
		if args != nil {
			*args = append(*args, a)
		}
		return b, nil
	}
}

func (e env) opts(t *testing.T, scope, proj string, layers ...string) Options {
	return Options{
		Since: since, Until: until, Scope: scope, ScopePrefixes: []string{filepath.Join(e.root, proj)},
		Layers: layers, UserDir: e.userDir, Home: e.home, Exec: fakeExec(t, nil),
	}
}

func byKey(rows []ccaudit.Row) map[string]ccaudit.Row {
	m := map[string]ccaudit.Row{}
	for _, r := range rows {
		m[r.Kind+"|"+r.SubjectKey] = r
	}
	return m
}

func mustRows(t *testing.T, o Options) []ccaudit.Row {
	t.Helper()
	rows, err := Rows(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestOneFixturePerKind(t *testing.T) {
	e := newEnv(t)
	rows := mustRows(t, e.opts(t, "alpha", "proj-a", "user", "rules", "project"))
	m := byKey(rows)
	want := map[string]string{ // kind|subject -> defined_in
		"stale-reference|ref:user:CLAUDE.md->docs/missing-guide.md": "user",
		"stale-reference|ref:project:CLAUDE.md->docs/absent.md":     "project",
		"stale-reference|link:project:.claude/skills/ghost":         "project",
		"conflict|setting:model":                                    "project",
		"conflict|setting:env.MODE":                                 "project",
		"conflict|skill-name:popular":                               "project",
		"oversize|skill:user:bulky":                                 "user",
		"oversize|memory:project:.claude/memory/big.md":             "project",
		"unused|skill:user:lonely":                                  "user",
		"unused|skill:user:bulky":                                   "user",
	}
	for k, in := range want {
		r, ok := m[k]
		if !ok {
			t.Errorf("missing row %s", k)
			continue
		}
		if r.DefinedIn != in {
			t.Errorf("%s: defined_in %q, want %q", k, r.DefinedIn, in)
		}
		if r.Query != QueryName || r.Scope != "alpha" {
			t.Errorf("%s: query %q scope %q", k, r.Query, r.Scope)
		}
	}
	var dup *ccaudit.Row
	for i, r := range rows {
		if r.Kind == "duplicate" {
			dup = &rows[i]
		}
	}
	if dup == nil {
		t.Fatal("no duplicate row")
	}
	if got := strings.Join(dup.Samples, ","); got != "project:.claude/memory/notes-copy.md,user:memory/notes.md" {
		t.Errorf("duplicate samples %q", got)
	}
	for _, not := range []string{
		"stale-reference|ref:user:CLAUDE.md->docs/present.md",
		"stale-reference|ref:user:CLAUDE.md->docs/inside-a-fence-is-ignored.md",
		"conflict|setting:theme",
		"conflict|setting:permissions.allow",
		"unused|skill:user:popular",
		"unused|skill:project:.claude/skills/popular",
	} {
		if _, ok := m[not]; ok {
			t.Errorf("unexpected row %s", not)
		}
	}
	// The thresholds are inclusive limits: a file at the limit is not oversize.
	if _, ok := m["oversize|instruction:user:CLAUDE.md"]; ok {
		t.Error("small instruction file flagged oversize")
	}
}

func TestSymlinkedSkillResolvesToTargetLayer(t *testing.T) {
	e := newEnv(t)
	rows := mustRows(t, e.opts(t, "alpha", "proj-a", "project"))
	r, ok := byKey(rows)["unused|skill:user:shared"]
	if !ok {
		t.Fatalf("symlinked skill row missing: %v", keys(rows))
	}
	if r.DefinedIn != "user" {
		t.Errorf("defined_in %q, want user", r.DefinedIn)
	}
	for k, r := range byKey(rows) {
		if strings.Contains(k, "lonely") {
			t.Errorf("user layer is not loaded, but %s appeared", k)
		}
		_ = r
	}
}

func keys(rows []ccaudit.Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Kind+"|"+r.SubjectKey)
	}
	return out
}

func TestSkillSeenFromTwoScopesIsOneSubject(t *testing.T) {
	e := newEnv(t)
	a := byKey(mustRows(t, e.opts(t, "alpha", "proj-a", "project")))["unused|skill:user:shared"]
	b := byKey(mustRows(t, e.opts(t, "beta", "proj-b", "user")))["unused|skill:user:shared"]
	if a.SubjectKey == "" || a.SubjectKey != b.SubjectKey || a.DefinedIn != b.DefinedIn {
		t.Fatalf("subjects differ: %+v vs %+v", a, b)
	}
	if a.Scope == b.Scope {
		t.Errorf("both occurrences carry scope %q", a.Scope)
	}
}

func TestOnlyListedLayersLoad(t *testing.T) {
	e := newEnv(t)
	rows := mustRows(t, e.opts(t, "beta", "proj-b", "rules"))
	if len(rows) != 0 {
		t.Errorf("rules-only scope with clean rules produced %v", keys(rows))
	}
	rows = mustRows(t, e.opts(t, "beta", "proj-b"))
	if len(rows) != 0 {
		t.Errorf("no layers produced %v", keys(rows))
	}
}

func TestIdsAndOutputAreDeterministicAndSchemaValid(t *testing.T) {
	e := newEnv(t)
	var outs [2]bytes.Buffer
	for i := range outs {
		if err := Run(context.Background(), e.opts(t, "alpha", "proj-a", "user", "rules", "project"), &outs[i]); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(outs[0].Bytes(), outs[1].Bytes()) {
		t.Error("two runs differ")
	}
	if err := schema.Validate("evidence-rows", outs[0].Bytes()); err != nil {
		t.Error(err)
	}
	if !strings.Contains(outs[0].String(), `"id": "E-001"`) {
		t.Error("ids do not start at E-001")
	}
	for _, bad := range []string{e.root, "/Users/", "/home/", "/private/"} {
		if strings.Contains(outs[0].String(), bad) {
			t.Errorf("output carries absolute path fragment %q", bad)
		}
	}
}

func TestAbsoluteReferenceTargetsAreNotShownAbsolute(t *testing.T) {
	e := newEnv(t)
	writeOld(t, filepath.Join(e.root, "proj-b", "CLAUDE.md"),
		"See [one]("+filepath.Join(e.root, "proj-b", "gone.md")+") and [two](/srv/elsewhere/other.md) and [three](~/gone-in-home.md).\n")
	m := byKey(mustRows(t, e.opts(t, "beta", "proj-b", "project")))
	for _, k := range []string{
		"stale-reference|ref:project:CLAUDE.md->gone.md",
		"stale-reference|ref:project:CLAUDE.md-><abs>/other.md",
		"stale-reference|ref:project:CLAUDE.md->~/gone-in-home.md",
	} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing %s; have %v", k, m)
		}
	}
}

func TestSkillCallsArgsAndUTCNormalisation(t *testing.T) {
	e := newEnv(t)
	var calls [][]string
	o := e.opts(t, "alpha", "proj-a", "user")
	o.Since, o.Until = "2026-01-31T19:00:00-05:00", "2026-02-14T19:00:00-05:00"
	o.ExcludeCwdPrefixes = []string{"/srv/neutral"}
	o.Exec = fakeExec(t, &calls)
	mustRows(t, o)
	if len(calls) != 1 {
		t.Fatalf("%d pg-ccaudit calls, want 1", len(calls))
	}
	got := strings.Join(calls[0], " ")
	want := "query skill-calls --since 2026-02-01T00:00:00Z --until 2026-02-15T00:00:00Z --format json --no-staleness --cwd-prefix " +
		filepath.Join(e.root, "proj-a") + " --exclude-cwd-prefix /srv/neutral"
	if got != want {
		t.Errorf("args\n got %s\nwant %s", got, want)
	}
}

func TestUnusedNeedsALongWindowAndAnOlderFile(t *testing.T) {
	e := newEnv(t)
	var calls [][]string
	o := e.opts(t, "alpha", "proj-a", "user")
	o.Until = "2026-02-05T00:00:00Z" // 4 days, under MinUnusedWindow
	o.Exec = fakeExec(t, &calls)
	for _, r := range mustRows(t, o) {
		if r.Kind == "unused" {
			t.Errorf("short window produced %s", r.SubjectKey)
		}
	}
	if len(calls) != 0 {
		t.Error("short window still queried skill-calls")
	}
	// A skill created inside the window is not yet unused.
	fresh := filepath.Join(e.userDir, "skills", "lonely", "SKILL.md")
	in := time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(fresh, in, in); err != nil {
		t.Fatal(err)
	}
	if _, ok := byKey(mustRows(t, e.opts(t, "alpha", "proj-a", "user")))["unused|skill:user:lonely"]; ok {
		t.Error("skill newer than the window start flagged unused")
	}
}

func TestQueryFailureFailsTheCall(t *testing.T) {
	e := newEnv(t)
	o := e.opts(t, "alpha", "proj-a", "user")
	o.Exec = func(context.Context, []string) ([]byte, error) { return nil, errors.New("boom") }
	if _, err := Rows(context.Background(), o); err == nil || errors.Is(err, ErrUsage) {
		t.Errorf("err = %v, want a non-usage failure", err)
	}
}

func TestUsageAndReadErrors(t *testing.T) {
	e := newEnv(t)
	cases := map[string]func(*Options){
		"unknown layer":     func(o *Options) { o.Layers = []string{"nope"} },
		"project no root":   func(o *Options) { o.ScopePrefixes = nil; o.Layers = []string{"project"} },
		"until before":      func(o *Options) { o.Until = "2026-01-01T00:00:00Z" },
		"offset-less since": func(o *Options) { o.Since = "2026-02-01" },
		"no scope":          func(o *Options) { o.Scope = "" },
	}
	for name, mod := range cases {
		o := e.opts(t, "alpha", "proj-a", "user")
		mod(&o)
		if _, err := Rows(context.Background(), o); !errors.Is(err, ErrUsage) {
			t.Errorf("%s: err = %v, want usage error", name, err)
		}
	}
	writeOld(t, filepath.Join(e.userDir, "settings.json"), "{not json")
	if _, err := Rows(context.Background(), e.opts(t, "alpha", "proj-a", "user")); err == nil || errors.Is(err, ErrUsage) {
		t.Errorf("malformed settings: err = %v, want a failure that is not a usage error", err)
	}
}

func TestPerProjectMemoryNeverShowsItsSlug(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.userDir, "projects", slugRE.ReplaceAllString(filepath.Join(e.root, "proj-b"), "-"), "memory")
	writeOld(t, filepath.Join(dir, "big.md"), strings.Repeat("a synthetic memory line\n", 250))
	rows := mustRows(t, e.opts(t, "beta", "proj-b", "user"))
	if _, ok := byKey(rows)["oversize|memory:user:project-data/memory/big.md"]; !ok {
		t.Fatalf("per-project memory row missing: %v", keys(rows))
	}
}
