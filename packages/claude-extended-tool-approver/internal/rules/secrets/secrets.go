// Package secrets prompts (Ask) before any tool call touches a well-known
// credential/secret file, so such reads/writes are never silently
// auto-approved by a later rule — e.g. the safe-commands rule approving
// `cat <readable-path>` where the path is ~/.claude/.credentials (pg2-to8pe).
//
// secretpath.IsSecret is a lexical predicate, so WHICH SPELLING it is handed
// decides what the rule can see. Every path this rule tests is therefore tested
// in two normalizations — as NAMED and symlink-RESOLVED — so a link into a
// credential directory cannot be used to slip the access past it (pg2-cdmb1).
// pathRef holds that contract and the reasoning behind it.
package secrets

import (
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/secretpath"
)

// maxShellUnwrap bounds how deep the rule follows nested `sh -c '<inner>'`
// wrappers when scanning a Bash command for secret paths.
const maxShellUnwrap = 3

// maxResolutions bounds how many symlink resolutions ONE Evaluate may perform
// while scanning a Bash command (see resolvedRef).
//
// The lexical pass is filesystem-free, so the resolving pass is the only part
// whose cost scales with a command's argument count — and it runs precisely on
// the commands the lexical pass cleared, i.e. the overwhelming majority of
// calls. Each resolution is a filepath.EvalSymlinks walk plus, for a path that
// does not exist, one further EvalSymlinks per ancestor, so an unbounded pass
// turns a string check into a per-call stat storm on exactly the calls that gain
// nothing from it.
//
// The cap weakens only the symlink REFINEMENT and never the lexical check: every
// argument of an over-long command line is still tested AS NAMED, which is all
// this rule did before the resolving pass existed. So the failure mode of the cap
// is the pre-existing behavior, not a new hole.
const maxResolutions = 32

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
		if path, err := input.FilePath(); err == nil {
			if ref, ok := r.pathRef(path); ok {
				return r.decide(ref, writeTools[input.ToolName])
			}
		}
	case "Glob", "Grep":
		if path, err := input.SearchPath(); err == nil {
			if ref, ok := r.pathRef(path); ok {
				return r.decide(ref, false)
			}
		}
	case "Bash":
		if cmd, err := input.BashCommand(); err == nil {
			if ref, ok := r.bashRef(cmd); ok {
				// Bash read/write intent is ambiguous per-argument; treat as a
				// read for deny-list purposes (the bead is about reads).
				return r.decide(ref, false)
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

// secretRef is a path reference that matched secretpath.IsSecret. It carries the
// form the call NAMED as well as — only when it was the symlink-resolved form
// that matched — what that name resolves to, so the decision reason can name the
// indirection a reader of the asklog would otherwise have to reconstruct.
type secretRef struct {
	named    string
	resolved string // "" when the named form itself matched
}

// describe renders the reference for a decision reason. For a named-form match
// it is the name alone, byte-identical to what this rule emitted before the
// resolving pass existed.
func (ref secretRef) describe() string {
	if ref.resolved == "" {
		return ref.named
	}
	return ref.named + " -> " + ref.resolved
}

// candidateMatch tests one candidate path string for secret-ness. The rule runs
// two of them over the same candidates — namedRef, then resolvedRef — so the
// traversal is written once (see firstSecretRef).
type candidateMatch func(string) (secretRef, bool)

// pathRef tests a path a tool call NAMES against secretpath.IsSecret in BOTH
// normalizations, mirroring pathsafety's isAgentConfigWrite (pathsafety.go): the
// path as NAMED, and the symlink-RESOLVED path. Either matching is a hit — the
// named form so a credential file that is ITSELF a symlink still matches (its
// target is typically not in a credential directory), the resolved form so a
// symlink elsewhere pointing INTO a credential directory cannot be used to slip
// the access past this rule (`~/mykeys/id_rsa -> ~/.ssh/id_rsa`).
//
// The named form is the RAW string, not eval.CleanPath(path) as pathsafety uses.
// The two differ deliberately: pathsafety's predicate inspects a path's parent
// directory, so it needs an absolutized path, whereas secretpath.IsSecret is
// purely lexical and matches the `~/`, `$VAR/` and cwd-relative spellings
// directly. Keeping the named form raw also keeps it working with a nil
// evaluator, which is a supported configuration here (see resolve).
//
// A RESOLVED PATH IS CLASSIFIED WHEREVER IT LANDS — there is no check that it
// stays inside the project root, the workspace, or any other known zone. That is
// deliberate, and it is the point: the shape most worth catching is a link out of
// the workspace to credentials that live outside it (`~/mykeys ->
// /Volumes/backup/.ssh`), so a zone gate would reopen the bypass for exactly the
// worst case. It is also safe in the only direction that matters — this rule's
// output is Ask, or Reject when the user has deny-listed the path, and NEVER
// Approve — so classifying an out-of-zone resolution can only ADD a prompt, never
// grant access. Whether the file is reachable at all is patheval.Evaluate's
// question, and it is asked later by path-safety.
//
// Resolution is UNCONDITIONAL here, with none of the gating bashRef applies: a
// file tool's `file_path` and a search tool's `path` ARE paths by the tool's own
// contract, and there is exactly one of them per call, so neither the
// path-shapedness question nor the resolution budget arises.
func (r *Rule) pathRef(path string) (secretRef, bool) {
	if path == "" {
		return secretRef{}, false
	}
	if ref, ok := namedRef(path); ok {
		return ref, true
	}
	return r.resolvedForm(path)
}

// bashRef returns the first secret path referenced by a Bash command, testing the
// same two normalizations as pathRef but in TWO SEPARATE PASSES over the command:
// every candidate as NAMED first, and only if that finds nothing, the candidates
// worth resolving.
//
// The pass split is load-bearing in two ways, so it must not be collapsed into a
// per-candidate `named || resolved`:
//
//   - It makes the change strictly additive. A command whose lexical pass hits
//     selects the SAME first reference it always did, so its decision — including
//     the Reject that a deny-listed reference earns — is unchanged. Only commands
//     that used to Abstain can move, and they can only move to Ask or Reject.
//     Interleaved, a resolved hit on an early argument could outrank a deny-listed
//     named hit on a later one and downgrade that Reject to an Ask.
//   - It keeps the filesystem out of the hit path entirely: the resolving pass
//     never runs for a command the cheap pass already answered.
func (r *Rule) bashRef(cmd string) (secretRef, bool) {
	if ref, ok := firstSecretRef(cmd, maxShellUnwrap, namedRef); ok {
		return ref, true
	}
	if r.pe == nil {
		return secretRef{}, false
	}
	budget := maxResolutions
	return firstSecretRef(cmd, maxShellUnwrap, r.resolvedRef(&budget))
}

// namedRef is the lexical, filesystem-free candidate test: the candidate exactly
// as the call named it.
func namedRef(path string) (secretRef, bool) {
	if secretpath.IsSecret(path) {
		return secretRef{named: path}, true
	}
	return secretRef{}, false
}

// resolvedRef builds the resolving candidate test for one Evaluate, spending from
// a shared budget (maxResolutions).
//
// It resolves only PATH-SHAPED candidates. A Bash argument is not necessarily a
// path — which is the whole reason secretCandidateArgs exists — and resolution
// ABSOLUTIZES against the cwd, so resolving a bare word would reclassify it as a
// file in the current directory: `kubectl get secrets` would resolve `secrets` to
// `<cwd>/secrets` and match the secrets/ directory arm, which is precisely the
// false-positive class pg2-ia640.2 removed.
//
// The cost of that bound, stated plainly: a bare-basename symlink in the cwd
// (`cat mykey` where `./mykey -> ~/.ssh/id_rsa`) is NOT resolved and so is not
// detected. Closing it means resolving every bare word of every command, which
// buys back that false-positive class and the stat storm together. A path-shaped
// spelling of the same link IS caught, including `./mykey`.
func (r *Rule) resolvedRef(budget *int) candidateMatch {
	return func(path string) (secretRef, bool) {
		if *budget <= 0 || !isPathShaped(path) {
			return secretRef{}, false
		}
		*budget--
		return r.resolvedForm(path)
	}
}

// resolvedForm tests the symlink-resolved form of path.
func (r *Rule) resolvedForm(path string) (secretRef, bool) {
	resolved := r.resolve(path)
	if resolved == "" || !secretpath.IsSecret(resolved) {
		return secretRef{}, false
	}
	return secretRef{named: path, resolved: resolved}, true
}

// resolve returns the symlink-resolved absolute form of path, or "" when no
// resolved form can be produced.
//
// A NIL EVALUATOR IS A SUPPORTED CONFIGURATION and is what makes this rule
// cwd-independent (ad-hoc per-row A/B harnesses construct it that way — see
// pg2-faaq2). Resolution needs the evaluator's cwd and home to expand a relative
// or `~`-prefixed path, so with a nil evaluator it DEGRADES: "" here means the
// resolved-form check simply never matches, the named-form check still runs, and
// the rule behaves exactly as it did before this pass existed. It must not panic
// and must not skip the named form.
//
// "" is also what the evaluator itself returns for a path that cannot be resolved
// at all — a broken symlink, or an unexpanded variable — and it is handled the
// same way, since secretpath.IsSecret("") is false.
func (r *Rule) resolve(path string) string {
	if r.pe == nil {
		return ""
	}
	return r.pe.ResolvePath(path)
}

// isPathShaped reports whether a candidate looks like a filesystem path rather
// than a bare word: it contains a separator, or begins with `~` or `$` (a home or
// variable reference the evaluator expands into one). See resolvedRef for why the
// resolving pass is gated on this and the named pass is not.
func isPathShaped(path string) bool {
	return strings.Contains(path, "/") ||
		strings.HasPrefix(path, "~") ||
		strings.HasPrefix(path, "$")
}

// decide returns Reject when the referenced secret is one the user has
// deny-listed for the relevant access, otherwise Ask.
//
// The deny-list is consulted with the NAMED form: patheval's IsDenyRead /
// IsDenyWrite resolve symlinks themselves, so a symlink into a deny-listed
// directory is already covered and pre-resolving here would gain nothing.
func (r *Rule) decide(ref secretRef, isWrite bool) hookio.RuleResult {
	if r.pe != nil {
		denied := r.pe.IsDenyRead(ref.named)
		if isWrite {
			denied = r.pe.IsDenyWrite(ref.named)
		}
		if denied {
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "credential/secret path is deny-listed: " + ref.describe(),
				Module:   r.Name(),
			}
		}
	}
	return hookio.RuleResult{
		Decision: hookio.Ask,
		Reason:   "references credential/secret path " + ref.describe() + " — prompting instead of auto-approving",
		Module:   r.Name(),
	}
}

// firstSecretRef returns the first reference in the command that match accepts —
// whether an argument or an I/O redirection target (e.g. `cat < secrets/x`).
// It also descends one `sh`/`bash -c '<inner>'` level at a time (up to depth)
// so the check cannot be trivially bypassed by wrapping the read in a shell
// string.
//
// match is the candidate test — namedRef or resolvedRef. The traversal is
// identical for both, and bashRef runs it once per pass.
func firstSecretRef(cmd string, depth int, match candidateMatch) (secretRef, bool) {
	for _, pc := range cmdparse.Parse(cmd) {
		if depth > 0 {
			if inner, ok := shellDashC(pc); ok {
				if ref, found := firstSecretRef(inner, depth-1, match); found {
					return ref, true
				}
				continue
			}
		}
		for _, arg := range secretCandidateArgs(pc) {
			if isFlag(arg) {
				continue
			}
			if ref, ok := match(arg); ok {
				return ref, true
			}
		}
		for _, redir := range pc.Redirections {
			if ref, ok := match(redir.Path); ok {
				return ref, true
			}
		}
	}
	return secretRef{}, false
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
