// gitenv.go: the child environment every git invocation in this backend runs
// under. It is reduced to PATH and HOME only, so an inherited GIT_DIR (or any
// other GIT_* variable) from a hook parent cannot redirect the read onto a
// different repository regardless of the explicit -C (the pg2-67h4y /
// pg2-12795 corruption class). This is a deliberate allowlist of two names,
// not a GIT_-prefix filter.
package internal

import (
	"context"
	"os"
	"os/exec"
)

// gitEnviron returns the child environment: PATH (to resolve git helpers) and
// HOME (to find the operator's global git config), when set, and nothing else.
func gitEnviron() []string {
	env := make([]string, 0, 2)
	for _, name := range []string{"PATH", "HOME"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// gitCommand builds `git <args...>` rooted at dir (empty dir means this
// process's cwd) under the reduced environment.
func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = gitEnviron()
	return cmd
}
