package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/budget"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/prompt"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// envRoleConfig/envConfig name this module's OWN configuration inputs — a
// per-process role (this handler process dispatches ONE role, registered
// under its own `register --id`; DEC-WIRE-1's register message names the
// participant, not a per-dispatch role selection) and the launch/prompt
// Config. These small JSON files remain this packet's minimum viable config
// story — a fuller one (this module's own TOML file, decoded like
// packages/pg-router/internal/config/registry.go decodes pg-router's own
// [[role]] table) is still not built. Docket pg2-oju6w's Task 5.7 DID land
// here, though, narrower than that: loadConfig below now calls
// config.Config.Validate(), which rejects an invalid claude
// --permission-mode value at THIS module's own config-load time (the real
// enum check pg-router's own config used to duplicate).
const (
	envRoleConfig = "PG_ROUTER_CCPOOL_HANDLER_ROLE"
	envConfig     = "PG_ROUTER_CCPOOL_HANDLER_CONFIG"
)

// defaultBudgetStopEscalateAfter is the default budget_stop_escalate_after
// (bead pg2-6akgz): stops of one bead before escalation; 0 disables.
const defaultBudgetStopEscalateAfter = 3

// roleFile is the on-disk JSON shape --role-config decodes. Completion/
// FailureAction/DispatchFailAction already implement encoding.TextUnmarshaler
// (internal/roles/enums.go), so encoding/json calls those directly for the
// matching string fields — only PromptBody/Argv need a second pass to parse
// into internal/prompt's *template.Template form roles.CCPoolConfig/
// CommandConfig actually carry.
type roleFile struct {
	Name   string `json:"name"`
	Type   string `json:"type"` // "ccpool" | "command"
	CCPool *struct {
		Actor           string                   `json:"actor"`
		SkillMD         string                   `json:"skillMD"`
		Completion      roles.Completion         `json:"completion"`
		OnFailure       roles.FailureAction      `json:"onFailure"`
		OnDispatchFail  roles.DispatchFailAction `json:"onDispatchFail"`
		AuthorshipGuard bool                     `json:"authorshipGuard"`
		PromptBody      string                   `json:"promptBody"`
		// Budget carries this role's own Tokens/Cost/Time ceiling
		// (roles.CCPoolConfig.Budget's own shape, minus Thresholds/Prices —
		// those stay pool-wide, see overlayBudgetThresholds below; the OLD
		// TOML schema's per-role [role.ccpool.budget] had this same
		// three-key shape). Time is a time.ParseDuration string ("25m"), not
		// raw nanoseconds, for the same human-friendly reason
		// packages/pg-router's own TOML `duration` type parses "25m"/"30m"
		// strings rather than requiring nanosecond integers. Absent/zero
		// means unlimited (budget.Limit(0).Unlimited() == true; Time <= 0
		// means no time bound) — matching the old schema's own explicit
		// `budget.time = "0s"` meaning "deliberately unlimited".
		Budget struct {
			Tokens int64  `json:"tokens"`
			Cost   int64  `json:"cost"`
			Time   string `json:"time"`
		} `json:"budget"`
		Isolation roles.IsolationConfig `json:"isolation"`
		// PoolDir (bead pg2-mr0sl): this role's own dedicated ccpool pool
		// directory override -- see roles.CCPoolConfig.PoolDir's own doc
		// comment for the full rationale. "" (absent/omitted, the
		// zero-value default) means unchanged behavior: no override, this
		// process's own inherited CCPOOL_POOL still applies.
		PoolDir string `json:"poolDir"`
		// BeadsDir (bead pg2-2grpj): workspace dir of the bd tracker this
		// role's items live in, when not cfg.RepoRoot's. See
		// roles.CCPoolConfig.BeadsDir.
		BeadsDir string `json:"beadsDir"`
		// BudgetStopEscalateAfter (TOML key budget_stop_escalate_after, bead
		// pg2-6akgz): per-bead budget-stop threshold. Absent => defaultBudgetStopEscalateAfter
		// (3); 0 => disabled (kill switch). A pointer distinguishes absent from 0.
		BudgetStopEscalateAfter *int `json:"budgetStopEscalateAfter"`
		// WorktreeQuietWindow (bead pg2-uyahp, INV-CCH-23): per-role override of
		// the handler-wide worktree quiet window, a time.ParseDuration string
		// ("30s"). Absent/"" => no override (the handler default applies). An
		// explicit zero or negative value is rejected (it would read as "disable
		// the check"). The bounds that depend on the handler Config
		// (pollInterval, worktreeQuietMax) are checked at dispatch, where both
		// are loaded (config.Config.ValidateQuietWindowOverride).
		WorktreeQuietWindow string `json:"worktreeQuietWindow"`
		// ExtraAllowedTools (bead pg2-nk6th.5): per-role tool grants merged onto
		// the handler-wide allowedTools for this role only; see
		// roles.CCPoolConfig.ExtraAllowedTools. Absent/empty => no extra grants.
		ExtraAllowedTools []string `json:"extraAllowedTools"`
	} `json:"ccpool,omitempty"`
	Command *struct {
		Argv []string `json:"argv"`
	} `json:"command,omitempty"`
}

