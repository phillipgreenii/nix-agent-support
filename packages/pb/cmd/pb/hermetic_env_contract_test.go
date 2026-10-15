//go:build contract

package main

// Guards for hermeticEnviron / goBuildEnviron (contract_test.go), bead
// pg2-rxs4j: no name that selects a git repository or a beads workspace may
// reach a real bd/git/pn/pb/go child, whatever the parent process exports.

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// hookLeakEnv is a git-hook-shaped environment plus every workspace binding a
// test process could have inherited, all pointing at "another repository".
var hookLeakEnv = []string{
	"GIT_DIR=/outer/repo/.git",
	"GIT_WORK_TREE=/outer/repo",
	"GIT_INDEX_FILE=/outer/repo/.git/index",
	"GIT_COMMON_DIR=/outer/repo/.git",
	"GIT_OBJECT_DIRECTORY=/outer/repo/.git/objects",
	"GIT_PREFIX=packages/pb/",
	"GIT_CEILING_DIRECTORIES=/outer",
	"GIT_ANYTHING_FUTURE=1",
	"BEADS_DIR=/outer/repo/.beads",
	"BEADS_DOLT_SERVER_PORT=25252",
	"BD_ACTOR=outer",
	"WORKSPACE_ROOT=/outer",
	"ZR_MACHINE_SUPPORT_WORKSPACE_ROOT=/outer",
}

// allowedGitNames are the GIT_* names a child legitimately receives: the
// deterministic identity/editors isolate() sets, and the system-config pin.
var allowedGitNames = map[string]bool{
	"GIT_AUTHOR_NAME": true, "GIT_AUTHOR_EMAIL": true,
	"GIT_COMMITTER_NAME": true, "GIT_COMMITTER_EMAIL": true,
	"GIT_EDITOR": true, "GIT_SEQUENCE_EDITOR": true,
	"GIT_CONFIG_NOSYSTEM": true,
}

func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	have := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := have[k]; dup {
			t.Errorf("%s appears twice in the child environment", k)
		}
		have[k] = v
	}
	return have
}

func assertNoRepoSelectors(t *testing.T, have map[string]string) {
	t.Helper()
	for _, kv := range hookLeakEnv {
		k, _, _ := strings.Cut(kv, "=")
		if v, leaked := have[k]; leaked {
			t.Errorf("%s=%s leaked into the child environment", k, v)
		}
	}
	for k := range have {
		if strings.HasPrefix(k, "GIT_") && !allowedGitNames[k] {
			t.Errorf("unexpected GIT_ variable %s in the child environment", k)
		}
	}
}

// TestHermeticEnvFrom_DropsEverythingOffTheAllowlist is the unit-tier guard: a
// git-hook environment (and any other workspace binding) in the parent MUST NOT
// reach the child, and the names the child does need MUST still arrive.
func TestHermeticEnvFrom_DropsEverythingOffTheAllowlist(t *testing.T) {
	parent := append([]string{
		"PATH=/usr/bin", "HOME=/iso/home", "TMPDIR=/tmp/x", "BD_JSON_ENVELOPE=1",
		"GIT_AUTHOR_NAME=t", "GIT_EDITOR=true", "NIX_REMOTE=daemon",
		"LC_ALL=C", "SOMETHING_ELSE=1", "GIT_CONFIG_NOSYSTEM=0", "BEADS_DOLT_AUTO_START=1",
	}, hookLeakEnv...)
	have := envMap(t, hermeticEnvFrom(parent))
	assertNoRepoSelectors(t, have)
	if _, leaked := have["SOMETHING_ELSE"]; leaked {
		t.Error("a name off the allowlist leaked")
	}
	want := map[string]string{
		"PATH": "/usr/bin", "HOME": "/iso/home", "TMPDIR": "/tmp/x", "BD_JSON_ENVELOPE": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_EDITOR": "true", "NIX_REMOTE": "daemon",
		"LC_ALL": "C", "GIT_CONFIG_NOSYSTEM": "1", "BEADS_DOLT_AUTO_START": "0",
	}
	for k, v := range want {
		if have[k] != v {
			t.Errorf("%s = %q, want %q", k, have[k], v)
		}
	}
}

