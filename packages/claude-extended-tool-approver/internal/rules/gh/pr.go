package gh

import (
	"strconv"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// DRAFT-FIRST PR LANDING (pg2-25oru; operator ruling 2026-07-30, pg2-4yy4r item 2)
//
// This workspace lands PR-driven repos DRAFT FIRST: the PR is opened as a draft and a
// PERSON marks it ready at the moment it becomes mergeable. Until pg2-25oru that flow
// rested entirely on a human SEEING A PROMPT — `gh pr create` was one undifferentiated
// Ask and `gh pr ready` was not gated at all (modifyingPR held only "create", so
// `ready` fell through to the final Abstain and emitted `{}`). In an auto-approving
// session the whole design was therefore inert: create (Ask, auto-accepted) -> `gh pr
// ready` (`{}`, auto-approved) -> `gh pr merge --auto` (`{}`, auto-approved) -> merged,
// with no person anywhere in it. The ruling makes the flow MECHANICAL instead.
//
// THE RULED TABLE, and what changed:
//
//	command                     before          ruled (pg2-25oru)   re-ruled (pg2-psiqh)
//	gh pr create --draft        Ask             Approve             Approve   (UNCHANGED)
//	gh pr create (no draft)     Ask             Reject              Reject    (UNCHANGED)
//	gh pr create --web          Ask             Approve             Approve   (UNCHANGED)
//	gh pr ready                 {} (ungated)    Ask                 Abstain
//	gh pr ready --undo          {} (ungated)    Approve             Approve   (UNCHANGED)
//	gh pr merge --auto          Abstain         Abstain             Abstain   (UNCHANGED)
//	gh pr merge (immediate)     Reject          Reject              Reject    (UNCHANGED)
//
// SUPERSEDED (operator ruling, Phillip, 2026-08-24, pg2-psiqh): the gh rule module carries
// no Ask verdict anywhere now, and `gh pr ready` is the only row this ruling touches — it
// moved Ask -> Abstain. The paragraph below is the ORIGINAL pg2-25oru reasoning for why
// Ask was required; it is kept for context but is NO LONGER THE LIVE DECISION. The operator
// was shown, explicitly, that Abstain here defers to Claude Code's own permission
// evaluation — auto-approved in an autonomous/headless session or a repo whose settings
// already allow the underlying Bash call, i.e. exactly the "create (Ask, auto-accepted) ->
// ready ({}, auto-approved) -> merge --auto ({}, auto-approved)" inertness this ruling
// originally existed to close — and chose Abstain anyway. See pg2-psiqh for the full
// record; a later reader MUST NOT re-derive the old "Ask is required" conclusion from the
// paragraph below without first checking whether pg2-psiqh has itself been superseded.
//
// WHY `gh pr ready` WAS RULED Ask (pg2-25oru, now superseded — see above). With non-draft
// creation rejected, `gh pr ready` is the SINGLE act that makes a PR mergeable, so it was
// ruled the one place a person must stand. It also REPAIRED the sibling `--auto` rationale
// in gh.go, which justified its Abstain with "a human un-drafts (the real gate)" while no
// such gate existed — that comment cites this enforcement rather than assuming it, and
// that citation is still accurate: `gh pr ready` is still the named gate, it now runs at
// Abstain rather than Ask.
//
// WEB — `gh pr create --web` / `-w` is Approve. The CLI does not create the PR: it
// opens the browser and the human picks draft-or-not in the GitHub UI, so it is
// human-in-the-loop by construction and gating it would spend a prompt on a step that
// already ends in a person. This row was NOT explicitly ruled (it was recorded in the
// bead as an open sub-question with Approve as the recorded default, and implemented
// that way); an operator who prefers it Rejected for uniformity changes only this
// branch. Measured, gh 2.97.0, 2026-08-12: `gh pr create -dw` and `-wd` both answer
// "the `--draft` flag is not supported with `--web`", so the draft+web combination
// cannot create anything either way and the branch order between them is inert.
//
// FLAG SPELLINGS ARE MEASURED, gh 2.97.0 (nixpkgs), 2026-08-12, by running each
// spelling OUTSIDE a git repository and reading whether it reached gh's own
// validation (spelling accepted) or failed at flag parsing (spelling rejected):
//
//	--draft        -> accepted   ("must provide `--title` and `--body` …")
//	-d             -> accepted   (a SHORT form exists: `-d, --draft`)
//	-dw / -wd      -> accepted, then refused as draft+web (see WEB above)
//	-dtx           -> accepted   (cluster: -d boolean, then -t with value "x")
//	-tdocs         -> accepted   (title "docs"; the `d` here is VALUE, not the flag)
//	--draft=false  -> accepted   (pflag bool; this is a NON-draft create)
//	--draft=0      -> accepted   (likewise false: strconv.ParseBool)
//	--draft=       -> REFUSED by gh itself: strconv.ParseBool "": invalid syntax
//	--draft=garbage-> REFUSED by gh itself: strconv.ParseBool "garbage"
//	--draf         -> REFUSED: `unknown flag: --draf`
//	--no-draft     -> REFUSED: `unknown flag: --no-draft`
//	-- --draft     -> accepted as parsing, then `unknown argument "--draft"`
//	gh pr new -d   -> accepted: `new` is a documented ALIAS of `create`
//
// The last two REFUSED rows are why no abbreviation or negation handling exists here
// and none should be added: gh is cobra/pflag, which matches long names EXACTLY (the
// same measurement api.go records for `--meth`). This is the one place `gh` differs
// from the git rule, whose matchers must cover git's unique-prefix abbreviations.
// Re-measure before adding prefix handling; today it would be dead code.
//
// THE PRE-SUBCOMMAND-FLAG BYPASS IS CLOSED (pg2-by1ij), and this gate depends on that.
// `gh --repo o/r pr create` is ACCEPTED by gh (measured the same day: it reached the same
// title/body validation), because cobra lets an inherited flag precede the command path.
// While Evaluate resolved resource/subcmd as pc.Args[0]/pc.Args[1] that spelling read as
// resource "--repo", matched no branch and reached the final Abstain, escaping this gate —
// and the LANDED `gh pr merge` Reject with it. pg2-by1ij replaced the positional read with
// ghCommandPath, which skips the flags cobra skips at every level of the path; see its doc
// and the measurement table above it in gh.go. The verdicts here are unchanged by that
// work: TestGH_GlobalFlagBeforeCommandPath pins each spelling to the SAME verdict as its
// plain form, so a regression in the extraction fails as a draft-first failure too.
//
// A SEPARATED VALUE THAT LOOKS LIKE A FLAG IS ALSO CLOSED (pg2-ylrda), and it is why arity
// is now MODELED here rather than only truncated. pg2-25oru handled a value GLUED to a short
// (`-tdocs`) by truncating the cluster, but not a value passed as its OWN argv token, so
// every gate below could be fed its own trigger word as somebody else's value:
//
//	gh pr create --title -d --body y   gh binds `-d` as the TITLE and creates a NON-DRAFT
//	                                   PR, yet a bare `-d` token was still in the args this
//	                                   file scanned -> APPROVE. A false Approve on exactly
//	                                   the command pg2-25oru made a Reject, and worse than
//	                                   the Abstain fall-through pg2-by1ij closed: an Abstain
//	                                   at least lands on a lower floor, whereas this ASSERTS
//	                                   the blessed verdict for the forbidden action.
//	gh pr create -t -d                 the same defect through the SHORT spelling.
//	gh pr ready -R --undo              `--undo` becomes the REPO and the PR is marked READY,
//	                                   yet the Abstain that is this flow's single human gate
//	                                   (Ask before pg2-psiqh) was answered with the `--undo`
//	                                   Approve.
//	gh pr merge -b --auto              `--auto` becomes the merge BODY and the merge is
//	                                   IMMEDIATE, yet the branch in gh.go took the --auto
//	                                   Abstain instead of its Reject.
//
// `gh issue create` is the one gated branch this class CANNOT reach: its verdict is a flat
// Abstain (Ask before pg2-psiqh) that reads no flag at all, so no value can move it.
// TestGH_SeparatedValue_IssueCreate pins that, so the day that branch becomes flag-aware
// the gap is already visible.
//
// MEASURED, gh 2.97.0 (nixpkgs), 2026-08-12, outside a git repository. Each binding is proven
// by a MUTUAL-EXCLUSION message gh emits only while BOTH tokens are still flags — "the
// `--draft` flag is not supported with `--web`" for `gh pr create`, and "specify only one of
// `--auto`, `--disable-auto`, or `--admin`" for `gh pr merge`. Its ABSENCE is the evidence
// that the token was swallowed as a value:
//
//	gh pr create --title -d --web         -> no draft conflict: `-d` is the TITLE
//	gh pr create -t -d --web              -> no draft conflict: likewise via the short
//	gh pr create --title x -d --web       -> DRAFT CONFLICT: a real -d after a consumed value
//	gh pr create -t -d --draft --web      -> DRAFT CONFLICT: `-t` ate `-d`, `--draft` is real
//	gh pr create --title=-d --web         -> no draft conflict: '='-glued binds in ONE token
//	gh pr create -td --web                -> no draft conflict: title "d", GLUED to the short
//	gh pr create -dt --web                -> `must provide --title and --body`: `-d` IS draft
//	                                         and the cluster's last letter ate `--web`
//	gh pr create --title -- --draft --web -> DRAFT CONFLICT: pflag hands `--` to the flag as
//	                                         its VALUE, so it is NOT a terminator there
//	gh pr create -t -- --draft --web      -> DRAFT CONFLICT: same for a bare short
//	gh pr create -- -d --web              -> `unknown arguments ["-d" "--web"]`: a REAL `--`
//	gh pr ready --repo / -R               -> `flag needs an argument: --repo` / `'R' in -R`
//	gh pr ready --repo --undo             -> that error is GONE: `--undo` is the repo value
//	gh pr merge -b --admin --disable-auto -> no auto conflict: `-b` ate `--admin`
//	gh pr merge -F --admin --disable-auto -> `open --admin: no such file or directory`
//	gh pr merge -d --admin --disable-auto -> AUTO CONFLICT: `-d` is boolean, ate nothing
//	gh issue create --title --web         -> `must provide --title and --body`: `--web` is
//	                                         the title, and the verdict is Abstain regardless
//	                                         (Ask before pg2-psiqh)

// FLAG ARITY FOR THE GATED `gh pr` SUBCOMMANDS (pg2-ylrda)
//
// Each table below enumerates the flags that consume NO value; EVERY OTHER flag — including
// one the table has never heard of — is treated as consuming the following argv token. That
// is the SAME polarity gh.go's ghNoValueLongFlags argues for, and it is restated here because
// the two error directions are again unequal, in the direction this bead is about:
//
//   - A value-taking flag MISSING from a value-taking list leaves its VALUE rescanned as a
//     flag, which is the false Approve above. Enumerating the value-taking side therefore
//     FAILS OPEN the moment gh adds a flag: `gh pr create --new-thing -d` would read as a
//     draft while gh titled nothing and created a mergeable PR.
//   - A no-value flag MISSING from a NO-VALUE list consumes one token too many, so a real
//     `-d` / `--undo` / `--auto` can be swallowed. That loses a POSITIVE signal, which for
//     every gate here moves the verdict toward the STRICTER side — Reject instead of the
//     draft Approve, Abstain instead of the `--undo` Approve (Ask before pg2-psiqh), Reject
//     instead of the `--auto` Abstain. Never past a gate.
//
// So an unknown flag defaults to value-taking on purpose, and a gh that grows a new BOOLEAN
// flag is the only change needing a re-measurement to stay ACCURATE — as opposed to safe.
type ghFlagArity struct {
	// longs are long-flag names WITHOUT the leading `--` that consume no value.
	longs map[string]bool
	// shorts are short-flag letters that consume no value. A nil map means the subcommand
	// has no boolean short at all, so every short letter is value-taking.
	shorts map[byte]bool
}

// prCreateArity is `gh pr create`'s (and its `new` alias's) no-value flag set, enumerated from
// the FLAGS and INHERITED FLAGS sections of `gh pr create --help` on gh 2.97.0 (nixpkgs) and
// CONFIRMED PER FLAG by running `gh pr create <flag> --draft --web` outside a git repository,
// 2026-08-12: gh answers "the `--draft` flag is not supported with `--web`" iff the `--draft`
// token is still a FLAG. Every entry below produced that answer. Every OTHER flag in the help
// swallowed the token instead, i.e. is value-taking: -a/--assignee, -B/--base, -b/--body,
// -F/--body-file, -H/--head, -l/--label, -m/--milestone, -p/--project, --recover,
// -r/--reviewer, -T/--template, -t/--title, and the inherited -R/--repo.
//
// RE-MEASURE, DO NOT RE-READ: the help's argument column is not sufficient on its own, and
// the probe must match gh's draft line EXACTLY. `--reviewer` was first mis-classified as
// boolean here because its own conflict message ("the `--reviewer` flag is not supported with
// `--web`") matched a looser test. WHAT TO RE-MEASURE when gh changes: any flag that moved
// INTO this set, which would rescan its value and restore the pg2-ylrda defect; and any NEW
// boolean flag, whose absence costs only accuracy (see the block above).
var prCreateArity = ghFlagArity{
	longs: map[string]bool{
		"draft": true, "dry-run": true, "editor": true, "fill": true,
		"fill-first": true, "fill-verbose": true, "no-maintainer-edit": true,
		"web": true, "help": true,
	},
	shorts: map[byte]bool{'d': true, 'e': true, 'f': true, 'w': true},
}

// prReadyArity is `gh pr ready`'s no-value flag set: its own `--undo` plus the inherited
// `--help`. Its ONLY value-taking flag is the inherited -R/--repo — measured 2026-08-12,
// `gh pr ready --repo` and `gh pr ready -R` answer pflag's `flag needs an argument: --repo`
// and `flag needs an argument: 'R' in -R`, while `gh pr ready --repo --undo` and
// `gh pr ready -R --undo` do NOT, so there the `--undo` token was consumed as the repo value.
//
// shorts is nil, and that is measured rather than forgotten: gh registers no shorthand for
// `--undo` or `--help`, so every short letter here is value-taking.
var prReadyArity = ghFlagArity{
	longs:  map[string]bool{"undo": true, "help": true},
	shorts: nil,
}

// prMergeArity is `gh pr merge`'s no-value flag set, enumerated from `gh pr merge --help` on
// gh 2.97.0 and CONFIRMED PER FLAG the same way against a DIFFERENT mutual exclusion, since
// `--auto` has no `--web` to clash with: gh answers "specify only one of `--auto`,
// `--disable-auto`, or `--admin`" iff two of that trio are still flags, so each candidate ran
// as `gh pr merge <flag> --admin --disable-auto`, 2026-08-12. -d/-m/-r/-s produced the
// conflict (boolean); -A/-b/-F/-t/-R and --body/--match-head-commit swallowed the `--admin`
// token instead, -F decisively (`open --admin: no such file or directory`).
//
// IT IS A SEPARATE TABLE FROM prCreateArity AND MUST STAY ONE: the letters collide with
// opposite arities. `-t` is `--subject` here and `--title` there (both value-taking), but
// `-m`/`-r` are BOOLEAN here (`--merge`, `--rebase`) and VALUE-TAKING on create
// (`--milestone`, `--reviewer`), while `-d` is boolean in both for different flags
// (`--delete-branch` vs `--draft`). One merged table would misread half of them.
var prMergeArity = ghFlagArity{
	longs: map[string]bool{
		"admin": true, "auto": true, "delete-branch": true, "disable-auto": true,
		"merge": true, "rebase": true, "squash": true, "help": true,
	},
	shorts: map[byte]bool{'d': true, 'm': true, 'r': true, 's': true},
}

// ghFlagTokens returns args reduced to the tokens that really are FLAGS of a subcommand with
// arity ar: a SEPARATED flag value is dropped, and a short-flag cluster is TRUNCATED at its
// first value-taking letter so a GLUED value goes with it. Long flags are returned VERBATIM,
// `=`-glued value and all, because lastLongFlag must still read that value.
//
// It is the SINGLE arity walk behind every flag question the `gh pr` branches ask, and it is
// the answer cmdparse.HasShortFlag and HasLongFlag each document pushing to their caller —
// the flag MATCHING stays in those primitives, asked about this slice. It REPLACES pg2-25oru's
// prCreateShortFlagTokens, which truncated clusters but modeled no separated value; see the
// pg2-ylrda block above for what that left open and for the measurement behind each form:
//
//	--title -d      the `-d` is the VALUE; the flag is kept, the value dropped
//	--title=-d      '='-glued: ONE token, so there is no separated value to drop
//	-t -d           a BARE short consumes the next token too
//	-dt -d          ... including the LAST letter of a cluster (`-dt` keeps `-d`)
//	-tdocs          a GLUED short value: truncate to `-`, consume nothing
//	--draft -d      a no-value long consumes nothing, so a real `-d` survives
//	--title --      pflag hands the `--` to the flag as its VALUE — NOT a terminator
//	-- -d           a real `--`: nothing after it is a flag, so the walk stops
//	-               a lone `-` is an operand and consumes nothing
func ghFlagTokens(args []string, ar ghFlagArity) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			// End of options: every remaining token is an operand. The terminator itself is
			// kept so HasShortFlag/HasLongFlag stop here for their own reasons too.
			return append(out, a)
		case strings.HasPrefix(a, "--"):
			out = append(out, a)
			if name, _, glued := strings.Cut(a[2:], "="); !glued && !ar.longs[name] && i+1 < len(args) {
				i++ // `--title -d`: the next token is this flag's VALUE
			}
		case len(a) > 1 && a[0] == '-':
			flags, separated := shortClusterFlags(a, ar.shorts)
			out = append(out, flags)
			if separated && i+1 < len(args) {
				i++ // `-t -d`: likewise
			}
		default:
			out = append(out, a) // an operand, or a lone `-`
		}
	}
	return out
}

