# CETA: one shared owner for "which argv tokens are candidate paths"

**Status**: Accepted (resolves `pg2-pmp25`)
**Date**: 2026-08-19
**Deciders**: Phillip Green II (autonomous decision; no operator was in the loop for this call — see
Context)

## Context

Two independent CETA rules each answer the same question for themselves: **given a parsed
command's `pc.Args`, which tokens are candidate paths that must be screened?**

- `internal/rules/secrets` answers it to decide whether an argument should reach
  `secretpath.IsSecret` (the deny-list).
- `internal/rules/safecmds` answers it to decide whether an argument should reach
  `patheval`'s zone/readability check (`readPathIssue`).

Both rules need the answer because a command's positional and flag arguments are not uniformly
paths: a search pattern, a jq variable binding, a commit message, or a literal like `--indent 2`
must NOT be tested as a filename, while a file-opening flag's value, or a positional the command
genuinely opens, MUST be. The rule for telling the two apart is already written down — in
`internal/cmdparse/argflags.go`'s `messageFlags` doc comment, as **"A FLAG WHOSE VALUE THE COMMAND
OPENS NEVER BELONGS IN A SKIP TABLE"** — but it lives in the doc comment of one table that only
that table's editor reads, and each rule maintains its own tables and its own traversal over them.

### Four failure modes, all measured, all violations of that one rule

1. **A file-opening (or program-running) flag IS IN a "skip, do not screen" table.** jq's
   `--rawfile`/`--slurpfile` and grep's `-f`/`--file` were both listed as "consume and discard,"
   deleting a real path from screening before it ever reached `secretpath.IsSecret` or
   `readPathIssue`.
2. **A boolean or optional-value flag is listed as mandatory-value, so it swallows the following
   token.** `jq --join-output`, `grep --color[=WHEN]`, ugrep's `--config[=FILE]` all take NO
   operand in the space spelling; a table that lists them as value-taking eats the next real
   argument as if it were the flag's value.
3. **A per-rule PRIVATE copy of the traversal exists at all**, so the two rules' tables can drift
   from each other for the identical command. `safecmds.go` alone had eleven independent
   `strings.HasPrefix(a, "-")` skip loops, each its own scanner with its own notion of which
   operands are paths.
4. **Absence from every table is not sufficient.** ugrep's `--exclude-from`, `--include-from`,
   `--from`, `--config`, `--save-config`, `--ignore-files` were in NO table anywhere. The flag NAME
   was still skipped as an ordinary `-`-prefixed token (arity assumed 0), so its operand fell
   through as an ordinary POSITIONAL — and was then discarded by grep's own "the first positional is
   the search pattern, not a file" heuristic. Measured: `grep --exclude-from ~/.ssh/id_rsa pat /tmp`
   returned `approve`, while the positional control `grep ~/.ssh/id_rsa pat /tmp` returned `reject`.
   Mode 4 is the important one: it proves a COMPLETE TABLE cannot be the whole answer, because
   completing it only relocates the hole to the next flag a future tool version adds — the failure
   is structural (an unknown flag's operand can be captured by an unrelated positional-role
   heuristic), not a missing entry.

### Evidence table — eight measured instances of this one shape

