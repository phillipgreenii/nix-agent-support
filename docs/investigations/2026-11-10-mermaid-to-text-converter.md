# Investigation: mermaid to box-drawing text converter (2026-11-10)

The key words MUST, MUST NOT, SHOULD, SHOULD NOT and MAY in this page are to be read as in RFC 2119.

**Status**: evaluation verdict and a recommendation. Nothing here is adopted or built into the repo's
nix code.

**Origin**: follow-up to the mermaid render spike in
`docs/investigations/2026-11-10-mod-render-hook-mermaid-and-images.md`, which found that no
mermaid-to-text converter exists in nixpkgs and that `graph-easy` reads its own syntax only. The
question: could a render hook show a mermaid fence as a box-drawing diagram in the transcript, with no
image support needed?

## Verdicts

| Diagram type     | Verdict            | One-line reason                                                                                      |
| ---------------- | ------------------ | ---------------------------------------------------------------------------------------------------- |
| Flowchart        | works with caveats | nodes, edges, edge labels and dotted edges come out readable; shapes and subgraphs are lost          |
| Sequence diagram | works with caveats | participants and message order are clear; alt/else/Note frames flatten to a text line, rows get long |
| State diagram    | works with caveats | all transitions present; self-loops are drawn as side loops and long labels stretch boxes very wide  |

Not adopted as a mod feature yet: the prototype handles only the syntax subset seen in this repo's
docs, and the output width (125 to 188 columns on the samples) exceeds a normal transcript width.

## How the tools were found and run

- **Search.** `nix search nixpkgs mermaid` lists `mermaid-cli`, `mermaid-filter`, `mdbook-mermaid`,
  `mermerd` and typst packages. All of them render pictures (SVG, PNG, PDF) or something unrelated; none
  emits text. `nix eval nixpkgs#mermaid-ascii` fails: the attribute does not exist. A converter that
  exists upstream outside nixpkgs (for example the Go `mermaid-ascii` project or npm packages) was
  **not evaluated: needs a non-nix install**.
- **Backend.** `graph-easy` 0.76 from nixpkgs (`nix shell nixpkgs#graph-easy`, fetched from the binary
  cache). It lays out a graph and writes Unicode box-drawing with `--as=boxart`.
- **Prototype.** A 92-line Python script, kept at
  `docs/investigations/2026-11-10-mermaid-to-text-prototype.py`. It parses a small subset of mermaid:
  for flowchart and state diagrams it translates the edges to `graph-easy` syntax and runs
  `graph-easy --as=boxart`; for sequence diagrams it draws the lifelines and arrows itself.
- **Run.** Everything ran in a scratch directory under `~/.cache/x9ltb1/` with paths passed as
  arguments:

```text
nix shell nixpkgs#python3 nixpkgs#graph-easy -c python3 -I m2t.py in/<sample>.mmd
```

- The samples are unmodified mermaid blocks taken from this repo's own docs. No network host other than
  the nixpkgs binary cache was contacted.

## 1. Flowchart: works with caveats

Source: the component diagram in `docs/superpowers/specs/2026-10-14-pg-keep-awake-design.md`.

Input:

```text
flowchart TD
  LD["launchd (keep-alive, AbandonProcessGroup)"] --> K["keeper (pg-keep-awake keeper)"]
  SH["pg-caffeinate-shim (installed as caffeinate)"] --> L[("leases dir (private)")]
  CL["pg-keep-awake acquire / release / run"] --> L
  PA["pa-monitor (via the CLI)"] --> CL
  K --> L
  K --> HB["keeper.heartbeat"]
  K --> AN["anchor (detached, own session)"]
  K --> CF["/usr/bin/caffeinate with the union flags and -w anchor-pid"]
  AN --> HB
  AN -. "exits at the release level or on watchdog timeout" .-> CF
  ST["pg-keep-awake status"] --> L
  ST --> SJ["status.json (written by the keeper)"]
```

Output (125 columns, 12 of 12 edges present, including the dotted edge with its label):