// shortClusterFlags splits ONE short-flag cluster at its first letter that is NOT in noValue,
// returning the flag-letter prefix and whether that letter's value is the NEXT argv token.
//
// Everything after a value-taking letter is that option's VALUE, not more flag letters, so it
// is dropped: scanning it would let a value containing a `d` or a `w` manufacture a false
// DRAFT signal, and there the error direction is the unsafe one — measured, `gh pr create
// -tdocs` sets title "docs" and is NOT a draft. TRUNCATION IS LOSSLESS for what these rules
// ask, by the same reading git's pushShortFlagTokens/branchShortFlagTokens record: a letter
// BEFORE the truncation point survives, so `-dtx` is still read as the draft it is.
//
// An EMPTY remainder means the value is the next token (`-t -d`, `-dt -d`); a non-empty one
// means the value was glued (`-tdocs`, and pflag's `-t=docs`, whose `=` is part of the value).
//
// The byte loop is a deliberate look at ONE already-tokenized, already-unquoted argument — the
// same pg2-x9452 Guard 2 false positive cmdparse's HasShortFlag, parseGhAPICall and
// ghCommandWordIndexes each record; no lexical or quoting decision is made here.
func shortClusterFlags(tok string, noValue map[byte]bool) (flags string, separatedValue bool) {
	for j := 1; j < len(tok); j++ {
		if noValue[tok[j]] {
			continue
		}
		// tok[j] takes a value, so the cluster ends here — as it does for a letter this table
		// does not know, which is the fail-closed default the arity block above explains.
		return tok[:j], j == len(tok)-1
	}
	return tok, false
}

