package pathsafety

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// agentConfigDir is the directory name whose immediate children this package treats
// as agent configuration / instruction (see agentConfigBasenames, isAgentConfigPath).
//
// Written in its canonical lowercase spelling. isAgentConfigPath compares against it
// with strings.EqualFold, so the spelling here is documentation rather than a
// matching constraint.
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
//
// Membership is tested by a strings.EqualFold SCAN, not by a hash lookup, because the
// match must be case-insensitive (see isAgentConfigPath) and no single normalization
// of the candidate reproduces EqualFold's folding. The set has four entries and is
// consulted once per file-tool call, so the scan is free. Keys are written in their
// canonical lowercase spelling for readability; EqualFold makes that a convention,
// not a correctness requirement.
var agentConfigBasenames = map[string]bool{
	"settings.json":       true,
	"settings.local.json": true,
	"mcp.json":            true,
	".mcp.json":           true,
}

// agentHooksDirName is the CLOSED, single directory name that, sitting directly
// inside a `.claude` directory, this rule must not approve a write to AT ANY DEPTH
// beneath it (the hooks carve-out ADR — see docs/adr/index.md for its current
// number — "the agent-config carve-out covers .claude/hooks/, not agents/ or
// commands/", which extends ADR 0041). A hook is arbitrary code executed on every
// tool call, the same privilege-escalation shape ADR 0041 already covers for
// settings.json/settings.local.json, so the reasoning transfers even though a hook
// script itself is not "config" in the ADR-0041 sense.
//
// UNLIKE agentConfigBasenames (a depth-1, exact-basename match), this carve-out
// matches at ANY DEPTH beneath `hooks/`, because hook scripts nest
// (`.claude/hooks/lib/foo.sh` is as executable as `.claude/hooks/foo.sh`). The
// `hooks` directory itself MUST still sit DIRECTLY inside `.claude` — a look-alike
// such as `packages/ccpool/ccpool-plugin/hooks/` is untouched, since its parent is
// not `.claude`.
//
// `.claude/agents/` and `.claude/commands/` sit in the same structural position
// (one level deeper than the depth-1 config predicate) but are deliberately NOT
// covered: they shape agent behavior without executing on every call, and the
// operator ruling this carve-out records priced that distinction explicitly.
const agentHooksDirName = "hooks"

// isAgentHooksPath reports whether an already-normalized absolute path names a
// file at or beneath a `.claude/hooks/` directory, for the purposes of the hooks
// carve-out ADR. Folding is unconditionally case-insensitive for both path
// components tested here, for the identical APFS-bypass reason documented on
// isAgentConfigPath (pg2-2ng80): this machine's home volume folds case, so an
// exact-match predicate would leave `.CLAUDE/hooks/x.sh` approved.
func isAgentHooksPath(path string) bool {
	if path == "" {
		return false
	}
	parts := splitPath(path)
	for i := 0; i+1 < len(parts); i++ {
		if strings.EqualFold(parts[i], agentConfigDir) && strings.EqualFold(parts[i+1], agentHooksDirName) {
			return true
		}
	}
	return false
}

// splitPath cleans path and splits it into its directory components, shared by
// every predicate in this package that needs to test path components at more than
// one fixed depth (isAgentHooksPath here; pluginHooksDir in plugin_hooks.go).
// filepath.Clean("") returns ".", so the empty-path case is handled by callers
// before reaching here (both current callers already guard it).
func splitPath(path string) []string {
	return strings.Split(filepath.Clean(path), string(filepath.Separator))
}

