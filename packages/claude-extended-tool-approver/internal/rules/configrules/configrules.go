package configrules

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// Config is the JSON structure read from
// $XDG_CONFIG_HOME/claude-extended-tool-approver/rules.json.
//
// It is the single, auditable home for every consumer-specific (non-generic)
// rule extension. The flat approvedCommands/blockedCommands drive the
// config-rules Rule itself; the structured Kubectl, Buildtools, Ssh, Vault,
// Curl, and Monorepo sub-configs are loaded once and INJECTED (dependency
// injection) into their respective rules by internal/setup/factory.go, so those
// rules stay generic in the base and become consumer-aware only via this config
// (ADR 0033). Every structured block is DATA only — the classification MECHANISM
// lives in the rule package; an absent/empty block leaves the rule at its safe
// base default (Abstain for the command-aware ssh/vault/curl/monorepo rules).
type Config struct {
	ApprovedCommands []string         `json:"approvedCommands"`
	BlockedCommands  []string         `json:"blockedCommands"`
	Kubectl          KubectlConfig    `json:"kubectl"`
	Buildtools       BuildtoolsConfig `json:"buildtools"`
	Ssh              SshConfig        `json:"ssh"`
	Vault            VaultConfig      `json:"vault"`
	Curl             CurlConfig       `json:"curl"`
	Monorepo         MonorepoConfig   `json:"monorepo"`
}

// SshConfig carries the consumer-specific ssh/scp policy DATA injected into the
// ssh rule. Every field is data-only; the classification MECHANISM lives in the
// ssh rule. An empty SshConfig makes the ssh rule Abstain on every command (the
// safe base default) — a consumer must supply data for the rule to classify.
type SshConfig struct {
	// AllowedUsers are the ssh/scp login users that may be targeted (e.g.
	// "deploy"). An explicit user outside this set is Rejected.
	AllowedUsers []string `json:"allowedUsers"`
	// ReadonlyCommands are remote executable basenames considered read-only
	// (e.g. "ls", "cat", "systemctl").
	ReadonlyCommands []string `json:"readonlyCommands"`
	// ReadonlySubcommands restricts a read-only command to specific first
	// subcommands (e.g. "systemctl" -> ["status","is-active"]). A command absent
	// from this map is read-only for any subcommand.
	ReadonlySubcommands map[string][]string `json:"readonlySubcommands"`
	// DangerousInlineFlags maps a read-only command basename to flags that DEMOTE
	// it from read-only to Ask when present, even though the command is in
	// ReadonlyCommands (e.g. "journalctl" -> ["--vacuum-size","--rotate"], "sed" ->
	// ["-i"], "find" -> ["-delete","-exec"]). A token matches when it equals the
	// flag or begins with "<flag>=". This lets a consumer read-approve a read-mostly
	// command while still forcing Ask on a destructive invocation.
	DangerousInlineFlags map[string][]string `json:"dangerousInlineFlags"`
	// SecretPathPatterns are substrings that mark a referenced remote path as
	// secret (e.g. "/etc/shadow", "id_rsa", ".env"); a match forces Ask.
	SecretPathPatterns []string `json:"secretPathPatterns"`
	// PasswordFlagPatterns are lowercased `key=value` substrings that mark an
	// -o option as enabling password auth (e.g. "passwordauthentication=yes");
	// a match forces Reject.
	PasswordFlagPatterns []string `json:"passwordFlagPatterns"`
}

// VaultConfig carries the consumer-specific HashiCorp Vault verb DATA injected
// into the vault rule. An empty VaultConfig makes the vault rule Abstain on
// every command (the safe base default).
type VaultConfig struct {
	// ReadVerbs are vault subcommands (single token like "read", or an
	// "a b" compound like "kv get") approved as read-only.
	ReadVerbs []string `json:"readVerbs"`
	// WriteVerbs are vault subcommands (single token or compound) that require
	// approval (Ask).
	WriteVerbs []string `json:"writeVerbs"`
}

