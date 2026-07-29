// Package session is ccpool's lifecycle logic over small ports (tmux, trust,
// store, wait). Ensure routes an external_id -> a live, ready session,
// launching, resuming, or pruning as needed (ADR 0015). It tracks observed
// session FACTS, never a work judgment, and never touches exec/tmux/sql directly.
package session

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/ccpool/internal/eventlog"
	"github.com/phillipgreenii/ccpool/internal/launch"
	"github.com/phillipgreenii/ccpool/internal/mcpconsent"
	"github.com/phillipgreenii/ccpool/internal/notify"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

type Tmux interface {
	HasSession(name string) bool
	NewSession(name, cwd string, env map[string]string, argv []string) error
	SendKeys(name string, keys ...string) error
	Paste(name, body string) error
	KillSession(name string) error
	CapturePane(name string) (string, error)
}
type (
	Truster interface{ EnsureTrusted(cwd string) error }
	Store   interface {
		GetByExternalID(ctx context.Context, externalID string) (store.Session, bool, error)
		Insert(ctx context.Context, s store.Session) error
		Transition(ctx context.Context, externalID string, to store.State, claudeSessionID, transcriptPath string) (store.State, error)
		Delete(ctx context.Context, externalID string) error
		List(ctx context.Context) ([]store.Session, error)
		// SetMeta upserts caller-supplied session metadata (single autocommit UPSERT).
		SetMeta(ctx context.Context, externalID, key, value string) error
	}
)

// Locker serializes operations on one external_id: a single writer per
// conversation, so a resume cannot race a send (two writers to one transcript
// corrupts it). A nil Locker on Deps means "no locking" (used by unit tests
// with fakes).
type Locker interface {
	Lock(name string) (unlock func(), err error)
}
type Waiter interface {
	Wait(ctx context.Context, externalID string, since int64) (wait.Outcome, error)
}

// Transcript reads reply text / awaiting-input state from a transcript file.
type Transcript interface {
	LastAssistantText(path string) (string, error)
	IsAwaitingInput(path string) (bool, error)
	// FirstMessageActivity reports the timestamp of the most recent real message
	// event in the transcript and whether one exists. ok=false means the
	// transcript has no user/assistant message yet (the model has not started a
	// turn). Backed by claude-transcript.LastMessageActivity. A missing/half-
	// written transcript yields (zero, false) — tolerated, never an error.
	FirstMessageActivity(path string) (time.Time, bool)
}

// Mode selects send behavior when (and how) to deliver.
type Mode int

const (
	ModeRefuseIfBusy Mode = iota // default: error if the session isn't idle
	ModeNoWait                   // deliver and return immediately
	ModeQueue                    // skip idle check; deliver into Claude's native queue; implies no-wait
	ModeInterrupt                // cancel the current turn, then deliver
)

// Result is the outcome of a Send.
type Result struct {
	State    store.State
	Reply    string
	TimedOut bool
}

// waitFunc adapts a func to Waiter (used in tests and the default wiring).
type waitFunc func(ctx context.Context, externalID string, since int64) (wait.Outcome, error)

func (f waitFunc) Wait(ctx context.Context, externalID string, since int64) (wait.Outcome, error) {
	return f(ctx, externalID, since)
}

// defaultPruneGrace is the fresh-session race guard: a row still in `starting`
// younger than this is NOT pruned even when no transcript exists yet (the
// SessionStart hook may not have written one). Wired into Deps.PruneGrace.
const defaultPruneGrace = 30 * time.Second

// claudeChildMarkers are the env vars Claude Code uses to flag a CHILD/nested
// session. When CLAUDE_CODE_CHILD_SESSION is set, Claude does NOT persist the
// conversation transcript .jsonl (it treats itself as nested), which breaks
// resume-by-claude_session_id and `ccpool result` (pg2-lki6). A ccpool process
// launched from inside a Claude session (an agent driving ccpool, or `go test`
// running the contract suite) inherits these, and the tmux server passes them
// through to the pane. launchAndWait neutralizes each by emitting it as an EMPTY
// `-e VAR=` override (verified live: empty value is treated as "not a child"), so
// every managed session starts as a fresh top-level claude. The whole family is
// blanked — not just CHILD_SESSION — so a managed session never appears to be a
// continuation of the launching claude.
var claudeChildMarkers = []string{
	"CLAUDE_CODE_CHILD_SESSION",
	"CLAUDECODE",
	"CLAUDE_CODE_ENTRYPOINT",
	"CLAUDE_CODE_SESSION_ID",
	"CLAUDE_CODE_EXECPATH",
}

