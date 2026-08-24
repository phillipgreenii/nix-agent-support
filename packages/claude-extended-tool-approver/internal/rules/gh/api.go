package gh

import (
	"strconv"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// `gh api` MUTATION DETECTION (pg2-cl0v2)
//
// `gh api` is a general-purpose REST/GraphQL client, so its HTTP METHOD — not the
// subcommand name — decides whether an invocation reads or writes. Until pg2-cl0v2
// the rule approved every `gh api` as "read-only gh api" without looking at the
// method, which made two deliberate sibling controls in Evaluate decorative:
// measured `allow` on 2026-07-30 for `gh api --method PUT repos/o/r/pulls/5/merge`
// (the operation `gh pr merge` REJECTS) and for `gh api -X POST repos/o/r/pulls
// -f title=x` (the operation `gh pr create` gated at Ask on that date; since
// pg2-25oru the porcelain is draft-aware — Approve with `--draft`, Reject without).
//
// SCANNER CLASS — everything here is TOKEN-LEVEL and POST-unquote: it consumes
// cmdparse.ParsedCommand.Args, never raw command text, and holds no quote state.
// That is the pg2-5b901 failure mode this deliberately avoids — a `--method PUT`
// inside a commit message, a heredoc, or a `bd comment` body is TEXT and can never
// reach this code, because Evaluate runs only when isGhExecutable(pc.Executable)
// and the first arg is `api`.
//
// WHY A gh-SPECIFIC WALK AND NOT cmdparse's PRIMITIVES ALONE. This walk needs
// three things the primitives deliberately do not give (each documents pushing the
// question to its caller):
//
//  1. FLAG ARITY. `gh api`'s value-taking shorts are -F -H -q -X -p -f -t; only -i
//     is boolean. cmdparse.HasShortFlag knows no arity, so it would scan a VALUE as
//     more flag letters — measured, `gh api -H 'X-Foo: PUT' repos/o/r` is a GET, yet
//     a letter-scan sees an `X` and would manufacture a false gate. This is the same
//     false-positive class git.pushShortFlagTokens solves for `git push -o`; the
//     difference is that -X's VALUE is the answer here, so the cluster is walked
//     rather than truncated.
//  2. A SEPARATED value. cmdparse.HasLongFlag/HasShortFlag return only the glued
//     form, and `-X PUT` / `--method PUT` are the spellings the bead probed.
//  3. THE ENDPOINT OPERAND. cmdparse.FirstOperand documents that it does not skip
//     separated flag values, so for `-X PUT repos/o/r/...` it returns "PUT". Only an
//     arity-aware walk finds the endpoint, and the endpoint is what routes the merge
//     Reject below.
//
// MEASURED SPELLINGS, gh 2.96.0 (nixpkgs), 2026-07-30, via
// `gh api --hostname no-such-host.invalid --verbose <spelling> <endpoint>` and
// reading the dumped request line (the dump precedes the DNS failure, so nothing
// was ever sent):
//
//	-X PATCH repos/o/r/pulls/5 -f draft=false  -> PATCH
//	--method PUT repos/o/r/pulls/5/merge       -> PUT
//	-XPUT      repos/o/r/pulls/5/merge         -> PUT   (value glued to the short)
//	-X=PUT     repos/o/r/pulls/5/merge         -> PUT   (pflag's '='-glued short)
//	-iXPUT     repos/o/r/pulls/5/merge         -> PUT   (bool short clustered ahead)
//	-iX PUT    repos/o/r/pulls/5/merge         -> PUT   (cluster + separated value)
//	--method=PUT repos/o/r/pulls/5/merge       -> PUT
//	-X get     repos/o/r/pulls                 -> GET   (gh UPPER-CASES the method)
//	-X post    repos/o/r/pulls                 -> POST  (likewise)
//	repos/o/r/pulls/5                          -> GET   (no signal at all)
//	-f  title=x  repos/o/r/pulls               -> POST  (no -X anywhere)
//	-F  body=x   repos/o/r/issues              -> POST
//	--field body=x     repos/o/r/issues        -> POST
//	--raw-field body=x repos/o/r/issues        -> POST
//	--input /dev/null  repos/o/r/pulls         -> POST
//	graphql -f 'query=query{viewer{login}}'    -> POST  (every GraphQL call is a POST)
//	-X GET repos/o/r/pulls -f state=open       -> GET   (explicit -X BEATS the -f default)
//	-H 'X-Foo: PUT' repos/o/r                  -> GET   (a VALUE containing X is not the method)
//
// LONG-FLAG ABBREVIATIONS ARE NOT ACCEPTED — MEASURED, so no abbreviation handling
// exists here and none should be added. `gh api --meth=PUT repos/o/r/pulls/5` fails
// with `unknown flag: --meth` (as does `--methodPUT`), because gh is
// cobra/pflag, which matches long names EXACTLY. This is the one place `gh` differs
// from the git rule, whose hasPushLongFlag must enumerate prefixes because git's
// parse-options accepts any unambiguous prefix. Re-measure before adding prefix
// handling; today it would be dead code.

// apiVerdict returns the verdict for a `gh api` — args being the tokens AFTER the
// `api` subcommand.
//
// THE VERDICTS, AND WHY THESE (pg2-cl0v2, 2026-07-30).
//
// READ (an effective GET/HEAD/OPTIONS) stays APPROVE. That is the whole point of
// the branch and the reason a blanket removal would be wrong: `gh api` is the
// normal way to read the API, it is in constant use, and demoting it would spend a
// prompt on every read.
//
// A PULL-REQUEST MERGE is REJECT. `gh api --method PUT repos/o/r/pulls/5/merge` is
// not merely "a mutation" — it is BIT-FOR-BIT the operation the sibling branch in
// Evaluate rejects as "gh pr merge (immediate) is prohibited: it merges now,
// bypassing the draft-first landing flow". A weaker verdict here would leave that
// Reject decorative and would TEACH ITS BYPASS: told `gh pr merge` is prohibited,
// an agent retries through `gh api`. So this is PATH-AWARE deliberately — a
// path-blind Ask, though it closes the blanket approval, would still leave the
// merge one notch below its own control, which is the defect restated.
//
// EVERY OTHER MUTATION WAS ASK — SUPERSEDED (operator ruling, Phillip, 2026-08-24,
// pg2-psiqh): it is now ABSTAIN. The paragraph below is the ORIGINAL pg2-cl0v2 reasoning
// for why Ask, kept for context; it is NOT the live decision — a later reader MUST NOT
// re-derive "generic mutations Ask" from it. The operator was shown, explicitly, that
// Abstain here defers to Claude Code's own permission evaluation — auto-approved in an
// autonomous/headless session or a repo whose settings already allow the underlying Bash
// call, which is bit-for-bit the `gh api -X POST repos/o/r/pulls -f title=x` bypass
// pg2-cl0v2 was written to close — and chose Abstain anyway, for the entire unclassified
// REST/GraphQL write surface. See pg2-psiqh for the full record; it also names what is
// UNCHANGED — the merge Reject above and the PR-create Approve/Reject split below are
// Ask sites, so pg2-psiqh does not touch them.
//
// [pg2-cl0v2's original reasoning, superseded above:] Ask and not Approve because `gh
// api` can perform any write the token permits, and the blanket Approve was already
// measured bypassing the Ask that `gh pr create` carried at the time (`gh api -X POST
// repos/o/r/pulls -f title=x` measured `allow`). Ask and not Reject because `gh api` has
// no single operation to rule on: it is the whole REST surface, most of it unremarkable,
// and a Reject would be a blanket prohibition on writing to GitHub that no operator
// ruling covers. Ask landed each write in front of the person who could judge it, and
// matched the verdict the equivalent porcelain (`gh issue create`) carried at the time —
// which is ALSO now Abstain, by the same pg2-psiqh ruling (see gh.go's modifyingIssue
// branch), so the two remain consistent with each other.
//
// PR CREATION MIRRORS THE PORCELAIN (pg2-h8h3f), which closes the divergence pg2-25oru
// recorded here. That divergence was NOT a ruling: `POST .../pulls` sat at the (former)
// Ask purely because parseGhAPICall read GitHub's `draft` as a PRESENCE boolean and could
// not tell `-f draft=true` from `-f draft=false`, so following the porcelain to Reject
// would also have refused the BLESSED create with no in-session override. The
// body-parameter reader below supplies the VALUE, so apiPullRequestCreateVerdict now
// answers Approve / Reject exactly as prCreateVerdict does — with ONE residual, stated
// there and not papered over: an `--input` body (or `-F draft=@file`) keeps the value
// outside argv, and that case holds the generic mutation floor, now Abstain (pg2-psiqh).
//
// GRAPHQL READS ARE APPROVED (pg2-44dsd). Every GraphQL call is a POST — measured
// below — so the method reading alone Asked on a read-only query; graphql.go's corpus
// measurement sized that at 378 of 576 logged `gh api graphql` invocations. A document
// that is argv-visible AND scans as query-only Approves; anything else, including a
// `createPullRequest` mutation, holds the generic mutation floor, now Abstain
// (pg2-psiqh; was Ask). See apiGraphQLVerdict.
//
// WHAT WOULD JUSTIFY CHANGING WHAT IS LEFT: for the merge Reject, only a new operator
// ruling on immediate merges — the same ruling that governs the `gh pr merge` branch it
// mirrors, so the two MUST move together; pg2-psiqh did not touch it. For the generic
// Abstain floor, only a new operator ruling superseding pg2-psiqh — narrowing a specific
// endpoint class by MEASURING it no longer buys anything at this floor's level, since
// Abstain and a measured-safe Approve differ only in whether Claude Code's own settings
// happen to have a rule for it; narrowing would still be worth doing to make the verdict
// deliberate rather than incidental, per pg2-psiqh's own bead. Widening the merge Reject
// to a broader endpoint set (`/merges`, `merge-upstream`, a `graphql` mergePullRequest
// mutation) — see IsPullRequestMerge and graphqlPullRequestCreateFields for why those
// hold the generic floor today.
func (r *Rule) apiVerdict(args []string) hookio.RuleResult {
	call := parseGhAPICall(args)
	if !call.IsMutating() {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "read-only gh api (" + call.methodLabel() + ")",
			Module:   r.Name(),
		}
	}
	if call.IsPullRequestMerge() {
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason: "gh: gh api " + call.methodLabel() + " " + call.Endpoint +
				" merges a pull request NOW, bypassing the draft-first landing flow — the same" +
				" operation `gh pr merge` (immediate) is prohibited for (pg2-cl0v2). Reaching it" +
				" through `gh api` is not a different operation. Open/keep the PR as draft and use" +
				" `gh pr merge --auto`, or merge via the WORKSPACE landing flow.",
			Module: r.Name(),
		}
	}
	if call.Endpoint == "graphql" {
		return r.apiGraphQLVerdict(call)
	}
	if call.IsPullRequestCreate() {
		return r.apiPullRequestCreateVerdict(call)
	}
	return r.apiMutationAbstain(call)
}

