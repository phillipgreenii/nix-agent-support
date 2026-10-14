package sync

import (
	"os"
	"strings"
	"testing"
)

// hermeticChildEnv builds the environment a test hands to the real bd and
// pg-connector child processes (bead pg2-g3204).
//
// It is an ALLOWLIST, never a scrub. The parent environment contributes only
// the names in hermeticPassthrough (and the LC_* locale family); everything
// else, including every variable of an outer git hook (GIT_DIR,
// GIT_INDEX_FILE, GIT_WORK_TREE, GIT_COMMON_DIR, ...) and every workspace
// binding (BEADS_*, BD_*, WORKSPACE_ROOT, ...), is dropped by construction. An
// enumerated deny list is only as complete as its author's memory: the
// previous one named BEADS_* but not GIT_*, so under the commit hook bd in the
// throwaway workspace followed GIT_DIR/GIT_WORK_TREE to the OUTER repository's
// beads workspace and `bd init` refused with "already initialized".
//
// Nothing about the invoking repository may reach the child, so HOME is not
// passed through either: the caller names a private home directory, which also
// keeps ~/.gitconfig and any user-level bd/dolt configuration out of the run.
// overrides win over everything and are applied last.
func hermeticChildEnv(parent []string, home string, overrides map[string]string) []string {
	var out []string
	for _, kv := range parent {
		k, _, _ := strings.Cut(kv, "=")
		if _, over := overrides[k]; over || k == "HOME" {
			continue
		}
		if hermeticPassthrough[k] || strings.HasPrefix(k, "LC_") {
			out = append(out, kv)
		}
	}
	out = append(out, "HOME="+home, "GIT_CONFIG_NOSYSTEM=1")
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}

// hermeticPassthrough is the complete list of parent-environment variables a
// bd / pg-connector child may inherit: how to find executables, where scratch
// space is, who and where the user is for locale-aware output. Adding a name
// here is a deliberate decision; it MUST NOT be a name that selects a git
// repository or a beads workspace.
var hermeticPassthrough = map[string]bool{
	"PATH":    true,
	"TMPDIR":  true,
	"USER":    true,
	"LOGNAME": true,
	"LANG":    true,
	"TZ":      true,
	"TERM":    true,
}

// goBuildEnv is the environment for the one child that needs the toolchain
// state of the parent (go build): the hermetic base plus the Go variables and
// the real HOME (GOCACHE and GOPATH default under it). It still carries no GIT_*
// name, so Go's VCS stamping cannot be redirected at the outer repository.
func goBuildEnv(parent []string) []string {
	home := os.Getenv("HOME")
	env := hermeticChildEnv(parent, home, nil)
	for _, kv := range parent {
		k, _, _ := strings.Cut(kv, "=")
		if goBuildPassthrough[k] {
			env = append(env, kv)
		}
	}
	return env
}

var goBuildPassthrough = map[string]bool{
	"GOCACHE": true, "GOMODCACHE": true, "GOPATH": true, "GOFLAGS": true,
	"GOPROXY": true, "GONOPROXY": true, "GOSUMDB": true, "GONOSUMDB": true,
	"GOPRIVATE": true, "GOTOOLCHAIN": true, "GOROOT": true, "GOENV": true,
	"GOWORK": true, "GOOS": true, "GOARCH": true, "CGO_ENABLED": true,
}

// hookLeakEnv is a git-hook-shaped environment plus every workspace binding a
// test process could have inherited, all pointing at "another repository".
var hookLeakEnv = []string{
	"GIT_DIR=/outer/repo/.git",
	"GIT_WORK_TREE=/outer/repo",
	"GIT_INDEX_FILE=/outer/repo/.git/index",
	"GIT_COMMON_DIR=/outer/repo/.git",
	"GIT_OBJECT_DIRECTORY=/outer/repo/.git/objects",
	"GIT_PREFIX=packages/pg-desk/",
	"GIT_CEILING_DIRECTORIES=/outer",
	"GIT_ANYTHING_FUTURE=1",
	"BEADS_DIR=/outer/repo/.beads",
	"BEADS_DOLT_SERVER_PORT=25252",
	"BD_ACTOR=outer",
	"WORKSPACE_ROOT=/outer",
	"ZR_MACHINE_SUPPORT_WORKSPACE_ROOT=/outer",
	"PG_CONNECTOR_ISSUE_BEADS_DIR=/outer/repo/.beads",
}

// TestHermeticChildEnv_DropsEverythingOffTheAllowlist is the unit-tier
// regression guard for pg2-g3204: a git-hook environment (and any other
// workspace binding) in the parent MUST NOT reach the child, and the names the
// child does need MUST still arrive. Reintroducing a deny-list or a plain
// pass-through of os.Environ fails it at the first GIT_* name.
func TestHermeticChildEnv_DropsEverythingOffTheAllowlist(t *testing.T) {
	parent := append([]string{
		"PATH=/usr/bin", "HOME=/outer/home", "TMPDIR=/tmp/x", "LANG=en_US.UTF-8",
		"LC_ALL=C", "SOMETHING_ELSE=1",
	}, hookLeakEnv...)
	got := hermeticChildEnv(parent, "/private/home", map[string]string{"XDG_STATE_HOME": "/state", "PATH": "/bin"})

	have := map[string]string{}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := have[k]; dup {
			t.Errorf("%s appears twice in %v", k, got)
		}
		have[k] = v
	}
	for _, kv := range hookLeakEnv {
		k, _, _ := strings.Cut(kv, "=")
		if v, leaked := have[k]; leaked {
			t.Errorf("%s=%s leaked into the child environment", k, v)
		}
	}
	for k := range have {
		if strings.HasPrefix(k, "GIT_") && k != "GIT_CONFIG_NOSYSTEM" {
			t.Errorf("unexpected GIT_ variable %s in the child environment", k)
		}
	}
	if _, leaked := have["SOMETHING_ELSE"]; leaked {
		t.Errorf("a name off the allowlist leaked: %v", got)
	}
	want := map[string]string{
		"HOME": "/private/home", "PATH": "/bin", "XDG_STATE_HOME": "/state", "TMPDIR": "/tmp/x",
		"LANG": "en_US.UTF-8", "LC_ALL": "C", "GIT_CONFIG_NOSYSTEM": "1",
	}
	for k, v := range want {
		if have[k] != v {
			t.Errorf("%s = %q, want %q (env %v)", k, have[k], v, got)
		}
	}
}

// TestGoBuildEnv_CarriesToolchainButNoGit pins the one place the parent's own
// state is passed through: the Go toolchain variables and the real HOME, never
// a git selector.
func TestGoBuildEnv_CarriesToolchainButNoGit(t *testing.T) {
	t.Setenv("HOME", "/real/home")
	parent := append([]string{"PATH=/usr/bin", "GOCACHE=/gc", "GOFLAGS=-mod=mod", "NOT_GO=1"}, hookLeakEnv...)
	have := map[string]string{}
	for _, kv := range goBuildEnv(parent) {
		k, v, _ := strings.Cut(kv, "=")
		have[k] = v
	}
	if have["HOME"] != "/real/home" || have["GOCACHE"] != "/gc" || have["GOFLAGS"] != "-mod=mod" || have["PATH"] != "/usr/bin" {
		t.Errorf("go build env lost a toolchain variable: %v", have)
	}
	for k := range have {
		if (strings.HasPrefix(k, "GIT_") && k != "GIT_CONFIG_NOSYSTEM") || strings.HasPrefix(k, "BEADS_") || k == "NOT_GO" {
			t.Errorf("%s leaked into the go build environment", k)
		}
	}
}
