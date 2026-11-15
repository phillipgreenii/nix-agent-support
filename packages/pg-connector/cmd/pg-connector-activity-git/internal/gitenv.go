// gitenv.go: the child environment every git invocation in this backend runs
// under. It is reduced to PATH and HOME only, so an inherited GIT_DIR (or any
// other GIT_* variable) from a hook parent cannot redirect the read onto a
// different repository regardless of the explicit -C (the pg2-67h4y /
// pg2-12795 corruption class). This is a deliberate allowlist of two names,
// not a GIT_-prefix filter.
package internal

import (
	"os"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/x/cmdrun"
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

// gitCall builds the cmdrun.Call for `git <args...>` rooted at dir (empty dir
// means this process's cwd) under the reduced environment, bounded and in its
// own process group.
func gitCall(dir string, args ...string) cmdrun.Call {
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	return scriptout.BoundChild(cmdrun.Call{
		Name:    "git",
		Args:    full,
		Env:     gitEnviron(),
		EnvMode: cmdrun.EnvExact,
		Timeout: scriptout.ChildTimeout,
	})
}
