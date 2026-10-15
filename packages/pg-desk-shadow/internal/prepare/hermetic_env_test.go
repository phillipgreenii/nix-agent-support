package prepare

// Guard for goBuildEnv (bead pg2-rxs4j): the one child of seed_integration_test.go
// that needs the toolchain state of the parent (go build of the sibling pg-desk
// module) is given an ALLOWLIST-built environment, so a test run from inside a
// git hook cannot make Go stamp the OUTER repository's revision into the binary.
// The other children (pg-desk, the stand-in pg-connector) already receive the
// literal world.env allowlist.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// goBuildEnv is the environment for go build: PATH, scratch/locale/user names,
// the real HOME (GOCACHE and GOPATH default under it) and the Go toolchain
// variables. It is an allowlist, never a scrub: nothing that selects a git
// repository (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, ...) can reach the child.
func goBuildEnv(parent []string) []string {
	var out []string
	for _, kv := range parent {
		k, _, _ := strings.Cut(kv, "=")
		if goBuildPassthrough[k] || strings.HasPrefix(k, "LC_") {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_CONFIG_NOSYSTEM=1")
}

var goBuildPassthrough = map[string]bool{
	"PATH": true, "TMPDIR": true, "HOME": true, "USER": true, "LOGNAME": true,
	"LANG": true, "TZ": true, "TERM": true,
	"GOCACHE": true, "GOMODCACHE": true, "GOPATH": true, "GOFLAGS": true,
	"GOPROXY": true, "GONOPROXY": true, "GOSUMDB": true, "GONOSUMDB": true,
	"GOPRIVATE": true, "GOTOOLCHAIN": true, "GOROOT": true, "GOENV": true,
	"GOWORK": true, "GOOS": true, "GOARCH": true, "CGO_ENABLED": true,
}

var hookLeakEnv = []string{
	"GIT_DIR=/outer/repo/.git",
	"GIT_WORK_TREE=/outer/repo",
	"GIT_INDEX_FILE=/outer/repo/.git/index",
	"GIT_COMMON_DIR=/outer/repo/.git",
	"GIT_OBJECT_DIRECTORY=/outer/repo/.git/objects",
	"GIT_PREFIX=packages/pg-desk-shadow/",
	"GIT_CEILING_DIRECTORIES=/outer",
	"GIT_ANYTHING_FUTURE=1",
	"BEADS_DIR=/outer/repo/.beads",
	"WORKSPACE_ROOT=/outer",
}

func TestGoBuildEnv_CarriesToolchainButNoGitSelector(t *testing.T) {
	parent := append([]string{
		"PATH=/usr/bin", "HOME=/real/home", "GOCACHE=/gc", "GOFLAGS=-mod=mod",
		"LC_ALL=C", "NOT_GO=1", "GIT_CONFIG_NOSYSTEM=0",
	}, hookLeakEnv...)
	have := map[string]string{}
	for _, kv := range goBuildEnv(parent) {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := have[k]; dup {
			t.Errorf("%s appears twice in the go build environment", k)
		}
		have[k] = v
	}
	for k, v := range map[string]string{
		"PATH": "/usr/bin", "HOME": "/real/home", "GOCACHE": "/gc", "GOFLAGS": "-mod=mod",
		"LC_ALL": "C", "GIT_CONFIG_NOSYSTEM": "1",
	} {
		if have[k] != v {
			t.Errorf("%s = %q, want %q", k, have[k], v)
		}
	}
	for k := range have {
		if (strings.HasPrefix(k, "GIT_") && k != "GIT_CONFIG_NOSYSTEM") || strings.HasPrefix(k, "BEADS_") || k == "WORKSPACE_ROOT" || k == "NOT_GO" {
			t.Errorf("%s leaked into the go build environment", k)
		}
	}
}

// foreignRepo is another git repository with one commit; it returns its
// directory and HEAD revision. Built with real git under a private HOME.
func foreignRepo(t *testing.T) (dir, revision string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir = t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com",
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "a.txt")
	git("commit", "-q", "-m", "c1", "--no-gpg-sign")
	return dir, git("rev-parse", "HEAD")
}

func foreignGitEnv(repo string) []string {
	return []string{
		"GIT_DIR=" + filepath.Join(repo, ".git"),
		"GIT_WORK_TREE=" + repo,
		"GIT_INDEX_FILE=" + filepath.Join(repo, ".git", "index"),
	}
}

func vcsRevision(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command("go", "version", "-m", bin).CombinedOutput()
	if err != nil {
		t.Fatalf("go version -m: %v\n%s", err, out)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if f := strings.Fields(l); len(f) == 2 && strings.HasPrefix(f[1], "vcs.revision=") {
			return strings.TrimPrefix(f[1], "vcs.revision=")
		}
	}
	return ""
}

// siblingPgDesk resolves the sibling module source or skips (as buildPgDesk does).
func siblingPgDesk(t *testing.T) string {
	t.Helper()
	src, _ := filepath.Abs("../../../pg-desk")
	if _, err := os.Stat(filepath.Join(src, "go.mod")); err != nil {
		t.Skip("sibling pg-desk module not present (isolated build)")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain missing")
	}
	return src
}

// TestGoBuildPgDesk_ForeignGitEnv_IsNotStampedWithTheForeignRepo builds the
// sibling pg-desk module with GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE
// exported to ANOTHER repository and asserts Go did not stamp that repository's
// revision into the binary.
func TestGoBuildPgDesk_ForeignGitEnv_IsNotStampedWithTheForeignRepo(t *testing.T) {
	src := siblingPgDesk(t)
	foreign, foreignRev := foreignRepo(t)
	for _, kv := range foreignGitEnv(foreign) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	bin := goBuildPgDesk(t, src, goBuildEnv(os.Environ()))
	if got := vcsRevision(t, bin); got == foreignRev {
		t.Fatalf("pg-desk was stamped with the foreign repository's revision %s: the build inherited its GIT_* environment", got)
	}
}

// TestGoBuildPgDesk_ForeignGitEnv_NegativeControl: the SAME environment, handed
// to a go build that inherits it, stamps the foreign revision. If Go ever stops
// following GIT_DIR this fails and says so, instead of the guard above passing
// for no reason.
func TestGoBuildPgDesk_ForeignGitEnv_NegativeControl(t *testing.T) {
	src := siblingPgDesk(t)
	foreign, foreignRev := foreignRepo(t)
	bin := goBuildPgDesk(t, src, append(goBuildEnv(os.Environ()), foreignGitEnv(foreign)...))
	if got := vcsRevision(t, bin); got != foreignRev {
		t.Fatalf("a go build that inherits GIT_DIR was stamped %q, not the foreign revision %s: the guard above proves nothing (revisit pg2-rxs4j)", got, foreignRev)
	}
}
