package internal

import (
	"context"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
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
	cmd := gitCommand(context.Background(), "/some/repo", "log")
	if got := envKeys(cmd.Env); !reflect.DeepEqual(got, want) {
		t.Errorf("child env keys = %v, want %v", got, want)
	}
	if wantArgs := []string{"git", "-C", "/some/repo", "log"}; !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Errorf("args = %v, want %v", cmd.Args, wantArgs)
	}
}

func TestGitEnvironOmitsUnsetNames(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	os.Unsetenv("HOME")
	if got, want := envKeys(gitEnviron()), []string{"PATH"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
}
