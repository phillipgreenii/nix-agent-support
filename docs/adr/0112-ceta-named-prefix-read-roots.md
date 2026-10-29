# CETA: named-prefix read roots

**Status**: Accepted (implements the operator ruling on `pg2-ibih79`; work item `pg2-d00wo0`)
**Date**: 2026-10-28
**Deciders**: the operator (Phillip), ruling 2026-10-27 on `pg2-ibih79`: allow the agent scratch
directories under `~/.cache` (`hoist-decompose`, `prlane-decompose`, `prlane-fix`, `p2-decomp-*`,
`pdec-*`, `pd-*`), `~/Library/Logs` and the `$TMPDIR` `pd-*` directories; NOT `~/.claude/projects`
or the other small groups; widening to `~/.cache/*` as a whole or to all of `$TMPDIR` by the
named-prefix list needs a fresh ruling. (An unattended session's own `$TMPDIR` is a separate, already
ruled grant: ADR 0110.)

## Context

`CETA_EXTRA_READONLY_ROOTS` takes exact absolute paths (`resolveExtraRoots` cleans and symlink-resolves
each entry; a `*` is a literal character; matching is "equal to or under"). The exact-nameable groups
of the ruling landed through it. The generated-suffix groups (`p2-decomp-*`, `pdec-*`, `pd-*` under
`~/.cache`, and `pd-*` under the per-user, per-boot `$TMPDIR`, a `/var/folders/.../T/` that no nix
build can know) cannot be named, and the only exact roots that cover them (`~/.cache`, `$TMPDIR`) are
exactly the widenings the ruling withheld from the named-prefix list. (ADR 0110 separately grants an
unattended session its own `$TMPDIR`; that grant is outside this list.)

## Decision

1. A separate list, `CETA_EXTRA_READONLY_PREFIX_ROOTS` (home-manager option
   `extraReadOnlyPrefixRoots`), holds ":"-separated entries of one shape, `<parent>/<prefix>*`, where
   `<parent>` is an absolute directory or the literal token `$TMPDIR`. A prefix is a letter or digit,
   then `A-Za-z0-9._-`, and MUST end in `-`; the single trailing `*` is the only wildcard.
2. An entry grants read-only access to a DIRECT child of the parent whose name is the prefix followed
   by one or more `A-Za-z0-9._-`, and to everything under that child. It never grants the parent, the
   bare prefix directory, a name that lacks the dash (`pd`) or has another prefix (`pdx-foo`), a
   deeper `.../x/pd-1`, or a name with a space or any other character.
3. Matching runs on the already cleaned and symlink-resolved path (`Evaluate`), against the
   symlink-resolved parent. A `..` cannot survive cleaning, and a symlink that leaves a matching child
   resolves to its real target, which is judged on its own; a matching name pointing elsewhere grants
   nothing. A symlink that resolves INTO a matching child is granted, because that is where the data is.
4. `$TMPDIR` is read from the process environment when the evaluator is built, not baked in. It is
   honoured only under the same prefixes as the unattended TMPDIR (`/private/var/folders/`,
   `/private/tmp/`, `/tmp/`, strictly inside), else the entry grants nothing.
5. The Go side refuses an entry whose parent is `/`, at or above `$HOME`, or is, contains or sits under
   `~/.claude` (the transcripts under `~/.claude/projects` can hold secrets; ADR 0110). The home-manager
   option asserts the same shape at build time, reusing the `unattendedExtraReadOnlyRoots` rules, so a
   bad entry fails the build instead of silently granting nothing.
6. The wrapper `--set`s the variable, shell-escaped, so `$TMPDIR` and `*` reach the binary verbatim.
   Like the exact roots it is checked after every built-in zone and after the secrets and `denyRead`
   guards, so it can only make an unknown path known. `pathspec.SessionKind` mirrors it through
   `RootDecl.Under`.

## Consequences

- The exact-path variables behave exactly as before; a prefix entry is never merged into them.
- No entry of the ruled shape grants the whole `~/.cache` or the whole `$TMPDIR`; a test pins that the
  bare parents, lookalikes, `..` and symlink escapes are not matched. This is a property of the
  named-prefix list only: an UNATTENDED session (permission mode `dontAsk`, or an autonomous ccpool
  worker) also reads and writes its own `$TMPDIR` and everything under it, including `$TMPDIR` itself
  and `$TMPDIR/pdx-foo`, through the separate grant of ADR 0110
  (`internal/patheval/unattended.go`, `ExtendForUnattended`). An ATTENDED session still abstains on
  `$TMPDIR` and on `$TMPDIR/<non-prefixed child>`, and approves `$TMPDIR/pd-*` through the prefix root.
  Pinned by `TestPrefixRoots_SearchToolsThroughPathSafety` (`internal/patheval/prefixroots_search_test.go`).
- Setting the ruled groups is the consuming machine flake's job:
  `~/.cache/{p2-decomp-,pdec-,pd-}*` and `$TMPDIR/pd-*`.
- Pinned by `internal/patheval/prefixroots_test.go`, `TestSessionKind_ExtraReadOnlyPrefixRoots` and
  `checks.<system>.test-ceta-prefix-roots`.
- The variable lives only in the home-manager wrapper (`finalPackage`), so a session whose PATH is
  built separately (a pool session's `claudeBaseline`) MUST resolve that wrapper; with the bare
  package there, every entry was silently absent from pool sessions (bead `pg2-1peo5k`). Pinned by
  `checks.<system>.test-claude-baseline-ceta-wrapper` and
  `internal/patheval/prefixroots_search_test.go`.

## Note (2026-10-29)

Found while verifying `pg2-y37ggj` (bead `pg2-izvyqt`): Grep and Glob on `$TMPDIR` and
`$TMPDIR/pdx-foo` were approved by an unattended session, which looked like a breach of the ruling
above. It is not: the approval comes from the session's own `$TMPDIR` read-write root added for
unattended sessions (ADR 0110, operator ruling `pg2-r9kv7` W3/W6, work item `pg2-b1lir`). A probe of the
installed hook with permission mode `default` abstained on `$TMPDIR`; with `dontAsk` it approved. This
note only clarifies the wording above; the Decision, the status and the roots are unchanged, and
widening the named-prefix list to all of `$TMPDIR` still needs a fresh ruling.