type Deps struct {
	Tmux       Tmux
	Trust      Truster
	Store      Store
	Wait       Waiter
	Transcript Transcript
	Lock       Locker
	Notify     notify.Notifier  // optional (nil = no-op); fires the timeout-fallback edge
	NotifyOn   []string         // states that trigger a notification
	Events     *eventlog.Logger // optional (nil = no-op); records ordered input actions
	// Exister probes whether a Claude session is resumable on disk by stat-ing the
	// row's hook-recorded transcript path (ADR 0015). A nil Exister means "never
	// resumable" (tests inject a fake; the cmd layer wires NewFSSessionExister).
	Exister   SessionExister
	Socket    string
	Prefix    string
	PoolPath  string // canonical pool dir; injected as CCPOOL_POOL into sessions; "" = default mode
	PluginDir string
	ClaudeBin string
	NewUUID   func() string
	Now       func() time.Time
	Sleep     func(time.Duration) // injected delay for the cancel Escape burst (nil = no-op, for tests)
	// PruneGrace guards the fresh-session race; zero falls back to defaultPruneGrace.
	PruneGrace time.Duration
}

type Service struct{ d Deps }

func New(d Deps) *Service { return &Service{d: d} }

type Handle struct {
	ExternalID      string
	ClaudeSessionID string
	Name            string
	TmuxSession     string
	State           store.State
}

// EnsureOpts carries the per-call launch extras threaded into a launched or
// resumed session. The zero value is a valid "no extras" launch, so callers
// that only need resume-or-reuse (e.g. reply) can pass EnsureOpts{}.
type EnsureOpts struct {
	// Env is caller-supplied environment injected into the session at launch
	// (e.g. BEADS_ACTOR/BEADS_DIR/WORKSPACE_ROOT for pool workers). It is merged
	// with ccpool's own correlation markers at launch time; see launchAndWait for
	// the merge policy (ccpool's markers are authoritative on conflict).
	Env map[string]string

	// Name is the optional display label persisted on a brand-new row and passed
	// as claude's --name (ADR 0015). Empty leaves the row's name null.
	Name string

	// PermissionMode and Effort are claude launch flags passed through to
	// launch.BuildNew/BuildResume (see launch.Spec). Workers dispatched
	// non-interactively need a bypassing PermissionMode (bypassPermissions),
	// else claude stalls on the first tool-permission prompt.
	PermissionMode launch.PermissionMode
	Effort         string
	// AllowedTools is forwarded verbatim to launch.Spec.AllowedTools (claude
	// --allowed-tools). Empty omits the flag. Set by pr-pool to constrain a
	// dontAsk worker to an allowlist.
	AllowedTools string

	// Autonomous, when true, injects CCPOOL_AUTONOMOUS=1 into the session env so
	// the `ccpool hook ask` PreToolUse hook BLOCKS AskUserQuestion (emits a deny)
	// instead of only recording the needs_input edge. Set by pr-pool for human-less
	// workers; unset for attended sessions (which keep pg2-7a5b detection).
	Autonomous bool

	// Meta is caller-supplied session metadata upserted atomically as part of this
	// dispatch (e.g. pr-pool's prpool.bead/role/pool). Addressed by external_id and
	// tied to the Claude session lifecycle: preserved across reuse-live/resume,
	// cleared when a phantom row is pruned (reuse => new). Empty/nil is a no-op.
	Meta map[string]string
}

// tmuxSafe maps the characters tmux treats as target separators ('.' and ':',
// the session.window.pane delimiters) to '_'. tmux silently rewrites them on
// `new-session` and cannot resolve them on `has-session`, so an external_id that
// carries one (e.g. a sub-bead id "zr-fy5j5.1") would create a session under a
// name we could neither find nor kill. Sanitizing here makes create/has-session/
// kill all address the single name tmux actually stores.
func tmuxSafe(s string) string {
	return strings.NewReplacer(".", "_", ":", "_").Replace(s)
}

