// Package originprobe decides whether the handler should decline a dispatch
// because the git origin (or checkout path) that dispatch needs is
// unavailable (INV-CCH-10, bead pg2-4gi2c).
//
// # Why the handler, not the core
//
// The core's gate is a global gate over a fixed named set (INV-LIFE-2) and is
// agnostic of handler vocabulary. Whether one repository's origin is
// reachable is handler knowledge: only the handler knows which repo root a
// dispatch runs in. So the check lives here and the handler answers a gated
// dispatch with the same pre-accept busy decline it already uses for a full
// pool or a full disk. A decline mutates no bead.
//
// # Model
//
// Availability is PER ORIGIN, keyed by a normalized repo key
// <host>/<org>/<repo>. A dispatch whose repo root is not a watched origin's
// repo root has no origin and is never gated.
//
// The handler is exec-per-dispatch, so there is no process to hold state. Each
// origin's last result is cached in a small JSON file (State) under
// <StateDir>/origin-state/, and a probe runs at dispatch time when the cached
// result is older than the TTL.
//
// # Probe
//
//  1. RepoRoot exists and holds a .git entry (an unmounted volume fails here
//     and is [MountMissing]).
//  2. `git -C <RepoRoot> ls-remote <remote> HEAD` under a hard timeout, in the
//     environment [Env] returns.
//
// The text the probe produced is classified by internal/failsig, the module's
// ONE classification table; this package keeps no second one. It only maps a
// failsig.Signature onto a probe [Class]. Stored text is redacted first.
//
// # Environment
//
// The probe runs in gitenv.Environ() plus GIT_TERMINAL_PROMPT=0: the same
// hermetic git environment every other git child in this module gets
// (GIT_DIR and the other repository-naming variables dropped;
// SSH_AUTH_SOCK, GIT_SSH_COMMAND, HOME, and PATH kept). ccpool starts a
// session with the handler's own process environment plus a few explicit
// -e variables (BEADS_ACTOR, BEADS_DIR, WORKSPACE_ROOT), so credentials reach
// a session the same way they reach this probe. That is an approximation, not
// an identity: the session's shell environment comes from the tmux server, so
// a variable the server has and the handler lacks (or the reverse) is a gap
// this probe cannot see. Closing it is pg2-x5r2z's job, not the probe's.
//
// # Flap control
//
// An origin is gated only after K consecutive failed probes (default 2). The
// first ok probe clears it. While the failure count is between 1 and K-1 the
// cached result is NOT reused, so the confirming probe happens on the very next
// dispatch instead of a TTL later. While gated, each dispatch re-probes subject
// to the TTL, so recovery is noticed without a timer daemon.
//
// # Operator override
//
// <StateDir>/origin-state/<sanitized-key>.disable makes [Prober.Check] a no-op
// that never gates. There is no operator "pause this origin" switch: the
// automatic clear would fight it. The existing global pg-router pause is the
// tool for that.
//
// # Trade-off
//
// A declined event is re-offered only until it expires (INV-EVT-4). An outage
// longer than the event lifetime therefore lets events expire. That is
// accepted: events re-derive from their source on discovery once the origin
// returns.
package originprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/failsig"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/gitenv"
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

// Unchecked is what status shows for an origin with no state file yet. It is
// not a probe verdict and is never stored.
const Unchecked = "unchecked"

// ReasonOriginUnavailable is the busy-decline reason a gated dispatch carries.
// It is a fixed value: the origin key goes in the log line, never in the
// reason, so the metric label set stays bounded.
const ReasonOriginUnavailable = "origin-unavailable"

// maxErrorTail bounds State.LastError, in bytes.
const maxErrorTail = 300

