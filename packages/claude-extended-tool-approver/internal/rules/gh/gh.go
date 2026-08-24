package gh

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

var readOnlyPR = map[string]bool{
	"view": true, "list": true, "status": true, "diff": true, "checks": true,
}

var readOnlyIssue = map[string]bool{
	"view": true, "list": true, "status": true,
}

var readOnlyRepo = map[string]bool{
	"view": true, "list": true,
}

var readOnlyRun = map[string]bool{
	"view": true, "list": true, "watch": true,
}

var readOnlyRelease = map[string]bool{
	"view": true, "list": true,
}

// There is no modifyingPR map. `gh pr create` used to be its only member, at Ask; it now
// has a draft-aware verdict of its own in pr.go (operator ruling pg2-4yy4r item 2), and a
// one-entry map whose entry moved out would only invite the next modifying `pr`
// subcommand to be added where nothing reads it.

var modifyingIssue = map[string]bool{
	"create": true,
}

// prCreateSubcommands are the spellings that CREATE a pull request. `new` is a
// documented ALIAS of `create` (`gh pr create --help`, ALIASES section) and is live —
// measured on gh 2.97.0, 2026-08-12, `gh pr new -d` parses exactly as `gh pr create -d`.
// Gating only `create` would leave the verdict one synonym away from a bypass.
var prCreateSubcommands = map[string]bool{
	"create": true, "new": true,
}

// BranchResolver looks up branch context for runtime decisions.
type BranchResolver interface {
	CurrentBranch(cwd string) (string, error)
	RunBranch(runID string) (string, error)
}

type Rule struct {
	resolver BranchResolver
}

func New(resolver BranchResolver) *Rule {
	return &Rule{resolver: resolver}
}