// lastLongFlag is cmdparse.HasLongFlag's LAST-ONE-WINS sibling. HasLongFlag returns the
// FIRST occurrence, while pflag resolves a repeated scalar/bool flag to the LAST one, so
// for `--draft --draft=false` the primitive alone reports the bare (true) spelling while
// gh really creates a NON-draft PR — an Approve where a Reject was meant, which is the
// unsafe direction.
//
// Only the PRECEDENCE is local: the match itself is delegated to the primitive, asked
// about one token at a time, so the two cannot disagree about what a long flag is or
// about the `=`-glued value. Scanning stops at a `--` end-of-options terminator, matching
// HasLongFlag's own rule.
func lastLongFlag(args []string, name string) (value string, ok bool) {
	for i, a := range args {
		if a == "--" {
			break // end of options; the rest are operands
		}
		if v, hit := cmdparse.HasLongFlag(args[i:i+1], name); hit {
			value, ok = v, true
		}
	}
	return value, ok
}

// boolFlagIsTrue reads the value of a pflag BOOLEAN long flag as pflag does: the bare
// form (empty value) is true, and any other value goes through strconv.ParseBool, so
// `--draft=false` and `--draft=0` are both false.
//
// An UNPARSEABLE value resolves to false, i.e. toward the more restrictive verdict, and
// that choice is INERT rather than merely safe: measured on gh 2.97.0, 2026-08-12, gh
// itself refuses `--draft=garbage` and `--draft=` with its own
// `strconv.ParseBool: invalid syntax`, so no PR is created whatever this rule answers.
// `--draft=` is indistinguishable from the bare `--draft` after cmdparse.HasLongFlag
// (both yield an empty value) and is read as true for the same reason: gh rejects it, so
// the reading cannot matter.
func boolFlagIsTrue(value string) bool {
	if value == "" {
		return true
	}
	b, err := strconv.ParseBool(value)
	return err == nil && b
}

