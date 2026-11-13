//go:build integration

package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Shape names a repository fixture shape. Every shape has a main branch (the
// TARGET) and a branch feature/x (the BRANCH of a scenario).
type Shape string

// The fixture shapes of the scenario catalogue.
const (
	// ShapeAhead: feature/x is two commits ahead of main; main has not moved.
	ShapeAhead Shape = "ahead"
	// ShapeDiverged: main and feature/x both have commits since the base, on
	// different files, so a rebase is clean.
	ShapeDiverged Shape = "diverged"
	// ShapeConflict: main and feature/x change the same line of app.txt.
	ShapeConflict Shape = "conflict"
	// ShapeMerged: feature/x is already an ancestor of main.
	ShapeMerged Shape = "merged"
	// ShapeCherryPicked: main already holds a patch-equivalent copy of the
	// first commit of feature/x.
	ShapeCherryPicked Shape = "cherry-picked"
	// ShapeLockfile: main and feature/x both change flake.lock, feature/x
	// also adds a source file.
	ShapeLockfile Shape = "lockfile"
	// ShapeOrigin: the ahead shape, cloned from a bare remote (origin.git
	// next to repo). The test advances the remote with AdvanceRemote.
	ShapeOrigin Shape = "origin"
)

// AllShapes lists every shape, in build order.
var AllShapes = []Shape{ShapeAhead, ShapeDiverged, ShapeConflict, ShapeMerged, ShapeCherryPicked, ShapeLockfile, ShapeOrigin}

const (
	fixtureDate = "2026-01-01T00:00:00Z"
	fixtureUnix = "1767225600 +0000"
)

// Facts are the object ids of a built shape, keyed by the ref name relative
// to refs/heads (and "origin/<name>" for remote-tracking refs). Because the
// fixture dates are fixed, the ids are stable across runs and are the same in
// every per-scenario copy.
type Facts struct {
	Tips  map[string]string
	Trees map[string]string
}

// builtShape is one shape built once per process.
type builtShape struct {
	dir   string // holds repo/ (and origin.git/ for ShapeOrigin)
	facts Facts
}

// fiCommit is one commit of a fast-import stream.
type fiCommit struct {
	ref   string // full ref, e.g. refs/heads/main
	mark  int
	from  int // mark of the parent; 0 continues the ref's current tip
	msg   string
	files map[string]string
}

func fastImportStream(commits []fiCommit) []byte {
	var b bytes.Buffer
	for _, c := range commits {
		fmt.Fprintf(&b, "commit %s\nmark :%d\n", c.ref, c.mark)
		ident := "Fixture Author <fixture@example.invalid> " + fixtureUnix
		fmt.Fprintf(&b, "author %s\ncommitter %s\n", ident, ident)
		fmt.Fprintf(&b, "data %d\n%s\n", len(c.msg), c.msg)
		if c.from != 0 {
			fmt.Fprintf(&b, "from :%d\n", c.from)
		}
		names := make([]string, 0, len(c.files))
		for n := range c.files {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(&b, "M 644 inline %s\ndata %d\n%s\n", n, len(c.files[n]), c.files[n])
		}
		b.WriteString("\n")
	}
	b.WriteString("done\n")
	return b.Bytes()
}

const (
	baseApp  = "line1\nline2\nline3\n"
	lockV1   = "{\"version\":1}\n"
	lockV2   = "{\"version\":2}\n"
	lockV3   = "{\"version\":3}\n"
	mainRef  = "refs/heads/main"
	featRef  = "refs/heads/feature/x"
	baseMsg  = "base"
	featMsg1 = "feature one"
	featMsg2 = "feature two"
)

func baseFiles() map[string]string {
	return map[string]string{"README.md": "fixture\n", "app.txt": baseApp, "flake.lock": lockV1}
}

// shapeCommits returns the fast-import commits of a shape (ShapeOrigin uses
// the ahead commits and is cloned afterwards).
func shapeCommits(s Shape) []fiCommit {
	base := fiCommit{ref: mainRef, mark: 1, msg: baseMsg, files: baseFiles()}
	f1 := fiCommit{ref: featRef, mark: 2, from: 1, msg: featMsg1, files: map[string]string{"x1.txt": "one\n"}}
	f2 := fiCommit{ref: featRef, mark: 3, msg: featMsg2, files: map[string]string{"x2.txt": "two\n"}}
	switch s {
	case ShapeAhead, ShapeOrigin:
		return []fiCommit{base, f1, f2}
	case ShapeDiverged:
		m2 := fiCommit{ref: mainRef, mark: 3, msg: "main two", files: map[string]string{"m.txt": "m\n"}}
		return []fiCommit{base, f1, m2}
	case ShapeConflict:
		f := fiCommit{ref: featRef, mark: 2, from: 1, msg: featMsg1, files: map[string]string{"app.txt": "line1\nfeature\nline3\n"}}
		m2 := fiCommit{ref: mainRef, mark: 3, msg: "main two", files: map[string]string{"app.txt": "line1\nmain\nline3\n"}}
		return []fiCommit{base, f, m2}
	case ShapeMerged:
		m2 := fiCommit{ref: mainRef, mark: 3, from: 2, msg: "main two", files: map[string]string{"m.txt": "m\n"}}
		return []fiCommit{base, f1, m2}
	case ShapeCherryPicked:
		m2 := fiCommit{ref: mainRef, mark: 4, from: 1, msg: "main two", files: map[string]string{"m.txt": "m\n"}}
		pick := fiCommit{ref: mainRef, mark: 5, msg: featMsg1, files: map[string]string{"x1.txt": "one\n"}}
		return []fiCommit{base, f1, f2, m2, pick}
	case ShapeLockfile:
		f := fiCommit{ref: featRef, mark: 2, from: 1, msg: featMsg1, files: map[string]string{"flake.lock": lockV3, "src.txt": "src\n"}}
		m2 := fiCommit{ref: mainRef, mark: 3, msg: "main bumps the lock", files: map[string]string{"flake.lock": lockV2}}
		return []fiCommit{base, f, m2}
	}
	panic("unknown shape " + string(s))
}

// gitEnvBase is the hermetic environment of the harness and of every fixture
// git command: HOME and the XDG directories live under home; the global git
// config is a fixture file; the system config is off; dates are fixed.
func gitEnvBase(home, globalCfg string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"TMPDIR=" + filepath.Join(home, "tmp"),
		"GIT_CONFIG_GLOBAL=" + globalCfg,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_DATE=" + fixtureDate,
		"GIT_COMMITTER_DATE=" + fixtureDate,
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	}
}