```text
                                                     ┌────────────────────────────────────────────────────────────┐
                                                     │         launchd (keep-alive, AbandonProcessGroup)          │
                                                     └────────────────────────────────────────────────────────────┘
                                                       │
                                                       │
                                                       ∨
┌──────────────────────────────────────────────┐     ┌──────────────────────────────────────────────────────────────────────┐
│               keeper.heartbeat               │ <── │                    keeper (pg-keep-awake keeper)                     │
└──────────────────────────────────────────────┘     └──────────────────────────────────────────────────────────────────────┘
  ∧                                                    │                                                             │    │
  │                                                    │                                                             │    │
  │                                                    ∨                                                             │    │
  │                                                  ┌────────────────────────────────────────────────────────────┐  │    │
  └───────────────────────────────────────────────── │               anchor (detached, own session)               │  │    │
                                                     └────────────────────────────────────────────────────────────┘  │    │
                                                       :                                                             │    │
                                                       : exits at the release level or on watchdog timeout           │    │
                                                       ∨                                                             │    │
                                                     ┌────────────────────────────────────────────────────────────┐  │    │
                                                     │ /usr/bin/caffeinate with the union flags and -w anchor-pid │ <┘    │
                                                     └────────────────────────────────────────────────────────────┘       │
┌──────────────────────────────────────────────┐     ┌────────────────────────────────────────────────────────────┐       │
│     status.json (written by the keeper)      │ <── │                    pg-keep-awake status                    │       │
└──────────────────────────────────────────────┘     └────────────────────────────────────────────────────────────┘       │
                                                       │                                                                  │
                                                       │                                                                  │
                                                       ∨                                                                  ∨
┌──────────────────────────────────────────────┐     ┌──────────────────────────────────────────────────────────────────────┐
│ pg-caffeinate-shim (installed as caffeinate) │ ──> │                         leases dir (private)                         │
└──────────────────────────────────────────────┘     └──────────────────────────────────────────────────────────────────────┘
                                                                                                                     ∧
                                                                                                                     │
                                                                                                                     │
                                                     ┌────────────────────────────────────────────────────────────┐  │
                                                     │                  pa-monitor (via the CLI)                  │  │
                                                     └────────────────────────────────────────────────────────────┘  │
                                                       │                                                             │
                                                       │                                                             │
                                                       ∨                                                             │
                                                     ┌────────────────────────────────────────────────────────────┐  │
                                                     │           pg-keep-awake acquire / release / run            │ ─┘
                                                     └────────────────────────────────────────────────────────────┘

```

Second sample, a 14-node decision flow in
`docs/superpowers/specs/2026-11-02-ccpool-janitor-progress-tracking-design.md` (166 columns). The first
part of its output:

```text
                                                        ┌─────────────────────────────────────────────────────────────────────────────────────────────────────┐
                                                        │                                  Run starts (launchd, every 600 s)                                  │
                                                        └─────────────────────────────────────────────────────────────────────────────────────────────────────┘
                                                          │
                                                          │
                                                          ∨
┌───────────────────────────────────────────────┐  No   ┌─────────────────────────────────────────────────────────────────────────────────────────────────────┐
│        Record outcome skipped and exit        │ <──── │                                      Single-flight lock free?                                       │
└───────────────────────────────────────────────┘       └─────────────────────────────────────────────────────────────────────────────────────────────────────┘
                                                          │
                                                          │ Yes
                                                          ∨
                                                        ┌─────────────────────────────────────────────────────────────────────────────────────────────────────┐
                                                        │                           Sort roles by name, start at the rotated index                            │
                                                        └─────────────────────────────────────────────────────────────────────────────────────────────────────┘
                                                          │
                                                          │
                                                          ∨
                                                        ┌─────────────────────────────────────────────────────────────────────────────────────────────────────┐
                                                        │                              RUN TIMER starts: 240 s for the whole run                              │
                                                        └─────────────────────────────────────────────────────────────────────────────────────────────────────┘
                                                          │
                                                          │
                                                          ∨
                                                        ┌─────────────────────────────────────────────────────────────────────────────────────────────────────┐
                                                        │                               Take the next role, one role at a time                                │ <┐
                                                        └─────────────────────────────────────────────────────────────────────────────────────────────────────┘  │
                                                          │                                                                                                      │
                                                          │                                                                                                      │
                                                          ∨                                                                                                      │
┌───────────────────────────────────────────────┐  No   ┌─────────────────────────────────────────────────────────────────────────────────────────────────────┐  │
│ Role is skipped and recorded in roles_skipped │ <──── │                                           Run time left?                                            │  │
└───────────────────────────────────────────────┘       └─────────────────────────────────────────────────────────────────────────────────────────────────────┘  │
  │                                                       │                                                                                                      │
  │                                                       │ Yes                                                                                                  │
  │                                                       ∨                                                                                                      │
```

