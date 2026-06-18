package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/prompt"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// duration is a TOML-decodable time.Duration ("30m", "25m"), mirroring ccpool's
// Duration. Used for [pool].budget.time and per-role budget.time.
type duration struct{ D time.Duration }

func (d *duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	d.D = v
	return nil
}

// fileShape is the typed first-pass decode target. A single-bracket `[role]` table
// (the classic `[[role]]` typo) makes toml.Decode fail with a table-vs-array type
// mismatch, which surfaces as a hard error — no special detection needed.
type fileShape struct {
	Pool  poolTOML   `toml:"pool"`
	Roles []roleTOML `toml:"role"`
}

type poolTOML struct {
	SelfLogin string      `toml:"self_login"`
	Budget    *budgetTOML `toml:"budget"`
}

type budgetTOML struct {
	Tokens *int64    `toml:"tokens"`
	Cost   *int64    `toml:"cost"`
	Time   *duration `toml:"time"`
}

type roleTOML struct {
	Name    string         `toml:"name"`
	Type    string         `toml:"type"`
	Cap     int            `toml:"cap"`
	Enabled *bool          `toml:"enabled"` // pointer: absent => default true
	Query   queryTOML      `toml:"query"`
	CCPool  toml.Primitive `toml:"ccpool"`  // decoded by buildCCPool iff type==ccpool
	Command toml.Primitive `toml:"command"` // decoded by buildCommand iff type==command
}

// queryTOML holds the query discriminator plus each query type's sub-table as a
// deferred-decode Primitive (the factory decodes the one matching `type`).
type queryTOML struct {
	Type         string         `toml:"type"`
	BeadsReady   toml.Primitive `toml:"beads-ready"`
	BeadsList    toml.Primitive `toml:"beads-list"`
	Command      toml.Primitive `toml:"command"`
	GitHubIssues toml.Primitive `toml:"github-issues"`
	JiraIssues   toml.Primitive `toml:"jira-issues"`
}

// ccpoolTOML is decoded from the [role.ccpool] primitive. The enum fields validate
// at decode (UnmarshalText), so an invalid value fails with TOML location context.
type ccpoolTOML struct {
	Actor           string                   `toml:"actor"`
	SkillMD         string                   `toml:"skill_md"`
	Completion      roles.Completion         `toml:"completion"`
	OnFailure       roles.FailureAction      `toml:"on_failure"`
	OnDispatchFail  roles.DispatchFailAction `toml:"on_dispatch_fail"`
	AuthorshipGuard bool                     `toml:"authorship_guard"`
	Prompt          string                   `toml:"prompt"`
	PromptFile      string                   `toml:"prompt_file"`
	Budget          *budgetTOML              `toml:"budget"`
}

type commandTOML struct {
	Argv []string `toml:"argv"`
}

// Registry decodes a config file's roles and queries. It is instance-scoped (no
// package-level init() globals) — matching the codebase's constructor-injection
// convention. Adding a query type is one line in query.NewQueryFactories; adding a
// role type is one case in buildRole.
type Registry struct{ queries *query.Factories }

func NewRegistry() *Registry { return &Registry{queries: query.NewQueryFactories()} }