| #   | Bead                                     | Failure mode(s)                                                                                                                                                                         | What leaked / broke                                                                                                                                                                                                                                                                                                                                                      |
| --- | ---------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1   | `pg2-ia640.2` (closed)                   | 1, false-positive direction                                                                                                                                                             | Original fix that skipped grep/rg's pattern arg and jq's literal-value flags before `IsSecret` — this bead's tables are what modes 1–4 later violated                                                                                                                                                                                                                    |
| 2   | `pg2-ia640.5` (closed)                   | 1, false-positive direction                                                                                                                                                             | Skipped `bd`/`git -m` message args before `IsSecret` — created `messageFlags`/`SkipMessageArgs`, whose own doc states the rule this ADR generalizes                                                                                                                                                                                                                      |
| 3   | `pg2-wrxg6` (closed, was P0)             | 1, 2                                                                                                                                                                                    | jq `-f`/`--rawfile`/`--slurpfile` operands (files jq opens) were in the "discard" table; `--tab`/`--join-output`/`--jsonargs` (booleans) were in the "value-taking" table and swallowed the next token                                                                                                                                                                   |
| 4   | `pg2-cu3ro` (closed, was P0)             | 3 (glued spelling never handled by the space-form skip logic at all)                                                                                                                    | `git commit --file=~/.ssh/id_rsa` auto-approved because the `=`-glued token was skipped whole as "just a flag," never split into name+value                                                                                                                                                                                                                              |
| 5   | `pg2-ygjs5` (closed, was P0)             | 1, 2, 4                                                                                                                                                                                 | grep/rg's `-f`/`--file`/`--ignore-file` pattern-file operands; ugrep's six then-untabled file flags (mode 4, the `--exclude-from` measurement above); `--color` as an optional-value flag; the glued spelling a second time; `--pre`/`--filter`/`--pager`/`--view`/`--hostname-bin`, which name a PROGRAM the tool runs, a class the tables had no vocabulary for at all |
| 6   | `pg2-wxbr9` (closed, was P1)             | 3                                                                                                                                                                                       | The glued spelling a third time, independently rediscovered in `safecmds.go`'s eleven private skip loops — `secrets` and `safecmds` had drifted: one rule's fix for the `--flag=value` spelling did not reach the other                                                                                                                                                  |
| 7   | `pg2-mu8zg` (closed, was P2)             | 4-shaped (an untabled operand-taking flag falls through to a DIFFERENT classifier — the "which token is the program" role, not the pattern-skip heuristic — and is judged too narrowly) | `jq -L ~/.ssh . f.json` measured `abstain` (auto-approved in auto mode) because `-L`'s directory operand became jq's apparent first positional and was judged as the FILTER program by the narrower `isDynamicPathOperand`, not screened as a path                                                                                                                       |
| 8   | commit `6fb6df4f` / `pg2-e1163` (closed) | Related but distinct: a MISSING carve-out rather than a wrong table entry                                                                                                               | `git grep -E '\.pem\|\.key' ...`'s pattern argument reached `IsSecret` unfiltered, because `secretCandidateArgs` keys its grep/rg carve-out on `filepath.Base(pc.Executable)`, which is `"git"` for `git grep` — so it fell into the message-arg branch, which has no concept of a search pattern at all                                                                 |

**Premise correction, recorded here per this repo's own premise-freshness practice.** The bead that
opened this ADR (`pg2-pmp25`) describes instances 6 and 7 (`pg2-wxbr9`, `pg2-mu8zg`) as **open**
(P1 and P2 respectively) and frames the migration question as "what happens to them once fixed."
Re-checked live via `bd show` while writing this ADR (2026-08-19): **both are CLOSED.**
`pg2-wxbr9` landed at `b614d5d3`, adding a shared local seam (`safecmds.pathCandidate`, built on
`cmdparse.GluedFlagValue`) and routing 7 of its 11 skip sites through it, leaving 4 as documented
bare skips (see Decision below for why those 4 are not actually instances of this defect).
`pg2-mu8zg` landed at `abc15393`, adding a `jq`/`-L` entry to `safecmds.programOperandValueFlags`.
Neither change is wrong, and neither is undone here. What changes is only the FRAMING: the
migration this ADR describes does not "fix" either bead — it **subsumes two already-landed,
rule-local point fixes into one shared mechanism**, which is exactly the pattern the whole evidence
table above demonstrates does not hold up over time when left as independent tables.

### This is the same structural shape a prior decision already reconciled once

