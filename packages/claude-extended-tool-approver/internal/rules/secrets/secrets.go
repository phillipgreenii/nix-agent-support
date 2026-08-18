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
//
// # What is screened, and why it is scoped this way
//
// Settled by the OPERATOR RULING of 2026-08-13 on beads pg2-fhb9q (credential
// coverage was only `secrets` + `.ssh`, so ~/.aws, ~/.gnupg, ~/.kube, ~/.docker
// and ~/.netrc were screened by NOTHING) and pg2-pmk9q (the `secrets` component
// fires on this repo's OWN internal/rules/secrets/ source, so working the CETA
// queue prompts on reading this file). One mechanism settles both.
//
// THE MEASURED ROOT CAUSE, on main @93846155, isolated XDG_DATA_HOME,
// permission_mode=default:
//
//	cat ~/.ssh/id_rsa          -> deny      (.ssh IS in secretpath's dir list)
//	cat ~/.aws/credentials     -> abstain
//	cat ~/.gnupg/secring.gpg   -> abstain
//	cat ~/.kube/config         -> abstain
//	cat ~/.netrc               -> abstain
//
// All five parent directories were ALREADY in the nix-managed
// sandbox.filesystem.denyRead that patheval.LoadSandboxFilesystemConfig loads and
// that this rule already held an evaluator for. They abstained because
// secretpath.IsSecret GATED whether the deny-list was consulted at all: decide()
// only ran on a reference the lexical list had already matched, so a path the Go
// list did not recognize never reached patheval.IsDenyRead. The two lists were not
// redundant — the LEXICAL one sat UPSTREAM of the CONFIGURED one, and that
// inversion was the defect.
//
// The four scoping decisions, each ruled explicitly:
//
//  1. CREDENTIAL DIRECTORIES ARE CONFIG-DRIVEN. The deny-list is consulted for ANY
//     path, independent of secretpath (see configRef). Covering a new credential
//     store is therefore a CONFIG edit, not a code edit — the alternative, a
//     second hardcoded Go list, was rejected because it drifts from the machine
//     config that already names the same directories.
//  2. THE `.env` / `.env.*` ARM STAYS LEXICAL AND REPO-BLIND. Explicitly NOT
//     repo-scoped: a `.env` inside a repo is the most common real credential file
//     an agent reads, so making it config-only would silently retire a live
//     control.
//  3. THE BARE `secrets` COMPONENT STAYS LEXICAL BUT IS SKIPPED INSIDE A GIT
//     REPOSITORY, FOR READS ONLY ON THE DIRECT-TOOL ROUTE (see lexicalHit and,
//     for why the Bash route cannot honor this direction split, bashRef).
//     Operator rationale, verbatim: "anything that is in a git repo is not
//     secret. if someone does have secrets in a repo, then they can explicitly
//     set those paths in the config" — and the escape hatch is real, because
//     LoadSandboxFilesystemConfig already merges the PROJECT-level
//     .claude/settings.json.
//  4. EXTENSION ARMS ARE OUT — no `*.pem`, `*.p12`, `*.pfx`, `*.keystore`,
//     `service-account*.json` — on false-positive grounds: a repo full of test
//     fixtures named `*.pem` is common. pg2-ia640.1 owns the unconditional
//     `*.pem`/`*.key` question separately.
//
// Decision 3 fixes pg2-pmk9q BY CONSTRUCTION and more broadly than that bead's own
// sanctioned option: any project with an `internal/…/secrets/` package is covered,
// not just this one. It carries ONE deliberate coverage reduction, made by the
// operator with the guard's text in front of them: `deploy/secrets/token` inside a
// repo no longer Asks on a read, and covering such a tree becomes a project-level
// denyRead entry.
//
// READS AND WRITES STAY DISTINGUISHED ONLY ON THE DIRECT-TOOL ROUTE (Write / Edit
// / MultiEdit / Delete, where Check computes a real per-call isWrite) — there a
// write under a `secrets/` component is NOT relaxed, in or out of a repo, because
// the read relaxation is broader than the alternatives that were rejected, and a
// write is the act that cannot be undone by prompting later. ON THE BASH ROUTE
// THAT DISTINCTION DOES NOT EXIST: bashRef always evaluates the relaxation with
// isWrite hardcoded to false (Bash read/write intent is ambiguous per-argument),
// so a Bash write-shaped command (`rm`, `> file`, `| tee`) gets the identical
// relaxation a Bash read gets. That is INTENTIONAL, confirmed by the operator's
// 2026-08-17 ruling on pg2-ifbfa: the in-repo "not secret" judgment was meant to
// cover Bash writes too, not just Bash reads — it is not a gap this rule needs to
// close. See bashRef's comment for the mechanism and lexicalHit's for the
// resulting condition.
package secrets