Caveats found:

- Node shapes are lost: a decision `{...}` and a cylinder `[(...)]` both become a plain box.
- A `subgraph` is not supported. On the second diagram in that same janitor document (a `flowchart LR`
  with two subgraphs) the prototype printed only the group boxes and three of the member nodes
  (`K4`, `K1`, `K2`) under their bare ids, and dropped the member labels. Verdict for subgraphs:
  does not work.
- Layout is chosen by `graph-easy`, not mermaid: the order of nodes differs from the source order, and
  back-edges are drawn as long side rails that are readable but easy to mistake for forward flow.
- Width follows the longest label. Labels of 100 characters give a 166-column diagram.

## 2. Sequence diagram: works with caveats

Source: the sequence diagram in `docs/pr-review-flow.md`.

Input:

```text
sequenceDiagram
    participant Op as operator/scheduler
    participant ACL as pg-router reconcile
    participant PGPR as pg-pr (data)
    participant BD as bead store
    participant Role as ccpool review role
    participant GH as GitHub

    Op->>ACL: pg-router reconcile (pre-drain)
    ACL->>PGPR: pg-pr pr list --json (network-free, from store)
    PGPR-->>ACL: open PRs {repo, number, ownership, draft, head_sha, branch, last_synced_at, stale}
    ACL->>ACL: drop rows flagged stale (refuse to act, WARN)
    ACL->>BD: ensure review-pr bead (metadata incl. ownership) + pg-pr:active-pr gate (idempotent)
    Op->>Role: pg-router drain
    Role->>BD: claim ready review-pr bead
    Role->>GH: git fetch pull/N/head; checkout head_sha (in scratch worktree)
    alt ownership is mine or co-owned
        Role->>BD: bd create process-feedback: repo#n (label mine)
    else ownership is team
        Role->>PGPR: pg-pr review submit (post PENDING)
        PGPR->>GH: PostReview (PENDING, commit_id anchored)
    end
    Role->>BD: close review-pr bead
    ACL->>BD: (next pass) reopen review-pr if head_sha or ownership advanced
    Note over BD: process-feedback bead then flows through the<br/>UNCHANGED feedback role -> work bead -> worker role chain
```

Output (172 columns):

```text
   operator/scheduler      pg-router reconcile        pg-pr (data)             bead store          ccpool review role            GitHub
            │ pg-router reconcile (pre-drain)               │                       │                       │                       │
            ├───────────────────────►                       │                       │                       │                       │
            │                       │ pg-pr pr list --json (network-free, from store)                       │                       │
            │                       ├───────────────────────►                       │                       │                       │
            │                       │ open PRs {repo, number, ownership, draft, head_sha, branch, last_synced_at, stale}            │
            │                       ◄╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌┤                       │                       │                       │
            │                       │                       │                       │                       │                       │
            │                       │─┐ drop rows flagged stale (refuse to act, WARN)                       │                       │
            │                       │◄┘                     │                       │                       │                       │
            │                       │ ensure review-pr bead (metadata incl. ownership) + pg-pr:active-pr gate (idempotent)          │
            │                       ├───────────────────────────────────────────────►                       │                       │
            │ pg-router drain       │                       │                       │                       │                       │
            ├───────────────────────────────────────────────────────────────────────────────────────────────►                       │
            │                       │                       │                       │ claim ready review-pr bead                    │
            │                       │                       │                       ◄───────────────────────┤                       │
            │                       │                       │                       │                       │ git fetch pull/N/head; checkout head_sha (in scratch worktree)
            │                       │                       │                       │                       ├───────────────────────►
  [alt ownership is mine or co-owned]                       │                       │                       │                       │
            │                       │                       │                       │ bd create process-feedback: repo#n (label mine)
            │                       │                       │                       ◄───────────────────────┤                       │
  [else ownership is team]          │                       │                       │                       │                       │
            │                       │                       │ pg-pr review submit (post PENDING)            │                       │
            │                       │                       ◄───────────────────────────────────────────────┤                       │
            │                       │                       │ PostReview (PENDING, commit_id anchored)      │                       │
            │                       │                       ├───────────────────────────────────────────────────────────────────────►
  [end]     │                       │                       │                       │                       │                       │
            │                       │                       │                       │ close review-pr bead  │                       │
            │                       │                       │                       ◄───────────────────────┤                       │
            │                       │ (next pass) reopen review-pr if head_sha or ownership advanced        │                       │
            │                       ├───────────────────────────────────────────────►                       │                       │
  [Note over BD: process-feedback bead then flows through the<br/>UNCHANGED feedback role -> work bead -> worker role chain]        │
            │                       │                       │                       │                       │                       │
```

