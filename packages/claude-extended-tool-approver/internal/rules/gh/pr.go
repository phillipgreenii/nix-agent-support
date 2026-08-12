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
//	command                     before          ruled
//	gh pr create --draft        Ask             Approve
//	gh pr create (no draft)     Ask             Reject
//	gh pr create --web          Ask             Approve   (see WEB below)
//	gh pr ready                 {} (ungated)    Ask
//	gh pr ready --undo          {} (ungated)    Approve
//	gh pr merge --auto          Abstain         Abstain   (UNCHANGED)
//	gh pr merge (immediate)     Reject          Reject    (UNCHANGED)
//
// WHY `gh pr ready` -> Ask IS REQUIRED AND NOT OPTIONAL. With non-draft creation
// rejected, `gh pr ready` is the SINGLE act that makes a PR mergeable, so it is the one
// place a person must stand. It also REPAIRS the sibling `--auto` rationale in gh.go,
// which justified its Abstain with "a human un-drafts (the real gate)" while no such
// gate existed — that comment now cites this enforcement instead of assuming it.
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
// KNOWN RESIDUAL BYPASS, not closed here. `gh --repo o/r pr create` is ACCEPTED by gh
// (measured the same day: it reached the same title/body validation), because cobra
// lets an inherited flag precede the command path. Evaluate resolves resource/subcmd as
// pc.Args[0]/pc.Args[1], so that spelling reads as resource "--repo", matches no branch
// and reaches the final Abstain — escaping this gate. It escapes the LANDED `gh pr
// merge` Reject the same way, so it is a pre-existing property of the rule's
// resource resolution rather than of this gate, and closing it means re-deriving
// resource/subcmd for EVERY branch (including apiVerdict's args). That is its own
// reviewable change, not a tidy-up. What would justify making it one: any observed
// `gh <pre-subcommand-flag> pr …` invocation in the decision log.

// prCreateValueShorts are the `gh pr create` short flags that CONSUME A VALUE, read off
// `gh pr create --help` on gh 2.97.0 (2026-08-12): -a/--assignee, -B/--base, -b/--body,
// -F/--body-file, -H/--head, -l/--label, -m/--milestone, -p/--project, -r/--reviewer,
// -T/--template, -t/--title, plus the inherited -R/--repo.
//
// The BOOLEAN shorts — -d/--draft, -e/--editor, -f/--fill, -w/--web — are deliberately
// absent: those are the letters this rule asks about, and a boolean short carries no
// value for a letter to hide in.
const prCreateValueShorts = "aBbFHlmprTtR"

// prCreateShortFlagTokens returns args with every short-flag cluster TRUNCATED at its
// first value-taking letter (prCreateValueShorts). Everything after that letter is the
// option's VALUE, not more flag letters, so scanning it would let a value that happens
// to contain a `d` or a `w` manufacture a false DRAFT signal — and here that error
// direction is the unsafe one: it would APPROVE a non-draft create. Measured,
// gh 2.97.0, 2026-08-12: `gh pr create -tdocs` sets title "docs" and is NOT a draft,
// yet an arity-blind letter scan sees the `d`.
//
// TRUNCATION IS LOSSLESS for what this rule asks, by the same reading git's
// pushShortFlagTokens/branchShortFlagTokens record: a `d` or `w` AFTER a value-taking
// letter is part of that letter's value, so nothing that really requests draft or web
// is dropped, while `-dtx` and `-wt"title"` carry their letter BEFORE the truncation
// point and survive (measured: `-dtx` is draft plus title "x").
//
// cmdparse.HasShortFlag documents that it models no arity and pushes exactly this
// question to its caller; this is the answer for `gh pr create`, and the actual flag
// MATCHING stays in the primitive. An UNRECOGNISED letter is treated as boolean and
// scanning continues, which for a newer gh that adds a value-taking short this table
// does not know could scan that value — so a spelling using one MUST be re-measured
// when the table is refreshed.
//
// `--`, long flags and a lone `-` are returned untouched so HasShortFlag's own
// end-of-options and operand handling still applies.
func prCreateShortFlagTokens(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if len(a) > 1 && a[0] == '-' && a[1] != '-' {
			if v := strings.IndexAny(a, prCreateValueShorts); v > 0 {
				a = a[:v] // drop the value-taking letter and everything after it
			}
		}
		out[i] = a
	}
	return out
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

