package gh

import (
	"strings"

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
// -f title=x` (the operation modifyingPR gates at Ask).
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
// EVERY OTHER MUTATION is ASK — the conservative floor. Ask and not Approve because
// `gh api` can perform any write the token permits, and the blanket Approve was
// already measured bypassing modifyingPR's Ask on `gh pr create` (`gh api -X POST
// repos/o/r/pulls -f title=x` measured `allow`). Ask and not Reject because `gh
// api` has no single operation to rule on: it is the whole REST surface, most of it
// unremarkable, and a Reject would be a blanket prohibition on writing to GitHub
// that no operator ruling covers. Ask lands each write in front of the person who
// can judge it, and matches the verdict the equivalent porcelain (`gh pr create`,
// `gh issue create`) already carries — which is the property that makes the two
// consistent rather than one being the other's bypass.
//
// WHAT WOULD JUSTIFY CHANGING IT: for the merge Reject, only a new operator ruling
// on immediate merges — the same ruling that governs the `gh pr merge` branch it
// mirrors, so the two MUST move together. For the generic Ask, evidence that a
// specific endpoint class is read-only in practice (then narrow it by MEASURING the
// method, not by re-widening the branch), or an operator ruling extending the merge
// Reject to a broader endpoint set (`/merges`, `merge-upstream`, a `graphql`
// mutation) — see IsPullRequestMerge for why those are Ask today.
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
	return hookio.RuleResult{
		Decision: hookio.Ask,
		Reason: "gh: gh api with a mutating HTTP method (" + call.methodLabel() +
			") — `gh api` is a general-purpose REST/GraphQL client, so this writes to GitHub" +
			" (pg2-cl0v2). Only a read-only gh api is auto-approved.",
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
	path := c.Endpoint
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) < 2 || segs[len(segs)-1] != "merge" {
		return false
	}
	for _, s := range segs[:len(segs)-1] {
		if s == "pulls" {
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

	setMethod := func(v string) {
		methodSeen = true
		call.Method = v
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
