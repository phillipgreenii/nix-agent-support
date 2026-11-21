// Package pggitlander approves, in the LIVE hook, the read-only verbs of the
// pg-git-lander local lander (status, log, config check) and the resume forms
// of an interrupted operation (land|rebase [BRANCH] --abort|--quit|--continue|
// --skip).
//
// # Why a legacy rule as well as the command spec
//
// The cmddesc schema (internal/cmddesc/registry_repo_base.go,
// pgGitLanderSchema) feeds only the effect engine that drives evaluate, replay
// and the golden corpus. The live PreToolUse hook decides through the rule
// chain built in internal/setup, which had no pg-git-lander rule, so EVERY
// pg-git-lander call (even `status`) abstained live. In an unattended
// (dontAsk) session an abstention falls through to the session allowlist, which
// denies it: a pool lander could not run `pg-git-lander land <branch> --abort
// --repo <root>` and a stopped land stayed on its pg-git-lander/<op> branch.
// This rule is the live-hook half of that schema and mirrors its closed
// surface for these verbs only.
//
// # Classification
//
//   - APPROVE read-only: `status [BRANCH] [--json] [--repo P]`, `log [BRANCH]
//     [--json] [--tail N] [--repo P]`, `config check [--json] [--file P]`; each
//     path must be readable by the path evaluator.
//   - APPROVE resume: `land|rebase [BRANCH]` with exactly one of --abort,
//     --quit, --continue, --skip, plus optional --json, --wait DURATION and
//     --repo P (P must be writable by the path evaluator). --continue and
//     --skip run the repository's configured hooks, so they additionally need
//     the working directory inside a git/go/pn workspace, exactly like the
//     schema's trusted-checkout Exec fact.
//   - NOT approved: starting a land or rebase (no resume flag), --no-verify,
//     --source-worktree, --work-worktree, --target-worktree, --onto, --into,
//     --keep-*, --context, any unknown flag or verb, an absolute-path
//     executable, and any leaf with a redirection, process substitution,
//     heredoc or live expansion.
package pggitlander

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/pathspec"
)

// Rule approves the pg-git-lander read-only and resume forms.
type Rule struct {
	pe *patheval.PathEvaluator
}

// New constructs the rule; pe judges --repo / --file.
func New(pe *patheval.PathEvaluator) *Rule { return &Rule{pe: pe} }

func (r *Rule) Name() string { return "pg-git-lander" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("pg-git-lander: read bash command: %w", err)
	}
	for _, pc := range parsed {
		// Bare name only: an absolute-path executable is an untrusted identity.
		if pc.Executable != "pg-git-lander" {
			continue
		}
		if len(pc.Redirections) > 0 || len(pc.ProcessSubstitutions) > 0 || pc.HasHeredoc {
			continue
		}
		live := false
		for _, l := range pc.ArgLiveExpansion {
			live = live || l
		}
		if live {
			continue
		}
		if why, ok := r.classify(pc.Args, input.CWD); ok {
			return hookio.RuleResult{Decision: hookio.Approve, Reason: "pg-git-lander: " + why, Module: r.Name()}, nil
		}
	}
	return hookio.NotApplicable()
}

var resumeFlags = map[string]bool{"--abort": true, "--quit": true, "--continue": true, "--skip": true}

type flagKind int

const (
	boolFlag flagKind = iota
	valueFlag
	readPathFlag
	writePathFlag
)

var (
	readOnlyFlags = map[string]map[string]flagKind{
		"status":       {"--json": boolFlag, "--repo": readPathFlag},
		"log":          {"--json": boolFlag, "--tail": valueFlag, "--repo": readPathFlag},
		"config check": {"--json": boolFlag, "--file": readPathFlag},
	}
	resumeVerbFlags = map[string]flagKind{"--json": boolFlag, "--wait": valueFlag, "--repo": writePathFlag}
)

func (r *Rule) classify(args []string, cwd string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	verb, rest := args[0], args[1:]
	if verb == "config" && len(rest) > 0 && rest[0] == "check" {
		verb, rest = "config check", rest[1:]
	}
	if flags, ok := readOnlyFlags[verb]; ok {
		resume, positionals, ok := r.scan(rest, flags, verb != "config check")
		if !ok || len(resume) > 0 || positionals > 1 {
			return "", false
		}
		return "read-only " + verb, true
	}
	if verb != "land" && verb != "rebase" {
		return "", false
	}
	resume, positionals, ok := r.scan(rest, resumeVerbFlags, true)
	if !ok || len(resume) != 1 || positionals > 1 {
		return "", false
	}
	if flag := resume[0]; flag == "--continue" || flag == "--skip" {
		if r.pe == nil || cwd == "" || !pathspec.InsideMarkerWorkspace(pathspec.DefaultKinds(), []string{"git", "go", "pn"}, patheval.ResolveRealPath(cwd)) {
			return "", false
		}
	}
	return verb + " " + resume[0] + " (resume of an interrupted operation)", true
}

// scan walks args against flags. It returns the resume flags seen and the
// number of plain positionals; ok is false on anything outside the closed
// surface. Resume flags are only recognised when the flag table is the resume
// one or the read-only table never lists them (so they make a read-only verb
// fail via the returned slice).
func (r *Rule) scan(args []string, flags map[string]flagKind, allowPositional bool) (resume []string, positionals int, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !plain(a) {
			return nil, 0, false
		}
		if resumeFlags[a] {
			resume = append(resume, a)
			continue
		}
		if !strings.HasPrefix(a, "-") {
			if !allowPositional {
				return nil, 0, false
			}
			positionals++
			continue
		}
		kind, known := flags[a]
		if !known {
			return nil, 0, false
		}
		if kind == boolFlag {
			continue
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") || !plain(args[i+1]) {
			return nil, 0, false
		}
		i++
		switch kind {
		case readPathFlag:
			if r.pe == nil || !r.pe.Evaluate(args[i]).CanRead() {
				return nil, 0, false
			}
		case writePathFlag:
			if r.pe == nil || !r.pe.Evaluate(args[i]).CanWrite() {
				return nil, 0, false
			}
		}
	}
	return resume, positionals, true
}

// plain reports whether tok carries no shell metacharacter, quote, backslash,
// expansion or control character.
func plain(tok string) bool {
	if tok == "" || strings.ContainsAny(tok, ";&|<>()$`\\\"'{}*?[]!~#\x7f") {
		return false
	}
	for _, r := range tok {
		if r < 0x20 {
			return false
		}
	}
	return true
}