`pg2-zpct4` (closed P0, `commit a4411734`) found the identical shape one layer up: the static
command-substitution allowlist and `safecmds`' `readPathIssue` disagreed about whether
`X=$(cat /etc/shadow) echo hi` was safe, because **two path models classified the same command
independently**. Its resolution — recorded in its close reason and carried into ADR 0044's Context
and ADR 0048's mechanism — was to give **path IDENTIFICATION** one owner
(`cmdparse.LooksLikePath`, with `safecmds` delegating to it) and **path READABILITY** one owner
(`patheval`, consulted via `readPathIssue`), rather than extending both models in parallel. No
dedicated ADR was written for that decision at the time; it is recorded only in the bead and in the
two ADRs that build on it. **This ADR is that decision's sibling for one more path question**:
"which argv tokens are candidates at all," which `secrets` and `safecmds` currently each answer for
themselves, with the same drift risk `pg2-wxbr9` measured. ADR 0039 established the same principle
at the parsing layer ("one real shell parser front end … behind a single lowering seam") for a
structurally identical reason: several independent passes answering the same question is where
divergence lives.

### Scope note: this audit is bounded to the instances above, not a repo-wide sweep

While researching this ADR, `internal/rules/gitdir`, `internal/rules/kubectl`,
`internal/rules/sqlite3`, `internal/rules/docker`, and `internal/rules/git` were found to each
contain their own private `strings.HasPrefix(a, "-")` argument scans (eleven sites total across
those five files) near path- or write-target-relevant logic. **Whether any of those are the same
defect class is NOT established here** — some may be answering an unrelated question (e.g.,
locating a subcommand keyword), and auditing eleven more sites across five rules is outside a
single ADR's scope. This is flagged as follow-up scope, not claimed as either fixed or broken; see
Consequences.

## Decision

**Adopt a shared owner.** `which argv tokens are a candidate path for this command` becomes ONE
question with ONE answer, owned by `internal/cmdparse` (the same package that already owns
`GluedFlagValue`, the primitive every one of the eight instances above eventually routes through),
and consulted by both `secrets` and `safecmds` instead of each maintaining its own tables and
traversal.

### Shape of the owner

A per-command **flag spec** — `map[commandBasename]map[flagName]OperandKind` — replacing
`grepFlagsWithValue`/`rgFlagsWithValue`/`grepFileFlags`/`rgFileFlags`/`grepProgramFlags`/
`rgProgramFlags`/`grepExecPresenceFlags`/`jqValueFlags`/`jqOneArgFlags`/`messageFlags` and
`safecmds`' `programOperandValueFlags`/`programOperandFromFlag`, where `OperandKind` is one of:

- **`Literal`** — the operand is data the command never opens, executes, or transmits as a path
  (a pattern, a glob, a jq variable value, `--indent N`).
- **`PathRead`** — the operand is a path the command opens for reading (grep's `-f`, jq's
  `--rawfile`/`--slurpfile`).
