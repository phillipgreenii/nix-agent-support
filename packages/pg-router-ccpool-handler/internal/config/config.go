// Package config is this module's OWN, minimal runtime configuration for the
// ccpool/command executors it hosts. It is NOT a copy of
// packages/pg-router/internal/config: that package is pg-router's full
// pool-level configuration (roles, queries, gates, pool scalars) and stays
// in-core, unreachable from here (Go's internal-package visibility rule —
// see docs/adr/0065's Addendum). This Config instead carries exactly the
// narrow subset of fields the moved ccpool/command executor logic reads at
// dispatch time — the launch/prompt/isolation knobs, not pg-router's own
// pool-wide scalars (MaxFeedback/MaxWorker/gates/roles/queries), which have
// no meaning on this side of the wire boundary.
//
// Default() below is deliberately a plain Go literal, mirroring
// packages/pg-router/internal/config.Default()'s own values for the fields
// this module shares with it, so today's behavior is unchanged for a
// deployment that never overrides them.
//
// Validate's real claude --permission-mode enum check (docket pg2-oju6w Task
// 5.7) lives HERE, not on pg-router's own Config: this module is the side
// that actually invokes `ccpool new --permission-mode`, so it is the side
// that needs the real values. pg-router itself now carries PermissionMode as
// an opaque, un-validated string (kept only to display it).
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/budget"
)

// CCPoolCommand is the ccpool binary a ccpool-backed handler runs through.
// Mirrors packages/pg-router/internal/config.CCPoolCommand (same value, same
// rationale — the binary name is not configurable today on either side).
const CCPoolCommand = "ccpool"

// Config is the launch/prompt/isolation configuration one dispatched ccpool
// or command session reads.
type Config struct {
	// RepoRoot is the repo a dispatched session's WORKSPACE_ROOT derives
	// from (worktree/none isolation) or runs directly against.
	RepoRoot string
	// BeadsPrefix is the expected bd issue-store prefix, checked by this
	// module's own precheck (docket pg2-oju6w's Task 5.8, ADR 0065's
	// "Source-side boundary" section, closing register row R14 / bead
	// pg2-d4gvb): a guard against a `query` invocation resolving the wrong
	// beads store. Mirrors packages/pg-router/internal/config.Config's
	// same-named field.
	BeadsPrefix string
	// WorktreeDir is the parent directory fresh per-item git worktrees are
	// created under (worktree isolation, the default).
	WorktreeDir string
	// MaxWait bounds how long a ccpool session may run before it is
	// considered stuck (waitDone's deadline).
	MaxWait time.Duration
	// WorktreeQuietWindow is how long a finished dispatch's session (its
	// transcript plus every subagents/*.jsonl beside it) must have shown no
	// write activity before cleanupWorktree may remove its worktree
	// (pg2-9fwft: Agent-tool child subagents are invisible to the pool but
	// keep writing their transcripts). <= 0 disables the check (legacy
	// remove-immediately behavior).
	WorktreeQuietWindow time.Duration
	// WorktreeQuietMax bounds how long cleanupWorktree waits for the quiet
	// window; on expiry the worktree is LEFT in place (fail soft, for a later
	// sweep) rather than removed under a still-active subagent.
	WorktreeQuietMax time.Duration
	// PollInterval is how often waitDone re-checks ccpool's session list.
	PollInterval time.Duration
	// LeaseTTL is the supervision-lease TTL (bead pg2-g2u9m, INV-CCH-18): the
	// handler stamps pgrouter.lease_until = now + LeaseTTL on its session every
	// PollInterval, and an expired lease means nobody is supervising the session
	// (the handler died). Default 2m; validated at load to be at least
	// LeaseTTLMinPolls x PollInterval so a few missed refreshes never expire a
	// live handler's lease.
	LeaseTTL time.Duration
	// Effort/Model/PermissionMode/AllowedTools/Autonomous are forwarded
	// verbatim to `ccpool new` (NewCLIRunner).
	Effort         string
	Model          string
	PermissionMode string
	AllowedTools   string
	Autonomous     bool
	// PRTool is the external PR-management tool this module's preflight
	// (resolveSelf) shells out to — configuration, never a name compiled
	// into this module's own contract surface (GOAL-MIN-1's Floor). Empty
	// (the default) adds no extra grant to the built-in AllowedTools
	// default; see defaultAllowedTools below. (internal/pgrouteracl, the
	// other former consumer of this field, was dead code with no caller and
	// was deleted outright — bead pg2-gidpd.)
	// This also realizes this module's own INV-CCH-5 (docs/behavior/
	// invariants.md): pg-pr's name is configuration on THIS side, never
	// written into packages/pg-router's own contract surface (its --help
	// text, config schema, or wire messages) — dispatch.go/query.go's wire
	// replies carry no backing-tool literal.
	PRTool string
	// SessionPrefix names the ccpool --name label prefix (Role.DisplayName).
	SessionPrefix string
	// SelfLogin is the GitHub login the worker safety preamble asserts
	// authorship against. Empty until a deployment sets it explicitly —
	// resolving it automatically (`pg-pr config show self-login`, one of
	// ADR 0065's R14 checks) moved to this module's own deployment layer,
	// not built by this task.
	SelfLogin string
	// ReminderMsg/WrapUpMsg are the budget-escalation nudge templates
	// (text/template, {{.BeadID}}).
	ReminderMsg string
	WrapUpMsg   string
	// ConfirmIngest is the worker's initial-nudge ingestion-guard window.
	ConfirmIngest time.Duration
	// BudgetTokens/BudgetCost/BudgetTime/*Pct feed WorkerBudget() below —
	// mirrors packages/pg-router/internal/config.Config's same-named fields
	// and its WorkerBudget() method.
	BudgetTokens int64
	BudgetCost   int64 // cents
	BudgetTime   time.Duration
	ReminderPct  float64
	CancelPct    float64
	HardPct      float64
	// OriginProbe configures the per-origin availability probe and decline
	// gate (bead pg2-4gi2c, INV-CCH-10). The zero-origin default watches
	// nothing, so a deployment that never sets it is unchanged.
	OriginProbe OriginProbe
	// ConnectorBinary is the pg-connector executable the review precheck
	// (INV-CCH-22, bead pg2-5x29j) reads PR merged state and pending-review
	// state through. Empty means "pg-connector" resolved on PATH. A binary that
	// cannot be run only disables the precheck's PR reads (it fails open).
	ConnectorBinary string
}