func (r *Rule) Name() string {
	return "gh"
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := cmdparse.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("gh: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if !isGhExecutable(pc.Executable) {
			continue
		}
		// The command path is resolved by ghCommandPath, NOT read positionally off
		// pc.Args[0]/pc.Args[1]: cobra lets a global flag precede or sit inside the path,
		// and the positional read resolved `resource` to that flag, so every such
		// spelling matched no branch and reached the final Abstain — a bypass of the WHOLE
		// rule (pg2-by1ij; see the block below its definition for the measurements).
		//
		// rest are the tokens with BOTH path words removed — the argv slice every flag
		// test below is asked about, so a verdict cannot be changed by the subcommand
		// words themselves and is independent of where in `rest` a flag sits.
		// resourceArgs keeps the SUBCOMMAND word and is what the api branch needs; see
		// ghCommandPath's doc for why the two differ.
		resource, subcmd, resourceArgs, rest := ghCommandPath(pc.Args)
		if resource == "status" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh status",
				Module:   r.Name(),
			}, nil
		}
		if resource == "auth" && subcmd == "status" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh auth status",
				Module:   r.Name(),
			}, nil
		}
		if resource == "api" {
			return r.apiVerdict(resourceArgs), nil
		}
		if resource == "search" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh search",
				Module:   r.Name(),
			}, nil
		}
		if resource == "pr" && subcmd == "merge" {
			// ghFlagTokens with `gh pr merge`'s OWN arity table, not create's: `-m`/`-r` are
			// boolean here and value-taking there, and without the filter a `--auto` gh
			// swallowed as the value of `-b`/`-t`/`-F`/`-A`/`-R`/`--body`/
			// `--match-head-commit` took this Abstain while gh merged IMMEDIATELY (pg2-ylrda).
			if boolFlagRequested(ghFlagTokens(rest, prMergeArity), "auto", 0) {
				// Intentionally Abstain — NOT a bypass on its OWN terms, though the gate it
				// defers to no longer forces a human prompt. --auto cannot merge while the PR
				// is a draft; since pg2-4yy4r item 2, non-draft creation is Rejected, and since
				// pg2-25oru `gh pr ready` was the step that un-drafts a PR and made it Ask, so
				// the two together were a real gate — this comment previously ASSUMED that
				// gate before pg2-25oru existed, when `gh pr ready` was ungated and emitted
				// `{}`, so the chain ran end to end un-prompted.
				//
				// OPERATOR RULING pg2-psiqh (2026-08-24) moved `gh pr ready` from Ask to
				// Abstain (see pr.go's prReadyVerdict), which RE-CREATES the un-prompted chain
				// this paragraph originally guarded against — in an autonomous/headless
				// session or a repo with a matching settings allow rule, `gh pr ready` then
				// `gh pr merge --auto` now both proceed with no human checkpoint from this
				// rule. That is a known, accepted consequence of pg2-psiqh, not a defect of
				// THIS branch: this Abstain still keeps the second reason intact (toggling
				// --auto refreshes the merge-commit message from the current PR title/body),
				// and pg2-psiqh deliberately left this specific branch UNCHANGED — seeing
				// pr ready re-tightened here would just mean re-deriving the pg2-psiqh
				// tradeoff from a different call site. A NEW operator ruling on the
				// draft-first flow is what would justify changing THIS branch.
				//
				// REFUSAL, not a not-applicable (pg2-qxe85), and this site is the one the
				// census left for last because the reading is not obvious: the branch reads
				// as "allowed", so a not-applicable looks harmless. It is not. An
				// ErrNotApplicable makes the leaf indistinguishable from one NO rule ever
				// examined, which is the half a LATER rule may still APPROVE. An approve here
				// would remove the gh-side half of this history while the comment went on
				// asserting it.
				//
				// A refusal is the floor that cannot happen to: it records that gh EXAMINED
				// this invocation and declined to clear it, the chain still continues, and a
				// later Reject still wins (no later rule Asks any more — pg2-psiqh). So the
				// reasoning above is enforced rather than merely documented, and the recorded
				// Former Reason becomes the live one.
				return hookio.Refused(r.Name(), "gh pr merge --auto: not cleared here — the merge cannot proceed until `gh pr ready`, which now Abstains rather than Asks (operator ruling pg2-psiqh); --auto also refreshes the merge message from the PR title/body")
			}
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "gh pr merge (immediate) is prohibited: it merges now, bypassing the draft-first landing flow. Open/keep the PR as draft and use --auto, or merge via the WORKSPACE landing flow.",
				Module:   r.Name(),
			}, nil
		}
		// The draft-first PR gate (pg2-25oru). Both branches live in pr.go; they are
		// tested here rather than under readOnlyPR/modifyingIssue because `create` and
		// `ready` are the two acts the draft-first ruling keys on, and `ready` reached the
		// final Abstain before this existed.
		if resource == "pr" && prCreateSubcommands[subcmd] {
			return r.prCreateVerdict(rest), nil
		}
		if resource == "pr" && subcmd == "ready" {
			return r.prReadyVerdict(rest), nil
		}
		// Ask -> Abstain by operator ruling (pg2-psiqh, 2026-08-24): the gh rule module
		// carries no Ask verdict anywhere now. Abstain hands this to Claude Code's own
		// permission evaluation, which auto-approves in an autonomous/headless session or
		// a repo whose settings already allow the underlying Bash call — an accepted,
		// explicit tradeoff, not an oversight. See pg2-psiqh for the full record.
		if modifyingIssue[subcmd] && resource == "issue" {
			return hookio.RuleResult{
				Decision: hookio.NoOpinion,
				Reason:   "modifying gh issue command (pg2-psiqh: gh module no longer Asks)",
				Module:   r.Name(),
			}, nil
		}
		if readOnlyPR[subcmd] && resource == "pr" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh pr",
				Module:   r.Name(),
			}, nil
		}
		if readOnlyIssue[subcmd] && resource == "issue" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh issue",
				Module:   r.Name(),
			}, nil
		}
		if readOnlyRepo[subcmd] && resource == "repo" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh repo",
				Module:   r.Name(),
			}, nil
		}
		if resource == "run" && subcmd == "rerun" {
			runID := extractRunID(pc.Args)
			if runID == "" {
				// REFUSAL (pg2-qxe85): gh KNOWS `gh run rerun`, examined this invocation, and
				// could not find the run ID it needs to decide. That is a judgement about a
				// command this rule owns, not the absence of one — and as an exhaustion it
				// read as "no rule ever looked", which is the class a consumer may clear.
				return hookio.Refused(r.Name(), "gh run rerun: no run ID found, so the branch it targets cannot be resolved (deferred to claude-code)")
			}
			if r.resolver == nil {
				// REFUSAL (pg2-qxe85). The reason is a MISSING DEPENDENCY of this rule rather
				// than anything about the command, and that is exactly why it must not be an
				// exhaustion: an unconfigured resolver would otherwise present as "nobody
				// examined this", so a deployment that forgot to wire the resolver would look
				// identical to one where the rule does not apply — and would be clearable.
				return hookio.Refused(r.Name(), "gh run rerun: no branch resolver configured, so the run's branch cannot be compared to the current one (deferred to claude-code)")
			}
			// THE ADR 0043 CANONICAL ERROR SITE. This resolver shells out
			// (`git rev-parse` under a timeout), and the pre-ADR code folded the
			// failure into the loop sentinel, DISCARDING the error — the exact
			// conflation the ADR's Context quotes. It is now a genuine error on the
			// out-of-band channel: recorded per rule, and the chain still continues,
			// so the decision is unchanged. NEVER a %w of ErrNotApplicable.
			currentBranch, err := r.resolver.CurrentBranch(input.CWD)
			if err != nil {
				return hookio.RuleResult{}, fmt.Errorf("gh: resolve current branch for `gh run rerun`: %w", err)
			}
			runBranch, err := r.resolver.RunBranch(runID)
			if err != nil {
				return hookio.RuleResult{}, fmt.Errorf("gh: resolve branch of run %s: %w", runID, err)
			}
			if currentBranch == runBranch {
				return hookio.RuleResult{
					Decision: hookio.Approve,
					Reason:   "gh run rerun for current branch",
					Module:   r.Name(),
				}, nil
			}
			// REFUSAL (pg2-qxe85), and the clearest of the four: this branch is reached only
			// AFTER both resolvers succeeded, so the rule did real work — two subprocess
			// resolutions — to establish that the run belongs to a DIFFERENT branch than the
			// one checked out. Reporting that as "not applicable" discards the finding.
			return hookio.Refused(r.Name(), "gh run rerun targets a run on branch "+runBranch+", not the current branch "+currentBranch+" (deferred to claude-code)")
		}
		if readOnlyRun[subcmd] && resource == "run" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh run",
				Module:   r.Name(),
			}, nil
		}
		if readOnlyRelease[subcmd] && resource == "release" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh release",
				Module:   r.Name(),
			}, nil
		}
		return hookio.NotApplicable()
	}
	return hookio.NotApplicable()
}