// boolFlagRequested reports whether a pflag BOOLEAN flag is asked for in any spelling gh
// accepts and INDEPENDENTLY OF FLAG POSITION. long is the long name (without `--`); short is
// its one-letter form, or 0 when gh registers none (`--undo` and `--auto` have none).
//
// flags MUST already be arity-filtered by ghFlagTokens, with the arity table of the
// subcommand being judged: this helper asks cmdparse's primitives about the tokens it is
// given, and those primitives model no arity at all, so handing it a raw argv is the
// pg2-ylrda defect. Making the caller do the filtering is deliberate — one walk serves both
// questions `gh pr create` asks, and the arity table used is visible at the call site.
//
// The LONG form is authoritative when present, because it is the only form that can carry an
// explicit `=false`. Falling back to the short form only when no long spelling appears at all
// leaves ONE mixed spelling read differently from gh: `--draft=false -d` resolves to draft
// under pflag's last-one-wins but to NON-draft here, so it is Rejected. That is the safe
// direction and the spelling is pathological (it sets the same boolean twice, in opposite
// senses, via two different forms); a caller needing the exact pflag reading of a mixed
// spelling would have to model per-form ORDER, which no cmdparse primitive exposes.
//
// It replaces pg2-25oru's identical draftRequested/webRequested pair. The four gated booleans
// — create's --draft and --web, ready's --undo, merge's --auto — are now read by ONE function,
// so a precedence or arity fix cannot land on some of them and not the others, which is how
// the separated-value hole reached three branches at once.
func boolFlagRequested(flags []string, long string, short byte) bool {
	if v, ok := lastLongFlag(flags, long); ok {
		return boolFlagIsTrue(v)
	}
	return short != 0 && cmdparse.HasShortFlag(flags, short)
}

