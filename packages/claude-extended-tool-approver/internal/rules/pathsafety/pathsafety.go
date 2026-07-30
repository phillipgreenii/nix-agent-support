package pathsafety

import (
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// agentConfigDir is the directory name whose immediate children this package treats
// as agent configuration / instruction (see agentConfigBasenames, isAgentConfigPath).
const agentConfigDir = ".claude"

// agentConfigBasenames is the CLOSED set of agent-CONFIG basenames that, sitting
// directly inside a `.claude` directory, this rule must not approve a write to
// (ADR 0041, "CETA abstains on agent-config writes"). Each entry grants the agent
// capability for every SUBSEQUENT call — permissions in the settings files, tool
// surface in the mcp files — so writing one is privilege-escalation shaped.
//
// The set is deliberately CLOSED rather than "anything under `.claude/`". ADR 0041's
// Context names the collateral of a subtree-wide block explicitly — "the memory
// directories, skills, plugins, and transcripts" — as the reason the existing
// `sandbox.filesystem.denyWrite` mechanism could not express this decision. Matching
// only the immediate children of `.claude` keeps every one of those out of scope,
// since each lives a directory deeper.
var agentConfigBasenames = map[string]bool{
	"settings.json":       true,
	"settings.local.json": true,
	"mcp.json":            true,
	".mcp.json":           true,
}

// isAgentConfigPath reports whether an already-normalized absolute path names an
// agent-config or agent-instruction file for the purposes of ADR 0041.
//
// The predicate is: the path's PARENT directory is named exactly `.claude`, AND the
// basename is either a member of agentConfigBasenames (agent CONFIG) or a `*.md`
// file (agent INSTRUCTION — the `.claude/rules.md` shape from logged row 273301,
// and `.claude/CLAUDE.md`). A markdown file sitting directly in `.claude` has no
// role other than steering future sessions.
//
// Requiring the parent to BE `.claude` — rather than merely to contain it — is what
// bounds the blast radius: `.claude/skills/**`, `.claude/plugins/**`,
// `.claude/projects/**` (transcripts, memory) and `.claude/plans/**` are all a level
// deeper and therefore unaffected, which is what ADR 0041 requires. It also holds
// for BOTH scopes the ADR covers, project-local `<project>/.claude/` and user-global
// `~/.claude/`, without either being special-cased.
func isAgentConfigPath(path string) bool {
	if path == "" {
		return false
	}
	if filepath.Base(filepath.Dir(path)) != agentConfigDir {
		return false
	}
	base := filepath.Base(path)
	if agentConfigBasenames[base] {
		return true
	}
	return strings.EqualFold(filepath.Ext(base), ".md")
}

var fileTools = map[string]bool{
	"Read": true, "Write": true, "Edit": true, "MultiEdit": true, "Delete": true,
}

var searchTools = map[string]bool{
	"Glob": true, "Grep": true,
}

type Rule struct {
	eval *patheval.PathEvaluator
}

func New(eval *patheval.PathEvaluator) *Rule {
	return &Rule{eval: eval}
}

func (r *Rule) Name() string {
	return "path-safety"
}

// isAgentConfigWrite applies isAgentConfigPath to the tool's raw path argument,
// normalized two ways: the path as NAMED (env/~/cwd-relative expansion only) and the
// symlink-RESOLVED path. Either shape matching is a hit — the named form so a config
// file that is itself a symlink still matches, the resolved form so a symlink
// elsewhere pointing INTO `.claude` cannot be used to slip the write past this check.
func (r *Rule) isAgentConfigWrite(path string) bool {
	if isAgentConfigPath(r.eval.CleanPath(path)) {
		return true
	}
	return isAgentConfigPath(r.eval.ResolvePath(path))
}

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if fileTools[input.ToolName] {
		path, err := input.FilePath()
		if err != nil {
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		switch input.ToolName {
		case "Read":
			if r.eval.IsDenyRead(path) {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: "path is deny-read: " + path, Module: r.Name()}
			}
			access := r.eval.Evaluate(path)
			if access.CanRead() {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "path allows read: " + path, Module: r.Name()}
			}
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "path is " + access.String() + " " + path + " (deferred to claude-code)",
				Module:   r.Name(),
			}
		case "Write", "Edit", "MultiEdit", "Delete":
			if r.eval.IsDenyWrite(path) {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: "path is deny-write: " + path, Module: r.Name()}
			}
			// ADR 0041: CETA MUST NOT approve a write to an agent-config or
			// agent-instruction file under `.claude/`; it abstains so the verdict
			// stays with Claude Code (the interactive prompt, or the
			// auto_mode_classifier in auto mode). This check MUST sit ahead of the
			// CanWrite() approve below and INSIDE this branch: Abstain means
			// "continue to the next rule", so a separate rule returning Abstain
			// ahead of path-safety would be a silent no-op — path-safety itself has
			// to stop approving. Reads are unaffected (see the Read case above).
			if r.isAgentConfigWrite(path) {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "agent-config write under " + agentConfigDir + "/: " + path + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			access := r.eval.Evaluate(path)
			if access.CanWrite() {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "path allows write: " + path, Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Abstain, Reason: "path access unknown: " + path + " (deferred to claude-code)", Module: r.Name()}
		default:
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
	} else if searchTools[input.ToolName] {
		return r.evaluateSearch(input)
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func (r *Rule) evaluateSearch(input *hookio.HookInput) hookio.RuleResult {
	path, err := input.SearchPath()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	if path == "" {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "search tool with no explicit path (defaults to CWD)",
			Module:   r.Name(),
		}
	}
	if r.eval.IsDenyRead(path) {
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "search path is deny-read: " + path,
			Module:   r.Name(),
		}
	}
	access := r.eval.Evaluate(path)
	if access.CanRead() {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "search path allows read: " + path,
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{
		Decision: hookio.Abstain,
		Reason:   "search path is " + access.String() + " " + path + " (deferred to claude-code)",
		Module:   r.Name(),
	}
}