import (
	"fmt"
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

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	switch input.ToolName {
	case "Read", "Write", "Edit", "MultiEdit", "Delete":
		path, err := input.FilePath()
		if err != nil {
			return hookio.RuleResult{}, fmt.Errorf("secrets: read file_path: %w", err)
		}
		isWrite := writeTools[input.ToolName]
		if ref, ok := r.pathRef(path, isWrite); ok {
			return r.decide(ref, isWrite), nil
		}
	case "Glob", "Grep":
		path, err := input.SearchPath()
		if err != nil {
			return hookio.RuleResult{}, fmt.Errorf("secrets: read search path: %w", err)
		}
		if ref, ok := r.pathRef(path, false); ok {
			return r.decide(ref, false), nil
		}
	case "Bash":
		cmd, err := input.BashCommand()
		if err != nil {
			return hookio.RuleResult{}, fmt.Errorf("secrets: read bash command: %w", err)
		}
		ref, found, malformed := r.bashRef(cmd)
		if malformed {
			// pg2-52eod: a glued flag value's shell quoting could not be
			// resolved (cmdparse.GluedFlagValue's decline, generalized from
			// pg2-mp9oq's one call site), so this rule cannot rule out that it
			// names a credential path. NotApplicable would let a later rule
			// (safe-commands, path-safety) fill the gap with an unqualified
			// Approve exactly as the pre-fix bug did for a bare secret name
			// like `.env`, which safe-commands' zone check never even
			// considers (it is not path-shaped) — so this returns Ask
			// directly, the same "cannot classify — fail closed" verdict
			// decide() gives a classified-but-not-deny-listed reference.
			return hookio.RuleResult{
				Decision: hookio.Ask,
				Reason:   "a glued flag value has shell quoting this rule cannot resolve, so it cannot rule out a credential/secret reference — prompting instead of auto-approving",
				Module:   r.Name(),
			}, nil
		}
		if found {
			// Bash read/write intent is ambiguous per-argument; treat as a
			// read for deny-list purposes (the bead is about reads).
			return r.decide(ref, false), nil
		}
	}
	// NOT-APPLICABLE, not NoOpinion: this rule exists to stop path-safety and
	// safe-commands (both registered AFTER it) from silently approving a
	// credential path, so on a path it did NOT match those rules MUST still be
	// reached (ADR 0043's Decision, point 2 — Shape B).
	//
	// The three malformed-input branches above used to land HERE, on this one
	// shared return, so "no secret path in this call" and "I could not read the
	// tool input" were the same value — the gitdir dual-meaning defect in a second
	// place. They are now genuine errors, recorded per rule, and the chain still
	// continues, so the decision is unchanged.
	return hookio.NotApplicable()
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
// THREE of them over the same candidates — lexicalRef, then resolvedRef, then
// configRef — so the traversal is written once (see firstSecretRef).
//
// Each is built for one Evaluate and closes over that call's ACCESS DIRECTION,
// because two of the three answers depend on it: the in-repo relaxation of the
// generic `secrets` arm applies to reads only, and the deny-list has separate
// read and write halves.
type candidateMatch func(string) (secretRef, bool)

// pathRef tests a path a tool call NAMES against the lexical classifier in BOTH
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
// The in-repo relaxation does need an absolutized form, so it asks CleanPath for
// one INSIDE inGitRepo rather than absolutizing the candidate everyone else sees.
// That keeps this property: with a nil evaluator the lexical arms still match on
// the raw string, and the relaxation simply never fires (inGitRepo fails closed).
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
//
// The CONFIG arm runs last, for the same reason bashRef orders its passes that
// way: it can only fire where both lexical forms declined, so it only ever
// converts an Abstain into a Reject.
func (r *Rule) pathRef(path string, isWrite bool) (secretRef, bool) {
	if path == "" {
		return secretRef{}, false
	}
	if r.lexicalHit(path, isWrite) {
		return secretRef{named: path}, true
	}
	if ref, ok := r.resolvedForm(path, isWrite); ok {
		return ref, true
	}
	if r.denyListed(path, isWrite) {
		return secretRef{named: path}, true
	}
	return secretRef{}, false
}

// bashRef returns the first secret path referenced by a Bash command, testing the
// same normalizations as pathRef but in THREE SEPARATE PASSES over the command:
// every candidate as NAMED first; only if that finds nothing, the candidates worth
// RESOLVING; and only if that finds nothing either, the candidates the user has
// DENY-LISTED.
//
// The pass split is load-bearing in two ways, so it must not be collapsed into a
// per-candidate `named || resolved || denied`:
//
//   - It makes the change strictly additive. A command whose lexical pass hits
//     selects the SAME first reference it always did, so its decision — including
//     the Reject that a deny-listed reference earns — is unchanged. Only commands
//     that used to Abstain can move, and they can only move to Ask or Reject.
//     Interleaved, a resolved hit on an early argument could outrank a deny-listed
//     named hit on a later one and downgrade that Reject to an Ask.
//   - It keeps the filesystem out of the hit path as far as possible: neither the
//     resolving pass nor the deny-list pass runs for a command the cheap pass
//     already answered. The cheap pass is no longer filesystem-FREE in every case
//     (see lexicalHit), but it touches the filesystem only where the generic
//     `secrets` arm matched — never on the majority path.
//
// EACH RESOLVING PASS GETS ITS OWN maxResolutions BUDGET rather than sharing one.
// They resolve the SAME candidate strings, so the second pass's walks are
// cache-warm repeats of the first's and the real cost is far below 2x; sharing one
// budget would instead let a long argument list spend the whole allowance on pass
// 2 and silently disable the deny-list pass, which is the fail-OPEN direction for
// a control the user configured explicitly.
func (r *Rule) bashRef(cmd string) (ref secretRef, found bool, malformed bool) {
	// Bash read/write intent is ambiguous per-argument, so every candidate is
	// judged as a READ — the direction the beads are about, and the one that
	// governs the in-repo relaxation.
	//
	// SO THE "READ ONLY" HALF OF THE IN-REPO RELAXATION IS VACUOUS ON THIS ROUTE.
	// `isWrite` is never true for a Bash command, so `write >= read` is trivially
	// satisfied and the relaxation reaches WRITE-SHAPED commands exactly as it reaches
	// reads. TestRule_WriteNeverLessRestrictiveThanRead cannot see this: it supplies
	// `isWrite` directly, so it proves the rule HONOURS the parameter, not that any Bash
	// caller ever sets it. MEASURED through internal/setup's replay harness on
	// `<repo>/internal/rules/secrets/secrets.go` with cwd inside a git worktree —
	// `rm`, `> `, and `| tee` all moved ask -> approve alongside `cat`. THIS IS
	// INTENTIONAL, not a gap: the operator's 2026-08-17 ruling on pg2-ifbfa
	// confirmed the in-repo "not secret" judgment covers Bash writes too (the
	// package comment's decision 3 and lexicalHit's doc now say so directly —
	// they used to claim READ ONLY unconditionally, which was wrong for this
	// route; pg2-ifbfa corrected them).
	//
	// The GUARD THAT DOES HOLD on this route is the repo test itself: outside any git
	// working tree the arm still fires, so `~/secrets/prod.env` keeps its Ask either way.
	const isWrite = false
	// pg2-52eod: malformed is checked BEFORE match ever runs inside firstSecretRef,
	// so it is deterministic per COMMAND, not per candidateMatch — every pass would
	// report the same malformed verdict for the same cmd. The first pass that finds
	// EITHER a match OR a malformed value short-circuits the remaining passes.
	if ref, found, malformed := firstSecretRef(cmd, maxShellUnwrap, r.lexicalRef(isWrite)); found || malformed {
		return ref, found, malformed
	}
	if r.pe == nil {
		return secretRef{}, false, false
	}
	resolveBudget := maxResolutions
	if ref, found, malformed := firstSecretRef(cmd, maxShellUnwrap, r.resolvedRef(&resolveBudget, isWrite)); found || malformed {
		return ref, found, malformed
	}
	denyBudget := maxResolutions
	return firstSecretRef(cmd, maxShellUnwrap, r.configRef(&denyBudget, isWrite))
}

// lexicalRef is the lexical candidate test: the candidate exactly as the call
// named it, run through lexicalHit.
func (r *Rule) lexicalRef(isWrite bool) candidateMatch {
	return func(path string) (secretRef, bool) {
		if !r.lexicalHit(path, isWrite) {
			return secretRef{}, false
		}
		return secretRef{named: path}, true
	}
}

// lexicalHit applies secretpath's classification and then the ONE relaxation the
// operator ruled for it: a match on the bare, role-describing `secrets` component
// and nothing else is DROPPED for a READ of a path inside a git repository (see
// the package comment's decision 3).
//
// TWO CONDITIONS, both necessary — but the first only BINDS where its caller
// passes a real, per-call `isWrite`:
//
//   - READ ONLY ON THE DIRECT-TOOL ROUTE (Write/Edit/MultiEdit/Delete, via
//     Check). A write under a `secrets/` component is never relaxed there. This
//     is the guard pg2-pmk9q pinned and the ruling explicitly kept: the read
//     relaxation is the broad one, so keeping the directions distinguished is what
//     bounds it. A write that turns out to have been to a real credential store
//     cannot be taken back by prompting afterwards. ON THE BASH ROUTE THIS
//     CONDITION IS VACUOUS: bashRef always calls lexicalHit with isWrite=false,
//     so a Bash write-shaped command is relaxed exactly like a Bash read. That is
//     INTENTIONAL — the operator's 2026-08-17 ruling on pg2-ifbfa confirmed the
//     in-repo relaxation was meant to cover Bash writes too — not a gap in this
//     function.
//   - GenericSecretsDir ONLY. secretpath.Classify reports the STRONGEST arm that
//     matched, so `<repo>/secrets/.ssh/id_rsa` and `<repo>/secrets/.env` come back
//     WellKnownSecret and keep asking. The relaxation can only ever discard the
//     weakest evidence there is.
//
// It is the only place this rule consults the filesystem outside a resolution, and
// it does so only when the generic arm matched — never on the majority path where
// nothing matched at all.
func (r *Rule) lexicalHit(path string, isWrite bool) bool {
	switch secretpath.Classify(path) {
	case secretpath.WellKnownSecret:
		return true
	case secretpath.GenericSecretsDir:
		return isWrite || !r.inGitRepo(path)
	default:
		return false
	}
}

// inGitRepo reports whether path lies inside a git working tree, asked of the
// path's NAMED form (env/`~`/cwd-relative expansion, no symlink resolution) — the
// same normalization every other named-form question in this rule uses. The
// resolved form gets its own separate lexicalHit call from resolvedForm, so a link
// out of a repo into a real `secrets/` store is still classified on where it LANDS.
//
// IT FAILS CLOSED in both of its failure modes. A nil evaluator (a supported
// configuration — see resolve) and a path CleanPath cannot expand both report
// false, i.e. "not in a repo", which leaves the `secrets` arm FIRING. The
// relaxation only ever removes a prompt, so an unanswerable question must cost an
// Ask, never silence.
func (r *Rule) inGitRepo(path string) bool {
	if r.pe == nil {
		return false
	}
	cleaned := r.pe.CleanPath(path)
	if cleaned == "" {
		return false
	}
	return patheval.InGitRepo(cleaned)
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
func (r *Rule) resolvedRef(budget *int, isWrite bool) candidateMatch {
	return func(path string) (secretRef, bool) {
		if *budget <= 0 || !isPathShaped(path) {
			return secretRef{}, false
		}
		*budget--
		return r.resolvedForm(path, isWrite)
	}
}

// resolvedForm tests the symlink-resolved form of path.
func (r *Rule) resolvedForm(path string, isWrite bool) (secretRef, bool) {
	resolved := r.resolve(path)
	if resolved == "" || !r.lexicalHit(resolved, isWrite) {
		return secretRef{}, false
	}
	return secretRef{named: path, resolved: resolved}, true
}

// configRef is the CONFIG-DRIVEN candidate test: a path the user has deny-listed
// for THIS direction of access, whether or not any lexical arm recognizes it.
//
// This is the arm that makes credential-directory coverage a CONFIG edit rather
// than a code edit (the package comment's decision 1). It is also the arm that
// closes the measured hole: before it existed, secretpath.IsSecret gated whether
// the deny-list was consulted at all, so `cat ~/.aws/credentials` abstained even
// though `.aws` was deny-listed. path-safety consults the deny-list too, but only
// for the FILE and SEARCH tools — a Bash argument path never reaches it, so for
// Bash this rule is the only place the deny-list can be applied.
//
// IT IS GATED ON isPathShaped, for exactly the reason resolvedRef is and with more
// force: IsDenyRead ABSOLUTIZES against the cwd, so a bare word would be
// reclassified as a file in the current directory. With a cwd anywhere under a
// deny-listed tree, `kubectl get secrets` would resolve `secrets` to
// `<cwd>/secrets`, land inside that tree and hard-REJECT — a false positive worse
// than the pg2-ia640.2 class, since a Reject cannot be waved through.
func (r *Rule) configRef(budget *int, isWrite bool) candidateMatch {
	return func(path string) (secretRef, bool) {
		if *budget <= 0 || !isPathShaped(path) {
			return secretRef{}, false
		}
		*budget--
		if !r.denyListed(path, isWrite) {
			return secretRef{}, false
		}
		return secretRef{named: path}, true
	}
}

// denyListed reports whether the user's sandbox.filesystem deny-list blocks this
// direction of access to path. It is the single place the read/write halves of the
// deny-list are selected between, so configRef's screen and decide's verdict
// cannot disagree about which half applies.
//
// READS AND WRITES ARE DELIBERATELY NOT UNIONED. A path in denyRead only is not
// blocked for writing here, matching path-safety's own split — denyWrite is the
// key that governs writes, and answering a write with the read list would make the
// two rules disagree about the same config.
func (r *Rule) denyListed(path string, isWrite bool) bool {
	if r.pe == nil {
		return false
	}
	if isWrite {
		return r.pe.IsDenyWrite(path)
	}
	return r.pe.IsDenyRead(path)
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
//
// A reference the CONFIG arm found (configRef) necessarily takes the Reject
// branch, since that arm's whole screen is this same predicate. The re-check is
// kept rather than short-circuited so there is ONE place the verdict is decided:
// every arm hands decide a reference and decide alone chooses Reject vs Ask.
func (r *Rule) decide(ref secretRef, isWrite bool) hookio.RuleResult {
	if r.denyListed(ref.named, isWrite) {
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "credential/secret path is deny-listed: " + ref.describe(),
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{
		Decision: hookio.Ask,
		Reason:   "references credential/secret path " + ref.describe() + " — prompting instead of auto-approving",
		Module:   r.Name(),
	}
}

// firstSecretRef returns the first reference in the command that match accepts —
// whether an argument or an I/O redirection target (e.g. `cat < secrets/x`) — and
// separately reports malformed: whether ANY glued flag value along the way carried
// shell quoting that could not be resolved (pg2-52eod). It also descends one
// `sh`/`bash -c '<inner>'` level at a time (up to depth) so the check cannot be
// trivially bypassed by wrapping the read in a shell string.
//
// match is the candidate test — lexicalRef, resolvedRef or configRef. The
// traversal is identical for all three, and bashRef runs it once per pass.
//
// malformed IS CHECKED BEFORE match EVER RUNS, so it is the SAME verdict on every
// one of bashRef's three passes for the same command — it does not depend on which
// candidateMatch was supplied. found and malformed are therefore mutually exclusive
// in practice (a malformed value returns immediately, before any match call), but
// both are reported explicitly rather than folding "cannot classify" into "not
// found": a caller that dropped the distinction would let the malformed subset fall
// through to secrets.Rule's NotApplicable exactly like an ordinary non-match — the
// P0 bypass this bead exists to close. See GluedFlagValue's doc for why this
// primitive is now the single source of the unwrap, and for the measured
// `grep --file='.env' x.log` row this centralization fixes.
func firstSecretRef(cmd string, depth int, match candidateMatch) (ref secretRef, found bool, malformed bool) {
	for _, pc := range cmdparse.Parse(cmd) {
		if depth > 0 {
			if inner, ok := shellDashC(pc); ok {
				if ref, found, malformed := firstSecretRef(inner, depth-1, match); found || malformed {
					return ref, found, malformed
				}
				continue
			}
		}
		// AN `--opt=value` TOKEN IS TESTED BY ITS VALUE, NOT SKIPPED WHOLE
		// (pg2-cu3ro). isFlag below is right that a flag NAME is not a filename,
		// but `--file=<path>` is ONE argv token, so skipping the token discarded
		// the path with it and `git commit --file=~/.ssh/id_rsa` measured ALLOW
		// while both `-F` and the space-separated `--file` measured DENY. The
		// space form only ever worked because the path was a SEPARATE token that
		// isFlag does not match, i.e. the coverage was incidental to the spelling.
		//
		// secretCandidateArgs runs FIRST, which is what keeps this loop from
		// re-opening the false positives its siblings closed: SkipMessageArgs
		// already drops a `--reason=<prose>` token whole (its own equalsFlagName
		// branch), and the grep/rg and jq skippers have already removed their
		// value-flag operands in both spellings. So a glued token that reaches
		// here is one no carve-out claims, and its value is a candidate path.
		// argsMalformed reports the grep/rg branch's OWN glued-flag decline
		// (cmdparse.SkipGrepPattern, e.g. `grep --file='.env'x'.env' x.log`) —
		// a value this loop never even sees as a `--flag=value` token, because
		// SkipGrepPattern already reduced it to a bare candidate.
		args, argsMalformed := secretCandidateArgs(pc)
		if argsMalformed {
			return secretRef{}, false, true
		}
		for _, arg := range args {
			// THE VALUE HALF IS UNWRAPPED by cmdparse.GluedFlagValue ITSELF
			// (pg2-52eod centralizes what used to be a per-call-site
			// cmdparse.UnwrapGluedQuotes call here, pg2-6f2gu). GluedFlagValue's
			// own doc records the measured rows this fixed and the audit that
			// resolved the DRY-vs-blast-radius tradeoff pg2-9zgso and pg2-6f2gu
			// each declined to close, because cmdparse.SkipGrepPattern's own
			// consequences (see argsMalformed above) had never been reviewed.
			//
			// malformed is GluedFlagValue's decline signal for a value an unwrap
			// could not resolve (double-wrapped, an interior wrapper character,
			// a mismatched quote pair). It MUST be treated as "cannot classify —
			// fail closed", never as an ordinary non-match: falling through to
			// `match(value)` on a still quote-wrapped value would silently miss
			// a real secret reference the same way the pre-fix bug did, and
			// falling through to `continue` would report it as "no secret path
			// in this call" when the true answer is "unknown."
			if value, glued, isMalformed := cmdparse.GluedFlagValue(arg); glued {
				if isMalformed {
					return secretRef{}, false, true
				}
				if ref, ok := match(value); ok {
					return ref, true, false
				}
				continue
			}
			if isFlag(arg) {
				continue
			}
			if ref, ok := match(arg); ok {
				return ref, true, false
			}
		}
		for _, redir := range pc.Redirections {
			if ref, ok := match(redir.Path); ok {
				return ref, true, false
			}
		}
	}
	return secretRef{}, false, false
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
//   - bd/git/gh: the value of a MESSAGE flag (`bd close --reason <prose>`,
//     `bd create --title/--description <prose>`, `git commit -m <prose>`,
//     `gh pr comment --body <prose>`) and the trailing body positional of
//     `bd comment <id> <body>` are free text, not files (pg2-ia640.5). The
//     enumerated flag/positional boundary lives in cmdparse.SkipMessageArgs.
//
// WHY THE MESSAGE CARVE-OUT IS NOT A BYPASS. A message value is STORED AS TEXT:
// the command never opens, executes or transmits it as a path, so a credential
// path spelled inside one grants no access whatsoever — while prompting on it
// costs the human a paragraph-length retype (the ~40-line bead comment of asklog
// row 325419). Every way of getting the FILE'S CONTENT into such a message is a
// DIFFERENT construct in the same command, and each is still checked:
// `< secrets/x` is a redirection (firstSecretRef tests pc.Redirections),
// `"$(cat ~/.ssh/id_rsa)"` is a substitution body the ENGINE recurses into as its
// own evaluation unit, `bash -lc '<inner>'` is unwrapped by shellDashC, and
// `-F`/`--file`/`--body-file` read the message FROM a path — which is exactly why
// those flags are absent from the message tables. The skip is also keyed on an
// ENUMERATED, CLOSED set of executables, so it cannot leak to a command that does
// open its arguments (`cp ~/.ssh/id_rsa /tmp` is unaffected).
// secretCandidateArgs' second return, malformed, reports cmdparse.SkipGrepPattern's
// pg2-52eod decline signal for the grep/rg branch — a glued file-flag value whose
// shell quoting it could not resolve. It is always false for every other branch,
// since none of them route through SkipGrepPattern's own GluedFlagValue call; the
// plain default/bd/git/gh/jq branches hand their args to firstSecretRef's OWN
// GluedFlagValue call unchanged, which carries its own malformed detection already.
func secretCandidateArgs(pc cmdparse.ParsedCommand) (args []string, malformed bool) {
	switch filepath.Base(pc.Executable) {
	case "grep", "rg":
		return cmdparse.SkipGrepPattern(filepath.Base(pc.Executable), pc.Args)
	case "jq":
		args := cmdparse.SkipJqValueFlags(pc.Args)
		if !jqFilterFromFile(pc.Args) {
			args = dropFirstPositional(args)
		}
		return args, false
	case "bd", "git", "gh":
		return cmdparse.SkipMessageArgs(filepath.Base(pc.Executable), pc.Args), false
	default:
		return pc.Args, false
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
