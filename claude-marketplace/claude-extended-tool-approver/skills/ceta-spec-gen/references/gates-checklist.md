# Gates checklist (Phase 3 item 2)

Five gates, verbatim from the CETA v16 plan's Phase 3 item 2 ("Gates (fact verification, no
trust tiers — R11): citations present, linter clean, `--help` drift check clean, goldens pass,
and the P15 approve-reachability report attached to the change"), each mapped to the
tool/packet in **this docket** (tc-o14i5.4) — or Phase 2, for P15 — that satisfies it. This is
the standard workflow any future spec change (built-in or custom) MUST satisfy before landing,
per docket tc-o14i5.4's Phase 3 item 4 (recorded by packet tc-o14i5.4.4's own evaluation, see
that docket's comment thread for the full quantitative writeup).

Run all of gates 1-3 from the module root, `packages/claude-extended-tool-approver`.

## 1. Citations present

**Tool**: `internal/speclint`'s citation-presence check (`speclint.CheckCitationPresence` /
`isThinCitation`, `internal/speclint/lint.go`).

**Status**: HARD for the embedded/builtin layer as of tc-o14i5.4.3 (which back-filled real
per-fact citations for all 46 embedded built-in specs and flipped this check from its prior
WARN staging — see `citationFinding`'s own doc comment).

**How to run**: folded into the same `lint` invocation as gate 2 below (citation-presence is
one of the checks `LintCommand` runs unconditionally, builtin or not).

## 2. Linter clean

**Tool**: `claude-extended-tool-approver lint --embedded` (existing since Phase 1.3 /
tc-o14i5.2.3).

**Wiring**: the `claude-extended-tool-approver-spec-lint` `nix flake check` entry (`flake.nix`).

**How to run**:

```bash
go run ./cmd/claude-extended-tool-approver lint --embedded
```

Exits 1 on any HARD finding. For a CUSTOM (non-built-in) spec, lint the bare file directly
instead (`lint <path-to-spec.json>` — this path always runs the checks the embedded/builtin
layer skips, checks 2 and 3 below, since a bare-file lint is never treated as builtin):

```bash
go run ./cmd/claude-extended-tool-approver lint <path-to-spec.json>
```

## 3. `--help` drift check clean

**Tool**: `claude-extended-tool-approver spec-drift-check --embedded` (default `--check` mode),
as of tc-o14i5.4.2.

**Wiring**: the `claude-extended-tool-approver-spec-help-drift` `nix flake check` entry
(`flake.nix`) — compares each command's live `--help` output (SHA-256 hash) against the
committed `internal/embeddedspecs/data/help-hashes.json`.

**How to run**:

```bash
go run ./cmd/claude-extended-tool-approver spec-drift-check --embedded
```

Hashes are only meaningful against the PINNED tool versions of the nix check's sandbox: run it
there (`nix build .#checks.<system>.claude-extended-tool-approver-spec-help-drift`), and record
new names with `spec-drift-check --embedded --record` in that same environment. A name with no
on-PATH binary is exempt (`specdrift.IsExempt`: `specdrift.Exempt` — `cd`, `export`, `pwd`,
`read`, `shift`, `exit`, `launchctl`, `man`; `specdrift.ExternalFlake` — commands whose binary
ships from another flake; `specdrift.ExemptOn` — per-GOOS, `ps`/`pgrep` on darwin; and every
`*.sh` plugin helper script). A new on-PATH tool must be added to that check's
`nativeBuildInputs`. Regenerating the embedded JSON (`go run ./cmd/genspecs`) never rewrites
`help-hashes*.json`, so recorded hashes survive it.

**A growing tool re-drifts on every change (pg-desk).** The hash covers the whole top-level
`--help`, and a cobra-style root lists every subcommand with its description, so adding,
renaming, removing or re-describing a verb of an actively developed tool (the canonical case is
`pg-desk`) MUST be followed by re-recording that tool's `help-hashes.json` entry in the same
change; otherwise the `claude-extended-tool-approver-spec-help-drift` check fails with
`--help drift: recorded <old>, live <new>`. The step is:

1. compare the tool's new `--help` with its embedded spec and update the spec (and its citations)
   if a subcommand or flag it models was added, renamed or removed;
2. replace that tool's entry in `internal/embeddedspecs/data/help-hashes.json` with the `live`
   hash from the failure line (a one-line change; the hash is platform-independent for a tool
   whose help does not vary by OS), or run `spec-drift-check --embedded --record` in the pinned
   environment. On a non-linux host `--record` writes only a `help-hashes.<goos>.json` overlay,
   never the shared baseline.

The check MUST NOT be weakened to hash only part of a tool's help (for example only `Usage:` and
`Flags:`): the subcommand list is what determines which subcommands the spec must model, so
excluding it would hide exactly the drift the check exists to catch. The check's failure output
repeats these steps (`specdrift.RecordHint`).