- **`PathWrite`** — the operand is a path the command opens for writing.
- **`Program`** — the operand names something the command RUNS as code: a program (`rg --pre`,
  ugrep `--filter`), or a directory of loadable modules (jq's `-L`/`--library-path` — see below).
- **`Message`** — the operand is free text the command stores, never opens (git's `-m`, gh's
  `--body`); kept distinct from `Literal` only because it is scoped to a closed set of
  message-storing commands, matching `messageFlags`' existing closed-set discipline.

A flag with a `Boolean` (no-operand) marker consumes nothing; anything not in a command's spec has
**no marker at all**, which is the load-bearing case (below).

One traversal, `cmdparse.ClassifyCandidateArgs(cmd string, args []string) (paths []string, malformed bool)`,
replaces `SkipGrepPattern`, `SkipJqValueFlags`, `SkipMessageArgs`, `safecmds.pathCandidate`, and the
table-driven half of `safecmds.programOperand`. It:

- resolves both the space and the `=`-glued spelling of every flag through the existing
  `GluedFlagValue`/`equalsFlagName` primitives (unchanged — this is not a re-litigation of pg2-52eod
  or pg2-cu3ro, only a relocation of the tables those primitives already serve);
- emits a flag's operand as a path candidate when its `OperandKind` is `PathRead`, `PathWrite`, or
  `Program`, drops it (screens nothing) when `Literal` or `Message`, and consumes nothing extra for
  an operandless (`Boolean`) flag;
- propagates `GluedFlagValue`'s `malformed` signal exactly as today's callers already must (fail
  closed, never silently drop).

### Mode 4's fix is a structural invariant, not a bigger table

Completing every table only relocates mode 4 to the next flag a future tool release adds. The
generalizable fix is: **a positional-role exemption (grep/rg's "first positional is the search
pattern," jq/awk/sed's "first positional is the program") is granted for a given invocation ONLY
when every `-`-prefixed token preceding that positional resolves in the command's spec.** The
moment `ClassifyCandidateArgs` encounters a flag it has no entry for, it suspends that command's
positional-role exemption for the REST of that invocation — every remaining positional stays a
screened candidate, rather than being handed to a heuristic that assumes it knows the full flag
grammar. This is the same "cost of one extra tested value that matches nothing is cheaper than the
cost of missing one" argument `GluedFlagValue`'s and `SkipGrepPattern`'s own doc comments already
make, generalized from "an unknown glued flag's value" to "an unknown flag's neighboring
positional." It closes mode 4 by construction: `--exclude-from`'s absence from a table no longer
matters, because ANY untabled flag suspends the exemption that let the operand fall into the
"discard as the pattern" slot in the first place.

### `jq -L`/`--library-path` is reclassified, not just relocated

`pg2-mu8zg`'s landed fix places `-L`/`--library-path` in `programOperandValueFlags["jq"]`, which
only stops it from being misjudged as the FILTER program — it is still a plain path-read as far as
that table is concerned. Its own bead text says otherwise: `-L` "let[s] jq LOAD AND EXECUTE `.jq`
module code from a directory." That is `OperandKind = Program`, the same class as `rg --pre` and
ugrep `--filter`, not `PathRead`. The migration should carry this correction, not just port the
table entry.

## Migration (described; not implemented in this change — see Consequences)

- **`secrets.secretCandidateArgs`** — its `grep`/`rg` branch (`cmdparse.SkipGrepPattern`), its
  `jq`/`yq` branch (`cmdparse.SkipJqValueFlags` + `dropFirstPositional`/`jqFilterFromFile`), its
  `bd`/`git`/`gh` branch (`cmdparse.SkipMessageArgs`), and the new `gitGrepArgs` git-grep carve-out
  (commit `6fb6df4f`, `pg2-e1163`) all become one call into `cmdparse.ClassifyCandidateArgs` for the
  resolved basename (with `git grep`'s args pre-sliced by `gitGrepArgs` exactly as today, since that
  slicing answers "which subcommand is this," a different and legitimate question this ADR does not
  touch).
- **`safecmds`** — `pathCandidate` and the table half of `programOperand`
  (`programOperandValueFlags`/`programOperandFromFlag`) are replaced by the same call; the grep/rg,
  sed, and jq inline branches in `Evaluate` stop maintaining their own per-command logic and consult
  the shared spec's answer, then apply their existing zone/readability check (`readPathIssue`)
  unchanged — this ADR only relocates "which tokens are candidates," not what happens to a candidate
  once identified.
- **`pg2-wxbr9`'s eleven private skip sites** — the 7 already routed through `pathCandidate` migrate
  to the shared spec for free (they are calling exactly the seam this ADR generalizes). Of the 4
  left as documented bare skips:
  - **`programOperand`'s own role-classifying loop** is subsumed — its job (deciding whether a
    positional is the PROGRAM) becomes a spec lookup (`OperandKind == Program`) instead of a
    hand-rolled scan.
  - **The other 3 — the `log` subcommand-keyword scan, `extractXargsCommand`'s inner-executable-name
    scan, and `evaluateUnzip`'s flag skip** — are correctly NOT subsumed. Each answers "which token
    plays role X" (a subcommand name, an inner command name, an archive-tool flag with no `=`-glue
    syntax to worry about), not "is this token a path to screen." Forcing them into the path-spec
    would conflate two different questions the way mode 3 already warns against. They stay local,
    same as `pg2-wxbr9` left them, and their own comments already say why.
