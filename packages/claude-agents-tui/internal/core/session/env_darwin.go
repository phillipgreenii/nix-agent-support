//go:build darwin

package session

import (
	"fmt"
	"os/exec"
	"strings"
)

// readProcessEnv reads the environment of pid on macOS by shelling
// `ps -E -ww -o command= -p <pid>`. Output is the executable path followed
// by env vars; we filter to KEY=VALUE tokens. Reads same-user processes
// without privileges; fails on other users.
//
// Parsing is best-effort: values containing spaces will be split. Since
// the labels package only consumes a known set of single-word vars
// (CMUX_WORKSPACE_ID, TMUX, GC_RIG, GC_AGENT, WORKSPACE), this is fine
// for the use case. Robust parsing would require libproc + cgo, which we
// avoid.
func readProcessEnv(pid int) (map[string]string, error) {
	out, err := exec.Command("ps", "-E", "-ww", "-o", "command=", "-p", fmt.Sprintf("%d", pid)).Output()
	if err != nil {
		return map[string]string{}, err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return map[string]string{}, nil
	}
	env := map[string]string{}
	for _, tok := range strings.Fields(line) {
		// ps may quote env tokens with leading/trailing single quotes.
		// Strip them before parsing.
		tok = strings.Trim(tok, "'")
		eq := strings.IndexByte(tok, '=')
		if eq <= 0 {
			continue
		}
		key := tok[:eq]
		// Heuristic guard: env var keys are uppercase, digits, underscores.
		// Skip anything else (e.g. CLI flag tokens like "--foo=bar").
		if !validEnvKey(key) {
			continue
		}
		env[key] = tok[eq+1:]
	}
	return env, nil
}

func validEnvKey(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r == '_' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}
