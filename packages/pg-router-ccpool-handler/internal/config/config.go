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
// A fuller config story — decoding this module's own config file, real
// `claude`-enum permission-mode validation — is Task 5.7's job (docket
// pg2-oju6w), not this one's; Default() below is deliberately a plain Go
// literal, mirroring packages/pg-router/internal/config.Default()'s own
// values for the fields this module shares with it, so today's behavior is
// unchanged for a deployment that never overrides them.
package config

import (
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
	// WorktreeDir is the parent directory fresh per-item git worktrees are
	// created under (worktree isolation, the default).
	WorktreeDir string
	// MaxWait bounds how long a ccpool session may run before it is
	// considered stuck (waitDone's deadline).
	MaxWait time.Duration
	// PollInterval is how often waitDone re-checks ccpool's session list.
	PollInterval time.Duration
	// Effort/Model/PermissionMode/AllowedTools/Autonomous are forwarded
	// verbatim to `ccpool new` (NewCLIRunner).
	Effort         string
	Model          string
	PermissionMode string
	AllowedTools   string
	Autonomous     bool
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

// Default returns this module's own baseline Config. Values mirror
// packages/pg-router/internal/config.Default()'s corresponding fields as of
// the 2026-09-11 move, so a deployment that supplies no overrides observes
// unchanged behavior.
func Default() Config {
	return Config{
		WorktreeDir:    "",
		MaxWait:        1800 * time.Second,
		PollInterval:   10 * time.Second,
		Effort:         "max",
		Model:          "",
		Autonomous:     true,
		PermissionMode: "dontAsk",
		AllowedTools:   "Read,Edit,Write,Glob,Grep,Bash(git status:*),Bash(git diff:*),Bash(git log:*),Bash(git add:*),Bash(git commit:*),Bash(git checkout:*),Bash(git switch:*),Bash(git branch:*),Bash(git worktree:*),Bash(git rev-parse:*),Bash(git fetch:*),Bash(bd:*),Bash(pg-pr:*),Bash(go build:*),Bash(go test:*),Bash(go vet:*),Bash(gofmt:*),Bash(go mod:*),Bash(nix flake check:*),Bash(nix fmt:*),Bash(prek:*),Bash(pre-commit:*)",
		SessionPrefix:  "pg-router-",
		ReminderMsg:    "You are nearing your budget for bead {{.BeadID}} — start wrapping up: record progress with bd comment {{.BeadID}}.",
		WrapUpMsg:      "Budget nearly exhausted for bead {{.BeadID}}. Stop now: commit your notes with bd comment {{.BeadID}}, then finish or hand back. Do not start new work on any other bead.",
		ConfirmIngest:  90 * time.Second,
		BudgetTokens:   0,                // unlimited until ccpool N3
		BudgetCost:     0,                // unlimited until ccpool N3
		BudgetTime:     25 * time.Minute, // strictly < MaxWait (30m)
		ReminderPct:    0.725,
		CancelPct:      0.90,
		HardPct:        1.00,
	}
}