// apiMutationAbstain is the conservative floor — the verdict every mutation this rule has
// nothing more specific to say about receives. It is a function rather than an inline
// literal because THREE sites return it (the generic fall-through, the GraphQL branch and
// the unreadable-draft case), and a floor that is copied is a floor that drifts: the point
// of the design is that these all land on the SAME level.
//
// WAS apiMutationAsk, RETURNING Ask, UNTIL OPERATOR RULING pg2-psiqh (2026-08-24): the gh
// rule module carries no Ask verdict anywhere now, so this floor is Abstain. The rename is
// deliberate — the old name would silently lie about what the function returns. Abstain
// defers to Claude Code's own permission evaluation, auto-approved in an
// autonomous/headless session or a repo whose settings already allow the underlying Bash
// call; that consequence was shown to the operator explicitly before the ruling. See
// pg2-psiqh for the full record, and pg2-cl0v2 for why this floor exists at all (closing
// the blanket "read-only gh api" approval that ignored HTTP method).
func (r *Rule) apiMutationAbstain(call ghAPICall) hookio.RuleResult {
	return hookio.RuleResult{
		Decision: hookio.NoOpinion,
		Reason: "gh: gh api with a mutating HTTP method (" + call.methodLabel() +
			") — `gh api` is a general-purpose REST/GraphQL client, so this writes to GitHub." +
			" Not gated beyond this (operator ruling pg2-psiqh, 2026-08-24; originally Ask" +
			" under pg2-cl0v2). Only a read-only gh api is auto-approved.",
		Module: r.Name(),
	}
}

