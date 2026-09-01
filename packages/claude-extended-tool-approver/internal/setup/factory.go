package setup

import (
	"os"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/engine"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/assume"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/buildtools"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/claudetools"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/curl"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/dangerouscmds"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/deniedroots"
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
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/primarycommit"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/primarypush"
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
	// Load the consumer config ONCE PER CALL and inject its structured
	// sub-configs into the kubectl, build-tools, ssh, vault, curl, and monorepo
	// rules (DI, ADR 0033), so the base binary stays generic and all consumer
	// specifics live in rules.json.
	//
	// This function (and therefore NewEngineForCWD / NewEngineForCWDWithShellStore,
	// and the live PreToolUse hook handler in cmd/claude-extended-tool-approver's
	// main.go) is UNCHANGED by pg2-rszk3's replay-path engine cache: it still
	// re-reads rules.json from disk on every call, exactly as before. The hook
	// handler is one process per invocation (main() parses one hookio.HookInput
	// and exits), so it already pays this cost exactly once; the replay cache
	// (EngineCache, in enginecache.go, this package) exists ONLY for the
	// evaluate/baseline/compare CLI subcommands, which replay hundreds of
	// thousands of rows against a small set of distinct CWDs in one process.
	cfg := configrules.Load(configrules.DefaultPath())
	return newEngineForCWDWithConfig(cwd, shells, cfg)
}

// newEngineForCWDWithConfig is newEngineForCWD with the consumer config
// factored out as a parameter, so EngineCache (enginecache.go, replay-only)
// can supply an already-loaded *configrules.Config once and skip re-parsing
// rules.json for every distinct CWD. Everything else here is still rebuilt
// per CWD: the project-root walk, the path evaluator, the sandbox config read,
// and the ~17 rule modules RuleChain constructs — only the config PARSE is
// shared.
func newEngineForCWDWithConfig(cwd string, shells killshell.ShellStore, cfg *configrules.Config) *engine.Engine {
	projectRoot := patheval.DetectProjectRoot(cwd)
	pe := patheval.NewWithCWD(projectRoot, cwd)

	sandboxCfg := patheval.LoadSandboxFilesystemConfig(cwd)
	pe.SetSandboxConfig(sandboxCfg)

	eng := engine.New()
	eng.SetPathEvaluator(pe)
	if os.Getenv("CLAUDE_TOOL_APPROVER_TRACE") == "1" {
		eng.SetTrace(true)
	}

	eng.RegisterRules(RuleChain(eng, pe, cfg, shells)...)

	return eng
}

