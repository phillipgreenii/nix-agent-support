# CETA temp-repo carve-out: the effective git directory, not "a temp path appears"

**Status**: Accepted (resolves `pg2-yoqsr`)
**Date**: 2026-08-28
**Deciders**: Phillip Green II

## Context

CETA's `gitdir` and `git` rules correctly refuse two things:

1. a direct write to git metadata under a `.git/` directory
   (`refusing to write git metadata under .git/ directly — modify it through git commands only`);
2. assigning a git repo-locating env var (`GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`,
   `GIT_COMMON_DIR`, `GIT_OBJECT_DIRECTORY`) on a git invocation.

Both are right for a REAL repository. Neither used to distinguish one from a THROWAWAY repository
that exists only under a temporary path, so there was no sanctioned way to build a disposable git
fixture and drive the exact failure mode those guards exist to prevent.

Observed 2026-08-27 while root-causing the recurring canonical-`.git/config` poisoning
(`pg2-12795` / `pg2-nxi54` / `pg2-jjlm8` / `pg2-rrhw2` / `pg2-kmhud`): two reproduction attempts
against a freshly-created throwaway repo under the session scratchpad were refused —

```
env GIT_DIR="$SCRATCH/repro/fake-canonical/.git" git -C "$SCRATCH/repro/fixture" init
cat > "$SCRATCH/repro/fake-canonical/.git/hooks/pre-commit" <<'HOOK' ...
```

— even though every path involved was inside the scratchpad and no real repository could be
reached. The investigation had to route around the guard via `core.hooksPath` pointed at a normal
temp directory, which is strictly less direct and does not cover the `GIT_DIR` half at all.