// apiGraphQLVerdict returns the verdict for `gh api graphql` (pg2-44dsd, pg2-h8h3f,
// pg2-psiqh).
//
// A READ-ONLY DOCUMENT IS APPROVE. Every GraphQL call is an HTTP POST — measured — so
// pg2-cl0v2's method reading alone put a read-only query at the mutation floor. The corpus
// measurement in graphql.go's doc block sized that at 378 of 576 logged `gh api graphql`
// invocations, 66%, and it is the whole reason this branch exists. The document must be
// ARGV-VISIBLE and must SCAN: a `-F query=@file` or an `--input` body is not in argv at all
// and cannot be shown to read, so it keeps the mutation floor (fail-safe, and the bead's
// own requirement).
//
// A PULL-REQUEST-CREATING MUTATION IS A PINNED VERDICT, AND THE PIN IS CHECKED FIRST.
// `createPullRequest` creates a PR exactly as `POST /repos/o/r/pulls` does, so it belongs to
// the draft-first design and must never be swept into the read Approve above by a
// classifier bug — testing it BEFORE the Kind is what makes that structural rather than
// hopeful. TestGH_ApiGraphQLCreatePullRequest_Pinned holds the level.
//
// THE PIN IS NOW ABSTAIN, NOT ASK — SUPERSEDED (operator ruling, Phillip, 2026-08-24,
// pg2-psiqh). The three paragraphs below are the ORIGINAL pg2-h8h3f reasoning for why Ask
// (and specifically not Abstain); they are kept for context but are NOT the live decision.
// The operator was shown Abstain's consequence explicitly — auto-approved in an
// autonomous/headless session or a repo whose settings already allow the underlying Bash
// call, i.e. losing the human checkpoint this pin exists to guarantee — and chose Abstain
// anyway, for the entire gh rule module. See pg2-psiqh for the full record.
//
// [pg2-h8h3f's original reasoning, superseded above:] WHY ASK AND NOT REJECT, which is the
// level the porcelain would suggest. The GraphQL draft argument lives INSIDE the document
// (`createPullRequest(input: {draft: true})`) and routinely arrives through a variable
// (`draft: $isDraft`) whose value is a separate `-f variables=<json>` blob. So unlike the
// REST path below there is no argv-visible VALUE to sort Approve from Reject on, and a
// blanket Reject would refuse the legitimate draft-creating mutation with no in-session
// override — the same objection that stopped pg2-25oru from following the porcelain here.
// Ask kept the capability and still put a person in front of it.
//
// WHY ASK AND NOT ABSTAIN (superseded — pg2-psiqh chose Abstain with this exact tradeoff
// named). Abstain returns the verdict to Claude Code, which auto-approves it in exactly
// the auto-approving sessions the draft-first gate exists for.
//
// WHAT WOULD JUSTIFY REJECT: a draft-argument reader for the GraphQL document AND its
// variables blob, so `draft: true` could be told from `draft: false` — plus the same
// operator ruling that governs prCreateVerdict, since the two would then have to move
// together. Measured in graphql.go's corpus block: zero `createPullRequest` rows, so there
// is no friction evidence to build it on today.
func (r *Rule) apiGraphQLVerdict(call ghAPICall) hookio.RuleResult {
	// A doc nobody could classify stays the zero value, graphqlOpaque — see graphql.go.
	var doc graphqlDoc
	if raw, state := call.bodyParam("query"); state == bodyParamValue {
		doc = classifyGraphQLDocument(raw)
	}

	if doc.CreatesPullRequest() {
		return hookio.RuleResult{
			Decision: hookio.NoOpinion,
			Reason: "gh: gh api graphql carries a `createPullRequest` mutation, which CREATES a" +
				" pull request just as `gh api -X POST .../pulls` does (pg2-h8h3f). Its draft" +
				" argument lives in the GraphQL document, often behind a variable, so it cannot be" +
				" read from argv and this call cannot be shown to be the blessed DRAFT create." +
				" Not gated beyond this (operator ruling pg2-psiqh, 2026-08-24; originally Ask" +
				" under pg2-h8h3f). Prefer `gh pr create --draft`, which is auto-approved.",
			Module: r.Name(),
		}
	}
	if doc.Kind == graphqlRead {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason: "read-only gh api graphql: every operation in the argv-visible document is a" +
				" query, so the POST reads (pg2-44dsd)",
			Module: r.Name(),
		}
	}
	if doc.Kind == graphqlWrite {
		return hookio.RuleResult{
			Decision: hookio.NoOpinion,
			Reason: "gh: gh api graphql carries a GraphQL mutation, so it writes to GitHub. Not" +
				" gated beyond this (operator ruling pg2-psiqh, 2026-08-24; originally Ask under" +
				" pg2-cl0v2). Only a read-only GraphQL document is auto-approved.",
			Module: r.Name(),
		}
	}
	return hookio.RuleResult{
		Decision: hookio.NoOpinion,
		Reason: "gh: gh api graphql whose document is not readable from the command line — it came" +
			" from a file or stdin (`-F query=@file`, `--input`) or did not scan as GraphQL — so it" +
			" cannot be shown to be a read (pg2-44dsd fail-safe). Not gated beyond this (operator" +
			" ruling pg2-psiqh, 2026-08-24; originally Ask). Pass the document as `-f query=…` to" +
			" have a read-only query auto-approved.",
		Module: r.Name(),
	}
}

