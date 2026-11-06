// Package builtin holds what ships inside the pg-review binary: the built-in
// lens, profile and provider definitions and the limit and ccpool defaults
// (Go literals), plus an embedded asset directory for prompt and vocabulary
// files. A config.toml entry with a built-in id is merged onto the built-in
// by internal/config.
package builtin

import (
	"embed"
	"io/fs"
)

// assetsFS holds the embedded prompt and vocabulary files. Packets that own
// that content add files under assets/; the dotfile keeps the directory
// embeddable while it is empty.
//
//go:embed all:assets
var assetsFS embed.FS

// FS returns the embedded assets rooted at the assets directory, so a path
// such as prompts/base.md addresses assets/prompts/base.md.
func FS() fs.FS {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic(err) // the directory is embedded above; cannot fail
	}
	return sub
}

// Limits are the numeric limits of [limits].
type Limits struct {
	ItemsMaxPerRun        int
	ItemsMaxPending       int
	EvidenceTTLDays       int
	FieldMaxChars         int
	ItemsAutoDeferDays    int
	InteractiveTTLMinutes int
}

// Profile is an analyst profile: how much model and effort a kind of review
// gets.
type Profile struct {
	Name       string
	Backend    string
	Model      string
	Effort     string
	Minutes    int
	QueriesMax int
}

// Ccpool is the [ccpool] section. PoolDir is optional: empty means ccpool's
// own default pool. NeutralDir is empty in the built-in default and is
// resolved from the state directory by the loader.
type Ccpool struct {
	PoolDir    string
	NeutralDir string
	ReadTools  []string
}

// Provider is an evidence provider: a command that prints evidence rows for
// a window and a scope.
type Provider struct {
	Name           string
	Command        []string
	SchemaVersion  int
	TimeoutSeconds int
	Scopes         []string
	// Queries maps a lens id to the canned query names the provider runs
	// for it.
	Queries map[string][]string
	// ArgsStyle is cwd-prefix or inventory; empty means the default for the
	// provider name (see DefaultArgsStyle).
	ArgsStyle string
}

// Lens is one analysis: prompt files, providers, a closed list of kinds and
// a profile.
type Lens struct {
	ID          string
	Family      string
	Profile     string
	BasePrompt  string
	Prompt      string
	Providers   []string
	MaxFindings int
	Kinds       []string
}

// Args styles a provider accepts.
const (
	ArgsStyleCwdPrefix = "cwd-prefix"
	ArgsStyleInventory = "inventory"
)

// DefaultArgsStyle is the args style of a provider with none configured:
// inventory for config-inventory, cwd-prefix for everything else.
func DefaultArgsStyle(providerName string) string {
	if providerName == "config-inventory" {
		return ArgsStyleInventory
	}
	return ArgsStyleCwdPrefix
}

// DefaultLimits returns the built-in limits.
func DefaultLimits() Limits {
	return Limits{
		ItemsMaxPerRun:        5,
		ItemsMaxPending:       12,
		EvidenceTTLDays:       14,
		FieldMaxChars:         1200,
		ItemsAutoDeferDays:    30,
		InteractiveTTLMinutes: 240,
	}
}

// DefaultCcpool returns the built-in [ccpool] section (no pool_dir; the
// neutral dir is resolved by the loader).
func DefaultCcpool() Ccpool {
	return Ccpool{ReadTools: []string{"Read", "Grep", "Glob"}}
}

// Profiles returns the built-in profiles. The model names are UNVERIFIED
// until the first live run per profile.
func Profiles() []Profile {
	return []Profile{
		{Name: "health-check", Backend: "ccpool", Model: "claude-sonnet-5-5", Effort: "low", Minutes: 15, QueriesMax: 10},
		{Name: "deep-review", Backend: "ccpool", Model: "claude-opus-5-5", Effort: "high", Minutes: 45, QueriesMax: 40},
	}
}

// Providers returns the built-in providers; each command is a binary of this
// package resolved on PATH.
func Providers() []Provider {
	return []Provider{
		{
			Name: "ccaudit", Command: []string{"pg-review-provider-ccaudit"},
			SchemaVersion: 1, TimeoutSeconds: 120,
			Queries: map[string][]string{
				"agent-workflow": {
					"error-rate-by-tool", "top-signatures", "cost-by-signature", "retry-chains",
					"denied-tool-calls", "interruptions", "hook-refusals-in-body",
				},
			},
			ArgsStyle: ArgsStyleCwdPrefix,
		},
		{
			Name: "hook-decisions", Command: []string{"pg-review-provider-hook-decisions"},
			SchemaVersion: 1, TimeoutSeconds: 60,
			Queries: map[string][]string{
				"agent-workflow": {"decisions-by-rule", "abstains-by-pattern"},
			},
			ArgsStyle: ArgsStyleCwdPrefix,
		},
		{
			Name: "config-inventory", Command: []string{"pg-review-provider-config-inventory"},
			SchemaVersion: 1, TimeoutSeconds: 120,
			Queries: map[string][]string{
				"config-health": {"inventory"},
			},
			ArgsStyle: ArgsStyleInventory,
		},
	}
}

// Lenses returns the two built-in lens definitions.
func Lenses() []Lens {
	return []Lens{
		{
			ID: "agent-workflow", Family: "workflow", Profile: "deep-review",
			BasePrompt: "prompts/base.md", Prompt: "prompts/agent-workflow.md",
			Providers:   []string{"ccaudit", "hook-decisions"},
			MaxFindings: 5,
			Kinds:       []string{"misclassified-error", "repeat-refusal", "tool-ux", "wasted-retries", "cost-hotspot"},
		},
		{
			ID: "config-health", Family: "health", Profile: "health-check",
			BasePrompt: "prompts/base.md", Prompt: "prompts/config-health.md",
			Providers:   []string{"config-inventory"},
			MaxFindings: 8,
			Kinds:       []string{"stale-reference", "conflict", "duplicate", "unused", "oversize"},
		},
	}
}