// TmuxName is the canonical tmux session name for an external_id: prefix + id,
// sanitized so it round-trips through tmux. Every site that addresses a session
// (create, has-session, kill, attach, doctor) MUST go through this so they agree
// on one name. Exported because cmd/ccpool builds the same name outside this
// package; this is the single source of truth for the convention.
//
// Mapping only '.'/':' is collision-free for bd's ID alphabet: bead ids use
// '-' and '.' but never '_', so two distinct external_ids cannot fold onto one
// name (and the per-attempt timestamp stamp further disambiguates same-bead ids).
func TmuxName(prefix, externalID string) string {
	return tmuxSafe(prefix + externalID)
}

// Ensure returns a live, ready handle for externalID, launching/resuming/pruning
// as needed (ADR 0015). tmux session name = tmuxName(Prefix, externalID).
func (s *Service) Ensure(ctx context.Context, externalID, cwd, model string, opts EnsureOpts) (Handle, error) {
	var h Handle
	err := s.withLock(externalID, func() error {
		var e error
		h, e = s.ensureLocked(ctx, externalID, cwd, model, opts)
		if e != nil {
			return e
		}
		// Upsert dispatch metadata atomically within the per-external_id lock, AFTER
		// ensureLocked has launched/resumed/reused the session. Running here (not inside
		// ensureLocked) means it lands AFTER the path-4 phantom-row prune, whose
		// store.Delete cascade already cleared any prior session's metadata — so a fresh
		// Claude session under a reused external_id keeps only this dispatch's metadata
		// (reuse => new), while reuse-live/resume preserve prior keys and upsert on top.
		return s.applyMeta(ctx, externalID, opts.Meta)
	})
	return h, err
}

// applyMeta upserts each (key,value) of meta onto externalID. Empty meta is a no-op.
// A write error is returned: the dispatch metadata is part of the creation contract,
// and SetMeta is a single autocommit UPSERT on the DB the row was just written to.
func (s *Service) applyMeta(ctx context.Context, externalID string, meta map[string]string) error {
	for k, v := range meta {
		if err := s.d.Store.SetMeta(ctx, externalID, k, v); err != nil {
			return fmt.Errorf("set dispatch metadata %q: %w", k, err)
		}
	}
	return nil
}

