# pg-ccaudit's mistake-census tiers are built in-house; only the taxonomies are adopted

**Status**: Accepted (resolves the DECISION REQUIRED in `pg2-oisvb`)
**Date**: 2026-08-14
**Deciders**: Phillip Green II

## Context

`pg2-oisvb` extends `pg-ccaudit` from a command-failure census into a **mistake** census: detect
agent errors that were never failed commands, detect corrections a person had to issue, and route
every confirmed finding to the artifact that can actually fix it. Its notes carry an explicit
decision gate, to be settled **before writing code**:

> whether to adopt AgentDebugX + CLEAR as the Tier 2/3 engine and build only the correction-detection
> front end, or build Tier 2/3 in-house.

Four external candidates were named:

| Candidate         | What it is                                                                  | Distribution       |
| ----------------- | --------------------------------------------------------------------------- | ------------------ |
| AgentDebugX       | Detect → Attribute → Recover → Rerun loop over agent trajectories           | `pip install`, MIT |
| IBM Agentic CLEAR | Trace evaluation and failure-pattern discovery with a dashboard             | service + web UI   |
| MAST              | 14 failure modes in 3 categories                                            | a paper            |
| FALAT             | 4-role step attribution (root cause / propagation / symptom / contributing) | a paper            |

`pg-ccaudit` is a **Go module on the gomod2nix engine** inside a nix flake
(`phillipg-nix-repo-base` ADR 0008, Pattern A). It has no `vendorHash`, no local `replace`, and its
whole dependency surface is `go.mod` plus a committed `gomod2nix.toml`.

## Decision

**Build Tier 2 and Tier 3 in-house. Adopt the TAXONOMIES, not the code.**

1. Tier 2's semantic pass is `internal/classify`, which shells out to the **Claude Code CLI already
   declared and managed by this flake** (`claude -p --output-format json`). No new language runtime,
   no HTTP client, no API-key surface, no second place a model id is configured.
2. Tier 3's routing is a table in `internal/route`, keyed on the Tier 2 class and on `is_sidechain`.
3. MAST's categories are adopted as the class vocabulary: `specification-miss` is its "specification
   issues", `verification-miss` its "task verification". They cost nothing to adopt because they are
   prose, not a package.
4. `AgentDebugX` and `CLEAR` are **not** dependencies of this repository.

## Consequences and rationale

### AgentDebugX does not detect the half this bead exists for

Its own scope is AGENT-INTERNAL failure: planning, memory, tool use, verification, coordination. It
does not detect user feedback or external corrections. A correction a person typed is the strongest
evidence in this corpus — it means nothing in the harness caught the mistake: no exit code, no
schema, no hook — and it is precisely what AgentDebugX leaves to the caller. Adopting it would
therefore buy machinery for the cheaper half while the expensive half still had to be written here.

### The dependency cost is a whole new packaging pattern, not one line in a lock file

`pip install agentdebugx` inside a Go/gomod2nix package means: a Python runtime in this flake's
package set for one consumer; a hand-written derivation for `agentdebugx` and every transitive PyPI
dependency, because it is not in nixpkgs; a second lock surface with its own update path alongside
`update-gomod2nix-deps.sh`; and a second language in a package whose entire build contract today is
"`go.mod` and `gomod2nix.toml` sit side by side at the package root".

The blast radius is best judged against the precedent already set in this package. `pg2-xnnab`
**declined to reuse `claude-transcript`** — a Go library, in this same repository, already built by
this same flake — because its types omitted the fields the index needed and its reader had no
byte-offset resume. If that reuse did not clear the bar, a cross-language dependency for a narrower
benefit cannot.

### CLEAR is the wrong shape and the wrong input

It evaluates OpenTelemetry GenAI agent traces and presents them in a dashboard. This corpus is
append-only JSONL transcripts, and the deliverable is a ranked text report an agent reads inside a
skill. A dashboard is not consumable by the review skill, and a service is not something a
`nix flake check` can gate.

Both tools' own stated limits also matter: AgentDebugX's taxonomy induction is implemented but
UNEVALUATED, and its scrubber redacts known credential patterns only. This corpus contains real
paths, real ticket ids and real prose from a live workstation, so an unevaluated scrubber is not a
property to inherit.

### What is given up, and why it is affordable

Not adopting AgentDebugX gives up a ready-made 19-mode taxonomy, a Recover/Rerun loop, and
attribution machinery. The taxonomy is replaced by MAST's, adopted here at zero cost. The
Recover/Rerun loop is out of scope by the bead's own non-goals — this is retrospective analysis
feeding instruction changes, explicitly not real-time intervention — so it would have been dead
weight. Attribution across steps is what Tier 3's routing table does, and it needs one fact
(`is_sidechain`) that this index already has and that a general framework would not know about.

### The in-house Tier 2 must earn its cost, and that is measured

Choosing to build carries the risk of building something worse than the naive alternative, so the
naive alternative SHIPS as `--classifier baseline`: "every typed turn following a tool call is a
correction". `pg-ccaudit evaluate` runs both over the same gold set and **exits non-zero** if the
semantic classifier does not beat it on correction F1. Measured 2026-08-14 over 63 hand-labelled
candidates:

| Classifier                       | correction precision | correction recall | correction F1 | accuracy |
| -------------------------------- | -------------------: | ----------------: | ------------: | -------: |
| baseline (the naive rule)        |                0.143 |             0.067 |         0.091 |    0.413 |
| `claude -p` (`claude-haiku-4-5`) |                1.000 |             0.267 |         0.421 |    0.581 |

Run cost for that pass: 7 calls, $0.4933, 3m 26s.

The comparison is made on the CORRECTION binary rather than on multi-class accuracy, because a
classifier that answers `not-a-mistake` to everything scores well on accuracy — the class is the
majority — while finding nothing.

## What this ADR does NOT do

- It does not forbid a future external dependency. It records that adopting one is an **operator
  decision about this flake's dependency surface**, not an implementation choice, and that on the
  evidence above the in-house path is cheaper and covers more of the requirement.
- It does not claim the in-house classifier is better than AgentDebugX at what AgentDebugX does. It
  claims AgentDebugX does not do the load-bearing half, and that the half it does do is reachable
  here with a binary this flake already ships.
- It does not settle whether the semantic pass should run over the WHOLE candidate set. At the
  measured rate a full pass over ~2,100 candidates is roughly 210 calls, so the default classifier
  stays `baseline` and the semantic pass is invoked with an explicit window or `--max`.