func isGhExecutable(exec string) bool {
	return exec == "gh" || filepath.Base(exec) == "gh"
}

// A GLOBAL FLAG BEFORE THE COMMAND PATH (pg2-by1ij)
//
// gh is cobra, and cobra finds a command by STRIPPING FLAGS from argv, so a flag may
// legally precede — or sit inside — the command words. Until pg2-by1ij this rule read the
// path POSITIONALLY (`resource = pc.Args[0]`, `subcmd = pc.Args[1]`), so `gh --repo o/r pr
// create` resolved resource to "--repo", matched NO branch, and reached the final Abstain,
// which a bypassPermissions session silently accepts (auto and acceptEdits instead PROMPT
// on it — operator-confirmed 2026-08-14/2026-08-15; only bypassPermissions is measured to
// auto-approve here, see primarycommit's package doc comment for the full per-mode basis).
// That escaped the whole rule at once, not one branch, in the modes that do auto-approve:
// the pg2-25oru draft-first Reject, the landed `gh pr merge` Reject and the pg2-cl0v2
// `gh api` merge Reject all sat behind the same extraction.
//
// MEASURED, gh 2.97.0 (nixpkgs), 2026-08-12, each spelling run from a directory that is
// NOT a git repository and pointed at an unresolvable host, reading whether it reached
// gh's own execution (spelling ACCEPTED) or failed at parsing (REFUSED):
//
//	gh --repo H/o/r pr create --title x --body y  -> ACCEPTED (reached the network)
//	gh --repo=H/o/r pr create …                  -> ACCEPTED ('='-glued: ONE token)
//	gh -R H/o/r pr create …                      -> ACCEPTED (short, separated value)
//	gh -RH/o/r pr create …                       -> ACCEPTED (short, glued value)
//	gh -R=H/o/r pr create …                      -> ACCEPTED (pflag's '='-glued short)
//	gh pr --repo H/o/r create …                  -> ACCEPTED — the flag sits INSIDE the
//	                                                path, so skipping only LEADING flags
//	                                                would still be bypassed
//	gh pr -R H/o/r merge                         -> ACCEPTED ("argument required when
//	                                                using the --repo flag")
//	gh --repo H/o/r pr ready                     -> ACCEPTED (likewise)
//	gh --repo H/o/r issue create …               -> ACCEPTED
//	gh --repo H/o/r pr view 1                    -> ACCEPTED
//	gh -X PUT api repos/o/r/pulls/5/merge        -> ACCEPTED, and with --verbose it dumped
//	                                                `> PUT /api/v3/repos/o/r/pulls/5/merge`
//	                                                — the api MERGE, method set BEFORE the
//	                                                `api` word
//	gh --hostname H api repos/o/r                -> ACCEPTED (an api flag, same position)
//	gh --repo H/o/r api repos/o/r                -> REFUSED: `unknown flag: --repo`; gh api
//	                                                inherits only --help
//	gh --help pr create                          -> prints `gh pr create` help: --help is a
//	                                                REGISTERED bool, so it does NOT consume
//	                                                the following token
//	gh -h pr create                              -> `unknown command "create" for "gh"`: -h
//	                                                is NOT registered, so it DID consume
//	                                                `pr` as its value
//	gh --version pr create                       -> `unknown flag: --version` with
//	                                                `Usage: gh pr` — a bool on the ROOT (it
//	                                                kept `pr`) but value-taking one level
//	                                                down, where it ate `create`
//	gh -- pr create                              -> `unknown command "pr"`: nothing after a
//	                                                `--` is a command word
//	gh - pr create                               -> resolves `gh pr create`, then refuses
//	                                                the operand: `unknown argument "-"`
//
// WHY THIS CANNOT LOOSEN AN EXISTING VERDICT. Whenever pc.Args[0] and pc.Args[1] are both
// non-flag tokens — every spelling the rule answered before — ghCommandPath returns the
// SAME pair the positional read did, `rest` is exactly the old `pc.Args[2:]`, and
// `resourceArgs` is exactly the old `pc.Args[1:]` the api branch was handed. Every OTHER
// spelling used to reach the final Abstain, so a newly-resolved path can only ADD a gate.
// The pg2-25oru and pg2-cl0v2 fixtures therefore pin unchanged behavior through unchanged
// code, which is what makes them a regression check on this change rather than a rewrite
// of it.
//
// ONE CONSEQUENCE IS WORTH NAMING: `gh --help pr create` now resolves to `pr create` and
// is REJECTED, though it only prints help. That is not new for a help invocation —
// `gh pr create --help` is Rejected today by the same reading, because the rule keys on the
// command path and not on the help flag — and it is inert, since gh creates nothing. What
// would justify carving help out: observed friction on a `--help` spelling in the decision
// log, which would be one carve-out covering both positions, not a change here.
//
// `run rerun` needs nothing from this: extractRunID locates the `rerun` token itself and
// scans AFTER it, skipping leading-dash tokens, so a flag before the path never reached it.