`pg2-oixbs` raised this once already, from the opposite direction ("there is currently no
sanctioned path to clear it once it's already corrupted"), and was closed without the carve-out
being filed. This bead is that carve-out.

### The naive reading is wrong

Operator ruling (Phillip, 2026-08-27): the carve-out MUST be specific to "temp" repos, which only
exist under a temporary path. "Allow it if a temp path appears in the command" is the WRONG reading
and would open the exact hole the guard closes. The defect this whole cluster is about has the
shape:

```
GIT_DIR=<REAL canonical>/.git   git -C <tmpdir> config user.email t@example.com
```

Here a temp path IS present (`-C <tmpdir>`) and yet the write lands on the REAL repository, because
`GIT_DIR` outranks `-C`.

### Side-finding: the guard was keyed on a path pattern, not the effective directory

The pre-existing env-var refusal was keyed on whether the VALUE assigned to a variable contained a
literal `.git` path COMPONENT (`internal/rules/gitdir`'s `isGitMetadataPath`). A subagent testing a
sibling bead was refused `GIT_DIR=<path>/.git` but ALLOWED `GIT_DIR=<bare-repo-path>` — a bare
repository's `GIT_DIR` IS its own top-level directory, with no `.git` path component anywhere in the
value, even though it redirects git's metadata exactly as surely as the non-bare spelling does. A
bare repo is a real repo; the false negative was in the guard, not in the input.

## Decision

**The carve-out MUST resolve the EFFECTIVE git directory a command would act on, and relax the two
refusals above only when EVERY operand that participates in resolving it independently resolves
under a temporary root.** If any one of them is outside a temporary root, the command MUST be
refused exactly as before this carve-out — mixed real+temp is the attack this decision exists to
keep refusing, not a use case to accommodate.

### The operands, and how each is checked (R1/R2)

- `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`, `GIT_COMMON_DIR`, `GIT_OBJECT_DIRECTORY` — checked
  by **variable NAME**, regardless of the shape of the value assigned to them. This is the
  side-finding fix: a bare repository's `GIT_DIR` is caught exactly as a non-bare one is, because
  the check no longer depends on the value containing a `.git` path segment at all.
- `--git-dir` / `--work-tree` / `-C` — a git invocation's own pre-subcommand option operands,
  resolved the same way `-C` chdirs already are (folded onto cwd in argv order).
- a literal `.git`-shaped path OPERAND or REDIRECTION target — the pre-existing refusal #1 case
  (`cat > .git/config`), and an ORDINARY variable bound to a `.git`-shaped value
  (`f=".../.git/config"`) — unchanged detection, now carrying the same carve-out.
- `GIT_PREFIX` is deliberately **excluded** from the name-keyed set even though it appears in the
  Context's enumeration of the pre-existing refusal: it is a relative in-tree offset git sets for
  its own subprocesses to recover the cwd they were invoked from, not a location git resolves its
  own metadata through. Assigning it does not create the hazard this decision guards against.
- `-c core.worktree=<path>` / `--config-env=core.worktree=<VAR>` (the config-key spelling of a
  `GIT_WORK_TREE`-shaped redirect — the exact mechanism behind `pg2-12795`) are **NOT** additionally
  carved out here. Every `-c <key>=<value>` pair outside `git`'s own `clearedConfigFlagPairs`
  allowlist is ALREADY refused unconditionally by `hasGitConfigInjection` — a separate, broader
  RCE-prevention floor this decision does not touch. A fixture that needs to redirect the working
  tree should use the `GIT_WORK_TREE` env var spelling instead, which IS covered.
- the cross-LEAF variable-binding case (`f=".../.git/x"` on one leaf, consumed by `sed -i "$f"` on a
  DIFFERENT leaf of the same compound command) is **NOT** carved out. It is a materially rarer shape
  than a single-leaf assignment, is not exercised by either of this bead's own reproduction commands,
  and extending the carve-out there would require intrusive changes to the cross-leaf scope walk
  (`internal/rules/gitdir`'s `bindingDirection`). Under-covering the carve-out is the fail-safe
  direction — it leaves MORE cases refused than strictly necessary, never fewer.

### The temporary-root set (R3/R4)

Paths are compared after **realpath (symlink) resolution**, matched as a **directory-boundary
prefix** against a temporary root — never a substring test. `/tmp` is a symlink to `/private/tmp` on
this machine, and `$TMPDIR` is a per-user `/var/folders/<x>/<y>/T/` that realpaths under
`/private/var/folders`; a substring test would wrongly admit `/Users/phillipg/my-tmp-repo` or
`/Users/phillipg/phillipg_mbp/tmp/...`.

The root set is explicit and derived, not hardcoded guesswork (`internal/temproot.Roots`):
the realpath of `$TMPDIR`, `/private/var/folders`, `/private/tmp`, and `/tmp` — the roots
`mktemp -d`, Go's `t.TempDir()`, and bats' `$BATS_TEST_TMPDIR` actually land in on this machine —
plus any path listed in `CETA_EXTRA_TEMP_ROOTS` (`:`-separated), mirroring the established
`CETA_EXTRA_READWRITE_ROOTS`/`CETA_EXTRA_READONLY_ROOTS` escape hatch (`internal/patheval`). The
Claude Code session scratchpad root is not a fifth hardcoded literal: there is no env var carrying
it to a hook subprocess as of 2026-08-28, and constructing it from a UID/project-hash pattern would
itself be the "hardcoded guesswork" this decision forbids. On this deployment it is always a
descendant of `/private/tmp` (observed pattern
`/private/tmp/claude-<uid>/<project>/<session>/scratchpad`), so it is already covered by that root.
If a deployment ever puts it somewhere `/private/tmp` does not reach, `CETA_EXTRA_TEMP_ROOTS` is the
sanctioned way to add it.

### Works on a not-yet-a-repo directory (R6)

The carve-out's resolution step (`internal/patheval.ResolveRealPath`, via
`internal/temproot.ResolveOperand`) falls back to the nearest EXISTING ancestor when a path does not
yet exist, so `git init` into a fresh, empty temporary directory resolves and carves out correctly —
the carve-out does not, and must not, require the directory to already contain a repo.

### The refusal message names the carve-out (D3)

The `.git`-metadata-write Reject reason now states the carve-out exists, so an agent that hits the
refusal can tell "forbidden here" from "forbidden here, but permitted under a temp root" at the one
place it is guaranteed to read: the prompt itself.

## Consequences

- Both of the pre-existing refusals gain the SAME carve-out, applied consistently: the `gitdir` rule
  (the `.git/`-metadata write and the env-var-value binding case) and the `git` rule's
  `hasRedirectEnvVar`-driven `Ask` for a `GIT_DIR`/`GIT_WORK_TREE` redirect on `checkout`/`rebase`/
  `filter-branch`/the modifying-subcommand set/a soft `reset`. Without the second half, a fixture
  command entirely under a temp root would still surface an `Ask` once `gitdir`'s stronger refusal
  stepped aside, because `gitdir` runs earlier in the chain and its Reject otherwise masks
  `hasRedirectEnvVar` from ever being reached at all.
- The R2 regression this bead exists to protect — `GIT_DIR=<real-canonical>/.git git -C <tmpdir>
config ...` — is refused exactly as it was before this decision landed: the carve-out only ever
  relaxes a refusal when EVERY participating operand is temp, and a single real operand fails that
  by construction.
- A `.git/` write in a real checkout is unaffected — no operand it carries resolves under a
  temporary root, so the carve-out never applies to it.
- `GIT_INDEX_FILE`/`GIT_COMMON_DIR`/`GIT_OBJECT_DIRECTORY` assigned to a value with no `.git` path
  segment now REFUSES where it previously auto-approved outright (no `Ask`, no `Reject` — a plain
  `allow`, since `git`'s `hasRedirectEnvVar` never checked these three at all). This closes a
  genuine gap the side-finding surfaced, not merely the bare-`GIT_DIR` case named in the Context —
  it is a strictly MORE conservative posture for these three variables, in exchange for the new
  carve-out giving back the relief for a temp fixture.
- A THIRD, independent call site needed the same `internal/temproot.Under` check, discovered only
  when `nix flake check` (not the narrower go-tests check) ran
  `TestIntegration_TempRepoCarveOut_BeadReproductionShapes` inside a nix build sandbox: `gitdir`'s
  own carve-out relaxes correctly (`tempFixtureCarveOutApplies` is unaffected by this), but
  `internal/engine`'s `evaluateRedirections` — a generic, pre-existing redirection-safety check that
  runs on EVERY Bash redirection, independent of and earlier than the rule chain that carries the
  carve-out — hard-Rejects a write whose target `internal/patheval`'s `classify()` ladder marks
  `PathReadOnly`. That ladder's unconditional `/nix/**` rule (meant to protect the immutable
  `/nix/store`) also matches `/nix/var/nix/builds/<id>` — this machine's nix places `$TMPDIR` /
  `NIX_BUILD_TOP` there during a sandboxed build, so a fixture built with `t.TempDir()` inside that
  sandbox lands under `/nix` and was rejected before the carve-out ever got a say. Fixed by making
  `evaluateRedirections` fall through to its EXISTING `!CanWrite()` check — yielding `NoOpinion`,
  never `Reject` — when a `PathReadOnly` target ALSO resolves under `temproot.Under`; `access` stays
  `PathReadOnly` (that check still fires), so this is deliberately NOT a blanket Approve.
  `/nix/store` can never be a descendant of a temp root, so the relaxation cannot reach it. The
  distinction between "not decisively rejected" and "actively approved" matters: a first cut of this
  fix instead skipped the `!CanWrite()` check entirely (`continue`), which turned into an outright
  `Approve` for ANY redirect target under a temp root — and in this SAME sandboxed run,
  `mkGoTest`'s `buildPhase` sets `HOME="$TMPDIR"`, so `~/.ssh/authorized_keys` is ALSO,
  coincidentally, under a temp root there. `TestIntegration_HookBypassRegression`'s
  "loop terminator redirect ssh keys" case (which must never resolve to `Approve`) caught this
  immediately. `internal/patheval`'s own zone ladder (`classify()`) is deliberately left untouched,
  for the same reason `escape_zone_ladder_test.go` (bead `pg2-lw19e`) already declined to touch it: a
  `t.TempDir()`-rooted `HOME`/`XDG_DATA_HOME` fixture is ALSO nested under `$TMPDIR`/`NIX_BUILD_TOP`
  in that same sandboxed environment, so narrowing or reordering `classify()`'s ladder to exempt the
  build's scratch directory would relax those more-specific zone checks too — a much larger blast
  radius than this one redirection-safety check.