// apiPullRequestCreateVerdict returns the verdict for `POST /repos/{owner}/{repo}/pulls`,
// the raw-API pull-request create (pg2-h8h3f).
//
// IT MIRRORS prCreateVerdict, WHICH IS THE POINT. pg2-25oru made the porcelain
// `gh pr create` draft-aware — Approve with `--draft`, Reject without — but deliberately
// left this path at the pg2-cl0v2 Ask, because parseGhAPICall then read GitHub's `draft`
// only as a PRESENCE boolean and so could not tell `-f draft=true` from `-f draft=false`.
// A Reject on that reading would have refused the BLESSED create with no in-session
// override. The body-parameter reader above supplies the missing VALUE, so the two
// spellings of one operation now carry one verdict and neither is the other's bypass.
//
// THE THREE-WAY SPLIT IS THE READER'S THREE STATES, and the middle one is the residual
// pg2-25oru named:
//
//   - draft KNOWN TRUE   -> Approve. Bit-for-bit `gh pr create --draft`.
//   - draft UNREADABLE   -> apiMutationAbstain, the conservative floor (Abstain; was Ask
//     under pg2-cl0v2 until operator ruling pg2-psiqh, 2026-08-24 — see that function's
//     doc comment). `--input payload.json` and `-F draft=@file` put the value outside argv
//     (measured), and a Reject there would refuse a legitimate draft create for no reason
//     but our inability to read it — the objection that produced this gap in the first
//     place. The floor preserves the capability; under Abstain the gap is now "auto-approved
//     in an autonomous/headless session or a repo whose settings already allow the
//     underlying Bash call" (pg2-psiqh's accepted consequence), not just "Ask is
//     auto-accepted in an auto-approving session".
//   - draft ABSENT or KNOWN FALSE -> Reject. Bit-for-bit the non-draft create the porcelain
//     Rejects, and the whole hole pg2-cl0v2 was about: `gh api -X POST repos/o/r/pulls
//     -f draft=false` was an Ask, hence auto-accepted, hence a non-draft PR the porcelain
//     would have refused. pg2-psiqh did not touch this Reject.
//
// WHAT WOULD JUSTIFY CHANGING IT: for the UNREADABLE case, only a new operator ruling
// superseding pg2-psiqh (narrowing it to a Reject would ALSO need `--input` bodies to be
// readable, which means reading a file at hook time — a different kind of change, I/O in a
// verdict, needing its own bead regardless of the Ask/Abstain question). For the ABSENT/
// KNOWN-FALSE Reject, the operator ruling that governs prCreateVerdict — pg2-psiqh
// deliberately left this Reject and prCreateVerdict's untouched, so "the two MUST move
// together" no longer describes the UNREADABLE row above, only this one and its porcelain
// mirror.
func (r *Rule) apiPullRequestCreateVerdict(call ghAPICall) hookio.RuleResult {
	value, state := call.bodyParam("draft")
	switch {
	case state == bodyParamValue && bodyParamIsTrue(value):
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason: "gh api POST " + call.Endpoint + " with draft=true: the blessed draft-first" +
				" landing step (creates nothing mergeable), mirroring `gh pr create --draft`",
			Module: r.Name(),
		}
	case state == bodyParamUnreadable:
		return r.apiMutationAbstain(call)
	}
	return hookio.RuleResult{
		Decision: hookio.Reject,
		Reason: "gh: gh api POST " + call.Endpoint + " without draft=true creates an immediately" +
			" mergeable pull request, which is prohibited: this workspace lands DRAFT FIRST, so" +
			" creating one skips the single point at which a person rules on that (operator ruling" +
			" pg2-4yy4r item 2, extended to the raw API by pg2-h8h3f). It is the same operation" +
			" `gh pr create` is Rejected for. Use the two-step `gh pr create --draft` and then" +
			" `gh pr ready`, which prompts; `-f draft=true` here is auto-approved too.",
		Module: r.Name(),
	}
}