- **`pg2-mu8zg`'s `jq -L` fix** is subsumed into the shared spec as a single `Program`-kind entry
  (see reclassification above), removing the now-redundant `programOperandValueFlags["jq"]` table
  entry.
- **Retirement order**: `cmdparse.SkipGrepPattern`, `SkipJqValueFlags`, `SkipMessageArgs`, and
  `safecmds.pathCandidate`/`programOperand`'s table half are deleted once their last caller migrates
  — not before, and not as a "keep both, prefer the new one" fork, which would just be a THIRD
  parallel model.
- **Validation this repo already requires for a change of this shape** (per ADR 0039's replay
  discipline and every closed bead in the evidence table): an A/B replay over the production asklog
  with an isolated `XDG_DATA_HOME`, read-only; the gate is no less-restrictive transition; every
  existing false-positive regression test (`pg2-ia640.2`, `pg2-ia640.5`, `pg2-wrxg6`, `pg2-cu3ro`,
  `pg2-ygjs5`, `pg2-wxbr9`, `pg2-mu8zg`, `pg2-e1163`) MUST pass unmodified in both the space and
  glued spelling; and the mode-4 invariant above gets its own fixture — a synthetic per-command flag
  the spec does NOT know, asserting the positional-role exemption is suspended rather than firing.

## Consequences

### Positive

- The "flag whose value the command opens never belongs in a skip table" rule stops being
  discoverable only by reading one table's doc comment; it is enforced by the shape of the owner
  (an `OperandKind` a flag either has or does not) rather than by a convention each table's author
  has to remember and restate.
- `secrets` and `safecmds` can no longer drift on the SAME command the way `pg2-wxbr9` measured —
  there is one traversal, not two, so a fix to it fixes both callers simultaneously by construction.
- Mode 4 (absence-from-every-table) closes as an invariant rather than as a chase for the next
  unenumerated flag: an unrecognized flag now WIDENS screening (suspends an exemption) instead of
  silently narrowing it.
- `jq -L`'s reclassification as `Program` (rather than a bare path-read) makes it consistent with
  how `rg --pre`/ugrep `--filter` are already modeled, closing a real, if minor, precision gap the
  landed `pg2-mu8zg` fix left behind.

### Negative

