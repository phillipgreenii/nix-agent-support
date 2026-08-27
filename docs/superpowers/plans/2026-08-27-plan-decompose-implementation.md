# plan-decompose — implementation plan

> Executes `docs/superpowers/specs/2026-08-27-plan-decompose-design.md` revision 2 (the design
> of record; section references below are into that file). Bead: `pg2-98dt2`. Repo root:
> `phillipgreenii-nix-agent-support`. All content is markdown — no Go, no nix module changes;
> the marketplace derivation discovers everything by directory convention (design §5.1).

## Global constraints

- Every skill/agent file MUST follow the design's D-rulings verbatim; where plan and design
  disagree, the design wins and this plan gets amended (S-2 style, superseded text struck).
- Policy language in the skills MUST use RFC 2119 keywords.
- No model name or token budget may appear as a hardcoded requirement anywhere except (a) the
  documented fallback default stated once in the core skill and (b) the `packet-implementer`
  frontmatter default that mirrors it (design D9, §10.2).
- Validation per task is change-scoped (`prek run --files <changed>`); the full
  `nix flake check` runs once at the end (L-1: background with explicit timeout).

## Task 0 — Build-time empirical probes (design §16)

Against a scratch bead (created and deleted in this task; never touch real beads):

1. The exact JSON path of the metadata field in `bd show <id> --json` output.
2. Whether `bd create` can create DEFERRED in one call; if not, record the create-then-defer
   window as a known limitation in the binding skill.
3. Whether `bd lint` reads the dedicated `--acceptance` field or only description headings.
4. Stamp-check cost: `read-metadata` output size on a docket with a large DESIGN field —
   MUST be bounded (< ~500 tokens) independent of design size.

Record all four results IN the binding skill text where each mechanism is used (P-2 style
provenance: command + date). These probes gate Task 3's wording.

## Task 1 — Plugin scaffold

**Files:**

- Create: `claude-marketplace/plan-decompose/.claude-plugin/plugin.json`
- Modify: `claude-marketplace/.claude-plugin/marketplace.json` (one entry: name,
  `source: "./plan-decompose"`, description)

**Steps:** copy the shape of `claude-marketplace/bead-grooming/.claude-plugin/plugin.json`
(name `plan-decompose`, version `0.1.0`, `defaultEnabled: true` — the marketplace norm; only
pg-pr opts out). Validate: `jq .` parses both files; plugin name matches directory.

## Task 2 — Core skill `plan-decompose`

**Files:**

- Create: `claude-marketplace/plan-decompose/skills/plan-decompose/SKILL.md`

**Content contract (from design §§4–9, §14):** frontmatter `name`/`description` (triggers:
decompose a design/plan into beads/issues/tickets/work packets, "split this plan into
implementation issues", reconcile a docket after a design amendment, "is this plan ready to
decompose"; anti-triggers: designs below the §9.2 floor — fewer than 3 anticipated packets ⇒
file beads directly; grooming existing issues → bead-grooming; working the queue → drain).
Body sections: concepts (docket/packet/all-in budget, §4 identification rules); the abstract
medium contract table (§5.2) with the D7 binding-resolution rule (named binding, else
auto-select a sole installed binding with announcement, else refuse with candidates); modes
`check` / `decompose` / `reconcile` (§8.1–§8.11 verbatim procedure: boundary sketch,
pre-check + durable gap report, dedup/resume via `pd-phase` and `pd-source`, HELD-as-deferred,
mechanical pre-filter, cold-read with re-check-on-edit, semantic post-check, §8.10 loop
bounds, §8.7 abort path, §8.11 reconcile ordering and claimed-packet handling); packet anatomy
(§6, nine parts, §6.1 citation syntax); metadata keys + sizing resolution (§7, D9); cost
model + floor (§9); caching/hoisting rules (§12); metric record format (§7/§13, gather-only);
a Usage section with one worked example per mode (§14). The fallback sizing default
(`sonnet`, `250k`, read-target `25`) appears here ONCE, labeled as the fallback.

**Validate:** `prek run --files` on the new file; a cold read by a fresh subagent confirms the
procedure is followable without this plan or the design open (the skill must stand alone).

## Task 3 — Beads binding skill `plan-decompose-beads`

**Files:**

- Create: `claude-marketplace/plan-decompose/skills/plan-decompose-beads/SKILL.md`