// WatchedOrigin is one git origin the handler probes before accepting a
// dispatch that runs in its repository.
type WatchedOrigin struct {
	// Key is the normalized repo key <host>/<org>/<repo>. It names the origin
	// in state files, logs, and the `origin` subcommands.
	Key string `json:"key"`
	// RepoRoot is the checkout the probe runs `git -C` in. A dispatch whose
	// repo root equals it (after path cleaning) is gated by this origin.
	RepoRoot string `json:"repoRoot"`
	// Remote is the remote name or URL passed to `git ls-remote`. Empty means
	// "origin".
	Remote string `json:"remote,omitempty"`
}

// OriginProbe is the origin probe's configuration. All durations are
// nanosecond integers on the wire, like the rest of Config.
type OriginProbe struct {
	// Origins is the watched set. Empty disables the feature.
	Origins []WatchedOrigin `json:"origins"`
	// FailureThreshold is K: an origin is gated only after K consecutive
	// failed probes.
	FailureThreshold int `json:"failureThreshold"`
	// TTL is how long a probe result is reused before the next dispatch
	// re-probes. A result showing 0 < failures < K is never reused, so the
	// confirming probe is not delayed by a TTL.
	TTL time.Duration `json:"ttl"`
	// Timeout is the hard bound on one `git ls-remote`.
	Timeout time.Duration `json:"timeout"`
	// StateDir is the handler state directory; the per-origin state files live
	// in its origin-state/ subdirectory. Empty resolves to
	// $XDG_STATE_HOME/pg-router-ccpool-handler, else
	// ~/.local/state/pg-router-ccpool-handler.
	StateDir string `json:"stateDir"`
}

// DefaultOriginProbe is OriginProbe's baseline: no origins, K=2, 60s TTL, 20s
// probe timeout.
func DefaultOriginProbe() OriginProbe {
	return OriginProbe{FailureThreshold: 2, TTL: 60 * time.Second, Timeout: 20 * time.Second}
}