// loadRole reads and decodes a roleFile from path into a roles.Role, parsing
// its prompt/argv templates. An empty path is a configuration error — every
// dispatch invocation needs to know which role it is.
func loadRole(path string) (roles.Role, error) {
	if path == "" {
		return roles.Role{}, fmt.Errorf("no role config given (--role-config, or " + envRoleConfig + ")")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return roles.Role{}, fmt.Errorf("read role config %s: %w", path, err)
	}
	var rf roleFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return roles.Role{}, fmt.Errorf("decode role config %s: %w", path, err)
	}
	r := roles.Role{Name: rf.Name, Type: rf.Type}
	switch rf.Type {
	case "ccpool":
		if rf.CCPool == nil {
			return roles.Role{}, fmt.Errorf("role config %s: type is ccpool but no [ccpool] block", path)
		}
		// The actor is the bd identity this role's worker sessions claim under
		// (exported as BEADS_ACTOR) and its handler-side bd writes carry as
		// --actor. Empty would make bd fall back to git user.name -- the
		// operator -- so a claim would be recorded in their name (pg2-lhi3b).
		if strings.TrimSpace(rf.CCPool.Actor) == "" {
			return roles.Role{}, fmt.Errorf("role config %s: ccpool.actor is required (an empty actor makes bd attribute claims to the operator)", path)
		}
		tmpl, err := prompt.Parse(rf.Name, rf.CCPool.PromptBody)
		if err != nil {
			return roles.Role{}, fmt.Errorf("role config %s: parse promptBody: %w", path, err)
		}
		// budget.time is a "25m"-style duration string (see roleFile's own
		// doc comment above); "" (absent) means no time bound, matching
		// budget.Budget's own zero value rather than erroring on
		// time.ParseDuration("").
		var budgetTime time.Duration
		if rf.CCPool.Budget.Time != "" {
			budgetTime, err = time.ParseDuration(rf.CCPool.Budget.Time)
			if err != nil {
				return roles.Role{}, fmt.Errorf("role config %s: parse budget.time %q: %w", path, rf.CCPool.Budget.Time, err)
			}
		}
		stopAfter := defaultBudgetStopEscalateAfter
		if rf.CCPool.BudgetStopEscalateAfter != nil {
			stopAfter = *rf.CCPool.BudgetStopEscalateAfter
		}
		if stopAfter < 0 {
			return roles.Role{}, fmt.Errorf("role config %s: budgetStopEscalateAfter %d must be >= 0", path, stopAfter)
		}
		var quietWindow time.Duration
		if rf.CCPool.WorktreeQuietWindow != "" {
			quietWindow, err = time.ParseDuration(rf.CCPool.WorktreeQuietWindow)
			if err != nil {
				return roles.Role{}, fmt.Errorf("role config %s: parse worktreeQuietWindow %q: %w", path, rf.CCPool.WorktreeQuietWindow, err)
			}
			if quietWindow <= 0 {
				return roles.Role{}, fmt.Errorf("role config %s: worktreeQuietWindow %q must be > 0 (omit it to use the handler default)", path, rf.CCPool.WorktreeQuietWindow)
			}
		}
		for i, g := range rf.CCPool.ExtraAllowedTools {
			if strings.TrimSpace(g) == "" {
				return roles.Role{}, fmt.Errorf("role config %s: extraAllowedTools[%d] is empty", path, i)
			}
		}
		// isolation.prefetch (bead pg2-hh32y) checks a commit out inside the
		// worktree this handler created, so it is valid ONLY with the
		// worktree strategy, and its templates must parse.
		if pf := rf.CCPool.Isolation.Prefetch; pf != nil {
			if t := rf.CCPool.Isolation.Type; t != "" && t != "worktree" {
				return roles.Role{}, fmt.Errorf("role config %s: isolation.prefetch requires worktree isolation, not %q", path, t)
			}
			if err := executor.ValidatePrefetch(*pf); err != nil {
				return roles.Role{}, fmt.Errorf("role config %s: isolation.%w", path, err)
			}
		}
		r.CCPool = &roles.CCPoolConfig{
			Actor: rf.CCPool.Actor, SkillMD: rf.CCPool.SkillMD,
			Completion: rf.CCPool.Completion, OnFailure: rf.CCPool.OnFailure, OnDispatchFail: rf.CCPool.OnDispatchFail,
			AuthorshipGuard: rf.CCPool.AuthorshipGuard, PromptBody: rf.CCPool.PromptBody, Prompt: tmpl,
			Budget: budget.Budget{
				Tokens: budget.Limit(rf.CCPool.Budget.Tokens),
				Cost:   budget.Limit(rf.CCPool.Budget.Cost),
				Time:   budgetTime,
			},
			Isolation: rf.CCPool.Isolation,
			PoolDir:   rf.CCPool.PoolDir,
			BeadsDir:  rf.CCPool.BeadsDir,

			BudgetStopEscalateAfter: stopAfter,
			WorktreeQuietWindow:     quietWindow,
			ExtraAllowedTools:       rf.CCPool.ExtraAllowedTools,
		}
	case "command":
		if rf.Command == nil {
			return roles.Role{}, fmt.Errorf("role config %s: type is command but no [command] block", path)
		}
		// ArgvTmpl is left nil: commandRun.renderArgv (internal/executor/
		// command.go) re-parses each Argv element fresh via prompt.Parse at
		// dispatch time and never reads Role.Command.ArgvTmpl — that field
		// exists on packages/pg-router/internal/roles.CommandConfig purely
		// for config-load-time template validation, which this module's own
		// fuller config story (Task 5.7) may add later.
		r.Command = &roles.CommandConfig{Argv: rf.Command.Argv}
	default:
		return roles.Role{}, fmt.Errorf("role config %s: unknown type %q (want ccpool or command)", path, rf.Type)
	}
	return r, nil
}