Caveats found:

- Participants, aliases (`participant X as Name`), message order, solid and dashed arrows and
  self-messages are correct.
- `alt`, `else`, `end` and `Note` become one bracketed line each with no frame around the branch, so
  the conditional structure is only hinted at. A `Note over` line keeps its raw `<br/>` text.
- Message text is not wrapped: it overflows the neighbouring lifelines and the longest rows are wider
  than the header row.
- Not implemented in the prototype: `autonumber`, `activate`, `loop`/`par` frames, `rect`, and nested
  blocks. A real converter would have to add each.

## 3. State diagram: works with caveats

Source: the state machine in `packages/beads-exporter/README.md`.

Input:

```text
stateDiagram-v2
    [*] --> Up: first run ok, or failures below the threshold
    Up --> Up: failed run, count below N (counter rises, up stays 1)
    Up --> Down: failed run, count reaches N (up = 0)
    Down --> Down: failed run
    Down --> Up: successful run (count resets to 0)
    Up --> Up: successful run (count resets to 0)
```

Output (188 columns, all 6 transitions present):

```text
                                         ┌────────────────────────────────────────────────┐
                                         │                    (start)                     │
                                         └────────────────────────────────────────────────┘
                                           │
                                           │ first run ok, or failures below the threshold
                                           ∨
    successful run (count resets to 0)   ┌───────────────────────────────────────────────────────────────────────────────────────┐   failed run, count below N (counter rises, up stays 1)
  ┌───────────────────────────────────── │                                                                                       │ ────────────────────────────────────────────────────────┐
  │                                      │                                          Up                                           │                                                         │
  └────────────────────────────────────> │                                                                                       │ <───────────────────────────────────────────────────────┘
                                         └───────────────────────────────────────────────────────────────────────────────────────┘
                                           │                                                 ∧
                                           │ failed run, count reaches N (up = 0)            │ successful run (count resets to 0)
                                           ∨                                                 │
    failed run                           ┌────────────────────────────────────────────────┐  │
  ┌───────────────────────────────────── │                                                │  │
  │                                      │                      Down                      │  │
  └────────────────────────────────────> │                                                │ ─┘
                                         └────────────────────────────────────────────────┘

```

Caveats found:

- `[*]` is shown as a `(start)` box; a final `[*]` would be `(end)` (the same input shape was not
  present in this sample, so the end box is not shown here).
- Self-transitions (`Up --> Up`, `Down --> Down`) are drawn as loops on the box sides. They read
  correctly once you know the idiom but are the hardest part of the picture to follow.
- A long transition label widens the whole box (the `Up` box here), which is why this small diagram is
  188 columns wide.
- Composite states, `<<choice>>`, `<<fork>>` and notes are not handled.

## Recommendation

1. Text conversion is feasible for flowchart and state diagrams by delegating layout to `graph-easy`,
   and for sequence diagrams by a small bespoke renderer. A mod that wants this SHOULD treat the
   prototype as a proof of the approach, not as the implementation.
2. A converter used inside a render hook MUST fall back to showing the original mermaid source for any
   construct it cannot convert (subgraphs, composite states, sequence frames). The prototype instead
   drops or flattens them silently, which is not acceptable in a shipped hook.
3. A converter MUST bound its output width (wrap labels, or fall back to source when the result exceeds
   the terminal width). All four samples exceeded 120 columns.
4. The runtime dependency is the Perl `graph-easy` package; a mod MUST NOT assume it is on `PATH`
   without the repo's nix packaging providing it. Adding it is a separate decision and is not made here.
5. If the operator wants pictures rather than text, the image route in the earlier investigation
   remains the alternative; this evaluation does not change that verdict.
6. No source here is a candidate for the repo's nix code. The prototype stays under
   `docs/investigations/` as evidence only.