// isAgentConfigPath reports whether an already-normalized absolute path names an
// agent-config or agent-instruction file for the purposes of ADR 0041.
//
// The predicate is: the path's PARENT directory is named `.claude` (case aside — see
// the case-handling note below), AND the basename is either a member of
// agentConfigBasenames (agent CONFIG) or a `*.md` file (agent INSTRUCTION — the
// `.claude/rules.md` shape from logged row 273301, and `.claude/CLAUDE.md`). A
// markdown file sitting directly in `.claude` has no role other than steering
// future sessions.
//
// Requiring the parent to BE `.claude` — rather than merely to contain it — is what
// bounds the blast radius: `.claude/skills/**`, `.claude/plugins/**`,
// `.claude/projects/**` (transcripts, memory) and `.claude/plans/**` are all a level
// deeper and therefore unaffected, which is what ADR 0041 requires. It also holds
// for BOTH scopes the ADR covers, project-local `<project>/.claude/` and user-global
// `~/.claude/`, without either being special-cased.
//
// CASE HANDLING IS UNCONDITIONALLY CASE-INSENSITIVE, and all THREE parts of the
// predicate — parent directory name, basename set, `.md` extension — use the SAME
// primitive, `strings.EqualFold`. Folding only the extension (as this predicate
// originally did) left the control trivially bypassable: this machine's home volume
// is APFS and case-INSENSITIVE, so `<project>/.CLAUDE/settings.local.json` and
// `<project>/.claude/settings.local.json` name the SAME real file, yet only the
// second matched — the first fell through to the CanWrite() approve below and was
// auto-approved, which is exactly the privilege escalation ADR 0041 exists to
// prevent (pg2-2ng80).
//
// EqualFold, NOT strings.ToLower. EqualFold implements Unicode simple case FOLDING;
// ToLower implements simple case MAPPING, and the two disagree on codepoints APFS
// treats as equal. Verified on this machine: writing `.claude/settings.local.json`
// and reading `.claude/ſettings.local.json` (U+017F LATIN SMALL LETTER LONG S)
// returns the same bytes, and EqualFold matches that spelling while ToLower leaves
// the `ſ` alone and misses it — the same bypass as `.CLAUDE`, one codepoint over.
// Downgrading to ToLower reopens that hole; the
// FoldsNotMerelyLowercases test fails if anyone does.
//
// DO NOT "optimize" this into a runtime probe of the volume's case-sensitivity.
// Some paths in this workspace ARE on case-sensitive volumes (e.g.
// /Volumes/acme), so a per-volume answer would be correct-but-fragile,
// and the two error directions are wildly asymmetric: over-matching costs ONE
// unnecessary Abstain (ceta declines to approve; Claude Code still decides), while
// under-matching costs the ENTIRE control. Fail-safe beats precision here. The same
// asymmetry is why EqualFold over-matching (it also folds pairs APFS keeps distinct,
// e.g. U+0130) is acceptable and ToLower under-matching is not.
//
// Folding case MUST NOT be confused with widening the match: the parent must still
// BE `.claude` (case aside), so the depth-1 blast-radius bound above is untouched
// and `.CLAUDE/skills/**` stays approved just as `.claude/skills/**` does.
func isAgentConfigPath(path string) bool {
	if path == "" {
		return false
	}
	if !strings.EqualFold(filepath.Base(filepath.Dir(path)), agentConfigDir) {
		return false
	}
	base := filepath.Base(path)
	for configBase := range agentConfigBasenames {
		if strings.EqualFold(base, configBase) {
			return true
		}
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

// refuse is this rule's ADR 0044 refusal-and-continue return. Every site that uses it
// has the same shape: the tool IS one this rule governs, the path HAS been classified by
// the PathEvaluator, the classification does not permit the access — and yet the chain
// must not stop, because mcp and the Bash-only rules still run after it.
//
// ADR 0043 had no outcome for that, so these sites became ErrNotApplicable and their
// reasons were demoted to comments opening "Former Reason, kept because it is the only
// record of WHY". This restores each one as a real Reason, and it matters more here than
// the comment census suggests: a path this rule DECLINED to clear reported as a
// not-applicable is indistinguishable from a tool call no rule ever examined — an
// EXHAUSTION — and exhaustion is the half a consumer may act on to clear an input.
// Under-conversion is the APPROVAL-WIDENING direction.
//
// IT IS NOT THE ADR 0041 SITE, and the two must not be conflated. The agent-config write
// branch below is a TERMINAL NoOpinion because ADR 0041 requires the chain to STOP there;
// these three sites are floors because the chain must CONTINUE. A refusal can only make a
// leaf MORE restrictive — the engine folds it and keeps going, so a later rule's Ask or
// Reject still wins and nothing is shadowed.
func (r *Rule) refuse(reason string) (hookio.RuleResult, error) {
	return hookio.Refused(r.Name(), reason)
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

// isAgentHooksWrite applies isAgentHooksPath the same double-normalized way
// isAgentConfigWrite applies isAgentConfigPath: the path as NAMED and the
// symlink-RESOLVED path, so a symlink elsewhere pointing INTO `.claude/hooks`
// cannot be used to slip the write past this check.
func (r *Rule) isAgentHooksWrite(path string) bool {
	if isAgentHooksPath(r.eval.CleanPath(path)) {
		return true
	}
	return isAgentHooksPath(r.eval.ResolvePath(path))
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if fileTools[input.ToolName] {
		path, err := input.FilePath()
		if err != nil {
			return hookio.RuleResult{}, fmt.Errorf("path-safety: read tool path: %w", err)
		}
		switch input.ToolName {
		case "Read":
			if r.eval.IsDenyRead(path) {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: "path is deny-read: " + path, Module: r.Name()}, nil
			}
			access := r.eval.Evaluate(path)
			if access.CanRead() {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "path allows read: " + path, Module: r.Name()}, nil
			}
			return r.refuse("path-safety: path is " + access.String() + " " + path + " (deferred to claude-code)")
		case "Write", "Edit", "MultiEdit", "Delete":
			if r.eval.IsDenyWrite(path) {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: "path is deny-write: " + path, Module: r.Name()}, nil
			}
			// ADR 0041: CETA MUST NOT approve a write to an agent-config or
			// agent-instruction file under `.claude/`; it declines to decide so the
			// verdict stays with Claude Code (the interactive prompt, or the
			// auto_mode_classifier in auto mode). This check MUST sit ahead of the
			// CanWrite() approve below and INSIDE this branch, because ADR 0041
			// requires PATH-SAFETY ITSELF to stop approving: a separate rule ahead of
			// path-safety returning the continue sentinel would be a silent no-op.
			//
			// THIS IS THE ONE SITE IN THE WHOLE RULESET THAT ADR 0043 CONVERTS TO A
			// TERMINAL NoOpinion RATHER THAN ErrNotApplicable (its Decision, point 4).
			// The two are NOT interchangeable here and getting it backwards reverses
			// ADR 0041: ErrNotApplicable means "continue", and a later rule that then
			// approved the write would defeat the control outright. NoOpinion emits
			// {} and stops the chain, which is exactly "ceta declines to approve, the
			// verdict is Claude Code's".
			//
			// It is behaviour-preserving in this position: path-safety is followed
			// only by mcp (mcp__ tools) and Bash-only rules, so no later rule acts on
			// a Write/Edit/MultiEdit/Delete today. Stopping here therefore reaches
			// the same {} the continue-to-exhaustion path reached.
			if r.isAgentConfigWrite(path) {
				return hookio.RuleResult{
					Decision: hookio.NoOpinion,
					Reason:   "agent-config write under " + agentConfigDir + "/: " + path + " (deferred to claude-code)",
					Module:   r.Name(),
				}, nil
			}
			// Hooks carve-out: a hook under `.claude/hooks/**` is arbitrary code run on
			// every tool call — the same escalation shape as the agent-config check
			// above, extended to a directory ADR 0041 did not weigh (see
			// agentHooksDirName's doc comment for the ADR and the operator ruling it
			// records). It MUST sit here, ahead of the CanWrite() approve and inside this
			// same terminal branch, for the identical reason ADR 0041's Implementation
			// constraint gives for the agent-config check: Abstain means "continue", so a
			// rule ahead of path-safety returning it would be a silent no-op.
			if r.isAgentHooksWrite(path) {
				return hookio.RuleResult{
					Decision: hookio.NoOpinion,
					Reason:   "agent-hooks write under " + agentConfigDir + "/" + agentHooksDirName + "/: " + path + " (deferred to claude-code)",
					Module:   r.Name(),
				}, nil
			}
			// Plugin hooks execution-path carve-out (ADR 0049): the carve-out above
			// covers `.claude/hooks/`, which is empty on this machine — hooks arrive
			// entirely via plugins, under `.claude/plugins/**`, which ADR 0041 leaves
			// approved as a whole (a plugin checkout is a large tree of
			// legitimately-written files; the hooks/-style enumerated-directory carve-out
			// does not transfer to it). ADR 0049 narrows this to exactly the paths within
			// a plugin that can cause execution: the plugin's own `hooks/hooks.json`
			// manifest, and the scripts it names via `${CLAUDE_PLUGIN_ROOT}`. See
			// plugin_hooks.go for the resolution logic.
			if r.isPluginHooksExecutionWrite(path) {
				return hookio.RuleResult{
					Decision: hookio.NoOpinion,
					Reason:   "plugin hooks execution-path write: " + path + " (deferred to claude-code)",
					Module:   r.Name(),
				}, nil
			}
			access := r.eval.Evaluate(path)
			if access.CanWrite() {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "path allows write: " + path, Module: r.Name()}, nil
			}
			return r.refuse("path-safety: path access unknown: " + path + " (deferred to claude-code)")
		default:
			// Unreachable in practice (fileTools gates the switch), and DELIBERATELY not a
			// refusal: a tool name this switch does not enumerate has had no path
			// classified, so there is no judgement to floor.
			return hookio.NotApplicable()
		}
	} else if searchTools[input.ToolName] {
		return r.evaluateSearch(input)
	}
	return hookio.NotApplicable()
}

func (r *Rule) evaluateSearch(input *hookio.HookInput) (hookio.RuleResult, error) {
	path, err := input.SearchPath()
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("path-safety: read tool path: %w", err)
	}
	if path == "" {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "search tool with no explicit path (defaults to CWD)",
			Module:   r.Name(),
		}, nil
	}
	if r.eval.IsDenyRead(path) {
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "search path is deny-read: " + path,
			Module:   r.Name(),
		}, nil
	}
	access := r.eval.Evaluate(path)
	if access.CanRead() {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "search path allows read: " + path,
			Module:   r.Name(),
		}, nil
	}
	return r.refuse("path-safety: search path is " + access.String() + " " + path + " (deferred to claude-code)")
}