// Validate rejects a malformed OriginProbe.
func (o OriginProbe) Validate() error {
	if o.FailureThreshold < 1 {
		return fmt.Errorf("originProbe.failureThreshold %d: must be >= 1", o.FailureThreshold)
	}
	if o.TTL < 0 {
		return fmt.Errorf("originProbe.ttl %v: must be >= 0", o.TTL)
	}
	if o.Timeout <= 0 {
		return fmt.Errorf("originProbe.timeout %v: must be > 0", o.Timeout)
	}
	seen := map[string]bool{}
	for i, w := range o.Origins {
		if !validOriginKey(w.Key) {
			return fmt.Errorf("originProbe.origins[%d].key %q: want <host>/<org>/<repo>", i, w.Key)
		}
		if seen[w.Key] {
			return fmt.Errorf("originProbe.origins[%d].key %q: duplicate", i, w.Key)
		}
		seen[w.Key] = true
		if w.RepoRoot == "" {
			return fmt.Errorf("originProbe.origins[%d] (%s): repoRoot is required", i, w.Key)
		}
	}
	return nil
}

// validOriginKey reports whether k has exactly three non-empty segments of
// [A-Za-z0-9._-] separated by "/". That charset keeps the key safe to embed in
// a file name once the separators are replaced.
func validOriginKey(k string) bool {
	parts := strings.Split(k, "/")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return false
		}
		for _, r := range p {
			ok := r == '.' || r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			if !ok {
				return false
			}
		}
	}
	return true
}

// validPermissionModes is the set of claude --permission-mode values this
// module may pass through to `ccpool new` (mirrors ccpool's own
// launch.PermissionMode enum; duplicated here rather than imported because
// this module does not depend on the ccpool module's internal packages). The
// empty string is valid: it means "omit the flag" (NewCLIRunner.Ensure only
// appends --permission-mode when PermissionMode is non-empty — see
// internal/ccpool/cli.go).
var validPermissionModes = map[string]bool{
	"":                  true,
	"default":           true,
	"acceptEdits":       true,
	"plan":              true,
	"auto":              true,
	"dontAsk":           true,
	"bypassPermissions": true,
}

// Validate rejects a Config whose PermissionMode is not one of the claude
// --permission-mode values this module knows how to forward. Docket
// pg2-oju6w Task 5.7: this is the real enum check pg-router's own Config used
// to duplicate — it now lives only here, on the side that actually invokes
// `ccpool new --permission-mode`. Callers (cmd/pg-router-ccpool-handler's
// loadConfig) MUST call this after decoding so an invalid value fails at
// config-load time rather than surfacing later as a rejected ccpool argv.
func (c Config) Validate() error {
	if !validPermissionModes[c.PermissionMode] {
		return fmt.Errorf("invalid permissionMode %q (valid: default, acceptEdits, plan, auto, dontAsk, bypassPermissions)", c.PermissionMode)
	}
	if err := c.validateLease(); err != nil {
		return err
	}
	return c.OriginProbe.Validate()
}

// WorkerBudget builds the budget.Budget a worker/review role's CCPoolConfig
// carries, from this Config's BudgetTokens/BudgetCost/BudgetTime/*Pct
// fields. Mirrors packages/pg-router/internal/config.Config.WorkerBudget()
// exactly.
func (c Config) WorkerBudget() budget.Budget {
	return budget.Budget{
		Tokens:     budget.Limit(c.BudgetTokens),
		Cost:       budget.Limit(c.BudgetCost),
		Time:       c.BudgetTime,
		Thresholds: budget.Thresholds{Reminder: c.ReminderPct, Cancel: c.CancelPct, Hard: c.HardPct},
	}
}

