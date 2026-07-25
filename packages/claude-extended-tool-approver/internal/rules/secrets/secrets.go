// Package secrets prompts (Ask) before any tool call touches a well-known
// credential/secret file, so such reads/writes are never silently
// auto-approved by a later rule — e.g. the safe-commands rule approving
// `cat <readable-path>` where the path is ~/.claude/.credentials (pg2-to8pe).
package secrets

import (
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/secretpath"
)

// maxShellUnwrap bounds how deep the rule follows nested `sh -c '<inner>'`
// wrappers when scanning a Bash command for secret paths.
const maxShellUnwrap = 3

// writeTools are the file tools whose access is a write (deny-listing is
// checked against denyWrite rather than denyRead).
var writeTools = map[string]bool{
	"Write": true, "Edit": true, "MultiEdit": true, "Delete": true,
}

// Rule flags tool calls that reference a well-known credential/secret path.
//
// It is registered EARLY in the chain — after the consumer `configrules` (so an
// explicit consumer decision still wins) but before the generic path/command
// approvers `path-safety` and `safe-commands`. The engine's per-leaf evaluation
// is first-match-wins, so this ordering is what lets the rule override a
// downstream Approve.
//
// Decision policy: for a secret path that the user has ALSO deny-listed
// (sandbox.filesystem.denyRead/denyWrite) the rule returns Reject — preserving
// the hard block that path-safety would otherwise give (path-safety runs after
// this rule, so this rule must honor the deny-list itself rather than let its
// Ask silently downgrade the block). For a secret path that is not deny-listed
// it returns Ask: the goal is to replace a silent auto-approval with a human
// prompt, not to hard-block a legitimate read.
type Rule struct {
	pe *patheval.PathEvaluator
}

func New(pe *patheval.PathEvaluator) *Rule { return &Rule{pe: pe} }