**Content contract (from design §11, updated by Task 0's probe results):** frontmatter
description (triggers when plan-decompose needs its beads binding; names the abstract ops it
implements). Body: the §11 operation table verbatim, including `find-docket` via the `docket`
label + `pd-source` metadata; native metadata commands (`--metadata`, `--set-metadata`, JSON
read path from Task 0); create-deferred (or create-then-defer) with `--no-inherit-labels`;
the 65,535-byte DESIGN-field chunking rule with the numbered chunk index; `bd undefer` release
sweep with `pd-phase` progress; `write-report`/`append-metric` via comments; docket-scoped
filtering of the global `bd dep cycles` output; the S-2 amend procedure for plain and chunked
designs; `close-docket` semantics; and citations (not restatements) of the workspace's
standing bd hygiene rules.

**Validate:** every abstract operation named in the core skill's §5.2 table has exactly one
mapping row here, with the SAME row grouping as §5.2 (slash-paired operations share a row);
`prek run --files`.

## Task 4 — Agent `plan-decomposer`

**Files:**

- Create: `claude-marketplace/plan-decompose/agents/plan-decomposer.md`

**Frontmatter:** `name: plan-decomposer`; description (executes the plan-decompose procedure
against a named binding; dispatched with design source, binding, absolute repo roots, docket
metadata, and a tracking bead for gap reports); `tools: Bash, Read, Glob, Grep` (matching the
pg-pr agents' tool shape); no `model:` line — inherits the session model, justified by design
§10.1 (judgment-heavy), NOT by pg-pr precedent (pg-pr agents all pin `model: sonnet`).

**Body:** the §8 pipeline as the agent's operating procedure, including the §8.7 abort path
and §8.10 loop bounds; the D4 charter (split and curate, never author; §6.1 citations; gap
report is the primary halt); MUST NOT edit the plugin's own sources (hoisting flags are
advisory report entries); and FIXED sub-dispatch templates so implementations cannot diverge:

- Cold-reader brief template (cheap model, read-only): input = packet content ONLY; output
  schema = `executable: yes|no` + `missing:` list.
- Semantic post-check brief template (mid model, read-only): input = design + all packet
  contents + the planned ordering recorded during curation (design §8.2); output schema =
  findings list, each `packet(s)`, `check: coverage|seam`, `evidence`, `proposed-fix`.
- The decomposition-report format (§8.6 contents).

**Validate:** frontmatter parses (matches pg-pr agents' shape); `prek run --files`.

## Task 5 — Agent `packet-implementer`

**Files:**

- Create: `claude-marketplace/plan-decompose/agents/packet-implementer.md`

**Frontmatter:** `name: packet-implementer`; description (works ONE claimed packet
packet-first; stamp check; escalation ladder; metric closeout); `tools` unrestricted (it
implements code); `model: sonnet` — the static default mirroring the core skill's documented
fallback, overridable at dispatch from docket policy (design §10.2; sanctioned by this plan's
Global Constraints exception (b)).

**Body:** the §10.2 discipline verbatim: claim-with-actor; stamp check as two metadata reads
(`pd-curated-rev` vs docket `pd-rev`, `pd-stale` unset), mismatch ⇒ re-defer + `pd-stale` +
stop; one content read; change-scoped validation only; the three-step escalation ladder with
`pd-stale` re-check and recorded docket reads; never guess across a contract seam; never read
sibling packets; closeout `pd-stale` re-check + metric record (§7 fixed key order) +
close-or-release hygiene.

**Validate:** `prek run --files`; cross-check that every discipline item the design hoists out
of packet content (§12.1) appears here — the hoisting audit's target must exist.

## Task 6 — ADR

**Files:**

- Create: `docs/adr/<next-free>-plan-decompose-layered-medium-binding.md`

Short ADR restating the D6/D7/D9/D11 decisions SELF-CONTAINED (new plugin; medium-agnostic
core + binding skills with no silent default; native-metadata channel; deferred-held packet
lifecycle) so the ADR stands without the spec (per this repo's citation conventions, which
disfavor load-bearing pointers into design specs); it MAY cite
`docs/superpowers/specs/2026-08-27-plan-decompose-design.md` as provenance, noting that file
is repo-committed by operator ruling (not an ephemeral workspace-root spec). The ADR number
MUST be recomputed at land time (F-8), not trusted from drafting.

## Task 7 — Repo-level validation

- `git add -A` in the worktree, then `prek run --files <all changed files>`.
- `nix flake check` in the worktree: background via `nohup`, explicit timeout, watched with
  Monitor (never re-issued unchanged after a timeout).
- Commit (branch `plan-decompose`); landing via the `integrate-branch` skill (R-9) when the
  operator says land.

## Task 8 — Follow-up beads (filed, not worked here)

1. Dogfood acceptance run: decompose a real approved design end-to-end and work ≥1 packet with
   `packet-implementer`; gather the first metric records (design §16).
2. Report/aggregation mode over `read-metrics` (design D10, §13) — scoped by docket, paginated.
3. Decide whether `/drain-beads` should prefer `packet-implementer` for beads carrying
   curation metadata (design §17; a `pb` change, operator decision).
4. Tune `pd-read-target` and the §9.2 floor against gathered metrics (design §17).
5. Diff-based `amend-design` (design §17) — a patch form to cut the reconcile resupply cost
   for large designs.
