# CI gate — decisions

### `DEC-CIGATE-1` — the check interpreter and the verdict parser do not share a code-level abstraction <!-- uuid: d69b5711-f34f-49fd-892f-fb94e86686dd -->

**Decided.** `pg2-4dz88.2`'s pluggable check interpreter (classifying commit-status description
strings into satisfied/partially-satisfied/unsatisfied/unknown) and `pg2-4dz88.1`'s pluggable
verdict parser (leaf `pg2-4dz88.1.4`; classifying comment bodies into a two-axis findings/authority
result via config-declared generations) are architecturally similar — both are config-driven,
versioned/pluggable pattern-matchers replacing a single regex/exclusion mechanism, and both need
warn-and-skip on a bad pattern plus an explicit unknown/unmatched fallback that surfaces rather
than silently absorbing. They deliberately do **not** share one package or interface, per a ruling
posted 2026-08-21 by the `pg2-4dz88.1.1` planning pass (on both `pg2-4dz88.1` and `pg2-4dz88.2`,
for symmetric visibility). Three reasons:

1. **Different input shapes and axes.** The verdict parser classifies free-text comment bodies on
   two independent axes with generation precedence and marker-anchoring; the check interpreter
   (`pg2-4dz88.2.4`) classifies a short machine-generated status string (e.g. `"n/m rules
approved"`) into a single tri/four-state axis with fraction parsing. A shared interface would
   need to either over-abstract (indirection with no real reuse) or bend one domain's shape onto
   the other's.
2. **Different consumers and different correctness properties for "unknown."** The check
   interpreter's mandatory rule is that an unclaimed check must still roll up into CI health
   exactly as today — never silently excluded. The verdict parser's mandatory rule is that an
   unmatched verdict must become an observable signal — never silently "no approval." Conflating
   the two domains risks one domain's fallback semantics leaking into the other.
3. **Loose ordering between the two beads.** `pg2-4dz88.1`'s decomposition and `pg2-4dz88.2` have
   no dependency on each other, so building a shared abstraction now would create cross-bead
   coupling neither side is positioned to design well in isolation.

Both mechanisms instead follow the same **naming/design convention** — a config-driven registry of
pluggable matchers, versioned/prioritized, warn-and-skip on a bad pattern, explicit tested
unknown-fallback — matching `internal/cirollup.Excluder`, the named precedent for both. The shape
is copied; the code is not shared.

**Not decided here.** Whether the two mechanisms should later be unified is explicitly deferred: if,
after both ship, the duplication proves substantial and stable, a follow-up refactor bead can
revisit it. That was deliberately not scoped as part of this ruling (premature-abstraction risk).

### `DEC-CIGATE-2` — `excluded_ci_checks` is removed outright, not kept as back-compat sugar <!-- uuid: 30e15fe9-3b70-4d2e-9981-140a1ea4b41a -->

**Decided.** The old `excluded_ci_checks` config key (a regex exclusion list consumed by
`internal/cirollup.Excluder`) is removed outright rather than retained as a back-compat sugar
layer over the new check-interpreter registry. Operator ruling, 2026-08-24, on `pg2-dw73b`: the
sugar option would leave the repo permanently carrying two ways to express the same exclusion —
the old regex-skip-list vocabulary alongside the new interpreter vocabulary — so outright removal
is the cleaner end state, accepted despite a real cross-repo cost (the key's only live consumer is
`phillipg-nix-ziprecruiter`'s `machines/phillipg-mbp-02/default.nix`, so removing it requires a
coordinated flake-lock bump plus a `darwin-rebuild switch` apply in that repo).

Implemented by `pg2-4dz88.2.3` (pg-pr's config schema bead, closed): a config that still declares
`repos[].excluded_ci_checks` now fails to load hard, rather than silently no-op-ing. See
`packages/pg-pr/internal/config/config.go`'s `errExcludedCIChecksRemoved` and the
`RepoConfig.UnmarshalYAML` hook that detects the key by name during decode and rejects it — a
config carrying the removed key gets an explicit load error, never a silent drop. The replacement
vocabulary is `repos[].check_interpreters`, an ordered list of `{patterns, type}` declarations;
`pg2-4dz88.2.3` defines and parses this schema only.

As of this writing, the schema exists but the mechanism it describes is **not yet landed**: the
actual classification/interpreter registry (`pg2-4dz88.2.4`) and its wiring into CI-rollup call
sites (`pg2-4dz88.2.6`) are both still open. The coordinated cross-repo follow-up — updating
`phillipg-nix-ziprecruiter`'s config to the new `check_interpreters` vocabulary — is tracked as
`pg2-sguph`, which is itself blocked on `pg2-4dz88.2.4` and `pg2-4dz88.2.6` landing first.

**Not decided here.** Whether `check_interpreters` itself should ever grow a convenience form
closer to the old exclusion-list shape is not addressed by this ruling — the ruling is only about
`excluded_ci_checks` specifically, not about the design space of its replacement.