// ensureLocked decision order (ADR 0015):
//  1. tmux session for externalID is alive → reuse (no relaunch).
//  2. canonicalize cwd (EvalSymlinks) + pre-trust it.
//  3. row exists AND Claude session exists on disk → resume by claude_session_id.
//  4. row exists AND Claude session GONE → prune the row, then fall to (5);
//     guarded against the fresh-session race (don't prune a young `starting` row).
//  5. no row → brand-new: generate claude_session_id, Insert(Starting), launch.
func (s *Service) ensureLocked(ctx context.Context, externalID, cwd, model string, opts EnsureOpts) (Handle, error) {
	tmuxName := TmuxName(s.d.Prefix, externalID)
	row, exists, err := s.d.Store.GetByExternalID(ctx, externalID)
	if err != nil {
		return Handle{}, err
	}

	// 1. Already live → reuse.
	if s.d.Tmux.HasSession(tmuxName) {
		return Handle{
			ExternalID: externalID, ClaudeSessionID: row.ClaudeSessionID, Name: row.Name,
			TmuxSession: tmuxName, State: row.State,
		}, nil
	}

	// 2. Canonicalize the cwd so the trust key matches what Claude records (it
	// resolves symlinks, e.g. macOS /tmp -> /private/tmp). Best-effort.
	if resolved, rerr := filepath.EvalSymlinks(cwd); rerr == nil {
		cwd = resolved
	}
	if err := s.d.Trust.EnsureTrusted(cwd); err != nil {
		return Handle{}, fmt.Errorf("pre-trust %q: %w", cwd, err)
	}
	// Pre-record MCP consent so an automated launch does not stall on the
	// interactive "New MCP server found" prompt for any unclassified server in
	// the worktree's .mcp.json (pg2-80ji). No-op when the worktree has no
	// .mcp.json. Same pre-launch window as trust, so no concurrent Claude writer.
	if err := mcpconsent.PreDisableUnclassified(cwd); err != nil {
		return Handle{}, fmt.Errorf("pre-disable unclassified MCP servers for %q: %w", cwd, err)
	}

	if exists {
		// 3. Resume IFF the Claude session still exists on disk (a fact, not a state).
		if s.claudeSessionResumable(row) {
			// Flip to `starting` BEFORE launch so `list` doesn't show the stale prior
			// outcome during the launch window, and snapshot THAT generation as the
			// wait baseline.
			if _, err := s.d.Store.Transition(ctx, externalID, store.Starting, "", ""); err != nil {
				return Handle{}, fmt.Errorf("mark resuming: %w", err)
			}
			since, err := s.currentGeneration(ctx, externalID)
			if err != nil {
				return Handle{}, err
			}
			argv := launch.BuildResume(launch.Spec{
				ClaudeBin: s.d.ClaudeBin, ClaudeSessionID: row.ClaudeSessionID, PluginDir: s.d.PluginDir,
				Model:          orDefault(model, row.Model),
				PermissionMode: opts.PermissionMode, Effort: opts.Effort, AllowedTools: opts.AllowedTools,
			})
			return s.launchAndWait(ctx, externalID, tmuxName, row.ClaudeSessionID, row.Name, row.CWD, since, argv, opts.Env, opts.Autonomous)
		}
		// 4. Claude session is GONE. Prune the phantom row UNLESS it is a fresh
		// `starting` row that hasn't had a chance to write a transcript yet.
		if !s.isFreshStarting(row) {
			if err := s.d.Store.Delete(ctx, externalID); err != nil {
				return Handle{}, fmt.Errorf("prune phantom row %q: %w", externalID, err)
			}
			exists = false
		}
	}

	if exists {
		// A fresh `starting` row whose Claude session isn't on disk yet: keep the
		// row, resume-launch by its csid (the SessionStart hook should land shortly).
		if _, err := s.d.Store.Transition(ctx, externalID, store.Starting, "", ""); err != nil {
			return Handle{}, fmt.Errorf("mark resuming: %w", err)
		}
		since, err := s.currentGeneration(ctx, externalID)
		if err != nil {
			return Handle{}, err
		}
		argv := launch.BuildResume(launch.Spec{
			ClaudeBin: s.d.ClaudeBin, ClaudeSessionID: row.ClaudeSessionID, PluginDir: s.d.PluginDir,
			Model:          orDefault(model, row.Model),
			PermissionMode: opts.PermissionMode, Effort: opts.Effort, AllowedTools: opts.AllowedTools,
		})
		return s.launchAndWait(ctx, externalID, tmuxName, row.ClaudeSessionID, row.Name, row.CWD, since, argv, opts.Env, opts.Autonomous)
	}

	// 5. Brand new.
	csid := s.d.NewUUID()
	if err := s.d.Store.Insert(ctx, store.Session{
		ExternalID: externalID, ClaudeSessionID: csid, Name: opts.Name, CWD: cwd, State: store.Starting,
		TmuxSession: tmuxName, Model: model,
	}); err != nil {
		return Handle{}, fmt.Errorf("insert row: %w", err)
	}
	since, err := s.currentGeneration(ctx, externalID)
	if err != nil {
		return Handle{}, err
	}
	argv := launch.BuildNew(launch.Spec{
		ClaudeBin: s.d.ClaudeBin, ClaudeSessionID: csid, Name: opts.Name, PluginDir: s.d.PluginDir, Model: model,
		PermissionMode: opts.PermissionMode, Effort: opts.Effort, AllowedTools: opts.AllowedTools,
	})
	return s.launchAndWait(ctx, externalID, tmuxName, csid, opts.Name, cwd, since, argv, opts.Env, opts.Autonomous)
}

// claudeSessionResumable reports whether the row's Claude session still exists on
// disk at its HOOK-RECORDED transcript path (the resume precondition, ADR 0015).
// The transcript path is authoritative (Claude reported it via the hook); ccpool
// no longer reconstructs the path from the cwd. Nil Exister or an empty recorded
// path → false. Resume itself still launches `--resume <row.ClaudeSessionID>`.
func (s *Service) claudeSessionResumable(row store.Session) bool {
	return row.TranscriptPath != "" && s.d.Exister != nil && s.d.Exister.Exists(row.TranscriptPath)
}