// State is one origin's persisted probe state.
type State struct {
	Key                 string    `json:"key"`
	Class               Class     `json:"class"`
	Since               time.Time `json:"since"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LastChecked         time.Time `json:"last_checked"`
	// LastError is a redacted tail of the failed probe's text. It is empty
	// for an ok probe.
	LastError string `json:"last_error"`
}

// Decision is [Prober.Check]'s answer.
type Decision struct {
	// Gated is true when the dispatch MUST be declined.
	Gated bool
	State State
}

// Runner runs `git <args>` in dir under ctx and returns its combined output.
// It is the seam tests replace; the default is [gitRunner].
type Runner func(ctx context.Context, dir string, args ...string) (output string, err error)

// Emitter records a structured event (an eventlog.Writer's Emit has this
// shape). Nil means no event log.
type Emitter func(level, kind, msg string, fields map[string]any) error

// Prober checks watched origins. The zero value is not usable; use [New].
type Prober struct {
	cfg  config.OriginProbe
	dir  string // <state>/origin-state
	Run  Runner
	Now  func() time.Time
	Emit Emitter
}

// New builds a Prober from cfg. StateDir empty resolves via [DefaultStateDir].
func New(cfg config.OriginProbe) *Prober {
	sd := cfg.StateDir
	if sd == "" {
		sd = DefaultStateDir()
	}
	return &Prober{cfg: cfg, dir: filepath.Join(sd, "origin-state"), Run: gitRunner, Now: time.Now}
}

// DefaultStateDir is $XDG_STATE_HOME/pg-router-ccpool-handler, else
// ~/.local/state/pg-router-ccpool-handler.
func DefaultStateDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "pg-router-ccpool-handler")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".local", "state", "pg-router-ccpool-handler")
}

// Origins returns the watched set.
func (p *Prober) Origins() []config.WatchedOrigin { return p.cfg.Origins }

// Dir is the origin-state directory.
func (p *Prober) Dir() string { return p.dir }

// Resolve returns the watched origin whose RepoRoot is repoRoot (after path
// cleaning). ok is false when repoRoot is empty or matches none, and such a
// dispatch is never gated.
func (p *Prober) Resolve(repoRoot string) (config.WatchedOrigin, bool) {
	if repoRoot == "" {
		return config.WatchedOrigin{}, false
	}
	want := filepath.Clean(repoRoot)
	for _, w := range p.cfg.Origins {
		if filepath.Clean(w.RepoRoot) == want {
			return w, true
		}
	}
	return config.WatchedOrigin{}, false
}

// SanitizeKey turns a key into a file-name stem.
func SanitizeKey(key string) string { return strings.ReplaceAll(key, "/", "__") }

func (p *Prober) statePath(key string) string {
	return filepath.Join(p.dir, SanitizeKey(key)+".json")
}

// DisablePath is the kill-switch file for key.
func (p *Prober) DisablePath(key string) string {
	return filepath.Join(p.dir, SanitizeKey(key)+".disable")
}

// Disabled reports whether the kill-switch file for key exists.
func (p *Prober) Disabled(key string) bool {
	_, err := os.Stat(p.DisablePath(key))
	return err == nil
}

// Ignore writes the kill-switch file for key.
func (p *Prober) Ignore(key string) error {
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return err
	}
	body := "origin probe ignored by operator at " + p.Now().UTC().Format(time.RFC3339) + "\n"
	return os.WriteFile(p.DisablePath(key), []byte(body), 0o644)
}

// Unignore removes the kill-switch file for key. Absent is not an error.
func (p *Prober) Unignore(key string) error {
	if err := os.Remove(p.DisablePath(key)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// LoadState reads key's state. A missing or corrupt file yields the zero State
// (never gated) with ok=false: state is a cache, so it is rebuilt by probing.
func (p *Prober) LoadState(key string) (State, bool) {
	data, err := os.ReadFile(p.statePath(key))
	if err != nil {
		return State{}, false
	}
	var st State
	if json.Unmarshal(data, &st) != nil {
		return State{}, false
	}
	return st, true
}

func (p *Prober) saveState(st State) error {
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(p.dir, ".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	if err := os.Rename(tmp.Name(), p.statePath(st.Key)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Check returns whether a dispatch into w's repository must be declined. It
// never returns an error: a probe problem is a probe RESULT, and a state-file
// write problem is logged and the verdict still stands.
func (p *Prober) Check(ctx context.Context, w config.WatchedOrigin) Decision {
	if p.Disabled(w.Key) {
		return Decision{}
	}
	k := p.cfg.FailureThreshold
	if k < 1 {
		k = 1
	}
	st, have := p.LoadState(w.Key)
	now := p.Now()
	confirming := st.ConsecutiveFailures > 0 && st.ConsecutiveFailures < k
	if have && !confirming && now.Sub(st.LastChecked) < p.cfg.TTL {
		return Decision{Gated: st.ConsecutiveFailures >= k, State: st}
	}

	class, tail := p.probe(ctx, w)
	now = p.Now()
	prev := st
	st.Key = w.Key
	st.LastChecked = now
	if class == OK {
		st.ConsecutiveFailures = 0
		st.LastError = ""
	} else {
		st.ConsecutiveFailures++
		st.LastError = tail
	}
	if !have || st.Class != class {
		st.Since = now
	}
	st.Class = class

	wasGated := have && prev.ConsecutiveFailures >= k
	nowGated := st.ConsecutiveFailures >= k
	switch {
	case nowGated && !wasGated:
		p.transition("warn", "origin_gated", "origin gated: declining dispatches into it", st, 0)
	case wasGated && !nowGated:
		p.transition("info", "origin_cleared", "origin cleared: dispatches resume", st, now.Sub(prev.Since))
	}
	if err := p.saveState(st); err != nil {
		slog.Warn("origin probe: could not persist state", "origin", w.Key, "err", err)
	}
	return Decision{Gated: nowGated, State: st}
}

// transition logs and records a gated/cleared transition. The stderr tail is
// already redacted; the raw text never reaches here.
func (p *Prober) transition(level, kind, msg string, st State, dur time.Duration) {
	attrs := []any{"origin", st.Key, "class", string(st.Class), "consecutive_failures", st.ConsecutiveFailures, "stderr_tail", st.LastError}
	fields := map[string]any{"origin": st.Key, "class": string(st.Class), "consecutive_failures": st.ConsecutiveFailures, "stderr_tail": st.LastError}
	if dur > 0 {
		attrs = append(attrs, "duration", dur.String())
		fields["duration_ms"] = dur.Milliseconds()
	}
	if level == "warn" {
		slog.Warn("origin probe: "+msg, attrs...)
	} else {
		slog.Info("origin probe: "+msg, attrs...)
	}
	if p.Emit != nil {
		_ = p.Emit(level, kind, msg, fields)
	}
}

// probe runs both probe steps and returns the class plus a redacted tail of
// the text that explains a failure.
func (p *Prober) probe(ctx context.Context, w config.WatchedOrigin) (Class, string) {
	if _, err := os.Stat(filepath.Join(w.RepoRoot, ".git")); err != nil {
		return MountMissing, tailOf(fmt.Sprintf("repo root %s: %v", w.RepoRoot, err))
	}

	remote := w.Remote
	if remote == "" {
		remote = "origin"
	}
	pctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	out, err := p.Run(pctx, w.RepoRoot, "ls-remote", remote, "HEAD")
	if err == nil {
		return OK, ""
	}
	text := out + "\n" + err.Error()
	if c, ok := fromSignature(failsig.Classify(text).Signature); ok {
		return c, tailOf(text)
	}
	if errors.Is(pctx.Err(), context.DeadlineExceeded) {
		return Timeout, tailOf(text)
	}
	return Unknown, tailOf(text)
}

// fromSignature maps failsig's closed set onto probe classes. Budget,
// index-lock, and the api-* and session-* signatures cannot come from a
// read-only ls-remote, so they (and unknown) are not mapped and fall to the
// timeout/unknown handling.
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
	// Collapse whitespace so the tail is one line (status output, log fields).
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

// Env is the environment the probe's git child gets: gitenv.Environ() with
// GIT_TERMINAL_PROMPT forced to 0 so a credential prompt fails fast instead of
// blocking on a terminal.
func Env() []string {
	base := gitenv.Environ()
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if !strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT=") {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_TERMINAL_PROMPT=0")
}

// gitRunner is the production Runner. gitenv.Command owns the argv shape and
// the hermetic environment; Env layers the no-prompt policy on top.
func gitRunner(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitenv.Command(ctx, dir, args...)
	cmd.Env = Env()
	// A killed git can leave an ssh grandchild holding the pipe open; do not
	// let Wait block on it past the timeout.
	cmd.WaitDelay = 2 * time.Second
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