// CurlConfig carries the consumer-specific curl domain DATA injected into the
// curl rule. An empty CurlConfig leaves only the base generic hosts
// (localhost/loopback and the well-known GitHub read hosts) approved for
// read-only requests. The curl rule only ever Approves or Abstains — it never
// Rejects/Asks; a non-matching request Abstains (defers to Claude).
type CurlConfig struct {
	// AllowedDomainSuffixes name domains whose endpoints may be fetched with a
	// read-only method (GET/HEAD) without confirmation. An entry without a
	// leading dot (e.g. "nixos.org") matches the domain itself AND its
	// subdomains; an entry WITH a leading dot (e.g. ".internal.example") matches
	// subdomains ONLY (never the bare apex).
	AllowedDomainSuffixes []string `json:"allowedDomainSuffixes"`
	// DomainMethods grant additional (possibly non-read-only) HTTP methods to
	// specific domains — the mechanism for allowing, e.g., a POST to an internal
	// API. A request whose host matches a DomainSuffix and whose method is in
	// that entry's Methods is Approved.
	DomainMethods []CurlDomainMethods `json:"domainMethods"`
}

// CurlDomainMethods approves the listed HTTP Methods for hosts matching
// DomainSuffix. DomainSuffix uses the same matching rule as
// CurlConfig.AllowedDomainSuffixes (leading dot => subdomains only). Methods are
// case-insensitive HTTP verbs (e.g. "GET", "POST").
type CurlDomainMethods struct {
	DomainSuffix string   `json:"domainSuffix"`
	Methods      []string `json:"methods"`
}

// MonorepoConfig carries the consumer-specific monorepo command-boundary DATA
// injected into the monorepo rule. An empty MonorepoConfig makes the monorepo
// rule Abstain on every command (the safe base default).
type MonorepoConfig struct {
	// ApprovedCommands are monorepo command/script basenames approved
	// unconditionally (after normalizing the executable relative to the project
	// root), e.g. "tc", "uv".
	ApprovedCommands []string `json:"approvedCommands"`
	// DangerousEnvByWrapper maps an approved command basename to a set of env
	// var names that, when present as an inline assignment, cause the approval to
	// be withheld (Abstain, deferred to Claude) — e.g. a wrapper that honors a
	// dangerous override var.
	DangerousEnvByWrapper map[string][]string `json:"dangerousEnvByWrapper"`
}

// KubectlConfig carries consumer-specific kc/kubectl extensions injected into
// the kubectl rule. Every field is additive: an empty KubectlConfig leaves the
// base generic kubectl behavior fully intact (recognizes only `kubectl`, the
// standard read-only/exec verbs, and rollout status/history — no dev-workspace
// scope, no aliases). See ADR 0033 and the kubectl rule for the semantics of
// each key.
type KubectlConfig struct {
	// ExecutableAliases are extra command basenames treated as kubectl (e.g. "kc").
	ExecutableAliases []string `json:"executableAliases"`
	// ReadOnlyVerbs are extra verbs approved as read-only (e.g. plugin log verbs).
	ReadOnlyVerbs []string `json:"readOnlyVerbs"`
	// ExecVerbs are extra exec-family verbs that recurse into their inner command
	// when dev-scoped (in addition to the base `exec`).
	ExecVerbs []string `json:"execVerbs"`
	// ScopedApproveVerbs are mutating verbs auto-approved iff dev-workspace-scoped.
	ScopedApproveVerbs []string `json:"scopedApproveVerbs"`
	// PositionalWorkspaceVerbs take the dev workspace as a bare positional arg
	// (e.g. `kc sync -f <path> d-phillipg01`) rather than behind a --ws/-n flag.
	// MUST be a subset of ScopedApproveVerbs to have any effect.
	PositionalWorkspaceVerbs []string `json:"positionalWorkspaceVerbs"`
	// DevWorkspaceFlags are extra flags (beyond the generic -n/--namespace) whose
	// value names a workspace to check for the dev prefix (e.g. --ws/--workspace).
	DevWorkspaceFlags []string `json:"devWorkspaceFlags"`
	// ClusterEnvVar names an env var whose value must carry a dev-cluster prefix
	// for a command to be dev-scoped (e.g. "KC_CLUSTER"). Empty disables the check.
	ClusterEnvVar string `json:"clusterEnvVar"`
	// DevClusterPrefixes are the acceptable prefixes of ClusterEnvVar's value.
	DevClusterPrefixes []string `json:"devClusterPrefixes"`
	// DevWorkspacePrefix marks a personal dev workspace name (e.g. "d-"). Empty
	// means no name is ever treated as a personal dev workspace.
	DevWorkspacePrefix string `json:"devWorkspacePrefix"`
	// NonDevAccounts are AWS_PROFILE accounts (the part before '/') that force a
	// non-dev classification (prod/shared clusters). AWS_PROFILE itself is the
	// generic, hardcoded env var; only the account names are consumer-specific.
	NonDevAccounts []string `json:"nonDevAccounts"`
}