const globalConfigText = `[user]
	name = Fixture User
	email = fixture@example.invalid
[init]
	defaultBranch = main
[commit]
	gpgsign = false
[tag]
	gpgsign = false
[advice]
	detachedHead = false
`

// runGit runs git in dir under env and returns stdout; a failure is an error
// carrying stderr.
func runGit(env []string, dir string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s in %s: %v: %s", strings.Join(args, " "), dir, err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// buildShape builds one shape under root with git fast-import.
func buildShape(env []string, root string, s Shape) (*builtShape, error) {
	dir := filepath.Join(root, "shape-"+string(s))
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return nil, err
	}
	src := repo
	if s == ShapeOrigin {
		src = filepath.Join(dir, "seed")
		if err := os.MkdirAll(src, 0o755); err != nil {
			return nil, err
		}
	}
	if _, err := runGit(env, src, nil, "init", "-q", "-b", "main"); err != nil {
		return nil, err
	}
	if _, err := runGit(env, src, fastImportStream(shapeCommits(s)), "fast-import", "--quiet", "--done"); err != nil {
		return nil, err
	}
	if _, err := runGit(env, src, nil, "reset", "-q", "--hard", "main"); err != nil {
		return nil, err
	}
	if s == ShapeOrigin {
		bare := filepath.Join(dir, "origin.git")
		if _, err := runGit(env, dir, nil, "clone", "-q", "--bare", src, bare); err != nil {
			return nil, err
		}
		if err := os.RemoveAll(repo); err != nil {
			return nil, err
		}
		if _, err := runGit(env, dir, nil, "clone", "-q", bare, repo); err != nil {
			return nil, err
		}
		if _, err := runGit(env, repo, nil, "branch", "feature/x", "origin/feature/x"); err != nil {
			return nil, err
		}
		if err := os.RemoveAll(src); err != nil {
			return nil, err
		}
	}
	facts := Facts{Tips: map[string]string{}, Trees: map[string]string{}}
	refs := []string{"main", "feature/x"}
	if s == ShapeOrigin {
		refs = append(refs, "origin/main", "origin/feature/x")
	}
	for _, r := range refs {
		tip, err := runGit(env, repo, nil, "rev-parse", "--verify", "--quiet", r+"^{commit}")
		if err != nil {
			continue
		}
		tree, err := runGit(env, repo, nil, "rev-parse", r+"^{tree}")
		if err != nil {
			return nil, err
		}
		facts.Tips[r], facts.Trees[r] = strings.TrimSpace(tip), strings.TrimSpace(tree)
	}
	return &builtShape{dir: dir, facts: facts}, nil
}

// copyTree copies src to dst, cloning copy-on-write where the platform
// offers it (cp -c on macOS APFS, --reflink=auto on Linux) and falling back
// to a plain recursive copy.
func copyTree(src, dst string) error {
	var attempts [][]string
	switch runtime.GOOS {
	case "darwin":
		attempts = append(attempts, []string{"-c", "-R", src, dst})
	case "linux":
		attempts = append(attempts, []string{"-a", "--reflink=auto", src, dst})
	}
	attempts = append(attempts, []string{"-R", src, dst})
	var last error
	for _, a := range attempts {
		_ = os.RemoveAll(dst)
		if out, err := exec.CommandContext(context.Background(), "cp", a...).CombinedOutput(); err != nil {
			last = fmt.Errorf("cp %s: %v: %s", strings.Join(a, " "), err, out)
			continue
		}
		return nil
	}
	return last
}