func (r *Rule) Name() string { return "secrets" }

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	switch input.ToolName {
	case "Read", "Write", "Edit", "MultiEdit", "Delete":
		if path, err := input.FilePath(); err == nil && secretpath.IsSecret(path) {
			return r.decide(path, writeTools[input.ToolName])
		}
	case "Glob", "Grep":
		if path, err := input.SearchPath(); err == nil && secretpath.IsSecret(path) {
			return r.decide(path, false)
		}
	case "Bash":
		if cmd, err := input.BashCommand(); err == nil {
			if path, ok := firstSecretRef(cmd, maxShellUnwrap); ok {
				// Bash read/write intent is ambiguous per-argument; treat as a
				// read for deny-list purposes (the bead is about reads).
				return r.decide(path, false)
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

// decide returns Reject when path is a secret the user has deny-listed for the
// relevant access, otherwise Ask.
func (r *Rule) decide(path string, isWrite bool) hookio.RuleResult {
	if r.pe != nil {
		denied := r.pe.IsDenyRead(path)
		if isWrite {
			denied = r.pe.IsDenyWrite(path)
		}
		if denied {
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "credential/secret path is deny-listed: " + path,
				Module:   r.Name(),
			}
		}
	}
	return hookio.RuleResult{
		Decision: hookio.Ask,
		Reason:   "references credential/secret path " + path + " — prompting instead of auto-approving",
		Module:   r.Name(),
	}
}

// firstSecretRef returns the first secret path referenced by the command —
// whether as an argument or an I/O redirection target (e.g. `cat < secrets/x`).
// It also descends one `sh`/`bash -c '<inner>'` level at a time (up to depth)
// so the check cannot be trivially bypassed by wrapping the read in a shell
// string.
func firstSecretRef(cmd string, depth int) (string, bool) {
	for _, pc := range cmdparse.Parse(cmd) {
		if depth > 0 {
			if inner, ok := shellDashC(pc); ok {
				if path, found := firstSecretRef(inner, depth-1); found {
					return path, true
				}
				continue
			}
		}
		for _, arg := range secretCandidateArgs(pc) {
			if isFlag(arg) {
				continue
			}
			if secretpath.IsSecret(arg) {
				return arg, true
			}
		}
		for _, redir := range pc.Redirections {
			if secretpath.IsSecret(redir.Path) {
				return redir.Path, true
			}
		}
	}
	return "", false
}

// secretCandidateArgs returns the subset of a command's arguments that could be
// FILE-path references worth testing against secretpath.IsSecret — filtering out
// arguments that merely LOOK path-like but are not files, which is what produced
// the grep/rg/jq false positives (pg2-ia640.2):
//
//   - grep/rg: the positional search PATTERN and value-flag values (a bare .env
//     pattern, `-e .env`, `-f .env`, `rg -g '*.env'`) are not searched files.
//   - jq: the value-flag arguments (`--arg x .env`) and the bare FILTER program
//     (the first positional, e.g. `.credentials`) are not files. The filter is
//     only exempt when it IS a positional — with -f/--from-file the filter comes
//     from a file and the first positional is instead an INPUT file, so it is
//     kept (avoids missing a secret input file).
func secretCandidateArgs(pc cmdparse.ParsedCommand) []string {
	switch filepath.Base(pc.Executable) {
	case "grep", "rg":
		return cmdparse.SkipGrepPattern(filepath.Base(pc.Executable), pc.Args)
	case "jq":
		args := cmdparse.SkipJqValueFlags(pc.Args)
		if !jqFilterFromFile(pc.Args) {
			args = dropFirstPositional(args)
		}
		return args
	default:
		return pc.Args
	}
}

// jqFilterFromFile reports whether the jq filter is supplied via -f/--from-file
// (in which case there is no positional FILTER program to exempt).
func jqFilterFromFile(args []string) bool {
	for _, a := range args {
		if a == "-f" || a == "--from-file" {
			return true
		}
	}
	return false
}

// dropFirstPositional returns args with the first non-flag argument removed,
// preserving order of the rest.
func dropFirstPositional(args []string) []string {
	result := make([]string, 0, len(args))
	dropped := false
	for _, a := range args {
		if !dropped && !isFlag(a) {
			dropped = true
			continue
		}
		result = append(result, a)
	}
	return result
}

// shellDashC returns the inner command string of a shell `-c` invocation —
// `sh -c '<inner>'` / `bash -c '<inner>'` (or zsh/dash) — INCLUDING combined
// single-dash short-flag groups that END in `c`, e.g. `bash -lc '<inner>'` or
// `sh -ilc '<inner>'`, where the `-c` still takes the NEXT token as its command
// string (pg2-ia640.4).
//
// It matches ONLY single-dash short-flag GROUPS whose final flag is `c`
// (`-c`, `-lc`, `-ilc`). It deliberately does NOT match:
//   - `--` long options — even ones that contain or end in `c`
//     (`--rcfile FILE`, `--norc`): treating those as a `-c` wrapper would wrongly
//     scan the following token (e.g. the rcfile path) as an inner command.
//   - non-terminal-`c` groups such as `bash -cx …`: there bash's `-c` inline-
//     consumes the REST OF THE SAME token (`x`) as its command string, so the
//     next token is a positional parameter, not a command to run — there is
//     nothing to unwrap, so we intentionally Abstain rather than mis-scan it.
func shellDashC(pc cmdparse.ParsedCommand) (string, bool) {
	switch filepath.Base(pc.Executable) {
	case "sh", "bash", "zsh", "dash":
	default:
		return "", false
	}
	for i, a := range pc.Args {
		if isShortFlagGroupEndingInC(a) && i+1 < len(pc.Args) {
			return pc.Args[i+1], true
		}
	}
	return "", false
}

// isShortFlagGroupEndingInC reports whether arg is a single-dash short-flag
// group whose last flag is `c` — i.e. a shell `-c` wrapper whose command is the
// NEXT token. True for `-c`, `-lc`, `-ilc`; false for `--` long options
// (`--rcfile`, `--norc`), for a bare `-`, and for groups not ending in `c`
// (`-l`, `-cx`).
func isShortFlagGroupEndingInC(arg string) bool {
	return len(arg) >= 2 && arg[0] == '-' && arg[1] != '-' && arg[len(arg)-1] == 'c'
}

func isFlag(arg string) bool {
	return len(arg) > 0 && arg[0] == '-'
}