// prCreateVerdict returns the verdict for a `gh pr create` (or its `new` alias) — args
// being the tokens AFTER the subcommand.
//
// A DRAFT create is APPROVE, not Ask. It is the blessed landing step, it creates nothing
// mergeable, and the ruling's whole purpose is to stop a person being prompted for the
// step that is always correct while nothing gates the step that is not.
//
// A NON-DRAFT create is REJECT, not Ask. Ask is what this replaces and it was measurably
// inert: in an auto-approving session Ask is auto-accepted, so the prompt the draft-first
// flow assumed never appeared. Reject is not user-overridable in-session, which is the
// intended cost and not an oversight — the capability is preserved by the two-step
// `gh pr create --draft` then `gh pr ready` (which no longer necessarily prompts —
// pg2-psiqh moved `gh pr ready` to Abstain, see prReadyVerdict — but still requires the
// separate, explicit un-drafting call), so the only thing removed is creating a MERGEABLE
// PR in one un-prompted call. It is deliberately NOT the weaker Abstain either: Abstain
// hands the verdict back to Claude Code, which auto-approves it in exactly the
// auto-approving sessions this exists for.
//
// KNOWN CONSERVATIVE EDGE: `--dry-run` prints the PR instead of creating it, so a
// non-draft `--dry-run` is Rejected while creating nothing. It is not carved out because
// the ruled table does not carve it out and a carve-out is a verdict, not a tidy-up.
// What would justify one: observed friction on that spelling in the decision log, plus
// an operator ruling.
//
// WHAT WOULD JUSTIFY CHANGING ANY OF IT: only a new operator ruling on the draft-first
// flow. pg2-25oru originally required this Reject, the `gh pr ready` gate and the
// `gh pr merge --auto` Abstain to move together; pg2-psiqh (2026-08-24) deliberately moved
// ONLY the middle one (Ask -> Abstain) and left this Reject and the merge --auto Abstain
// untouched, so "all three MUST move together" is no longer the live constraint — record
// any FURTHER change to this Reject as its own ruling rather than assuming it still must
// travel with `gh pr ready`.
func (r *Rule) prCreateVerdict(args []string) hookio.RuleResult {
	// One arity walk, both questions: a token consumed as some other flag's VALUE is neither
	// a draft nor a web request, however much it looks like one (pg2-ylrda).
	flags := ghFlagTokens(args, prCreateArity)
	if boolFlagRequested(flags, "draft", 'd') {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "gh pr create --draft: the blessed draft-first landing step (creates nothing mergeable)",
			Module:   r.Name(),
		}
	}
	if boolFlagRequested(flags, "web", 'w') {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "gh pr create --web: the browser opens and a human chooses draft-or-not, so the PR is not created by this call",
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{
		Decision: hookio.Reject,
		Reason: "gh pr create without --draft is prohibited: this workspace lands DRAFT FIRST, and a" +
			" non-draft PR is immediately mergeable, so creating one skips the single point at which a" +
			" person rules on that (operator ruling pg2-4yy4r item 2). Use the two-step" +
			" `gh pr create --draft` and then `gh pr ready`; `--web` is also allowed," +
			" since there a human picks draft-or-not in the browser.",
		Module: r.Name(),
	}
}