// baseAllowedTools is the built-in claude --allowed-tools allowlist granted
// to every autonomous worker regardless of configuration. Historically
// mirrored packages/pg-router/internal/config.baseAllowedTools byte-for-byte
// (same rationale — see that package's own doc comment); the two have
// deliberately DIVERGED as of bead pg2-s9zh5 below, since
// packages/pg-router/internal/config.baseAllowedTools is not on this
// module's own live dispatch path (this package's Config, not pg-router
// core's, is what `ccpool new --allowed-tools` actually receives —
// internal/ccpool/cli.go's NewCLIRunnerForPool) and that other constant
// carries its own "HUMAN SIGN-OFF REQUIRED" gate this bugfix does not
// touch.
//
// pg-connector issue */ccpool * (bead pg2-s9zh5): every dispatched role
// shares this ONE process-wide allowlist — there is no per-role override
// mechanism today (roles.CCPoolConfig and the nix module's roleFileFor carry
// no allowedTools field of their own) — so a triager-shaped role's own
// dispatch prompt instructing it to read/mutate the escalated bead
// (`pg-connector issue show/comment/update/close`) and inspect/reply to
// stuck sessions (`ccpool list/state/reply/tail`) had no path to those verbs
// under PermissionMode=dontAsk (auto-deny, no prompt possible). Widening the
// shared base list (rather than adding role-scoped plumbing) is the
// deliberately narrower of the two fixes the bead allows, since the
// role-scoped mechanism does not exist yet.
const baseAllowedTools = "Read,Edit,Write,Glob,Grep,Bash(git status:*),Bash(git diff:*),Bash(git log:*),Bash(git add:*),Bash(git commit:*),Bash(git checkout:*),Bash(git switch:*),Bash(git branch:*),Bash(git worktree:*),Bash(git rev-parse:*),Bash(git fetch:*),Bash(bd:*),Bash(go build:*),Bash(go test:*),Bash(go vet:*),Bash(gofmt:*),Bash(go mod:*),Bash(nix flake check:*),Bash(nix fmt:*),Bash(prek:*),Bash(pre-commit:*),Bash(pg-connector issue show:*),Bash(pg-connector issue comment:*),Bash(pg-connector issue update:*),Bash(pg-connector issue close:*),Bash(ccpool list:*),Bash(ccpool state:*),Bash(ccpool reply:*),Bash(ccpool tail:*)"

// defaultAllowedTools builds the AllowedTools default: baseAllowedTools plus,
// when prTool is configured, a Bash(<prTool>:*) grant — mirrors
// packages/pg-router/internal/config.defaultAllowedTools's same rationale
// (a review role's completion action needs that grant under dontAsk
// deny-by-default; see pg2-vmbn7). Empty prTool omits the grant entirely.
func defaultAllowedTools(prTool string) string {
	if prTool == "" {
		return baseAllowedTools
	}
	return baseAllowedTools + ",Bash(" + prTool + ":*)"
}

// Default returns this module's own baseline Config. Values mirror
// packages/pg-router/internal/config.Default()'s corresponding fields as of
// the 2026-09-11 move, so a deployment that supplies no overrides observes
// unchanged behavior.
func Default() Config {
	return Config{
		WorktreeDir:         "",
		BeadsPrefix:         "zr",
		MaxWait:             1800 * time.Second,
		PollInterval:        10 * time.Second,
		LeaseTTL:            2 * time.Minute,
		WorktreeQuietWindow: 2 * time.Minute,
		WorktreeQuietMax:    10 * time.Minute,
		Effort:              "max",
		Model:               "",
		Autonomous:          true,
		PermissionMode:      "dontAsk",
		PRTool:              "",
		AllowedTools:        defaultAllowedTools(""),
		SessionPrefix:       "pg-router-",
		ReminderMsg:         "You are nearing your budget for bead {{.BeadID}} — start wrapping up: record progress with bd comment {{.BeadID}}.",
		WrapUpMsg:           "Budget nearly exhausted for bead {{.BeadID}}. Stop now: commit your notes with bd comment {{.BeadID}}, then finish or hand back. Do not start new work on any other bead.",
		ConfirmIngest:       90 * time.Second,
		BudgetTokens:        0,                // unlimited until ccpool N3
		BudgetCost:          0,                // unlimited until ccpool N3
		BudgetTime:          25 * time.Minute, // strictly < MaxWait (30m)
		ReminderPct:         0.725,
		CancelPct:           0.90,
		HardPct:             1.00,
		OriginProbe:         DefaultOriginProbe(),
	}
}

// LeaseTTLMinPolls is the smallest LeaseTTL, in PollInterval units, Validate
// accepts: the lease is refreshed once per poll, so a TTL below this many polls
// could expire on a live handler after a couple of slow or failed refreshes.
const LeaseTTLMinPolls = 10

// validateLease enforces LeaseTTL >= LeaseTTLMinPolls x PollInterval (bead
// pg2-g2u9m). A zero PollInterval is not this check's concern (it is not a
// valid runtime value either way); only a positive one bounds the TTL.
func (c Config) validateLease() error {
	min := LeaseTTLMinPolls * c.PollInterval
	if c.LeaseTTL <= 0 || c.LeaseTTL < min {
		return fmt.Errorf("invalid leaseTTL %v: must be >= %d x pollInterval (%v)", c.LeaseTTL, LeaseTTLMinPolls, min)
	}
	return nil
}