// draftRequested reports whether a `gh pr create` argv asks for a DRAFT pull request,
// in any spelling gh accepts and INDEPENDENTLY OF FLAG POSITION.
//
// The long form is authoritative when present, because it is the only form that can
// carry an explicit `=false`. Falling back to the short form only when no `--draft`
// appears at all leaves ONE mixed spelling read differently from gh: `--draft=false -d`
// resolves to draft under pflag's last-one-wins but to NON-draft here, so it is
// Rejected. That is the safe direction and the spelling is pathological (it sets the
// same boolean twice, in opposite senses, via two different forms); a caller who needs
// the exact pflag reading of a mixed spelling would have to model per-form ORDER, which
// no cmdparse primitive exposes.
func draftRequested(args []string) bool {
	if v, ok := lastLongFlag(args, "draft"); ok {
		return boolFlagIsTrue(v)
	}
	return cmdparse.HasShortFlag(prCreateShortFlagTokens(args), 'd')
}

// webRequested reports whether a `gh pr create` argv defers creation to the BROWSER
// (`--web` / `-w`), read exactly as draftRequested reads draft — see its doc for the
// long-form precedence and the one mixed spelling that differs from pflag.
func webRequested(args []string) bool {
	if v, ok := lastLongFlag(args, "web"); ok {
		return boolFlagIsTrue(v)
	}
	return cmdparse.HasShortFlag(prCreateShortFlagTokens(args), 'w')
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
// `gh pr create --draft` then `gh pr ready` (which prompts), so the only thing removed
// is creating a MERGEABLE PR in one un-prompted call. It is deliberately NOT the weaker
// Abstain either: Abstain hands the verdict back to Claude Code, which auto-approves it
// in exactly the auto-approving sessions this exists for.
//
// KNOWN CONSERVATIVE EDGE: `--dry-run` prints the PR instead of creating it, so a
// non-draft `--dry-run` is Rejected while creating nothing. It is not carved out because
// the ruled table does not carve it out and a carve-out is a verdict, not a tidy-up.
// What would justify one: observed friction on that spelling in the decision log, plus
// an operator ruling.
//
// WHAT WOULD JUSTIFY CHANGING ANY OF IT: only a new operator ruling on the draft-first
// flow — the same ruling that governs the `gh pr ready` gate and the `gh pr merge
// --auto` Abstain below, so all three MUST move together (an un-drafted PR plus
// auto-merge is the merge this rule set exists to keep a person in front of).
func (r *Rule) prCreateVerdict(args []string) hookio.RuleResult {
	if draftRequested(args) {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "gh pr create --draft: the blessed draft-first landing step (creates nothing mergeable)",
			Module:   r.Name(),
		}
	}
	if webRequested(args) {
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
			" `gh pr create --draft` and then `gh pr ready`, which prompts; `--web` is also allowed," +
			" since there a human picks draft-or-not in the browser.",
		Module: r.Name(),
	}
}

// prReadyVerdict returns the verdict for a `gh pr ready` — args being the tokens AFTER
// the subcommand.
//
// MARKING READY is ASK. With non-draft creation rejected this is the SINGLE act that
// makes a PR mergeable, so it is the one place a person must stand; left ungated (its
// state before pg2-25oru) the entire draft-first design is inert, because the rest of
// the chain — `gh pr ready` then `gh pr merge --auto` — is auto-approved end to end.
//
// Ask and NOT Reject: un-drafting is a legitimate, routine act that the flow REQUIRES
// (it is the second half of the remedy prCreateVerdict names), so prohibiting it would
// remove the capability rather than gate it. Ask and NOT Abstain: Abstain returns the
// verdict to Claude Code, which auto-approves in an auto-approving session — that is
// precisely the state this replaces, so Abstain here would re-create the defect.
//
// `--undo` (back to draft) is APPROVE: it moves the PR AWAY from mergeable, which is the
// direction the flow wants, and it is the documented repair when a PR came back
// non-draft. `--undo=false` is a mark-ready and so gets the Ask.
//
// WHAT WOULD JUSTIFY CHANGING IT: a new operator ruling on the draft-first flow, or
// evidence that Ask is unreachable in practice (e.g. a session mode in which Ask is
// auto-accepted for this rule) — in which case the answer is Reject with a named
// alternative path, never Abstain.
func (r *Rule) prReadyVerdict(args []string) hookio.RuleResult {
	if v, ok := lastLongFlag(args, "undo"); ok && boolFlagIsTrue(v) {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "gh pr ready --undo converts the PR back to draft, away from mergeable",
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{
		Decision: hookio.Ask,
		Reason: "gh pr ready makes the pull request MERGEABLE — with non-draft creation rejected it is" +
			" the single point at which a person rules on that (operator ruling pg2-4yy4r item 2)." +
			" `gh pr ready --undo` (back to draft) is auto-approved.",
		Module: r.Name(),
	}
}