// ghAPIValueShorts are the `gh api` short flags that CONSUME A VALUE, read off
// `gh api --help` on gh 2.96.0 (2026-07-30): -F/--field, -H/--header, -q/--jq,
// -X/--method, -p/--preview, -f/--raw-field, -t/--template. `-i`/--include is the
// only boolean short and is deliberately absent.
//
// A value-taking short ENDS its cluster: everything after it in the token is the
// VALUE, not more flag letters. An UNRECOGNISED short letter is treated as boolean
// and scanning continues — a newer gh could add a value-taking short this table
// does not know, in which case its value would be scanned; the resulting error can
// only ADD a mutation signal (a spurious Ask), never remove one, so it fails
// closed.
var ghAPIValueShorts = map[byte]bool{
	'F': true, 'H': true, 'q': true, 'X': true, 'p': true, 'f': true, 't': true,
}

// ghAPIValueLongs are the `gh api` long flags that CONSUME A VALUE, from the same
// `gh api --help` reading. The booleans (--include, --paginate, --silent, --slurp,
// --verbose, --help) are deliberately absent, as is every inherited flag (gh api
// inherits only --help).
//
// An UNRECOGNISED long flag is treated as boolean, so its separated value would be
// read as the endpoint operand. That can only mis-attribute the ENDPOINT, never the
// method or a body parameter (both are matched by name, not by position), and the
// endpoint is consulted only once the method is ALREADY mutating — so the worst
// case is Ask where Reject was meant, never Approve where a gate was meant.
var ghAPIValueLongs = map[string]bool{
	"cache": true, "field": true, "header": true, "hostname": true,
	"input": true, "jq": true, "method": true, "preview": true,
	"raw-field": true, "template": true,
}

// ghAPIBodyParamLongs are the long flags whose presence makes `gh api` default to
// POST: the parameter flags (--field/--raw-field) and the request-body file
// (--input). MEASURED above — each alone produced `> POST` with no -X present. gh's
// own help states the rule: "The default HTTP request method is GET normally and
// POST if any parameters were added."
var ghAPIBodyParamLongs = map[string]bool{
	"field": true, "raw-field": true, "input": true,
}

// safeMethods are the HTTP methods that do not mutate server state — the RFC 9110
// safe methods the GitHub API actually serves. Membership is checked AFTER
// upper-casing, which matches gh: measured, `-X get` sends `GET` and `-X post`
// sends `POST`.
//
// The set is a POSITIVE ALLOWLIST and the check FAILS CLOSED: any method not
// listed — including an unknown verb, and including the `-X` with no value that gh
// itself rejects — is treated as mutating. A blocklist of {POST,PUT,PATCH,DELETE}
// would silently approve every method nobody thought to list.
var safeMethods = map[string]bool{
	"GET": true, "HEAD": true, "OPTIONS": true,
}

