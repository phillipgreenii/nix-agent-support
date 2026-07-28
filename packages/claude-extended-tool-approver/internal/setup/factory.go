package setup

import (
	"os"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/engine"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/assume"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/buildtools"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/claudetools"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/curl"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/dangerouscmds"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/docker"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/envvars"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/gh"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/git"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/gitdir"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/killshell"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/kubectl"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/mcp"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/monorepo"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/nix"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/pathsafety"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/pathtraversal"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/primarycommit"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/safecmds"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/secrets"
	sqlite3rule "github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/sqlite3"
	sshrule "github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/ssh"
	vaultrule "github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/vault"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/webfetch"
)

// NewEngineForCWD constructs a fully-configured engine for the given CWD.
// Used by the evaluate subcommand (offline replay) and by the hook handler when
// no persistent shell store is available. The killshell rule then has no
// ownership store and fails secure (Ask).
func NewEngineForCWD(cwd string) *engine.Engine {
	return newEngineForCWD(cwd, nil)
}

// NewEngineForCWDWithShellStore is like NewEngineForCWD but injects a persistent
// shell-ownership store into the killshell rule, so KillShell of an
// agent-tracked background shell is auto-approved. The live PreToolUse handler
// uses this once it has opened the ask-log store.
func NewEngineForCWDWithShellStore(cwd string, shells killshell.ShellStore) *engine.Engine {
	return newEngineForCWD(cwd, shells)
}

func newEngineForCWD(cwd string, shells killshell.ShellStore) *engine.Engine {
	projectRoot := patheval.DetectProjectRoot(cwd)
	pe := patheval.NewWithCWD(projectRoot, cwd)

	sandboxCfg := patheval.LoadSandboxFilesystemConfig(cwd)
	pe.SetSandboxConfig(sandboxCfg)

	eng := engine.New()
	eng.SetPathEvaluator(pe)
	if os.Getenv("CLAUDE_TOOL_APPROVER_TRACE") == "1" {
		eng.SetTrace(true)
	}

	// Load the consumer config ONCE and inject its structured sub-configs into
	// the kubectl, build-tools, ssh, vault, curl, and monorepo rules (DI, ADR
	// 0033), so the base binary stays generic and all consumer specifics live in
	// rules.json.
	cfg := configrules.Load(configrules.DefaultPath())

	nixRule := nix.NewWithEvaluator(eng)
	dockerRule := docker.New(eng, pe)

	eng.RegisterRules(
		configrules.NewFromConfig(cfg),
		// Generic security validators run in an early band — after the consumer
		// configrules (so an explicit consumer decision still wins) but before the
		// generic path/command approvers, so a `.git`/dangerous/traversal command
		// is never silently approved by pathsafety or safe-commands (hook-support
		// parity). first-match-wins makes ordering the override.
		gitdir.New(),
		dangerouscmds.New(),
		pathtraversal.New(),
		// secrets runs early (after consumer configrules, before the generic
		// path/command approvers) so a credential/secret-path reference is
		// prompted (Ask) instead of being silently approved by pathsafety or
		// safe-commands (pg2-to8pe). first-match-wins makes ordering the override.
		secrets.New(pe),
		// envvars is DECISIVE for flagged vars and runs before safe-commands so a
		// dangerous `export VAR=…` cannot be re-approved as a bare `export`
		// (pg2-gkd5e). It takes the engine as its Evaluator so a dynamic value's
		// substitution body can be recursed through the full chain.
		envvars.NewWithEvaluator(eng),
		assume.New(),
		new(webfetch.Rule),
		claudetools.New(),
		// killshell gates the (non-Bash) KillShell tool by shell ownership. It is
		// harmless for every other tool (Abstain), and claudetools already Abstains
		// on KillShell, so ordering here is safe. shells may be nil (offline replay
		// / no store) — the rule then fails secure (Ask).
		killshell.New(shells),
		pathsafety.New(pe),
		mcp.New(),
		primarycommit.New(primarycommit.NewFileResolver()),
		git.New(pe),
		gh.New(gh.NewExecResolver()),
		monorepo.New(pe, cfg.Monorepo),
		nixRule,
		dockerRule,
		// Command-aware classifiers (curl/ssh/vault) are config-driven MECHANISMS
		// (kubectl/buildtools template) fed the rules.json ssh/vault/curl blocks.
		// They Abstain on an empty config, so with no injected data they defer.
		// They MUST precede safe-commands: once a consumer supplies data, a
		// configured ssh/vault/curl leaf has to be decided by its dedicated rule,
		// not pre-approved by safe-commands as a bare "safe command". safe-commands
		// currently Abstains on these executables anyway, but ordering them first
		// makes that guarantee explicit and robust against future safe-list drift.
		curl.New(cfg.Curl),
		sshrule.New(cfg.Ssh),
		vaultrule.New(cfg.Vault),
		safecmds.New(pe),
		kubectl.New(eng, pe, cfg.Kubectl),
		buildtools.New(cfg.Buildtools),
		sqlite3rule.New(pe),
	)

	return eng
}