// ghNoValueLongFlags are the long flags that do NOT consume the following token while
// cobra searches for gh's command path — gh's ROOT flags, read off `gh --help` on
// gh 2.97.0 (2026-08-12): `--help` (registered persistent, so it reaches every level) and
// `--version` (root only). Any OTHER long flag is treated as value-taking, which is
// cobra's own rule for a flag it finds unregistered and is the reading `--repo o/r` needs.
//
// IT IS A NO-VALUE LIST, NOT A VALUE-TAKING ONE, because the two error directions are not
// equal. Omitting a value-taking flag would leave its VALUE read as the resource — the
// pre-pg2-by1ij bypass restated. Omitting a no-value flag consumes one token too many,
// which moves the resource LATER and can therefore only resolve FEWER branches than the
// positional read already did, i.e. back to today's Abstain — never past a gate.
//
// THERE IS NO SHORT-FLAG ENTRY, and that is measured rather than forgotten: gh registers
// no shorthand for --help (`gh -h pr create` answers `unknown command "create"`, so `-h`
// ate `pr`) and none for --version, so a bare short is always treated as value-taking.
// WHAT TO RE-MEASURE when gh changes: the FLAGS section of `gh --help`, for a new GLOBAL
// BOOLEAN flag — one missing from this map would consume the resource word and return that
// spelling to the fall-through Abstain.
var ghNoValueLongFlags = map[string]bool{"help": true, "version": true}

// ghCommandPath resolves a gh invocation's COMMAND PATH — the `<resource> <subcommand>`
// pair every branch in Evaluate keys on — from args, the tokens AFTER the gh executable
// (cmdparse.ParsedCommand.Args). Either is "" when argv holds no such word.
//
// It returns two argv slices, and the difference between them is load-bearing:
//
//   - rest is args with BOTH path words removed: the flag slice the `<resource>
//     <subcommand>` branches ask about. It KEEPS the flags that preceded the path, which
//     is what gh's own leaf command receives, so `gh --repo o/r pr create -d` is read as
//     the draft create it is.
//   - resourceArgs is args with only the RESOURCE word removed, and is what the `api`
//     branch must be given: `gh api` takes no subcommand, so the second command word is
//     its ENDPOINT and apiVerdict needs it. Handing api `rest` instead would drop the
//     pre-path flags, and measured (above) `gh -X PUT api repos/o/r/pulls/5/merge` really
//     does send that PUT — read without its `-X PUT` it would resolve to a GET and be
//     APPROVED, one notch WEAKER than the Abstain it produces today. That is the whole
//     reason for two slices rather than one.
func ghCommandPath(args []string) (resource, subcmd string, resourceArgs, rest []string) {
	words := ghCommandWordIndexes(args, 2)
	resourceArgs = args
	if len(words) > 0 {
		resource = args[words[0]]
		resourceArgs = omitIndexes(args, words[:1])
	}
	if len(words) > 1 {
		subcmd = args[words[1]]
	}
	return resource, subcmd, resourceArgs, omitIndexes(args, words)
}