- **This ADR does not implement the migration.** The four failure modes above are all measured
  security-relevant behavior in a hook that runs on every `Bash` tool call; every prior fix in the
  evidence table required a corpus replay to land safely (several explicitly reference "isolated
  `XDG_DATA_HOME`," "no less-restrictive transition," and named regression tests). Implementing the
  relocation and the mode-4 invariant together, correctly, for `secrets` AND `safecmds`
  simultaneously, without that replay infrastructure available in the same session as this docs bead,
  risks reproducing exactly the "false-positive fix becomes a false-negative" failure direction
  `pg2-wrxg6` and `pg2-cu3ro` both warn about in their own doc comments. Writing the decision now and
  implementing it as its own reviewed, replayed change is the safer split.
- **Mode 4's fix increases prompt volume** for any command invocation using a flag genuinely outside
  the spec (a new tool version, or a legitimately-unenumerated boolean flag) — the positional-role
  exemption suspends for the rest of that invocation. The cost is bounded (one extra `Ask`, never an
  `Approve` becoming a `Reject` or vice versa) but is a real, measurable UX change the eventual
  implementation MUST replay and report, not merely assert.
- **The eleven newly-observed private skip sites in `gitdir`/`kubectl`/`sqlite3`/`docker`/`git`
  (Context, Scope note) are neither fixed nor characterized by this decision.** Adopting a shared
  owner for the instances this ADR's evidence table names does not by itself extend to those five
  rules; doing so is explicitly left as unstarted follow-up scope rather than silently assumed to be
  either safe or broken.

### Neutral

- The shared owner's `OperandKind` taxonomy (`Literal`/`PathRead`/`PathWrite`/`Program`/`Message`)
  is a judgment call about the right granularity. `Message` could instead be folded into `Literal`
  with a separate closed-command-set gate exactly as `messageFlags` does today; keeping it distinct
  costs one more enum value and buys nothing this ADR's evidence needed, but is the more literal
  reading of `messageFlags`' own doc comment's distinction, so it is kept.
- This decision does not change any currently-shipped verdict; the eight instances in the evidence
  table are all already fixed. Its effect is entirely on how the NEXT instance (a mode-4 shape
  against a flag nobody has enumerated yet) is prevented, not on any live behavior today.

## Alternatives Considered

### Status quo: keep extending the per-rule tables as new gaps are measured

**Rejected.** This is the alternative that produced eight instances across four repeated failure
modes, with the fifth and sixth landing more than a week apart in the SAME file (`pg2-wxbr9`
rediscovering the glued-spelling defect `pg2-cu3ro` had already fixed once, in the sibling rule).
Mode 4 is the decisive argument against this alternative specifically: completing today's tables
does not bound the defect, because the next unenumerated flag reproduces it by construction, not by
omission.

### A full declarative flag-parser (getopt-style) covering every third-party tool's complete grammar

**Rejected as the goal, though the spec above is a bounded version of it.** grep/rg/jq/awk/sed are
CETA's actual measured surface; a general-purpose flag grammar for arbitrary future tools has no
upstream spec to verify against (this repo's own tables are already built by exhaustively reading
`--help` output per tool version, e.g. `argflags.go`'s doc citing "ugrep 7.5.0" and "GNU grep"
explicitly) and would grow without bound. The chosen shape keeps the same per-command, per-version
verification discipline this repo already uses; it only moves WHERE that verified table lives and
adds the mode-4 invariant on top of it.

### Abstain on any command containing any unrecognized flag at all

**Considered and rejected as too blunt.** This is the maximally fail-closed version of mode 4's fix
— rather than suspending only the positional-role exemption, refuse the WHOLE command. It would
close mode 4 with less new code, but at an unmeasured and likely large prompt-volume cost (every
grep/jq/sed invocation using a flag this repo's tables have not yet enumerated would newly ask,
including entirely benign ones with nothing path-shaped in them at all). The adopted design
narrows the blast radius to exactly the property mode 4 needs — a positional that COULD be a path
stays a candidate — without abstaining on commands that never touch that heuristic.

### Defer this ADR and continue filing point fixes as each new instance is measured

**Rejected; this is the option the bead itself rules out.** "Defer" produced the eight-instance
evidence table above, including two instances (`pg2-wxbr9`, `pg2-mu8zg`) that were independently
discovered and independently fixed while a bead explicitly warning that this shape "may deserve an
ADR rather than three point fixes" already existed. A ninth instance is not a hypothetical.

## Related Decisions

- `pg2-zpct4` (closed P0, commit `a4411734`) — the direct precedent: reconciled two disagreeing path
  models by giving path IDENTIFICATION and path READABILITY each one owner, for the same underlying
  reason (drift between independent classifiers of the same command). Recorded in its own close
  reason and referenced by ADR 0044's Context and ADR 0048's mechanism; no dedicated ADR exists for
  it, which is part of why this decision is now written down as one.
- [ADR 0039](0039-ceta-shell-parser-front-end.md) — "one real shell parser front end … behind a
  single lowering seam," the same principle applied one layer below argument classification: several
  independent passes deriving the same structure is where CETA's measured defects have repeatedly
  lived.
- [ADR 0050](0050-ceta-git-config-injection-key-value-predicate.md) — a smaller instance of the same
  discipline (a relaxation is a name **plus** a value predicate, never name-only), which the
  `OperandKind` taxonomy above generalizes from one rule's config-injection carve-out to the whole
  candidate-path question.
- Beads carrying the eight measured instances: `pg2-ia640.2`, `pg2-ia640.5`, `pg2-wrxg6`,
  `pg2-cu3ro`, `pg2-ygjs5`, `pg2-wxbr9`, `pg2-mu8zg`, `pg2-e1163` (commit `6fb6df4f`) — all closed;
  see the evidence table for what each fixed.