// TestHermeticEnviron_DropsAHookEnvironmentOfTheProcess drives the helper the
// tests actually call, with a hook-shaped environment in the real process
// environment. The old helper removed six enumerated GIT_* names; it fails here
// on every other name (GIT_CEILING_DIRECTORIES, GIT_ANYTHING_FUTURE, BD_ACTOR,
// ...), which is the point of an allowlist.
func TestHermeticEnviron_DropsAHookEnvironmentOfTheProcess(t *testing.T) {
	for _, kv := range hookLeakEnv {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	t.Setenv("SOMETHING_ELSE", "1")
	have := envMap(t, hermeticEnviron())
	assertNoRepoSelectors(t, have)
	if _, leaked := have["SOMETHING_ELSE"]; leaked {
		t.Error("a name off the allowlist leaked")
	}
	if have["PATH"] == "" {
		t.Error("PATH must still reach the child")
	}
}

// TestGoBuildEnviron_CarriesToolchainButNoGit pins the one place the parent's
// own toolchain state is passed through, and that a git selector never is.
func TestGoBuildEnviron_CarriesToolchainButNoGit(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("NOT_GO", "1")
	for _, kv := range hookLeakEnv {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	have := envMap(t, goBuildEnviron())
	if have["GOFLAGS"] != "-mod=mod" || have["PATH"] == "" {
		t.Errorf("go build env lost a toolchain variable: %v", have)
	}
	assertNoRepoSelectors(t, have)
	if _, leaked := have["NOT_GO"]; leaked {
		t.Error("NOT_GO leaked into the go build environment")
	}
}

// foreignGitRepo is another repository with one commit and no beads workspace.
func foreignGitRepo(t *testing.T) (dir, revision string) {
	t.Helper()
	skipNoBin(t, "git")
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "pb-contract-foreign"})
	if _, err := repo.Commit(t.Context(), "c1", map[string]string{"a.txt": "x\n"}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	return repo.Dir, strings.TrimSpace(shellOut(t, repo.Dir, "git", "rev-parse", "HEAD"))
}

// foreignRepoWithBeads is another repository with a beads workspace in it. It
// calls isolate(t), so it MUST run after any go build that needs the real HOME.
func foreignRepoWithBeads(t *testing.T) string {
	t.Helper()
	skipNoBin(t, "bd", "git")
	dir, _ := foreignGitRepo(t)
	isolate(t)
	bdInit(t, dir, "foreign")
	return dir
}

// foreignGitEnv is what a git hook run inside repo exports.
func foreignGitEnv(repo string) []string {
	return []string{
		"GIT_DIR=" + filepath.Join(repo, ".git"),
		"GIT_WORK_TREE=" + repo,
		"GIT_INDEX_FILE=" + filepath.Join(repo, ".git", "index"),
	}
}

func setForeignGitEnv(t *testing.T, repo string) {
	t.Helper()
	for _, kv := range foreignGitEnv(repo) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
}

// vcsRevision is the vcs.revision Go stamped into bin ("" when unstamped).
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

// TestContract_ForeignGitEnv_PbBuildIsNotStampedWithTheForeignRepo builds pb
// (the go build child of the unstick contract suite) with GIT_DIR,
// GIT_WORK_TREE and GIT_INDEX_FILE exported to ANOTHER repository and asserts
// Go did not stamp that repository's revision into the binary.
func TestContract_ForeignGitEnv_PbBuildIsNotStampedWithTheForeignRepo(t *testing.T) {
	skipNoBin(t, "go")
	foreign, foreignRev := foreignGitRepo(t)
	setForeignGitEnv(t, foreign)
	bin := pbBin(t)
	if got := vcsRevision(t, bin); got == foreignRev {
		t.Fatalf("pb was stamped with the foreign repository's revision %s: the build inherited its GIT_* environment", got)
	}
}

// TestContract_ForeignGitEnv_PbBuildNegativeControl: the SAME environment,
// handed to a go build that inherits it, stamps the foreign revision. If Go
// ever stops following GIT_DIR this fails and says so, instead of the guard
// above passing for no reason.
func TestContract_ForeignGitEnv_PbBuildNegativeControl(t *testing.T) {
	skipNoBin(t, "go")
	foreign, foreignRev := foreignGitRepo(t)
	bin := filepath.Join(t.TempDir(), "pb")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = append(goBuildEnviron(), foreignGitEnv(foreign)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build under the foreign git environment: %v\n%s", err, out)
	}
	if got := vcsRevision(t, bin); got != foreignRev {
		t.Fatalf("a go build that inherits GIT_DIR was stamped %q, not the foreign revision %s: the guard above proves nothing (revisit pg2-rxs4j)", got, foreignRev)
	}
}

// TestContract_ForeignGitEnv_RealToolContractsStillPass runs the real bd gate
// surface and the real unstick sweep with GIT_DIR, GIT_WORK_TREE and
// GIT_INDEX_FILE exported to ANOTHER repository that holds a beads workspace.
// Restoring any inheritance of those names makes the first `bd init` in the
// throwaway workspace fail here.
func TestContract_ForeignGitEnv_RealToolContractsStillPass(t *testing.T) {
	skipNoBin(t, "go", "bd", "git")
	t.Run("BDGateSurface", func(t *testing.T) {
		foreign := foreignRepoWithBeads(t)
		setForeignGitEnv(t, foreign)
		bdGateSurface(t)
	})
	t.Run("UnstickSweep", func(t *testing.T) {
		// The foreign beads workspace needs isolate()'s temp HOME, which hides the
		// real Go caches from the go build inside unstickSweep, so pin them first.
		pinGoEnv(t)
		foreign := foreignRepoWithBeads(t)
		setForeignGitEnv(t, foreign)
		unstickSweep(t)
	})
}

// pinGoEnv exports the toolchain's own cache locations, so a later HOME swap
// (isolate) cannot move them.
func pinGoEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"GOCACHE", "GOMODCACHE", "GOPATH", "GOROOT"} {
		out, err := exec.Command("go", "env", name).Output()
		if err != nil {
			t.Fatalf("go env %s: %v", name, err)
		}
		t.Setenv(name, strings.TrimSpace(string(out)))
	}
}

// TestContract_ForeignGitEnv_StopsABdThatInheritsIt is the negative control for
// the real-tool guard: the SAME environment, handed to a bd that inherits it,
// makes `bd init` in a fresh directory fail. If bd ever stops following GIT_DIR
// this fails and says so.
func TestContract_ForeignGitEnv_StopsABdThatInheritsIt(t *testing.T) {
	foreign := foreignRepoWithBeads(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bd", "init", "--prefix", "leak")
	cmd.Dir = t.TempDir()
	cmd.Env = append(hermeticEnviron(), foreignGitEnv(foreign)...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd init succeeded under a foreign GIT_DIR/GIT_WORK_TREE: the hook environment no longer reaches bd, so the guard above proves nothing (revisit pg2-rxs4j)\n%s", out)
	}
	if !strings.Contains(string(out), "already initialized") {
		t.Fatalf("bd init failed under the foreign git environment, but not with the 'already initialized' refusal pg2-g3204 recorded:\n%s", out)
	}
}
