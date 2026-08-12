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
// /Volumes/ziprecruiter), so a per-volume answer would be correct-but-fragile,
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
			// Not applicable (ADR 0043): the chain must continue. Former Reason,
			// kept because it is the only record of WHY: "path is " + access.String() + " " + path + " (deferred to claude-code)"
			return hookio.NotApplicable()
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
			access := r.eval.Evaluate(path)
			if access.CanWrite() {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "path allows write: " + path, Module: r.Name()}, nil
			}
			// Not applicable (ADR 0043): the chain must continue. Former Reason,
			// kept because it is the only record of WHY: "path access unknown: " + path + " (deferred to claude-code)"
			return hookio.NotApplicable()
		default:
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
	// Not applicable (ADR 0043): the chain must continue. Former Reason,
	// kept because it is the only record of WHY: "search path is " + access.String() + " " + path + " (deferred to claude-code)"
	return hookio.NotApplicable()
}