// ghCommandWordIndexes returns the indexes in args of the first `want` COMMAND WORDS,
// skipping flags the way cobra's own command search does: a long flag consumes the NEXT
// token unless it is '='-glued or listed in ghNoValueLongFlags; a BARE short (`-R`,
// exactly two characters) consumes the next token; a short carrying its value in the same
// token (`-Ro/r`, `-R=o/r`) consumes nothing; and nothing after a `--` end-of-options
// terminator is a command word. A lone `-` is neither flag nor command word — cobra drops
// it from the search, and measured `gh - pr create` does resolve `gh pr create` — so it is
// skipped without consuming anything.
//
// The skip is applied at EVERY level, not just ahead of the resource, because a flag may
// sit INSIDE the path: measured, `gh pr --repo o/r create` creates a pull request. gh's
// per-level flag sets differ (`--version` is boolean on the root but value-taking one
// level down — measured), so using ONE table for all levels is an approximation. It errs
// only for a root-only boolean written at a deeper level, a spelling gh itself refuses as
// an unknown flag, so nothing can run whatever this answers.
//
// Indexing the bytes of a token is a deliberate look at ONE already-tokenized,
// already-unquoted argument, the same pg2-x9452 Guard 2 false positive cmdparse's
// HasShortFlag and parseGhAPICall record: no lexical or quoting decision is made here.
func ghCommandWordIndexes(args []string, want int) []int {
	out := make([]int, 0, want)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return out // end of options: no command word past it
		}
		if strings.HasPrefix(a, "--") {
			if name, _, glued := strings.Cut(a[2:], "="); !glued && !ghNoValueLongFlags[name] {
				i++ // `--repo o/r`: the next token is this flag's VALUE
			}
			continue
		}
		if len(a) == 2 && a[0] == '-' {
			i++ // `-R o/r`: a BARE short's value is the next token
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue // a cluster or a glued short value, or a lone `-`: one token
		}
		out = append(out, i)
		if len(out) >= want {
			return out
		}
	}
	return out
}

// omitIndexes returns args without the tokens at the ASCENDING indexes in drop. It is the
// single rule behind both of ghCommandPath's argv slices: a command WORD is removed and
// everything else — every flag, every operand, in order — is kept, which is the argv gh
// hands the command it resolved.
func omitIndexes(args []string, drop []int) []string {
	if len(drop) == 0 {
		return args
	}
	out := make([]string, 0, len(args)-len(drop))
	next := 0
	for i, a := range args {
		if next < len(drop) && drop[next] == i {
			next++
			continue
		}
		out = append(out, a)
	}
	return out
}

// There is no local hasFlag. It was an EXACT-TOKEN test, which is the wrong shape for
// every flag question this rule asks: it misses a short form (`-d` for `--draft`), a
// clustered short (`-dw`), and an `=`-glued value (`--draft=false`, `--auto=false` — the
// latter being an IMMEDIATE merge that must not reach the --auto Abstain). Flag matching
// now goes through cmdparse.HasShortFlag / cmdparse.HasLongFlag, with the arity and
// precedence answers those primitives push to their caller supplied in pr.go
// (ghFlagTokens over a measured per-subcommand ghFlagArity, plus lastLongFlag), and asked
// through the one boolFlagRequested every gated boolean shares.

// extractRunID returns the first positional (non-flag) argument after the
// "rerun" subcommand in a gh run rerun invocation. Returns "" if not found.
func extractRunID(args []string) string {
	// args layout: ["run", "rerun", ...rest]
	// Find "rerun" index and scan after it for first non-flag arg.
	rerunIdx := -1
	for i, a := range args {
		if a == "rerun" {
			rerunIdx = i
			break
		}
	}
	if rerunIdx < 0 {
		return ""
	}
	for _, a := range args[rerunIdx+1:] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		// Only return if all characters are digits (run IDs are numeric).
		allDigits := len(a) > 0
		for _, c := range a {
			if !unicode.IsDigit(c) {
				allDigits = false
				break
			}
		}
		if allDigits {
			return a
		}
	}
	return ""
}