// RuleChain is the SINGLE SOURCE OF TRUTH for the production rule chain: which
// rule modules ceta consults and — because engine.Evaluate is first-match-wins —
// in what order. newEngineForCWD is its only production caller; the engine
// integration suite's own harness (internal/engine, package engine_test) builds
// its chain from this same function so the two CANNOT diverge (pg2-v94d7).
//
// Why that matters: a rule absent from the test harness is invisible to every
// integration case, so its first-match-wins ORDERING against its neighbours is
// untested — and ordering is load-bearing here (envvars' scoped Approve is only
// safe because it runs early; an ungated Approve there was measured to become a
// universal auto-approve prefix). Unit tests cannot catch that class of defect:
// it lives in the COMPOSITION, not in any one rule. The `gitdir` rule shipped
// hard, non-overridable Rejects with unit coverage only precisely because the
// harness had hardcoded its own list and omitted it (pg2-3hk7t).
//
// Therefore: register new rules HERE and nowhere else. Do not reintroduce a
// second, hand-maintained rule list in a test.
//
// VOCABULARY, for anyone adding a rule (ADR 0043). A rule returns
// (hookio.RuleResult, error) and the three outcomes are NOT interchangeable:
//
//	hookio.NotApplicable()          "not my business" — the chain CONTINUES. This is
//	                                what almost every pre-ADR-0043 `Abstain` meant,
//	                                and it is what a rule ordered BEFORE another rule
//	                                that owns the input must return.
//	{Decision: NoOpinion, …}, nil   "handled, and my answer is no gate" — TERMINAL.
//	                                Emits {} and STOPS the chain, so no later rule can
//	                                approve. Only pathsafety's agent-config write
//	                                branch uses it today (ADR 0041).
//	{}, someOtherError              "I could not determine" — counted per rule in
//	                                internal/metrics, then the chain continues.
//
// Older comments in this package and in internal/rules still say "Abstain" and
// "Abstains" for the pre-ADR-0043 combined sentinel; read those as not-applicable
// unless the surrounding code says otherwise. The SERIALIZED value is still
// "abstain" for both NoOpinion and an exhausted chain.
//
// eng is passed back in because the recursive rules (nix, kubectl, envvars,
// docker) take the engine as their Evaluator so a nested command/substitution
// body can be re-evaluated through the whole chain. shells MAY be nil — the
// killshell rule then fails secure (Ask).
func RuleChain(eng *engine.Engine, pe *patheval.PathEvaluator, cfg *configrules.Config, shells killshell.ShellStore) []hookio.RuleModule {
	nixRule := nix.NewWithEvaluator(eng)
	dockerRule := docker.New(eng, pe)

	// primary-commit and primary-push share one on-disk resolver (canonical/primary/
	// current-branch detection) so the commit-side and push-side R-6/R-8 guards agree.
	primaryResolver := primarycommit.NewFileResolver()

	return []hookio.RuleModule{
		configrules.NewFromConfig(cfg),
		// denied-roots runs FIRST among the generic security validators (pg2-fxu7k):
		// a fabricated-absolute-root reference (a machine-configured root known not
		// to exist here — see home/programs/claude-extended-tool-approver's
		// denyRoots option) is a "this cannot be right" signal independent of every
		// other check below, so it should Reject before gitdir/dangerouscmds/secrets
		// spend any effort classifying a path that was never real to begin with.
		// first-match-wins makes ordering the override; a machine with an empty
		// denyRoots list (the default) makes this rule a no-op ErrNotApplicable on
		// every call, so ordering it first costs nothing where it is unconfigured.
		deniedroots.New(pe),
		// Generic security validators run in an early band — after the consumer
		// configrules (so an explicit consumer decision still wins) but before the
		// generic path/command approvers, so a `.git`/dangerous command is never
		// silently approved by pathsafety or safe-commands (hook-support parity).
		// first-match-wins makes ordering the override.
		gitdir.New(),
		dangerouscmds.New(),
		// There is deliberately NO lexical traversal rule here. `pathtraversal`
		// (a `strings.Contains(cmd, "../..")` test over the raw command text ->
		// Ask) was DELETED per the operator ruling recorded in pg2-4yy4r item 6
		// and implemented in pg2-bn7sx: `../..` and variables MUST be accounted
		// for during PATH DETERMINATION, not by a substring test.
		//
		// It was actively HARMFUL, not merely redundant. Running here it preempted
		// `secrets`/`patheval` — which resolve a relative path against the cwd —
		// and substituted its weaker Ask for their `deny`. Measured on the commit
		// before its removal, cwd = this repo, permission_mode=auto:
		//
		//	cat ../.ssh/id_rsa      ask/secrets          (single `../`: rule silent)
		//	cat ../../.ssh/id_rsa   ask/PATH-TRAVERSAL   (masked deny/secrets)
		//
		// And its coverage was an artifact of the literal substring rather than a
		// policy: of five spellings of one `.git/config` read, only the `../..`
		// one was gated (`.git/config`, `./.git/config`, `../.git/config` and the
		// absolute form all reached allow/safe-commands), so an agent one
		// directory shallower got no gate at all. Removal makes the spellings
		// AGREE. Do not reintroduce a lexical traversal rule; if a path needs
		// gating, gate it in the path model (see pg2-dswtg for the `.git` case).
		//
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
		// assume takes the engine as its Evaluator (pg2-bm0ai) so its
		// `--exec "<inner>"` payload can be structurally delegated through
		// the I13 entry point (EvaluateStructure) rather than the bare-Reject
		// blanket that would otherwise cover the wrapped form — mirroring
		// safecmds/kubectl/nix/docker above.
		assume.NewWithEvaluator(eng),
		new(webfetch.Rule),
		claudetools.New(),
		// killshell gates the (non-Bash) KillShell tool by shell ownership. It is
		// harmless for every other tool (ErrNotApplicable — NOT a terminal NoOpinion,
		// which would shadow path-safety below it), and claudetools is likewise
		// not-applicable on KillShell, so ordering here is safe. shells may be nil
		// (offline replay / no store) — the rule then fails secure (Ask).
		killshell.New(shells),
		pathsafety.New(pe),
		mcp.New(),
		// primary-commit MUST precede the generic git rule (first-match-wins): git
		// approves a plain `git commit`, so primary-commit has to get its verdict in
		// first. That ordering is also why its UNRESOLVED-DIRECTORY branch is a
		// fail-closed Ask (Reject in an auto-approving mode) rather than the fail-open
		// not-applicable the rest of the rule uses — ErrNotApplicable there would let
		// `git -C $WT commit`, whose target repository and branch are unknowable from
		// the command text, fall through to git and be APPROVED. This is the same
		// "identity check the rule could not complete" carve-out ADR 0043's error
		// policy names for killshell (pg2-h2npt).
		primarycommit.New(primaryResolver),
		// primary-push mirrors primary-commit for `git push` advancing the canonical
		// primary. It MUST precede the generic git rule (first-match-wins): git treats a
		// non-force push as an approved subcommand, so primary-push has to hard-deny an
		// auto-approving push-to-primary before git can approve it.
		primarypush.New(primaryResolver),
		git.New(pe),
		gh.New(gh.NewExecResolver()),
		monorepo.New(pe, cfg.Monorepo),
		nixRule,
		dockerRule,
		// Command-aware classifiers (curl/ssh/vault) are config-driven MECHANISMS
		// (kubectl/buildtools template) fed the rules.json ssh/vault/curl blocks.
		// They report not-applicable on an empty config, so with no injected data
		// they defer to the rest of the chain.
		// They MUST precede safe-commands: once a consumer supplies data, a
		// configured ssh/vault/curl leaf has to be decided by its dedicated rule,
		// not pre-approved by safe-commands as a bare "safe command". safe-commands
		// is currently not-applicable on these executables anyway, but ordering them
		// first makes that guarantee explicit and robust against future safe-list drift.
		curl.New(cfg.Curl),
		sshrule.New(cfg.Ssh),
		vaultrule.New(cfg.Vault),
		// safecmds takes the engine as its Evaluator (pg2-1zrup) so its
		// `xargs sh|bash -c '<script>'` inner-command handling can delegate
		// through the I13 structural entry point (EvaluateStructure) rather than
		// self-recursing on rule-constructed text — mirroring nixRule/dockerRule
		// above and kubectl below.
		safecmds.NewWithEvaluator(eng, pe),
		kubectl.New(eng, pe, cfg.Kubectl),
		buildtools.New(pe, cfg.Buildtools),
		sqlite3rule.New(pe),
	}
}
