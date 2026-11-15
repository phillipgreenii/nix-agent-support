package internal

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/x/cmdrun"
)

func envKeys(env []string) []string {
	keys := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestGitEnvironIsPathAndHomeOnly leaks every redirecting variable into this
// process and asserts none reaches the git child.
func TestGitEnvironIsPathAndHomeOnly(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/home-for-test")
	for _, kv := range []string{
		"GIT_DIR=/elsewhere/.git",
		"GIT_WORK_TREE=/elsewhere",
		"GIT_INDEX_FILE=/elsewhere/.git/index",
		"GIT_COMMON_DIR=/elsewhere/.git",
		"GIT_OBJECT_DIRECTORY=/elsewhere/.git/objects",
		"GIT_PREFIX=sub/",
		"GIT_CONFIG_COUNT=1",
		"GIT_AUTHOR_EMAIL=someone@example.test",
		"UNRELATED=1",
	} {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}

	want := []string{"HOME", "PATH"}
	if got := envKeys(gitEnviron()); !reflect.DeepEqual(got, want) {
		t.Errorf("gitEnviron keys = %v, want %v", got, want)
	}
	cmd := gitCall("/some/repo", "log")
	if got := envKeys(cmd.Env); !reflect.DeepEqual(got, want) {
		t.Errorf("child env keys = %v, want %v", got, want)
	}
	if wantArgs := []string{"-C", "/some/repo", "log"}; cmd.Name != "git" || !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Errorf("call = %s %v, want git %v", cmd.Name, cmd.Args, wantArgs)
	}
	if cmd.EnvMode != cmdrun.EnvExact {
		t.Error("EnvMode is not EnvExact: the reduced env would be appended to the parent's")
	}
	if cmd.Timeout != scriptout.ChildTimeout || !cmd.OwnProcGroup || cmd.TermGrace != scriptout.DefaultTermGrace || cmd.WaitDelay != scriptout.DefaultWaitDelay {
		t.Errorf("call is not bounded: %+v", cmd)
	}
}

func TestGitEnvironOmitsUnsetNames(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	if got, want := envKeys(gitEnviron()), []string{"PATH"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
}