// loadConfig reads and decodes the launch/prompt config.Config from path,
// overlaying config.Default() so an absent/partial file still yields sane
// values. An empty path uses config.Default() outright (still validated: a
// mis-edited Default() would otherwise ship silently).
//
// c.Validate() runs on every path — including the empty-path/Default() case
// — so an invalid PermissionMode (docket pg2-oju6w Task 5.7's real claude
// --permission-mode enum check) is rejected HERE, at this module's own
// config-load time, rather than surfacing later as a rejected ccpool argv.
func loadConfig(path string) (config.Config, error) {
	c := config.Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return config.Config{}, fmt.Errorf("read config %s: %w", path, err)
		}
		if err := json.Unmarshal(data, &c); err != nil {
			return config.Config{}, fmt.Errorf("decode config %s: %w", path, err)
		}
	}
	if err := c.Validate(); err != nil {
		return config.Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

// overlayBudgetThresholds sets a ccpool role's budget escalation Thresholds
// from cfg's pool-wide Config.WorkerBudget() — Thresholds (Reminder/Cancel/
// Hard fractions) are pool-wide only: roleFile.CCPool.Budget carries no
// thresholds field of its own (see its doc comment above), so every ccpool
// role's watchdog escalates against the SAME percentages, while
// Tokens/Cost/Time (loadRole's own per-role decode) is the one per-role
// override. A no-op for a non-ccpool role (role.CCPool == nil).
//
// This is load-bearing, not cosmetic: without it, a role whose roleFile sets
// any finite Tokens/Cost/Time would evaluate against a Thresholds{0,0,0}
// zero value — Hard == 0 makes budget.Budget.Evaluate return Hard on its
// very first tick (pct >= 0 is always true) — an instant false-positive
// hard-stop rather than the working watchdog this bead exists to restore.
func overlayBudgetThresholds(role roles.Role, cfg config.Config) {
	if role.CCPool == nil {
		return
	}
	role.CCPool.Budget.Thresholds = cfg.WorkerBudget().Thresholds
}
