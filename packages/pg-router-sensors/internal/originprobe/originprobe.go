// Package originprobe is a trimmed COPY of pg-router-handlers' originprobe
// (that package lives under another module's internal/, so it cannot be
// imported). It keeps the probe itself: a bounded, non-interactive
// `git ls-remote <remote> HEAD` in a hermetic git environment, whose failure
// text is classified through the sensors module's copy of the shared
// classification table (internal/failsig) into a probe [Class]. The
// per-origin state file, flap counter and kill switch of the original are NOT
// copied: a sensor keeps its own dwell state, so every call is one fresh
// probe.
//
// The probe runs through the cmdrun seam, in the environment [Env] returns:
// every repository-naming GIT_* variable dropped (SSH_AUTH_SOCK,
// GIT_SSH_COMMAND, HOME and PATH kept) plus GIT_TERMINAL_PROMPT=0, so a
// credential prompt fails fast instead of blocking.
//
// Exported names: Class and its constants, Probe, Env, Hermetic.
package originprobe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/x/cmdrun"

	"github.com/phillipgreenii/pg-router-sensors/internal/failsig"
)

// Class is the probe's verdict for one origin.
type Class string

// The closed set of classes.
const (
	OK              Class = "ok"
	MountMissing    Class = "mount-missing"
	AuthUnavailable Class = "auth-unavailable"
	Network         Class = "network"
	Timeout         Class = "timeout"
	Unknown         Class = "unknown"
)

// maxErrorTail bounds the returned error tail, in bytes.
const maxErrorTail = 300

// inheritableGitVars are the GIT_* variables that carry credential delivery or
// user configuration and so MUST reach a git child; every other GIT_*
// variable is dropped.
var inheritableGitVars = map[string]struct{}{
	"GIT_SSH": {}, "GIT_SSH_COMMAND": {}, "GIT_SSH_VARIANT": {}, "GIT_PROXY_COMMAND": {},
	"GIT_ASKPASS": {}, "GIT_TERMINAL_PROMPT": {}, "GIT_EDITOR": {},
	"GIT_CONFIG_GLOBAL": {}, "GIT_CONFIG_SYSTEM": {}, "GIT_CONFIG_NOSYSTEM": {},
}

// Hermetic returns base without the GIT_* variables that could redirect a git
// child at another repository, index or config.
func Hermetic(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(key, "GIT_") {
			if _, inherit := inheritableGitVars[key]; !inherit {
				continue
			}
		}
		out = append(out, kv)
	}
	return out
}

// Env is the environment the probe's git child gets: the hermetic process
// environment with GIT_TERMINAL_PROMPT forced to 0.
func Env() []string {
	base := Hermetic(os.Environ())
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if !strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT=") {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_TERMINAL_PROMPT=0")
}

// Probe runs `git ls-remote --exit-code <remote> HEAD` (in repoRoot via -C
// when it is non-empty) under timeout and returns the class plus a redacted
// tail of the text that explains a failure ("" for OK). A repoRoot that does
// not exist is MountMissing without running git.
func Probe(ctx context.Context, run cmdrun.Runner, repoRoot, remote string, timeout time.Duration) (Class, string) {
	if repoRoot != "" {
		if _, err := os.Stat(repoRoot); err != nil {
			return MountMissing, tailOf(fmt.Sprintf("repo root %s: %v", repoRoot, err))
		}
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var args []string
	if repoRoot != "" {
		args = append(args, "-C", repoRoot)
	}
	args = append(args, "ls-remote", "--exit-code", remote, "HEAD")
	res := run(pctx, cmdrun.Call{Name: "git", Args: args, Env: Env(), EnvMode: cmdrun.EnvExact, OwnProcGroup: true})
	if res.OK() {
		return OK, ""
	}
	text := string(res.Stdout) + "\n" + string(res.Stderr)
	switch {
	case res.StartErr != nil:
		text += "\n" + res.StartErr.Error()
	case res.CtxErr != nil:
		text += "\n" + res.CtxErr.Error()
	default:
		text += fmt.Sprintf("\nexit status %d", res.Code)
	}
	if c, ok := fromSignature(failsig.Classify(text).Signature); ok {
		return c, tailOf(text)
	}
	if errors.Is(pctx.Err(), context.DeadlineExceeded) || errors.Is(res.CtxErr, context.DeadlineExceeded) {
		return Timeout, tailOf(text)
	}
	return Unknown, tailOf(text)
}

func fromSignature(s failsig.Signature) (Class, bool) {
	switch s {
	case failsig.GitAuth:
		return AuthUnavailable, true
	case failsig.GitNetwork:
		return Network, true
	case failsig.MountOrPath:
		return MountMissing, true
	default:
		return "", false
	}
}

// tailOf redacts the whole text first, THEN keeps at most maxErrorTail bytes
// from the end (cutting first could split a credential into a fragment no
// pattern recognizes).
func tailOf(text string) string {
	r := strings.Join(strings.Fields(failsig.Redact(text)), " ")
	if len(r) <= maxErrorTail {
		return r
	}
	r = r[len(r)-maxErrorTail:]
	for len(r) > 0 && !utf8.RuneStart(r[0]) {
		r = r[1:]
	}
	return strings.TrimSpace(r)
}
