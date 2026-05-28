# pgii gascity packs — migration to nix-agent-support

- **Status:** Design
- **Date:** 2026-05-26
- **Owner:** phillipg
- **Companion repos:** `phillipgreenii-nix-agent-support` (target), `~/gc` (source of legacy packs), `phillipg-nix-ziprecruiter` (source of `pg-pr-zr`)

> **Update 2026-05-28 — Phase 5 (`pgii-bead-importer`) dropped.** The legacy
> `bead-importer` was an early experiment; the user confirmed it has been
> unused since and the legacy helpers it depended on (`source-<name>.sh`,
> `bead-upsert.sh`) were already missing from disk. Phase 5 sections, the
> `bead-importer` HM submodule, the `sources` override pattern, and the
> per-pack rollout entry have been removed from the body. The
> `substitutions` machinery in `mkPgiiPack` is retained for future packs.
> Legacy `bead-importer.sh` + `bead-importer.toml` deleted from
> `~/gc/assets/imports/zr/` (Phase 1 cutover no longer gated on Phase 5).
> If a bead-importer is ever needed again, it should land via a fresh
> design rather than a revival of Phase 5.

## Motivation

Gas City currently loads five custom packs from `~/gc/assets/imports/`:

| Pack                            | What it contains                                                                           |
| ------------------------------- | ------------------------------------------------------------------------------------------ |
| `pgii-dolt-hacks`               | HACK 2, 10, 12 workarounds (autoclose, archive+compact, override watchdog)                 |
| `pgii-gastown`                  | mayor / deacon / operator / foreman agents + `mol-deacon-patrol` formula                   |
| `pgii-workers`                  | rig-scoped generic `worker` agent                                                          |
| `zr`                            | PR review / triage / self-fix agents, pr-watcher order, wake-on-work HACK 1, doctor checks |
| (the partial migration of `zr`) | `phillipg-nix-ziprecruiter/modules/pg-pr-zr/` — nix-built, parallel-running                |

These packs share five problems:

1. **Hand-edited source trees in `~/gc/`.** No version control story beyond ad-hoc commits in the gc repo; no way to keep multiple machines in sync.
2. **`zr` is named for one company and referenced by hard-coded paths in every script.** The functionality (PR-watching, review drafting, action-fixing) is not company-specific.
3. **The half-done migration to `pg-pr-zr` was scoped to one repo (`nix-ziprecruiter`) instead of the agent-support repo where the rest of the nix-built agent tooling lives.**
4. **`pgii-gastown` carries a `zr-worker` agent** whose only difference from `pgii-workers`' generic `worker` is `max_active_sessions = 3` — already expressible via `[[rigs.patches]]` in `city.toml`.
5. **No declarative wiring between packs and cities.** Each pack is imported by a hand-written line in `city.toml`; disabling a pack means hand-editing that file.