// isFreshStarting guards the fresh-session race: a row still in `starting`
// younger than the prune grace must NOT be pruned even when no transcript exists
// yet (the SessionStart hook may not have written one).
func (s *Service) isFreshStarting(row store.Session) bool {
	if row.State != store.Starting {
		return false
	}
	grace := s.d.PruneGrace
	if grace <= 0 {
		grace = defaultPruneGrace
	}
	return s.now().Sub(time.Unix(row.CreatedAt, 0)) < grace
}

// now reads the injected clock (defaults to time.Now when unwired, for tests
// that only exercise paths not gated on time).
func (s *Service) now() time.Time {
	if s.d.Now != nil {
		return s.d.Now()
	}
	return time.Now()
}

// currentGeneration reads the row's current generation (the wait baseline).
func (s *Service) currentGeneration(ctx context.Context, externalID string) (int64, error) {
	row, ok, err := s.d.Store.GetByExternalID(ctx, externalID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("row %q vanished before launch", externalID)
	}
	return row.Generation, nil
}

// launchAndWait starts the tmux session (in cwd) and blocks until generation > since.
// extraEnv (caller-supplied) is injected first; ccpool's own correlation markers
// are written last so they are authoritative — hooks key the store row off
// CCPOOL_EXTERNAL_ID, so a caller must never be able to clobber it.
// autonomous injects CCPOOL_AUTONOMOUS=1 when true so the hook ask path emits a
// blocking deny for human-less sessions (pg2-2f9d).
func (s *Service) launchAndWait(ctx context.Context, externalID, tmuxName, csid, name, cwd string, since int64, argv []string, extraEnv map[string]string, autonomous bool) (Handle, error) {
	env := make(map[string]string, len(extraEnv)+len(claudeChildMarkers)+4)
	maps.Copy(env, extraEnv)
	env["CCPOOL_EXTERNAL_ID"] = externalID
	env["PA_MONITOR_NO_NUDGE"] = "1"
	if s.d.PoolPath != "" {
		env["CCPOOL_POOL"] = s.d.PoolPath
	}
	if autonomous {
		env["CCPOOL_AUTONOMOUS"] = "1"
	}
	// Blank the Claude child-session markers (authoritatively, after the caller
	// env) so the launched claude persists its transcript (pg2-lki6).
	for _, k := range claudeChildMarkers {
		env[k] = ""
	}
	if err := s.d.Tmux.NewSession(tmuxName, cwd, env, argv); err != nil {
		return Handle{}, fmt.Errorf("tmux new-session: %w", err)
	}
	out, err := s.d.Wait.Wait(ctx, externalID, since)
	if err != nil {
		return Handle{}, fmt.Errorf("wait ready: %w", err)
	}
	h := Handle{ExternalID: externalID, ClaudeSessionID: csid, Name: name, TmuxSession: tmuxName, State: out.State}
	if out.TimedOut {
		return h, fmt.Errorf("session %q did not reach ready before timeout (state=%s)", externalID, out.State)
	}
	return h, nil
}

// fireNotify edge-triggers the configured notifier for a transition that the
// hook does NOT see — the AskUserQuestion timeout fallback in resolveOutcome,
// which fires no Notification hook at all. No-op when no notifier is wired or
// the edge/membership test fails.
func (s *Service) fireNotify(ctx context.Context, externalID string, prior, to store.State) {
	if s.d.Notify == nil || !notify.ShouldNotify(s.d.NotifyOn, string(prior), string(to)) {
		return
	}
	csid, cwd := "", ""
	if row, ok, _ := s.d.Store.GetByExternalID(ctx, externalID); ok {
		csid, cwd = row.ClaudeSessionID, row.CWD
	}
	_ = s.d.Notify.Notify(notify.Event{Name: externalID, UUID: csid, State: string(to), CWD: cwd})
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// sleep is the injected delay; nil (tests) is a no-op so fakes need no wiring.
func (s *Service) sleep(d time.Duration) {
	if s.d.Sleep != nil {
		s.d.Sleep(d)
	}
}

// withLock runs fn while holding the per-external_id lock. A nil Locker (tests)
// is a no-op so existing fakes need no Lock wiring.
func (s *Service) withLock(externalID string, fn func() error) error {
	if s.d.Lock == nil {
		return fn()
	}
	unlock, err := s.d.Lock.Lock(externalID)
	if err != nil {
		return fmt.Errorf("lock %q: %w", externalID, err)
	}
	defer unlock()
	return fn()
}
