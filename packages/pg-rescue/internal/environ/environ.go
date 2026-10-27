// Package environ reads KEY=VALUE environment lists, the form os.Environ and
// exec.Cmd.Env use. Each command's run function takes its environment as such
// a list, so a test can hand in a made-up one instead of the process's own.
package environ

import "strings"

// Lookup returns the value of key in env, or "" when it is not set. When a key
// appears more than once the last entry wins, as it does for exec.Cmd.Env.
func Lookup(env []string, key string) string {
	val := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val = v
		}
	}
	return val
}

// Getenv returns an os.Getenv-shaped function over env.
func Getenv(env []string) func(string) string {
	return func(key string) string { return Lookup(env, key) }
}
