# CETA: named-prefix read roots

**Status**: Accepted (implements the operator ruling on `pg2-ibih79`; work item `pg2-d00wo0`)
**Date**: 2026-10-28
**Deciders**: the operator (Phillip), ruling 2026-10-27 on `pg2-ibih79`: allow the agent scratch
directories under `~/.cache` (`hoist-decompose`, `prlane-decompose`, `prlane-fix`, `p2-decomp-*`,
`pdec-*`, `pd-*`), `~/Library/Logs` and the `$TMPDIR` `pd-*` directories; NOT `~/.claude/projects`
or the other small groups; widening to `~/.cache/*` as a whole or to all of `$TMPDIR` needs a fresh
ruling.

## Context

`CETA_EXTRA_READONLY_ROOTS` takes exact absolute paths (`resolveExtraRoots` cleans and symlink-resolves
each entry; a `*` is a literal character; matching is "equal to or under"). The exact-nameable groups
of the ruling landed through it. The generated-suffix groups (`p2-decomp-*`, `pdec-*`, `pd-*` under
`~/.cache`, and `pd-*` under the per-user, per-boot `$TMPDIR`, a `/var/folders/.../T/` that no nix
build can know) cannot be named, and the only exact roots that cover them (`~/.cache`, `$TMPDIR`) are
exactly the widenings the ruling withheld.

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
  bare parents, lookalikes, `..` and symlink escapes are not matched.
- Setting the ruled groups is the consuming machine flake's job:
  `~/.cache/{p2-decomp-,pdec-,pd-}*` and `$TMPDIR/pd-*`.
- Pinned by `internal/patheval/prefixroots_test.go`, `TestSessionKind_ExtraReadOnlyPrefixRoots` and
  `checks.<system>.test-ceta-prefix-roots`.
