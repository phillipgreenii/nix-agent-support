// Package actor derives the actor id a pb-workflow session passes as
// `bd --actor`: a session identifier plus a load-bearing role suffix.
package actor

import (
	"crypto/rand"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

// Roles are the actor suffixes the pb plugin's commands use. The suffix keeps
// actors distinct per role: /drain-beads' resume query recovers every in-progress
// bead of its own actor, so it must never share an id with /unblock-human-beads,
// and a dispatched subagent must never derive its orchestrator's actor id
// (operator ruling pg2-mcp1j).
var Roles = []string{"drain", "unblock", "unstick"}

// Source says which branch of the fallback chain produced the session id.
type Source string

const (
	// SourceEnv: $CLAUDE_SESSION_ID (or, failing that, $CLAUDE_CODE_SESSION_ID).
	SourceEnv Source = "env"
	// SourceScratchpad: the UUID inside the caller-supplied scratchpad path.
	SourceScratchpad Source = "scratchpad"
	// SourceRandom: a freshly generated UUID the caller MUST remember.
	SourceRandom Source = "random"
)

// EnvNames are the environment variables consulted, in order. CLAUDE_SESSION_ID
// is the documented one; CLAUDE_CODE_SESSION_ID is the name the Claude Code Bash
// tool actually exports.
var EnvNames = []string{"CLAUDE_SESSION_ID", "CLAUDE_CODE_SESSION_ID"}

var (
	// sessionIDRe bounds what may land in `--actor`: no whitespace or shell
	// metacharacter, so the id is safe to paste back into a command line.
	sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	uuidRe      = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// ValidRole reports whether role is one of Roles.
func ValidRole(role string) bool {
	for _, r := range Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Params is the input of Derive. Getenv and Rand are injected so the chain is
// testable without touching the process environment or entropy.
type Params struct {
	Role       string
	Scratchpad string // the session's own private path; "" when the caller has none
	Getenv     func(string) string
	Rand       io.Reader
}

// Result is the derived actor id and where its session part came from.
type Result struct {
	Actor     string
	SessionID string
	Source    Source
}

// Derive applies the fallback chain, stated once here: the session id from the
// environment, else the UUID in the scratchpad path, else a fresh random UUID;
// then "-<role>". A random UUID cannot be persisted by this function, so the
// caller MUST keep the printed id for the whole session.
func Derive(p Params) (Result, error) {
	if !ValidRole(p.Role) {
		return Result{}, fmt.Errorf("unknown role %q; valid roles: %s", p.Role, strings.Join(Roles, ", "))
	}
	sid, src := "", Source("")
	for _, name := range EnvNames {
		if v := strings.TrimSpace(p.Getenv(name)); v != "" {
			if !sessionIDRe.MatchString(v) {
				return Result{}, fmt.Errorf("$%s=%q: want letters, digits, dot, dash or underscore only", name, v)
			}
			sid, src = v, SourceEnv
			break
		}
	}
	if sid == "" {
		if u := uuidInPath(p.Scratchpad); u != "" {
			sid, src = u, SourceScratchpad
		}
	}
	if sid == "" {
		u, err := randomUUID(p.Rand)
		if err != nil {
			return Result{}, err
		}
		sid, src = u, SourceRandom
	}
	return Result{Actor: sid + "-" + p.Role, SessionID: sid, Source: src}, nil
}

// uuidInPath returns the lowercase UUID among path's components (the LAST one
// when several), or "". The path is only parsed, never touched on disk.
func uuidInPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if uuidRe.MatchString(parts[i]) {
			return strings.ToLower(parts[i])
		}
	}
	return ""
}

// randomUUID returns an RFC 4122 version-4 UUID read from r.
func randomUUID(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	var b [16]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("generate a random session id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