// BuildtoolsConfig carries consumer-specific build tool / script approvals
// injected into the build-tools rule. Additive over the base generic tool set.
type BuildtoolsConfig struct {
	// ApprovedTools are command basenames approved unconditionally (like the base
	// generic tools go/gradle/bats/…), e.g. Perl runners prove/yath.
	ApprovedTools []string `json:"approvedTools"`
	// ApprovedScripts are project script basenames safe to run directly or via
	// `bash <script>` / `sh <script>`.
	ApprovedScripts []string `json:"approvedScripts"`
	// VerbScopedApprovals approve a tool only for a specific first subcommand
	// (like the base generic `devbox search` / `cue vet` / `jar xf`).
	VerbScopedApprovals []VerbScopedApproval `json:"verbScopedApprovals"`
}

// VerbScopedApproval approves Tool only when its first subcommand is Verb.
type VerbScopedApproval struct {
	Tool string `json:"tool"`
	Verb string `json:"verb"`
}

// DefaultPath returns the XDG rules.json location:
// $XDG_CONFIG_HOME/claude-extended-tool-approver/rules.json, falling back to
// ~/.config/... when XDG_CONFIG_HOME is unset.
func DefaultPath() string {
	xdgCfg := os.Getenv("XDG_CONFIG_HOME")
	if xdgCfg == "" {
		home, _ := os.UserHomeDir()
		xdgCfg = filepath.Join(home, ".config")
	}
	return filepath.Join(xdgCfg, "claude-extended-tool-approver", "rules.json")
}

// Load reads and parses the config file, returning the full *Config so callers
// (factory.go) can inject the structured sub-configs. On an absent or malformed
// file it returns a zero *Config (every field empty), which makes all rules
// fall back to their base generic behavior — safe to deploy without the file.
func Load(path string) *Config {
	data, err := os.ReadFile(path)
	if err != nil {
		return &Config{}
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &Config{}
	}
	return &cfg
}

// Rule approves/blocks commands from a config file. Thread-safe after construction.
// Absent or malformed file: all inputs abstain.
type Rule struct {
	approved map[string]bool
	blocked  map[string]bool
}

// New constructs a Rule from the default XDG location:
// $XDG_CONFIG_HOME/claude-extended-tool-approver/rules.json
// Falls back to ~/.config/... when XDG_CONFIG_HOME is unset.
func New() *Rule {
	return NewFromFile(DefaultPath())
}

// NewFromFile constructs a Rule from an explicit path. Absent/malformed → no-op rule.
func NewFromFile(path string) *Rule {
	return NewFromConfig(Load(path))
}

// NewFromConfig constructs a Rule from an already-loaded *Config. Used by
// factory.go, which loads the config once and shares it across the config-rules,
// kubectl, and build-tools rules.
func NewFromConfig(cfg *Config) *Rule {
	if cfg == nil {
		cfg = &Config{}
	}
	r := &Rule{
		approved: make(map[string]bool, len(cfg.ApprovedCommands)),
		blocked:  make(map[string]bool, len(cfg.BlockedCommands)),
	}
	for _, cmd := range cfg.ApprovedCommands {
		r.approved[cmd] = true
	}
	for _, cmd := range cfg.BlockedCommands {
		r.blocked[cmd] = true
	}
	return r
}

func (r *Rule) Name() string { return "config-rules" }

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName != "Bash" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	parsed := cmdparse.Parse(cmdStr)
	for _, pc := range parsed {
		base := filepath.Base(pc.Executable)
		if r.blocked[base] {
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "config-rules: " + base + " is in blocked list",
				Module:   r.Name(),
			}
		}
		if r.approved[base] {
			if len(pc.EnvVars) > 0 {
				return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
			}
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "config-rules: " + base + " is in approved list",
				Module:   r.Name(),
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}