This design migrates all custom packs to `phillipgreenii-nix-agent-support`, renames anything ZR-specific to `pgii-*`, and provides a home-manager module that declaratively installs packs into specified cities (writing managed blocks into each city's `city.toml`).

## Scope

**In scope**

- New nix library function `lib/mkPgiiPack.nix` for building pack derivations.
- New home-manager module `home/programs/pgii-packs/` that takes per-pack toggles + a city list and writes managed `[packs.<name>]` blocks into each city's `city.toml`.
- Five packs in `packages/pgii-pack-<name>/`: `pr-support`, `dolt-hacks`, `workers`, `gastown`, plus a `test-fixture` pack used by tests.
- Activation behavior: idempotent block insertion, removal-on-disable, hand-written-block protection, optional `gc supervisor reload`.
- Migration plan that retires `~/gc/assets/imports/{zr,pgii-*}` and the `pg-pr-zr/pack-src/` after each phase.

**Out of scope**

- Auto-discovery of cities from `~/.gc/cities.toml`. Cities are specified explicitly via module option; auto-discovery can be added later if friction shows up.
- Multi-machine shared city.toml (one home-manager invocation, one machine, one city.toml).
- Migrating non-pack tooling. `pg-pr` the binary, `pg-pr-issues-jira` the wrapper, etc. stay where they are; only the gascity pack layer is in scope.
- Replacing `pg-pr-issues-jira-zr` in nix-ziprecruiter. The Jira binary wrapper stays in `phillipg-nix-ziprecruiter` because the credentials and tenant URL are host-specific; only the wrapper renames to `pg-pr-issues-jira`.

## Architecture

```
packages/
  pgii-pack-pr-support/
    default.nix              # callPackage entry
    pack-src/
      pack.toml
      agents/{pr-self-fixer,pr-reviewer,pr-triage}/{agent.toml,prompt.md}
      orders/{pr-watcher,wake-on-work}.toml.template
      scripts/{pr-watcher,wake-on-work}.sh
      doctor/{check-pr-watcher-recent-runs,check-pr-agent-woke-no-progress,
              check-pr-feedback-backlog,check-pr-feedback-throughput,
              check-pr-orphan-beads,check-hack-1-still-needed}/
  pgii-pack-dolt-hacks/
    default.nix
    pack-src/
      pack.toml
      orders/{hack-autoclose-completed-mols,hack-archive-and-compact,
              hack-order-override-watchdog}.toml.template
      scripts/{hack-autoclose-completed-mols,hack-archive-and-compact,
               hack-order-override-watchdog}.sh
  pgii-pack-workers/
    default.nix
    pack-src/
      pack.toml
      agents/worker/{agent.toml,prompt.md}
  pgii-pack-gastown/
    default.nix
    pack-src/
      pack.toml
      agents/{mayor,deacon,operator,foreman}/{agent.toml,prompt.md}
      formulas/mol-deacon-patrol.toml
      doctor/{check-misplaced-beads,check-stale-beads}/
  pgii-pack-test-fixture/
    default.nix
    pack-src/
      pack.toml                # trivial: one no-op order

lib/
  mkPgiiPack.nix               # shared builder

home/programs/
  pgii-packs/
    default.nix                # phillipgreenii.programs.pgii.{gascity,packs.*}
    activation.sh              # writes managed blocks, removes disabled packs
    tests/                     # bats tests for activation.sh
      fresh-write.bats
      replace-existing.bats
      no-op-rebuild.bats
      remove-on-disable.bats
      hand-written-collision.bats
      multi-pack.bats
      multi-city.bats
```

### Data flow

```
phillipgreenii.programs.pgii.packs.<pack>.enable = true
phillipgreenii.programs.pgii.gascity.cities      = ["/Users/phillipg/gc"]
                              │
                              ▼
HM module evaluates options, instantiates pack derivations:
  pkgs.pgii-pack-pr-support
  pkgs.pgii-pack-gastown
  …
                              │
                              ▼
Pack derivations realize:
  /nix/store/<hash>-pgii-pack-pr-support/{pack.toml,agents/,orders/,scripts/,doctor/}
                              │
                              ▼
home.activation runs activation.sh with --cities + --packs JSON args
                              │
                              ▼
city.toml gains:
  # BEGIN pgii-pack:pgii-pr-support (managed)
  [packs.pgii-pr-support]
  path = "/nix/store/<hash>-pgii-pack-pr-support"
  # END pgii-pack:pgii-pr-support (managed)
                              │
                              ▼
On next gc supervisor reload (auto if --reload, manual otherwise),
gascity loads the packs.
```

## Pack derivation contract

### `lib/mkPgiiPack.nix`

```nix
{ lib, pkgs }:
{
  name,                              # "pgii-pr-support"
  version ? "0.1.0",
  src,                               # path to pack-src/
  substitutions ? { },               # extra @KEY@ → value pairs
  meta ? { },
}:
pkgs.runCommand "${name}-${version}"
  {
    passthru = { inherit name; };
    nativeBuildInputs = [ pkgs.envsubst ];
  }
  ''
    cp -R ${src}/. $out/
    chmod -R u+w $out

    # @SCRIPTS_DIR@ always resolves to the pack's scripts/ subdir in the store.
    export SCRIPTS_DIR="$out/scripts"
    ${lib.concatStringsSep "\n" (lib.mapAttrsToList (k: v: ''
      export ${k}=${lib.escapeShellArg v}
    '') substitutions)}

    while IFS= read -r -d "" f; do
      envsubst < "$f" > "''${f%.template}"
      rm "$f"
    done < <(find $out -name "*.template" -print0)

    mkdir -p $out/formulas $out/agents $out/orders $out/scripts
    [ -d $out/scripts ] && chmod +x $out/scripts/*.sh 2>/dev/null || true

    test -f $out/pack.toml
    ! find $out -name "*.template" | grep -q .

    cat > $out/.pack-meta.json <<EOF
    { "name": "${name}", "version": "${version}" }
    EOF
  ''
```

### Template substitution

- Files ending `.template` are processed; `.template` is stripped from output name.
- Marker syntax: `${KEY}` (envsubst's native form).
- Substituted via plain `envsubst`. Only names that mkPgiiPack exports get substituted; unexported `${...}` patterns inside template files pass through unchanged. Gascity's runtime `{{.Foo}}` go-template syntax is unaffected.
- `${SCRIPTS_DIR}` is always exported (= `$out/scripts`). Additional vars come from each pack's `substitutions` arg.
- Known gotcha: if a pack ships a shell script as `*.sh.template` and the script uses `${SCRIPTS_DIR}` or any other exported var as a normal shell expansion, envsubst will eat it. If a future pack hits this (e.g. it needs `${HOME}` or similar at runtime), switch mkPgiiPack to envsubst's variable-list arg `'$SCRIPTS_DIR $X $Y'` so only declared names are replaced.

### Per-pack `default.nix` shape

For most packs (no extra substitutions):

```nix
{ lib, mkPgiiPack }:
mkPgiiPack {
  name = "pgii-pr-support";
  src = ./pack-src;
}
```

For a hypothetical pack that needs build-time substitutions (no current pack uses this; kept as a forward-looking example of the `substitutions` arg):

```nix
{ lib, mkPgiiPack }:
{ extraValue ? "" }:
mkPgiiPack {
  name = "pgii-pack-example";
  src = ./pack-src;
  substitutions = {
    EXTRA_VALUE = extraValue;
  };
}
```

### Output layout (uniform across packs)

```
$out/
  pack.toml                # gascity manifest
  agents/<name>/{agent.toml,prompt.md}
  orders/<name>.toml
  scripts/<name>.sh        # executable
  doctor/<name>/{doctor.toml,run.sh}
  formulas/                # may be empty, must exist
  .pack-meta.json
```

## Home-manager module API

### Option tree

```nix
phillipgreenii.programs.pgii = {

  gascity = {
    cities = mkOption {
      type = listOf path;
      default = [ ];
      example = [ "/Users/phillipg/gc" ];
      description = "Cities whose city.toml should be updated with managed pack blocks.";
    };

    reloadSupervisor = mkOption {
      type = bool;
      default = true;
      description = "After writing city.toml, run `gc supervisor reload` if the supervisor socket is reachable.";
    };
  };

  packs = {
    pr-support.enable    = mkEnableOption "pgii-pr-support pack (PR review + triage)";
    dolt-hacks.enable    = mkEnableOption "pgii-dolt-hacks pack (HACK 2, 10, 12 workarounds)";
    workers.enable       = mkEnableOption "pgii-workers pack (rig-scoped worker pool)";
    gastown.enable       = mkEnableOption "pgii-gastown pack (mayor/deacon/operator/foreman)";
  };
};
```

### Config block (sketch)

```nix
config = lib.mkIf anyPackEnabled {

  # Root the pack store paths via home.file (not home.packages — packs
  # contribute no $PATH or $XDG_DATA_DIRS).
  home.file = lib.mkMerge (map (p: {
    ".local/share/pgii-packs/${p.name}".source = p.drv;
  }) enabledPacks);

  home.activation.pgii-packs = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
    ${./activation.sh} \
      --cities '${builtins.toJSON cfg.gascity.cities}' \
      --packs  '${builtins.toJSON packStorePathMap}' \
      ${lib.optionalString cfg.gascity.reloadSupervisor "--reload"}
  '';

  assertions = [
    {
      assertion = !cfg.packs.pr-support.enable || config.phillipgreenii.programs.pg-pr.enable;
      message = "pgii.packs.pr-support requires phillipgreenii.programs.pg-pr.enable = true (pack scripts call pg-pr).";
    }
    {
      assertion = !anyPackEnabled || cfg.gascity.cities != [ ];
      message = "Enabling pgii packs requires at least one city in phillipgreenii.programs.pgii.gascity.cities.";
    }
  ];
};
```

### Validation behavior

- **pr-support depends on pg-pr**: assertion failure at eval time. Pack scripts call `pg-pr`; broken without it.
- **No cities = no install**: assertion failure. Cheaper than building packs and noticing later nothing references them.
- **No per-pack options unless needed**: each pack starts with only `.enable`. Add options later if real friction surfaces.

## Activation script

### Marker format

```toml
# BEGIN pgii-pack:<pack-name> (managed)
[packs.<pack-name>]
path = "/nix/store/<hash>-<pack-name>"
# END pgii-pack:<pack-name> (managed)
```

The `pgii-pack:` prefix on the sentinel allows one regex to find all our managed blocks across packs.

### Inputs

- `--cities '<JSON array of city paths>'`
- `--packs '<JSON object: { "<pack-name>": "<store-path>", … }>'` (enabled packs only)
- `--reload` (optional flag)

### Algorithm, per city

1. If `<city>/city.toml` does not exist → create empty file.
2. Scan existing managed blocks → set `existing` (pack names currently managed).
3. For each pack in `--packs`:
   a. If `[packs.<name>]` exists without our BEGIN sentinel → error: "Hand-written `[packs.<name>]` exists; rename or delete it before enabling `pgii.packs.<name>`".
   b. If managed block exists with same store path → no-op (log "unchanged").
   c. If managed block exists with different path → replace in place.
   d. Else → append new block.
4. For each name in `existing` but NOT in `--packs`:
   - Remove its managed block (pack was disabled in this rebuild).
5. Atomic write: temp file → fsync → rename.
6. If `--reload` and `<city>/.gc/controller.sock` exists and `gc` is on PATH:
   - Run `gc --city "$city" supervisor reload`.
   - On failure, warn but do not fail activation (`home-manager switch` shouldn't bail on supervisor health).

### Edge cases handled

- City.toml as symlink into nix store → rename fails with clear OS error. No custom check.
- Hand-written `[packs.<name>]` → loud error.
- Same-path rebuild → no-op log, no churn.
- Disabled pack → block removed.
- Supervisor reload failure → warn, continue.

### Testing

`bats` tests under `home/programs/pgii-packs/tests/`:

- `fresh-write` — empty city.toml, two packs enabled, two blocks appear.
- `replace-existing` — block exists, store path changes, block rewritten in place.
- `no-op-rebuild` — block exists with same path, file untouched.
- `remove-on-disable` — pack previously enabled, now absent from `--packs`, block removed.
- `hand-written-collision` — non-managed `[packs.X]` exists, activation errors.
- `multi-pack` — three packs in one activation, three blocks land in order.
- `multi-city` — two cities in `--cities`, both get the block set.

Tests run against fake city.toml fixtures in `$TEST_TMPDIR`; no real gascity invocation needed.

## Per-pack migration plan

### Phase 0 — Generic machinery

- `lib/mkPgiiPack.nix`
- `home/programs/pgii-packs/` (HM module + `activation.sh` + bats tests)
- `packages/pgii-pack-test-fixture/` — trivial pack used by tests and the first end-to-end activation run on the real city. Contains: a `pack.toml` declaring the pack name, an empty `formulas/` dir, and one no-op order that runs `:` (the bash null command) every hour. No agents, no scripts, no doctor checks. Exists purely to validate the activation pipeline end-to-end against a real city without risking real workflows.
- Merge before any real-pack migration. Real packs ride on proven machinery.

### Phase 1 — `pgii-pr-support`

Renamed from `pg-pr-zr`; supersedes legacy `zr`.

| What                                 | Source                                                       | Action                                                                                                                                                                                                                                                                                                        |
| ------------------------------------ | ------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| pack body (agents, orders, scripts)  | `phillipg-nix-ziprecruiter/modules/pg-pr-zr/pack-src/`       | Copy verbatim to `packages/pgii-pack-pr-support/pack-src/`; rename "pg-pr-zr" → "pgii-pr-support" throughout                                                                                                                                                                                                  |
| pack name in markers                 | `BEGIN pg-pr-zr (managed)`                                   | `BEGIN pgii-pack:pgii-pr-support (managed)`                                                                                                                                                                                                                                                                   |
| pack.toml.template header            | "ZipRecruiter PR monitoring"                                 | Scrub ZR refs in comment                                                                                                                                                                                                                                                                                      |
| Jira wrapper binary                  | `pg-pr-zr/default.nix` builds `pg-pr-issues-jira-zr`         | Stays in `phillipg-nix-ziprecruiter` — Jira creds and tenant URL are host-specific. Wrapper renames to `pg-pr-issues-jira`.                                                                                                                                                                                   |
| doctor checks (carry from legacy zr) | `~/gc/assets/imports/zr/doctor/`                             | Carry these into pgii-pr-support's `doctor/` and rewrite agent-prefix matchers from `zr.pr-*` to `pgii-pr-support.pr-*`: `check-pr-watcher-recent-runs`, `check-pr-agent-woke-no-progress`, `check-pr-feedback-backlog`, `check-pr-feedback-throughput`, `check-pr-orphan-beads`, `check-hack-1-still-needed` |
| legacy bead-importer                 | `~/gc/assets/imports/zr/scripts/bead-importer.{sh,toml}`     | Dropped 2026-05-28 (Phase 5 cancelled — unused, helpers already missing). Files deleted from legacy; no replacement pack. See header note.                                                                                                                                                                    |
| legacy notify-terminal-notifier.sh   | `~/gc/assets/imports/zr/scripts/notify-terminal-notifier.sh` | Drop — pgii-pr-support's prompts route through pg-pr, never through this script                                                                                                                                                                                                                               |
| Parallel-run                         | already established in pg-pr-zr MIGRATION.md                 | Carry over: one-week parallel-run between legacy `zr` pack and new `pgii-pr-support` pack                                                                                                                                                                                                                     |
| Cutover                              |                                                              | Delete `~/gc/assets/imports/zr/`; delete `phillipg-nix-ziprecruiter/modules/pg-pr-zr/{pack-src/,activation.sh}`; keep Jira wrapper renamed to `pg-pr-issues-jira`                                                                                                                                             |

### Phase 2 — `pgii-dolt-hacks`

| What               | Source                                                              | Action                                                                                                                                                                                                                                         |
| ------------------ | ------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| pack body          | `~/gc/assets/imports/pgii-dolt-hacks/`                              | Copy verbatim to `packages/pgii-pack-dolt-hacks/pack-src/`                                                                                                                                                                                     |
| order TOMLs        | reference scripts via absolute path under `~/gc/assets/imports/...` | Convert to `*.toml.template` with `@SCRIPTS_DIR@` substitution                                                                                                                                                                                 |
| script state paths | `jsonl-archive/`, `jsonl-export-state.json` (runtime state)         | These belong under `$GC_CITY/.gc/runtime/packs/pgii-dolt-hacks/` (gascity-managed runtime state dir, not pack source). Audit each script; rewrite any reference that points at the pack source tree to point at the runtime state dir instead. |
| Parallel-run       | not needed — orders are idempotent hacks                            | Straight cutover                                                                                                                                                                                                                               |
| Cutover            |                                                                     | Delete `~/gc/assets/imports/pgii-dolt-hacks/` (source only; runtime state stays in place)                                                                                                                                                      |

### Phase 3 — `pgii-workers`

| What            | Source                                                   | Action                                                                                                                                                                                                                                                                                                                                            |
| --------------- | -------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| pack body       | `~/gc/assets/imports/pgii-workers/`                      | Copy verbatim to `packages/pgii-pack-workers/pack-src/`                                                                                                                                                                                                                                                                                           |
| Templating      | none expected                                            | —                                                                                                                                                                                                                                                                                                                                                 |
| city.toml shape | currently `[rigs.imports.pgii-workers] source = "./..."` | **Investigate first** — does writing `[packs.pgii-workers] path = "/nix/store/..."` cause gascity to bind the rig-scoped `worker` agent to every rig the same way `[rigs.imports]` does? If not, activation needs a per-pack scope mode that writes `[rigs.imports.<name>] path = "..."` instead. Resolution lives in this phase, not in Phase 0. |
| Cutover         |                                                          | Delete `~/gc/assets/imports/pgii-workers/`; remove `[rigs.imports.pgii-workers]` from city.toml                                                                                                                                                                                                                                                   |

### Phase 4 — `pgii-gastown`

| What                                             | Source                                                                     | Action                                                                                                                                                                                       |
| ------------------------------------------------ | -------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| pack body                                        | `~/gc/assets/imports/pgii-gastown/`                                        | Copy verbatim to `packages/pgii-pack-gastown/pack-src/` minus the `zr-worker` agent                                                                                                          |
| `agents/zr-worker/`                              | `~/gc/assets/imports/pgii-gastown/agents/zr-worker/`                       | Delete during migration. The 3-session-on-ZR-rig behavior is already covered by pgii-workers' generic `worker` + `[[rigs.patches]] agent = "worker" max_active_sessions = 3` in `city.toml`. |
| bd-hygiene doctor checks                         | `~/gc/assets/imports/zr/doctor/{check-misplaced-beads,check-stale-beads}/` | Move into pgii-gastown's `doctor/` (deacon's patrol responsibility)                                                                                                                          |
| ZR refs in mayor/deacon/operator/foreman prompts | unknown                                                                    | Audit during migration; rewrite if found (none expected, but verify)                                                                                                                         |
| Cutover                                          |                                                                            | Delete `~/gc/assets/imports/pgii-gastown/`                                                                                                                                                   |

### Phase 5 — `pgii-bead-importer` (cancelled 2026-05-28)

Dropped per header note. Legacy `bead-importer.sh` + `bead-importer.toml`
were removed from `~/gc/assets/imports/zr/`; no replacement pack ships.

## Phased rollout

```
Phase 0 (machinery)
   │
   ▼
Phase 1 (pgii-pr-support build)  ─── parallel-run with legacy zr, 1 week
   │
   ▼
Phase 1 cutover                  ─── delete ~/gc/assets/imports/zr/
   │
   ▼
Phase 2 (pgii-dolt-hacks)
   │
   ▼
Phase 3 (pgii-workers)
   │
   ▼
Phase 4 (pgii-gastown)
```

Each phase: own bead epic, own design spec or plan, own PR, own validation. Phase 0 is the heavy lift; phases 1-4 are mechanical once the machinery exists. (Phase 5 was cancelled 2026-05-28 — see header note.)

**Sequencing dependency:** Phase 1 splits into two sub-phases: a build sub-phase (port pack body, doctor checks, run parallel-run alongside legacy `zr`) and a cutover sub-phase (delete legacy `zr/`). With Phase 5 cancelled, Phase 1's cutover is no longer blocked on it — the legacy `bead-importer.sh` is dropped outright in Phase 1's cutover.

## Open items deferred to their phases

1. **Phase 3 — rig-scope registration.** Determine experimentally whether `[packs.<name>]` is enough for rig-scoped agents or whether `[rigs.imports.<name>]` is the required shape. Will affect whether activation needs a per-pack scope mode.
2. **Phase 4 — audit prompts for ZR refs.** None expected in mayor/deacon/operator/foreman; verify during migration.

## Renaming rules (applies everywhere)

- All pack names use `pgii-*` prefix. No `zr` prefix anywhere.
- "ZipRecruiter" and "ziprecruiter" do not appear in pack source files. Generic terms (`team`, `org`, `repo`, `user`) are fine.
- Marker syntax: `# BEGIN pgii-pack:<name> (managed)` / `# END pgii-pack:<name> (managed)`.
- Agent prefixes in queries / matchers: `pgii-pr-support.pr-*` (was `zr.pr-*`).
- HM option namespace: `phillipgreenii.programs.pgii.*`.
- Nix package names: `pgii-pack-<name>` (mirrors the pack name, prefixed by `pack-` to distinguish from other pgii-\* nix packages).

## References

- Companion design (predecessor): `docs/superpowers/specs/2026-05-19-pg-pr-design.md`
- Legacy pack source: `/Users/phillipg/gc/assets/imports/{zr,pgii-dolt-hacks,pgii-gastown,pgii-workers}/`
- Half-done nix migration: `phillipg-nix-ziprecruiter/modules/pg-pr-zr/`
- pg-pr-zr migration plan (precedent for activation pattern and parallel-run): `phillipg-nix-ziprecruiter/modules/pg-pr-zr/MIGRATION.md`
- Workspace conventions: `/Users/phillipg/gc/CLAUDE.md` (gc/Gas City rules)