// prReadyVerdict returns the verdict for a `gh pr ready` — args being the tokens AFTER
// the subcommand.
//
// MARKING READY WAS RULED Ask BY pg2-25oru, SUPERSEDED BY pg2-psiqh — operator ruling,
// Phillip, 2026-08-24: it is now ABSTAIN. With non-draft creation rejected this is the
// SINGLE act that makes a PR mergeable, so pg2-25oru made it the one place a person must
// stand; left ungated (its state before pg2-25oru) the entire draft-first design was
// inert, because the rest of the chain — `gh pr ready` then `gh pr merge --auto` — is
// auto-approved end to end. THAT IS THE EXACT CONSEQUENCE pg2-psiqh re-creates: Abstain
// returns the verdict to Claude Code, which auto-approves in an autonomous/headless
// session or a repo whose settings already allow the underlying Bash call. The operator
// was shown this tradeoff in those terms, explicitly, before ruling — this is not a
// rediscovery of the old defect, it is a deliberate re-acceptance of it. See pg2-psiqh for
// the full record.
//
// Reject was and remains available as the alternative floor (un-drafting is legitimate,
// routine, and REQUIRED by the flow — the second half of the remedy prCreateVerdict names —
// so prohibiting it removes the capability rather than gating it) but was NOT the ruled
// choice; the operator chose Abstain over Reject with the consequence above already
// stated.
//
// `--undo` (back to draft) is still APPROVE, UNCHANGED by pg2-psiqh: it moves the PR AWAY
// from mergeable, which is the direction the flow wants, and it is the documented repair
// when a PR came back non-draft. `--undo=false` is a mark-ready and so gets the same
// Abstain as the plain form. So is a `--undo` that gh swallowed as the value of
// `-R`/`--repo`, which is a MARK-READY and was reaching this Approve before pg2-ylrda — the
// arity filter is what tells the two apart.
//
// WHAT WOULD JUSTIFY CHANGING IT: a new operator ruling superseding pg2-psiqh. A reader
// finding this Abstain surprising should read pg2-psiqh first — it is the CURRENT decision,
// not a regression from the Ask this replaced.
func (r *Rule) prReadyVerdict(args []string) hookio.RuleResult {
	if boolFlagRequested(ghFlagTokens(args, prReadyArity), "undo", 0) {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "gh pr ready --undo converts the PR back to draft, away from mergeable",
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{
		Decision: hookio.NoOpinion,
		Reason: "gh pr ready makes the pull request MERGEABLE — with non-draft creation rejected it was" +
			" the single point at which a person ruled on that (operator ruling pg2-4yy4r item 2), until" +
			" operator ruling pg2-psiqh (2026-08-24) removed every Ask verdict from the gh rule module" +
			" and chose Abstain here with that consequence already stated. `gh pr ready --undo` (back to" +
			" draft) is auto-approved.",
		Module: r.Name(),
	}
}