## 4. Goldens pass

**Tool/runner**: **no goldens-sidecar runner is wired** (the practical stand-in is below).
tc-o14i5.4.1's `SKILL.md` ("Generated goldens"
section) defines the per-command JSON FORMAT (one file per command, `approve`/`reject`/
`not-approve`/`context-dependent` verdicts per flag role, re-derived from
`internal/effectpolicy` at generation time) and the STORAGE location (a sibling root — e.g.
`internal/embeddedspecs/data/goldens/<name>.goldens.json` for built-ins — never a subdirectory
of the spec layer itself, per that file's "loadLayer footgun" section). It does **not** wire
any CLI subcommand, test, or `nix flake check` entry that verifies a goldens file against
current policy output; "Generation procedure" in `SKILL.md` is a documented MANUAL derivation
(map role → effect kind → judging `Policy` → verdict), not an automated one. This was
confirmed empirically by tc-o14i5.4.4 (`grep -rln goldens internal/ cmd/` finds no runner
outside `internal/goldencorpus`/`internal/effectpolicy/testdata`, which are both distinct,
pre-existing mechanisms the SKILL.md text itself says do NOT store this shape).

**Today, satisfying this gate means** (all of the following MUST hold; re-verified against the
current tree for pg2-cr59k):

1. `go test ./...` from the module root passes (run it in the background or with an explicit
   multi-minute timeout). It is the practical stand-in, with this precision about what it checks:
   - `TestRoundTrip` (`internal/embeddedspecs`): the embedded JSON equals
     `cmddesc.DefaultRegistry()` exactly (schema facts only; citations are not compared).
   - `TestCorpusWellFormed` (`internal/goldencorpus`): `corpus.json` rows are STRUCTURALLY valid
     (case name, tool input, cwd, mode, expected verdict, tags). It does NOT replay or grade any
     verdict.
   - `TestPluginInstructedForms` and `TestGolden` (`internal/effectpolicy`) and the
     `internal/cmddesc/*_test.go` family tests ARE the verdict-bearing tests: they run commands
     through the real engine against the registry.
2. `go run ./cmd/claude-extended-tool-approver evaluate --corpus
internal/goldencorpus/testdata/corpus.json` is the only thing that grades the corpus
   verdicts against the engine. It exits 0 regardless of result, so the gate is the `Miss` count
   and case list (`--format json`), compared with the pre-change baseline: a change MUST NOT add
   a miss, and every row it adds MUST be `correct`. (At the time of pg2-cr59k the baseline
   was 565 correct, 26 miss, 8 not-comparable of 599 rows; a non-zero baseline is expected.)
3. If you author a per-flag goldens sidecar (the SKILL.md format), manually re-derive each verdict per
   "Generation procedure" against the CURRENT `internal/effectpolicy/policy.go` `Judge` logic.
   No file in the repo consumes a sidecar, and none exists under
   `internal/embeddedspecs/data/goldens/`; wiring a runner for the format is unstarted work, out
   of both tc-o14i5.4.1's and tc-o14i5.4.4's own scope.

## 5. P15 approve-reachability report attached

**Tool**: Phase 2 scope, tracking bead **tc-o14i5.3.10** ("Phase 2i: R6 push policy + P15
approve-reachability report") — open as of this writing (tc-o14i5.4.4's evaluation date).

**This docket's own finding** (tc-o14i5.4.4, Contract/Consumes): the changes tc-o14i5.4.1
through tc-o14i5.4.3 actually produced (the ceta-spec-gen skill itself, the `--help` drift
check, and the citation backfill for the 46 built-in specs) are citation/routing-only —
specs carry no verdicts per P14 ("Specs are FACTS, not policy"), so none of them makes any
`cmddesc.EffectKind` newly Permitted. This gate is therefore **inapplicable** to those three
packets' own changes; it applies to a FUTURE change that alters what gets approved (a new
`EffectPolicy` judgment, a widened allowlist, etc.), at which point the change's author
attaches Phase 2's P15 report (once tc-o14i5.3.10 lands) as part of landing that change.

## Provenance

Compiled by tc-o14i5.4.4 ("Phase 3.4: Skill evaluation"), 2026-09-29. See the evaluation
report posted as a comment on docket tc-o14i5.4 for the quantitative flag-role-agreement /
danger-flag-misclassification evaluation this checklist accompanies, and bead tc-6v2dm for
the danger-flag-check overbreadth finding that evaluation surfaced.