// ghAPICall is the resolved shape of one `gh api` invocation.
type ghAPICall struct {
	// Method is the effective HTTP method, upper-cased: the explicit -X/--method
	// value when one was given, else POST when a body parameter is present, else
	// GET. It is "" only when -X/--method was given with no value at all.
	Method string
	// Endpoint is the first operand — the API path, or "graphql". "" when absent.
	Endpoint string
	// bodyParams holds the ARGV-VISIBLE value of each `key=value` body parameter, keyed
	// by parameter name. See bodyParam for how to read it — never read it directly,
	// because a hit here is only half the answer.
	bodyParams map[string]string
	// bodyOpaqueParams names the parameters that ARE in the body but whose value gh read
	// from a file or from stdin, so it is not in argv.
	bodyOpaqueParams map[string]bool
	// bodyFromInput records that --input was given.
	bodyFromInput bool
}

// BODY PARAMETERS READ AS VALUES, NOT AS PRESENCE (pg2-h8h3f)
//
// Until pg2-h8h3f parseGhAPICall recorded only THAT a body parameter existed, because that
// is all the POST default needs. Two verdicts need the VALUE: the draft-first PR gate below
// (`-f draft=true` is the blessed create, `-f draft=false` is the one the porcelain
// Rejects) and the GraphQL classification in graphql.go (the document arrives as
// `-f query=…`). pg2-25oru named this reader as the prerequisite it was missing and left
// the raw-API path at its Ask floor rather than Reject a legitimate draft create.
//
// MEASURED, gh 2.97.0 (nixpkgs), 2026-08-14, via
// `gh api --hostname no-such-host.invalid --verbose <spelling>` and reading the dumped
// request line AND BODY (the dump precedes the DNS failure, so nothing was ever sent):
//
//	-f draft=true  -f title=x    -> body {"draft":"true","title":"x"}   (-f is a STRING)
//	-F draft=false -f title=x    -> body {"draft":false,"title":"x"}    (-F is TYPED)
//	--raw-field draft=true       -> body {"draft":"true"}
//	-F draft=@d.txt              -> body {"draft":"true"} — the FILE WAS READ
//	-f query=@q.graphql          -> body {"query":"@q.graphql"} — NOT read, a literal
//	graphql -F query=@q.graphql  -> body {"query":"{ viewer { login } }"} — READ
//	--input body.json            -> body IS the file
//	--input body.json -f draft=true
//	                             -> `POST …/pulls?draft=true` with body {"draft":false,…}
//	                                — the -f became a QUERY-STRING parameter and the BODY
//	                                still came wholly from the file
//	-f draft=false -f draft=true -> gh REFUSES: `unexpected override existing field
//	                                under "draft"`
//	-f draft=false -F draft=true -> the SAME refusal, across the two spellings
//
// THREE OF THOSE ROWS ARE THE WHOLE DESIGN:
//
//  1. `@` IS A FILE REFERENCE FOR -F/--field ONLY. `-f`/`--raw-field` sends it literally,
//     so its value really is argv-visible even when it starts with `@`. Modelling the two
//     the same way would either hide a value that is visible or trust one that is not.
//  2. `--input` DEMOTES -f/-F TO QUERY STRING. So with --input present NO body parameter is
//     argv-visible, whatever `-f draft=true` says — which is why bodyParam short-circuits
//     on bodyFromInput before it ever consults bodyParams. Reading the `-f` there would be
//     a false Approve on a call whose body says the opposite.
//  3. gh REFUSES A DUPLICATE KEY. So there is no last-one-wins precedence question to get
//     wrong (the one pr.go's lastLongFlag exists for on the FLAG side). The walk below
//     simply overwrites, and either policy is INERT because gh runs neither spelling.

// bodyParamState is how much is known about one body parameter.
type bodyParamState int

const (
	// bodyParamAbsent means the parameter is not in the body, and that is KNOWN: the whole
	// body is argv-visible and this name is not in it.
	bodyParamAbsent bodyParamState = iota
	// bodyParamValue means the parameter is in the body and its value is argv-visible.
	bodyParamValue
	// bodyParamUnreadable means the parameter may or may not be in the body and its value
	// cannot be read from argv — `--input` supplied the body, or `-F name=@file` supplied
	// this value. A verdict MUST NOT read this as either present-and-true or absent.
	bodyParamUnreadable
)

// bodyParam returns the argv-visible value of body parameter name and how much is known
// about it.
//
// The bodyFromInput short-circuit comes FIRST by measurement, not by caution: with
// `--input` present gh puts `-f`/`-F` in the QUERY STRING and takes the body wholly from
// the file, so an argv `-f draft=true` describes something other than the body.
func (c ghAPICall) bodyParam(name string) (string, bodyParamState) {
	if c.bodyFromInput || c.bodyOpaqueParams[name] {
		return "", bodyParamUnreadable
	}
	if v, ok := c.bodyParams[name]; ok {
		return v, bodyParamValue
	}
	return "", bodyParamAbsent
}

