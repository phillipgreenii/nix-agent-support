# pg-wrap — behavior docs

pg-wrap is a **standalone wrapper tool**. Given a **wrap spec** and a **payload**, it applies an
ordered chain of command prefixes around the payload, builds the environment the chain starts from,
and hands control to the chain, which ends in the payload. It is a Decorator (chain-loading, as in
`chpst`): every layer execs the next, so nothing of pg-wrap stays resident around the payload. This
set follows the behavior-docs method (`phillipgreenii-nix-agent-support · behavior-docs/docs/behavior`).

Start here, then the [glossary](glossary.md); the rules are in [invariants](invariants.md), the
boundaries in [interfaces](interfaces.md), the actors in [actors](actors.md), and the stories, use
cases and journeys in [journeys](journeys.md).

## The model

The diagram below **is** this set's interface inventory (`INV-8`), not a data-flow diagram and not a
component diagram.

```mermaid
flowchart LR
    AUTHOR["spec author"] -- "INTF-SPEC" --> WRAP["pg-wrap"]
    CALLER["caller"] -- "INTF-RUN / INTF-CHECK / INTF-SHOW" --> WRAP
    SLOT["launcher slot"] -- "INTF-EXEC" --> WRAP
    WRAP -- "INTF-LAYER" --> LAYERS["wrapper layers, outermost first"]
    LAYERS -- "INTF-PAYLOAD" --> PAYLOAD["the payload"]
```

Each interface is classified on **two axes**, counterparty kind and essential-versus-optional
participation, per the method (`INV-8`); see [interfaces](interfaces.md).

**pg-wrap names no caller.** By design (`INV-ISO-1`, this set's own floor), pg-wrap is mutually
unaware of whatever invokes it and of whatever it wraps: it speaks only of "a caller", "a launcher
slot" and "a payload". Anything about why a payload is run, which program asked for it, or what
happens to it afterwards is that caller's concern and is not carried here. A caller's own helper
that renders wrap specs is likewise the caller's: pg-wrap defines the spec and accepts any spec
however it was authored (`INV-SPEC-6`).

## Scope (extent + floor)

- **Extent (in)** — the wrap spec's format and its decode errors; the entry points (`run`, the
  second binary `pg-wrap-exec`, `check`, `show`); composition of one layer per spec entry; the
  minimal environment and the exact-name keep list; the exec prefix; the absolute-path rule; the
  `pg-wrap[<stage>]:` messages; exit 97 before the payload; removal of the spec-path variable from
  the payload's environment; the wrapper contract (`INV-WC-1` to `INV-WC-9`) every layer a spec
  names obeys.
- **Extent (out)** — what any particular layer program does beyond the wrapper contract; the
  payload's own behavior; how a caller chooses, renders or ships a spec; templating or per-item
  context (a spec has none); implementation mechanics (those are this package's own decision docs,
  which receive such content by the method's relocation procedure,
  `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior · USECASE-5`).
- **Floor** — pg-wrap speaks in specs, entries, layers, payloads, environments, stages and exit
  codes, never in a caller's vocabulary borrowed without restating it at this set's own floor
  (`INV-20`).

## Realization gaps

This set's **realization-gap register** (`INV-23`): intended behavior this set's implementation has
not built yet, one row per gap, each keyed by the element id the gap is against. Not an open
question: the intent is settled and the build has not caught up (`INV-15`).

None. The module (`packages/pg-wrap`) realizes every element of this set: the spec decoder, the
`run`, `check` and `show` entry points, the second binary `pg-wrap-exec`, the exit-97 failures with
their `pg-wrap[<stage>]:` messages, the environment rules, and the wrapper-contract suite
(`INV-WC-1` to `INV-WC-9`), each proven by tests against the real binaries and real wrappers on the
host. A case whose tool is absent from a host (`tmux`, `flox`, `nix develop`) skips there with a
one-line reason, so a host without that tool has not exercised that case.

## External references

This set follows the behavior-docs method and cites elements the method defines.

| Name        | What it is                                                                                                                            | Owner set-path                                                   | Owner UUID                                                                                                                                      |
| ----------- | ------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `INV-8`     | a cross-product interaction MUST be an explicit interface, classified by counterparty kind and by essential-vs-optional participation | `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` | [67a79e92-2f98-40a2-9392-034a697e457e](https://github.com/phillipgreenii/nix-agent-support/blob/main/behavior-docs/docs/behavior/invariants.md) |
| `INV-15`    | the behavior docs set is the source of truth; a realization gap is normal, not a defect                                               | `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` | [375b542f-2a9f-4cfd-a77e-7aed45a416d5](https://github.com/phillipgreenii/nix-agent-support/blob/main/behavior-docs/docs/behavior/invariants.md) |
| `INV-20`    | a borrowed term at a reference seam MUST be inherited or renamed, never silently redefined                                            | `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` | [bafdd784-81ed-46fe-88f0-1a8c5fc4caf0](https://github.com/phillipgreenii/nix-agent-support/blob/main/behavior-docs/docs/behavior/invariants.md) |
| `INV-22`    | traceability is a listing obligation on every story, use case and journey, not a coverage section                                     | `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` | [b2502527-1340-4a1f-858c-aaa80c601317](https://github.com/phillipgreenii/nix-agent-support/blob/main/behavior-docs/docs/behavior/invariants.md) |
| `INV-23`    | the realization-gap register is set-level, named `## Realization gaps`, and never an element                                          | `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` | [f3bba3e7-440f-4109-a4de-9d37daa34bcf](https://github.com/phillipgreenii/nix-agent-support/blob/main/behavior-docs/docs/behavior/invariants.md) |
| `USECASE-5` | the method's procedure for relocating implementation content out of a behavior doc                                                    | `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` | [7d6de948-3ef5-426a-949e-2dd872f06d28](https://github.com/phillipgreenii/nix-agent-support/blob/main/behavior-docs/docs/behavior/journeys.md)   |
