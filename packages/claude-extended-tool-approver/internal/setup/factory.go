package setup

import (
	"os"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/engine"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/assume"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/buildtools"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/claudetools"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/curl"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/docker"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/envvars"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/gh"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/git"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/kubectl"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/mcp"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/monorepo"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/nix"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/pathsafety"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/safecmds"
	sqlite3rule "github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/sqlite3"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/webfetch"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/znself"
)

// NewEngineForCWD constructs a fully-configured engine for the given CWD.
// Used by both the hook handler and the evaluate subcommand.
func NewEngineForCWD(cwd string) *engine.Engine {
	projectRoot := patheval.DetectProjectRoot(cwd)
	pe := patheval.NewWithCWD(projectRoot, cwd)

	sandboxCfg := patheval.LoadSandboxFilesystemConfig(cwd)
	pe.SetSandboxConfig(sandboxCfg)

	eng := engine.New()
	eng.SetPathEvaluator(pe)
	if os.Getenv("CLAUDE_TOOL_APPROVER_TRACE") == "1" {
		eng.SetTrace(true)
	}

	nixRule := nix.NewWithEvaluator(eng)
	dockerRule := docker.New(eng, pe)

	eng.RegisterRules(
		configrules.New(),
		envvars.New(),
		assume.New(),
		new(webfetch.Rule),
		claudetools.New(),
		pathsafety.New(pe),
		mcp.New(),
		git.New(),
		gh.New(gh.NewExecResolver()),
		monorepo.New(pe),
		nixRule,
		dockerRule,
		znself.New(),
		safecmds.New(pe),
		curl.New(),
		kubectl.New(),
		buildtools.New(),
		sqlite3rule.New(pe),
	)

	return eng
}