// bodyParamIsTrue reads a body parameter's value as a boolean the way GitHub does, through
// strconv.ParseBool, so `true`/`1`/`t` are true and `false`/`0`/`f` are not.
//
// IT IS NOT pr.go's boolFlagIsTrue AND MUST NOT BE REPLACED BY IT. That function reads a
// pflag BOOLEAN FLAG, where the BARE form is legitimately true (`gh pr create --draft`
// carries no value and means draft). A body parameter is always written `key=value`, so an
// empty value is an empty STRING gh sends verbatim — `{"draft":""}` — which GitHub does not
// read as true. Here empty is FALSE, and an unparseable value is false too, both of which
// move the draft gate toward its Reject.
func bodyParamIsTrue(value string) bool {
	b, err := strconv.ParseBool(value)
	return err == nil && b
}

// recordBodyParam records one `key=value` body parameter. fileMagic says whether the
// spelling that carried it honours gh's `@path` / `@-` file reference — MEASURED true for
// -F/--field and false for -f/--raw-field.
//
// A parameter with no `=` at all is dropped: gh itself refuses that spelling
// (`field: key=value expected`), so there is no value to record and nothing can run.
//
// The glued-quote unwrap (cmdparse.UnwrapGluedQuotes, pg2-9zgso — this call used to be the
// package-local `unwrapGluedQuotes`, deleted in favour of the shared helper) runs BEFORE the
// `@` test on purpose: `-F query='@q.graphql'` is the same file reference as
// `-F query=@q.graphql`, and reading it as a literal value would be the one direction that
// turns an unreadable document into a readable one.
func (c *ghAPICall) recordBodyParam(kv string, fileMagic bool) {
	name, value, ok := strings.Cut(kv, "=")
	if !ok || name == "" {
		return
	}
	value = cmdparse.UnwrapGluedQuotes(value)
	if fileMagic && strings.HasPrefix(value, "@") {
		if c.bodyOpaqueParams == nil {
			c.bodyOpaqueParams = map[string]bool{}
		}
		c.bodyOpaqueParams[name] = true
		return
	}
	if c.bodyParams == nil {
		c.bodyParams = map[string]string{}
	}
	c.bodyParams[name] = value
}

// IsMutating reports whether the call's effective method writes. It is the
// fail-closed reading of safeMethods: an empty or unrecognised method counts as
// mutating.
func (c ghAPICall) IsMutating() bool {
	return !safeMethods[strings.ToUpper(c.Method)]
}

// methodLabel renders Method for a reason string. It upper-cases (gh does too, so
// `-X post` really is a POST) and names the ONE case where Method is empty:
// `-X`/`--method` given with no value, which gh itself rejects and which
// IsMutating treats as mutating rather than guessing.
func (c ghAPICall) methodLabel() string {
	if c.Method == "" {
		return "no --method value"
	}
	return strings.ToUpper(c.Method)
}

// IsPullRequestMerge reports whether the endpoint is GitHub's pull-request merge
// endpoint, `/repos/{owner}/{repo}/pulls/{number}/merge` — the exact operation
// `gh pr merge` performs. Matched structurally on the path segments (last segment
// `merge`, with a `pulls` segment before it) so a leading slash, the
// `{owner}`/`{repo}` placeholders gh expands, and a trailing query string all
// still match.
//
// DELIBERATELY NARROW. `POST /repos/{owner}/{repo}/merges` and `merge-upstream`
// also merge, and a `graphql` mutation can merge a PR, but none of those is the
// operation `gh pr merge` runs, so none of them is the control this must not be
// weaker than. They stay at the generic mutation Ask, which still puts a person in
// the loop. Widening this to them is a separate ruling, not a tidy-up.
func (c ghAPICall) IsPullRequestMerge() bool {
	return c.endpointIs("merge", "pulls")
}

// IsPullRequestCreate reports whether the call is a pull-request CREATION —
// `POST /repos/{owner}/{repo}/pulls`, bit-for-bit the operation `gh pr create` performs.
// Matched on the same structural reading as IsPullRequestMerge (last segment `pulls`, with
// a `repos` segment before it), so a leading slash, gh's `{owner}`/`{repo}` placeholders
// and a trailing query string all still match.
//
// THE METHOD TEST IS PART OF THE IDENTITY, not a guard bolted on. POST is the verb that
// creates; `PATCH /repos/o/r/pulls/5` updates an existing PR and does not match anyway
// (different last segment), and no other verb on the collection path creates one. A method
// this does not recognise therefore keeps the generic mutation Ask rather than acquiring a
// draft-first verdict that would be about a different operation.
func (c ghAPICall) IsPullRequestCreate() bool {
	return strings.EqualFold(c.Method, "POST") && c.endpointIs("pulls", "repos")
}

