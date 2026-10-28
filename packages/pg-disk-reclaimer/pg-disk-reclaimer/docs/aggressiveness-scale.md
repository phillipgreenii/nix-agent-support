# pg-disk-reclaimer aggressiveness scale

This document is the single authoritative definition of the `aggressiveness`
scale (levels 0 to 5) used by every `pg-disk-reclaimer` registry variant and by
the `--aggressiveness N` option of `list` and `reclaim`. The `--help` text, the
tldr page, and the `aggressiveness` Nix option description all refer here
rather than restating the levels.

The key words "MUST", "MUST NOT", "SHOULD", "SHOULD NOT", and "MAY" in this
document are to be interpreted as described in RFC 2119.

## Semantics

- `--aggressiveness N` is a CEILING, not an exact match. An item is selected
  when it has at least one variant whose level is `<= N`. When an item has
  several qualifying variants, `reclaim` MUST run only the one with the HIGHEST
  level `<= N`, so raising the ceiling escalates an item from its mild variant
  to its more aggressive one.
- `N` is a non-negative integer. A registry variant's level MUST be a
  non-negative integer and MUST be unique within its item.
- `reclaim` is a dry run unless `--apply` is given.
- Confirmation gate: one run prompts AT MOST ONCE, and that one answer gates
  the whole run.
  - `reclaim --apply` MUST ask for interactive confirmation when any selected
    variant's level is `>= 4`. It MUST ask before any `removeCommand` runs, and
    it MUST NOT prompt at any other point of the run.
  - Before the prompt, `reclaim` MUST print the plan: each selected item with
    its size. The prompt MUST be visible on the controlling terminal, and the
    answer MUST be read from that terminal, never from stdin. Because the plan
    goes to standard output, which MAY be redirected to a file or a pager while
    the prompt goes to the terminal, the prompt text itself MUST name the item
    count and each level `>= 4` item with its size.
  - Only `y`, `yes` and their upper-case forms (`Y`, `YES`, `Yes`) confirm.
    Anything else is a decline: an empty answer, `n`, `no`, `yep`, a leading or
    trailing space, trailing words. End of input (Ctrl-D) is a decline too, and
    the "not confirmed" line then starts on its own line.
  - A declined confirmation MUST run nothing and exit with status 2. When a
    confirmation is needed but there is no controlling terminal, `reclaim` MUST
    say so, run nothing and exit with status 3; it MUST NOT report that as a
    decline. A bad explicit item id does not change either status: a decline or
    no terminal still exits 2 or 3, never 1.
  - A selected item whose `path` does not exist is skipped before the plan, so
    it neither triggers the prompt nor counts toward it.
  - A dry run MUST NOT prompt at any level, and neither MUST an `--apply` run
    whose selection has no variant at level `>= 4`.
  - There MUST NOT be any flag, environment variable, or non-interactive path
    that bypasses the gate.
- Commands never prompt. A registry `displayCommand`, `sizeCommand`,
  `heldSizeCommand`, `dryRunCommand` and `removeCommand` MUST NOT ask for
  input. `reclaim` and `list` run each with standard input from `/dev/null`, so
  a command that tries to read standard input fails instead of hanging. That
  guarantee covers standard input ONLY: a command that opens `/dev/tty` itself
  (`sudo`, `ssh`, an `osascript` call that raises a dialog) can still reach the
  terminal, and a registry author MUST NOT write one.
- Sizing: a dry run sizes each item just before printing it, so its first line
  appears at once. `--apply` sizes every item first, because the plan and the
  one prompt need them all. Each `removeCommand` and `dryRunCommand` runs in its
  own subshell, so a `cd` or a variable it sets never reaches a later item.
- The gate level is the code constant `PGDR_CONFIRM_GATE_LEVEL` in
  `pg-disk-reclaimer.bash`. The marker line below MUST equal that constant; the
  bats suite `tests/test-pg-disk-reclaimer-scale-docs.bats` fails when they
  differ.

<!-- pgdr-confirm-gate-level: 4 -->
<!-- pgdr-confirm-exit-declined: 2 -->
<!-- pgdr-confirm-exit-no-terminal: 3 -->

```mermaid
flowchart LR
    L0["0 tidy cruft"] --> L1["1 tidy"]
    L1 --> L2["2 caches"]
    L2 --> L3["3 deep"]
    L3 --> G{"confirmation gate"}
    G --> L4["4 costly or irreversible"]
    L4 --> L5["5 hardest to recover"]
```

A run whose selection stops left of the gate runs under `--apply` without a
prompt. A selection that includes level 4 or 5 prompts once, before anything
runs, for the whole run.

## Levels

### Level 0: tidy cruft

Clutter whose removal costs nothing, such as stray backup or temporary files. A
level 0 variant MUST NOT remove anything that is regenerated, referenced, or
valued.

### Level 1: tidy

Work that is already finished elsewhere, such as merged or stale worktrees and
branches. There is nothing to regenerate. A level 1 variant SHOULD be guarded so
that anything with uncommitted or unmerged work is left alone.

### Level 2: caches

Small or cheap regenerable caches. A cache miss refills in seconds, with no
meaningful network use. The plain (non-forfeiting) garbage collection of a
content-addressed store belongs here.

### Level 3: deep

Regenerable from local disk with no network access, but the refill is
expensive: minutes of CPU, large trees, or slowed concurrent builds. A Go build
cache is the reference case.

The 2-versus-3 boundary is the cost of refilling. A variant at level 2 refills
in seconds. A variant at level 3 refills in minutes or more, or makes
concurrent work noticeably slower while it refills.

### Level 4: costly or irreversible

Any of the following puts a variant at level 4 or above:

- regeneration needs the network (for example a module or package download
  cache), so it costs bandwidth and time and can fail offline;
- the removal forfeits rollback or history (for example deleting old system
  generations or compacting history);
- the removal touches live shared state that other processes depend on.

Level 4 is the first level behind the confirmation gate.

### Level 5: hardest to recover

Loss of user-visible state that is slow and manual to rebuild, such as an IDE
index or a scrub of caches for software that is currently installed. A level 5
variant is the last resort.

The 4-versus-5 boundary is who rebuilds. A level 4 variant is rebuilt
automatically, at a cost the operator can accept. A level 5 variant leaves the
operator waiting on, or redoing, work by hand.

## Rules for registry authors

- Every registry item MUST have a level for each variant, and each level MUST
  be justified by a one-line reason.
- The reason MAY be the variant description when the level is obvious from the
  definitions above. When the choice is not obvious (for example a network cost
  that pushes a cache from level 3 to level 4, or two similar caches at
  different levels), the registry source MUST carry a comment that states the
  reason.
- An item MAY offer several variants at different levels. A milder variant
  SHOULD be the lower level and a more destructive variant of the same area
  SHOULD be the higher level.
- A variant MUST NOT be placed at a lower level than the definitions above
  require just to avoid the confirmation gate.