// decodeRoleSet decodes path: overlays its [pool] scalars onto c, then builds the
// RoleSet. Returns (nil, nil) when the file has no [[role]] array (pool-only or
// empty) to signal "use the built-in role set". All per-role errors are aggregated.
func (r *Registry) decodeRoleSet(path, configDir string, c *Config) (roles.RoleSet, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var shape fileShape
	md, err := toml.Decode(string(body), &shape)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if shape.Pool.SelfLogin != "" {
		c.SelfLogin = shape.Pool.SelfLogin
	}
	overlayConfigBudget(c, shape.Pool.Budget)
	if len(shape.Roles) == 0 {
		return nil, nil // pool-only / empty => built-ins
	}
	var out roles.RoleSet
	var errs []error
	seen := map[string]bool{}
	for i, rt := range shape.Roles {
		role, err := r.buildRole(md, rt, configDir, *c)
		if err != nil {
			errs = append(errs, fmt.Errorf("role[%d] %q: %w", i, rt.Name, err))
			continue
		}
		if seen[role.Name] {
			errs = append(errs, fmt.Errorf("duplicate role name %q", role.Name))
			continue
		}
		seen[role.Name] = true
		out = append(out, role)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

func (r *Registry) buildRole(md toml.MetaData, rt roleTOML, configDir string, c Config) (roles.Role, error) {
	if rt.Name == "" {
		return roles.Role{}, fmt.Errorf("name is required")
	}
	q, err := r.decodeQuery(md, rt.Query)
	if err != nil {
		return roles.Role{}, err
	}
	enabled := true
	if rt.Enabled != nil {
		enabled = *rt.Enabled
	}
	role := roles.Role{Name: rt.Name, Type: rt.Type, Cap: rt.Cap, Enabled: enabled, Query: q}
	switch rt.Type {
	case "ccpool":
		cc, err := buildCCPool(md, rt.CCPool, configDir, c)
		if err != nil {
			return roles.Role{}, err
		}
		role.CCPool = cc
	case "command":
		cmd, err := buildCommand(md, rt.Command)
		if err != nil {
			return roles.Role{}, err
		}
		role.Command = cmd
	default:
		return roles.Role{}, fmt.Errorf("unknown role type %q (known: ccpool, command)", rt.Type)
	}
	return role, nil
}

func (r *Registry) decodeQuery(md toml.MetaData, qt queryTOML) (query.Query, error) {
	prims := map[string]toml.Primitive{
		"beads-ready":   qt.BeadsReady,
		"beads-list":    qt.BeadsList,
		"command":       qt.Command,
		"github-issues": qt.GitHubIssues,
		"jira-issues":   qt.JiraIssues,
	}
	prim, ok := prims[qt.Type]
	if !ok {
		return nil, fmt.Errorf("unknown query type %q", qt.Type)
	}
	return r.queries.Decode(qt.Type, md, prim)
}

func buildCCPool(md toml.MetaData, prim toml.Primitive, configDir string, c Config) (*roles.CCPoolConfig, error) {
	var ct ccpoolTOML
	if err := md.PrimitiveDecode(prim, &ct); err != nil {
		return nil, err
	}
	if (ct.Prompt == "") == (ct.PromptFile == "") {
		return nil, fmt.Errorf("ccpool role: exactly one of prompt / prompt_file is required")
	}
	if ct.Completion == "" || ct.OnFailure == "" || ct.OnDispatchFail == "" {
		return nil, fmt.Errorf("ccpool role: completion, on_failure, and on_dispatch_fail are required")
	}
	// actor is required: a ccpool role dispatches under BEADS_ACTOR, and an empty
	// actor (e.g. from a typo'd `actorr =` key, which BurntSushi silently ignores)
	// would create beads with no attribution and break the created-marker diff.
	if ct.Actor == "" {
		return nil, fmt.Errorf("ccpool role: actor is required")
	}
	body := ct.Prompt
	if ct.PromptFile != "" {
		b, err := os.ReadFile(filepath.Join(configDir, ct.PromptFile))
		if err != nil {
			return nil, fmt.Errorf("ccpool role: prompt_file: %w", err)
		}
		body = string(b)
	}
	tmpl, err := prompt.Parse("role-prompt", body)
	if err != nil {
		return nil, fmt.Errorf("ccpool role: prompt template: %w", err)
	}
	b := c.WorkerBudget() // pool default budget; per-role budget overlays it
	overlayBudget(&b, ct.Budget)
	return &roles.CCPoolConfig{
		Actor:           ct.Actor,
		SkillMD:         ct.SkillMD,
		Completion:      ct.Completion,
		OnFailure:       ct.OnFailure,
		OnDispatchFail:  ct.OnDispatchFail,
		AuthorshipGuard: ct.AuthorshipGuard,
		PromptBody:      body,
		Prompt:          tmpl,
		Budget:          b,
	}, nil
}

func buildCommand(md toml.MetaData, prim toml.Primitive) (*roles.CommandConfig, error) {
	var ct commandTOML
	if err := md.PrimitiveDecode(prim, &ct); err != nil {
		return nil, err
	}
	if len(ct.Argv) == 0 {
		return nil, fmt.Errorf("command role: argv is required")
	}
	return &roles.CommandConfig{Argv: ct.Argv}, nil
}

// overlayBudget applies a per-role budget over a base (pool default), field by field.
func overlayBudget(b *budget.Budget, t *budgetTOML) {
	if t == nil {
		return
	}
	if t.Tokens != nil {
		b.Tokens = budget.Limit(*t.Tokens)
	}
	if t.Cost != nil {
		b.Cost = budget.Limit(*t.Cost)
	}
	if t.Time != nil {
		b.Time = t.Time.D
	}
}

// overlayConfigBudget applies the [pool].budget over the Config's budget scalars.
func overlayConfigBudget(c *Config, t *budgetTOML) {
	if t == nil {
		return
	}
	if t.Tokens != nil {
		c.BudgetTokens = *t.Tokens
	}
	if t.Cost != nil {
		c.BudgetCost = *t.Cost
	}
	if t.Time != nil {
		c.BudgetTime = t.Time.D
	}
}