// endpointIs reports whether the endpoint's path ends in segment last and carries segment
// contains somewhere before it. It is the ONE structural path reading behind both
// pull-request endpoint tests, so a leading slash, a gh placeholder or a query string
// cannot be handled one way in one of them and another way in the other.
func (c ghAPICall) endpointIs(last, contains string) bool {
	path := c.Endpoint
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) < 2 || segs[len(segs)-1] != last {
		return false
	}
	for _, s := range segs[:len(segs)-1] {
		if s == contains {
			return true
		}
	}
	return false
}

// parseGhAPICall resolves the effective method and endpoint of a `gh api`
// invocation. args are the tokens AFTER the `api` subcommand.
//
// It walks the argv ONCE, honouring gh's flag arity (ghAPIValueShorts /
// ghAPIValueLongs) so that a flag VALUE is never scanned as more flags and never
// mistaken for the endpoint. The walk is POSITION-INDEPENDENT: -X/--method is found
// wherever it appears, before or after the endpoint (measured: `gh api
// repos/o/r/pulls/5/merge --method PUT` is a PUT).
//
// The LAST -X/--method wins, matching pflag's last-one-wins for a repeated scalar
// flag. After a `--` end-of-options terminator every remaining token is an operand.
//
// The byte loop over a short cluster is the only way to see a clustered flag; bead
// pg2-x9452's Guard 2 flags character loops as hand-rolled scanners and this is one
// of its documented false positives — the loop indexes bytes of a single
// already-tokenized, already-unquoted argument and makes no lexical decision.
func parseGhAPICall(args []string) ghAPICall {
	var call ghAPICall
	methodSeen, bodyParam := false, false

	// setMethod unwraps the SAME glued-quote boundary recordBodyParam does
	// (cmdparse.UnwrapGluedQuotes, pg2-9zgso) — a gap this bead FOUND rather than
	// widened: `--method='PUT'` / `-X='PUT'` never routed through the pre-existing
	// package-local unwrapGluedQuotes at all, only the body-parameter path did. So
	// `IsMutating` (which upper-cases Method and checks it against the safeMethods
	// ALLOWLIST) fails CLOSED on a quoted safe method today — `--method='GET'`
	// reads as the unsafe method `"'GET'"` and is asked/rejected as if mutating,
	// even though the real invocation is a plain GET. Unwrapping here can only
	// ever make Method match a value the caller ALREADY compares against an
	// allowlist, never weaken IsPullRequestMerge/IsPullRequestCreate's own
	// structural checks, which read Endpoint and the now-clean Method.
	setMethod := func(v string) {
		methodSeen = true
		call.Method = cmdparse.UnwrapGluedQuotes(v)
	}
	// operand records the first non-flag token as the endpoint.
	operand := func(v string) {
		if call.Endpoint == "" {
			call.Endpoint = v
		}
	}

	for i := 0; i < len(args); i++ {
		a := args[i]

		if a == "--" {
			for _, rest := range args[i+1:] {
				operand(rest)
			}
			break
		}

		// Long flag.
		if strings.HasPrefix(a, "--") {
			name, glued, hasGlued := strings.Cut(a[2:], "=")
			value := glued
			if !hasGlued && ghAPIValueLongs[name] && i+1 < len(args) {
				value = args[i+1]
				i++ // the next token is this flag's VALUE, not a flag or the endpoint
			}
			if name == "method" {
				setMethod(value)
			}
			if ghAPIBodyParamLongs[name] {
				bodyParam = true
			}
			switch name {
			case "field":
				call.recordBodyParam(value, true) // -F/--field honours gh's `@path`
			case "raw-field":
				call.recordBodyParam(value, false) // -f/--raw-field sends `@path` literally
			case "input":
				call.bodyFromInput = true
			}
			continue
		}

		// Short-flag cluster. A lone "-" is an operand (gh reads stdin for it), as is
		// any token not starting with '-'.
		if len(a) > 1 && a[0] == '-' {
			for j := 1; j < len(a); j++ {
				c := a[j]
				if !ghAPIValueShorts[c] {
					continue // boolean short (-i), or an unrecognised letter
				}
				// Value-taking: the remainder of the token is the VALUE (pflag also
				// accepts one '=' right after the letter). An empty remainder means the
				// value is the NEXT token, which must be consumed so it is not read as
				// the endpoint. Either way the cluster ends here.
				value := a[j+1:]
				value = strings.TrimPrefix(value, "=")
				if value == "" && i+1 < len(args) {
					value = args[i+1]
					i++
				}
				switch c {
				case 'X':
					setMethod(value)
				case 'f', 'F':
					bodyParam = true
					// MEASURED: `@path` is a file reference for -F only; -f sends it
					// literally. See the BODY PARAMETERS block above.
					call.recordBodyParam(value, c == 'F')
				}
				break
			}
			continue
		}

		operand(a)
	}

	if !methodSeen {
		// gh's documented default: GET, unless a parameter or request body was added,
		// which switches it to POST.
		if bodyParam {
			call.Method = "POST"
		} else {
			call.Method = "GET"
		}
	}
	return call
}
