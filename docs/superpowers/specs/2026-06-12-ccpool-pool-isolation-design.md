# ccpool pool isolation — design

**Status**: Approved (brainstorm) + subagent design review folded in — pending final spec review
**Date**: 2026-06-12
**Deciders**: Phillip Green II (with Claude)

## Context

ccpool today has exactly **one global scope**, defined entirely by env + a single
config file:

- Config: `$XDG_CONFIG_HOME/ccpool/config.toml` (one file → one pool's rules:
  `max_sessions`, `idle_ttl`, list TTLs, notify, model defaults)
- Store: `$XDG_DATA_HOME/ccpool/store.db` · State: `$XDG_STATE_HOME/ccpool` ·
  Runtime/lock: `$XDG_RUNTIME_DIR`
- tmux: `socket` (default `ccpool`) + `prefix` (default `cc-`)
- No `--config`, no `CCPOOL_CONFIG`, no `--pool`/profile/namespace knob.

The mechanism for isolation already exists — the contract harness (`newSandbox`)
spins up fully isolated pools per test by overriding `XDG_*`, a unique tmux socket,
the prefix, and its own config. But that isolation is bespoke Go code in the harness;
a human cannot invoke it. Concurrent use cases (e.g. a `pr-pool` workload vs. manual
exploration) that want different pool sizes/rules, or simply non-interference, are not
expressible today.

## Goals

- Let a user run **multiple isolated pools**, each with its own store, tmux server,
  and (optionally) rules.
- Support **both** persistent named pools (a stable path, reused) and **ephemeral**
  throwaway pools (a temp dir, discarded) with one mechanism.
- **Zero behavior change** for existing single-pool installs until they opt in.
- Comprehensive test coverage of every new component and behavior.

## Non-goals (YAGNI for v1)

- Moving/renaming/migrating a pool (path is identity; a future `rename`/`migrate`
  command can handle it if ever needed).
- A `destroy` convenience (close-all + kill tmux server + `rm -rf`).
- Cross-pool views (`list --all-pools`).
- cwd-bound auto-detection of the active pool.
- Per-pool isolation of the trust store and notify adapters (see Non-isolation
  boundaries — these stay process-global by design).

## Known v1 limitation: the reap timer only governs the default pool

`reap` runs from a launchd/systemd timer as bare `ccpool reap` with no `--pool`
(`darwin/modules/ccpool/default.nix`, the nixos mirror, interval in
`home/programs/ccpool/default.nix`). With no flag/env it resolves the **default**
pool, so a non-default pool's `idle_ttl`/`max_sessions` governance is **not** run by
the system timer. For v1, a non-default pool owner must run their own
`ccpool --pool P reap` (cron/direnv/manual). A follow-up bead will scope per-pool
timer registration (deferred because it reintroduces a pool registry in the nix
module, which v1 deliberately avoids). The implementation still wires `reap` to honor
`--pool`/`CCPOOL_POOL` so that future timer fix is a config change, not a code change.

## Design

### Model: a pool is a directory

A pool is identified by a **directory path**; its canonical (symlink-resolved) path
is its identity. There is no registry and no naming scheme — the path is the name.
This unifies the two use cases for free:

- **Persistent**: a stable path you reuse (`~/pools/pr-pool`, or `$PWD/.ccpool`
  dropped in a repo's `.envrc` via direnv → that repo is a pool).
- **Ephemeral**: a `mktemp -d` you `rm -rf` when done.

### Resolution precedence

For every command, the effective pool is resolved as:

1. `--pool <dir>` (global flag) — highest
2. `CCPOOL_POOL=<dir>` (env)
3. **built-in default** — today's XDG layout, unchanged

Two resolution modes result:

- **default mode** (no flag/env): config `$XDG_CONFIG_HOME/ccpool/config.toml`, db
  `$XDG_DATA_HOME/ccpool/store.db`, socket `ccpool`, prefix `cc-`, lock
  `$XDG_RUNTIME_DIR`, state `$XDG_STATE_HOME/ccpool`. Bit-for-bit the current
  behavior.
- **pool-dir mode** (flag/env set): everything collapses into the one directory
  (below).

### Pool-dir layout

A pool dir `P` is **canonicalized** (`EvalSymlinks` + `Abs` + `filepath.Clean`)
before any use, so `./pool`, `pool/`, and a symlinked path all map to the same pool.
Note: today the code canonicalizes the launch cwd with `filepath.EvalSymlinks` ONLY
(`session.go`, `trust.go`) — no `Abs`/`Clean` — and `EvalSymlinks` errors on a
not-yet-existing path. So `Abs`/`Clean` are NEW here, and for a not-yet-created leaf
we canonicalize the **parent** then join the leaf. Identity is the resolved target:
repointing a symlink that `P` traverses changes the hash and is therefore equivalent
to moving the pool (see orphaning note under Hook re-entry).

`P` contains **only** ccpool-managed files. Because the lock layer writes one file
per session (`<name>.lock`) and SQLite can leave several sidecars, the allowlist is a
set of **patterns**, not literal names:

| Pattern                  | Purpose                                                              |
| ------------------------ | -------------------------------------------------------------------- |
| `config.toml` (optional) | pool rules; absent → built-in compiled defaults                      |
| `store.db*`              | the session store + SQLite sidecars (`-wal`, `-shm`, `-journal`)     |
| `*.lock`                 | per-session flocks (`internal/lock` writes `<name>.lock` per name)   |
| `hook.log`               | per-pool hook/state diagnostics (in default mode this is global XDG) |

The allowlist is derived from the **patterns ccpool itself writes** (the same
prefixes/suffixes the code uses to create them), never a hand-maintained literal
list, so the two cannot drift. Any entry not matching a pattern — including a
subdirectory — is "foreign".

The tmux socket is **not** stored in `P`. It is a named socket
`-L cc-<hash(canonical P)>` (`cc-` + first 16 hex of sha256 = 19 chars). tmux places
a `-L` socket at `$TMUX_TMPDIR/<name>` (or `/tmp/tmux-<uid>/<name>`); the ~104-char
Unix-socket limit applies to that **full path**, so the short hashed basename keeps us
well clear regardless of how deep `P` is. **Prefix stays the constant `cc-`**: each
pool has its own tmux server (its own socket), so cross-pool prefix collisions are
impossible.

### Config inheritance

**No `config.toml` in `P` → built-in compiled defaults** (`max_sessions=6`,
`idle_ttl=30m`, etc.) — not the default pool's `config.toml`. A pool is
self-contained; it does not inherit the user's global config. (Confirmed judgment
call.)

### Validation & creation

On resolve in pool-dir mode:

1. Canonicalize the parent; join the leaf.
2. If the dir **exists**: every entry must match a managed allowlist pattern
   (`config.toml`, `store.db*`, `*.lock`, `hook.log`). Any other entry — a stray
   file OR a subdirectory — → refuse: `not a ccpool pool dir: <path> contains <name>`.
   Exit code **2** (precondition/usage failure, consistent with `runNew`'s usage
   exits).
3. If the dir is **missing**: `mkdir` the leaf only, mode `0700` (matching
   `store.Open`/`internal/lock`). The parent must already exist, else error (guards
   typos like `--pool /pat/typo`). Never `mkdir -p`.
4. An empty (or allowlist-only) dir is valid; `store.db` + tmux socket are created on
   demand by normal operation.

### Hook re-entry (correctness lynchpin)

A launched claude session's hooks call bare `ccpool` (from the plugin) to report
state. Two things must be pool-correct:

1. **State db.** For a session created in pool `P` to keep updating `P`'s db (not the
   default pool's), the launch sites inject `CCPOOL_POOL=<canonical P>` into the
   session env, alongside the existing `CCPOOL_NAME`/`CCPOOL_UUID`. There are **two**
   launch sites, not one: `runNew` and `runReply` (cold-resume goes
   `Ensure`→`ensureLocked`→`launchAndWait`). Both build `session.Deps` inline today,
   so both must thread the pool. The `ccpool hook` invocation then re-resolves the
   same pool via the precedence above.
2. **Hook log.** `hook.log` is per-pool. The hook resolves its state dir from
   `CCPOOL_POOL` directly, **without** `config.Load` — preserving the existing
   property that hook logging survives a malformed config (today `runHook` →
   `StateDirPath()` reads `XDG_STATE_HOME` with no config read). `StateDirPath` gains
   a pool argument (env-derived); in default mode it is unchanged.

Default-pool sessions leave `CCPOOL_POOL` unset → hooks resolve the default →
unchanged. Because path is identity, a live session is bound to its pool path; moving
`P` (or repointing a traversed symlink) while sessions are live changes the hash and
orphans them (out of scope, see non-goals).

### Non-isolation boundaries (explicit)

Pools isolate **store, tmux server, config, locks, and hook.log**. They do **not**
isolate:

- **Trust** (`~/.claude.json`): folder-trust is keyed by cwd and written to the
  shared `~/.claude.json` regardless of pool (`new.go`/`trust.go`). This is global by
  design (trust is per-directory, not per-pool); two pools launching the same cwd
  share the trust entry.
- **Notify adapters** (desktop/exec): resolved per process from the active pool's
  config, but the desktop adapter targets the one user session — there is no per-pool
  notification routing.

These are stated so consumers don't assume isolation that isn't there.

### Components (where it lands)

This touches more call sites than a single seam: `config.Load()` is invoked in ~13
command files plus `hook.go`, and `buildService` (`cancel.go`), `runNew` (`new.go`),
and `runReply` (`reply.go`) each assemble their service/`Deps` inline. The work:

- **`internal/config`** — a pool-resolution step producing the effective paths
  (config path, db path, lock dir, state dir) + tmux socket name from a selected pool
  root (or default). `Load()` (and `StateDirPath`) parameterized by the resolved
  root. New helpers: `ResolvePool(flag, env string) (PoolContext, error)`,
  `socketFor(canonicalPath) string`, `validatePoolDir(path) error`.
- **`cmd/ccpool/main.go`** — `main()` calls `pickSubcommand(os.Args)` which matches
  `args[1]` against a known-subcommand map; a leading `--pool` would fall through to
  the `list` default, and the subcommands' `flag.ExitOnError` flagsets would hard-exit
  on a leaked `--pool`. So `--pool <dir>` (and its value) must be **stripped from
  `os.Args` BEFORE `pickSubcommand`**, with the position contract
  `ccpool --pool <dir> <subcommand> …`. A `--pool` appearing after the subcommand is
  rejected deterministically (exit 2), not left to `ExitOnError`. Also read
  `CCPOOL_POOL`; thread the resolved `PoolContext` into the service/config builders.
- **`cmd/ccpool/{cancel.go,new.go,reply.go}` + every command's `config.Load`** — use
  the resolved pool (the ~13 call sites + the inline `Deps` builders in new/reply).
- **`internal/session/session.go`** — add a pool field to `Deps`; inject
  `CCPOOL_POOL` into the session env where `CCPOOL_NAME`/`CCPOOL_UUID` are set today
  (inline in `launchAndWait`).
- **`cmd/ccpool/hook.go`** — resolve pool via the same precedence; resolve `hook.log`
  under the pool (env-derived, no `config.Load`).
- **`cmd/ccpool/doctor.go`** — print the resolved pool context (root, db path, socket
  name) in both modes.

## Test plan (tests for everything)

### Unit — `internal/config`

- **Resolution precedence** (table-driven): flag-only, env-only, both (flag wins),
  neither (default). Assert the resolved root + that default mode yields the exact XDG
  paths. Extend the existing `config_test.go` default-path assertions rather than
  duplicating them.
- **Canonicalization**: a symlinked path, a trailing-slash path, and a relative path
  to the same dir all resolve to one identity → identical socket name + db path.
- **Socket derivation**: deterministic per canonical path; two distinct paths →
  distinct sockets; `cc-`+16-hex basename is fixed-length and short (the
  full-path length budget incl. `$TMUX_TMPDIR` is what matters — assert basename
  length and document the prefix).
- **Validation allowlist**: empty dir → OK; allowlist-only dir → OK including
  multiple distinct `<name>.lock` files and a `store.db-journal` sidecar; one foreign
  file → refuse with the exact error + offending name; a foreign **subdirectory** →
  refuse.
- **Creation**: missing leaf + existing parent → created at `0700`; missing parent →
  error (no `mkdir -p`); existing dir → not recreated/untouched.
- **No-config defaults**: a pool dir with no `config.toml` loads the built-in
  compiled defaults (assert `max_sessions=6`, `idle_ttl=30m`, etc.), not the default
  pool's config.

### Unit — `internal/session`

- **Env injection on new**: the env assembled in `launchAndWait` includes
  `CCPOOL_POOL=<canonical P>` in pool-dir mode (alongside `CCPOOL_NAME`/`CCPOOL_UUID`);
  omitted (unset) in default mode.
- **Env injection on resume**: a cold-resume via `Ensure`→`launchAndWait` (the
  `runReply` path) also injects `CCPOOL_POOL` — explicit second case so the resume
  launch site isn't missed.

### Unit — `cmd/ccpool`

- **Global `--pool` parse + `pickSubcommand`**: `ccpool --pool <dir> <cmd> …` strips
  `--pool`+value before dispatch and dispatches `<cmd>` with the remaining args
  intact; a `--pool` placed AFTER the subcommand is rejected deterministically (exit
  2), not swallowed by `ExitOnError`; an invalid pool dir → exit 2 with the validation
  error.
- **`hook` pool resolution**: `CCPOOL_POOL` set → hook resolves that pool's db AND
  writes `P/hook.log` without `config.Load`; unset → default (regression).
- **`reap` honors pool**: `ccpool --pool P reap` (or `CCPOOL_POOL=P`) reaps P's
  sessions; document/assert that bare `ccpool reap` only touches the default pool (the
  timer-limitation regression guard).
- **`doctor` output**: prints root + db path + socket name in pool-dir mode (and the
  pool's `hook.log` path); prints the XDG/default context in default mode.

### Integration — fake-claude + real tmux (the core isolation guarantee)

- Two distinct `--pool` dirs `A` and `B`. Create sessions in each (`--pool A new
alpha`, `--pool B new beta`). Assert:
  - `--pool A list` shows only `alpha`; `--pool B list` shows only `beta`.
  - the two pools use **distinct tmux sockets** (`cc-<hashA>` ≠ `cc-<hashB>`; servers
    independent).
  - `--pool A reap`/`close` does not touch `B`'s sessions.
  - the default pool (no flag) sees neither.
- Validation refusal end-to-end: `--pool <dir-with-a-foreign-file>` → exit 2.

### Gate

- `go test ./...` + `go vet ./...` (both tags) green.
- `nix build .#ccpool` green (hermetic go-test gate).
- The new fake-claude integration test is token-free, so it runs in the normal suite
  without driving real claude. The `nix run .#ccpool-contract` real-claude suite is
  unaffected (pools are an orthogonal layer; default-mode contract scenarios stay
  green).

## Acceptance criteria

- A user can run two pools concurrently via `--pool <dir>` (or `CCPOOL_POOL`) with
  fully isolated stores + tmux servers + `hook.log`, and differing rules via each
  pool's `config.toml`.
- No flag/env → byte-for-byte today's behavior (regression-tested).
- Pointing `--pool` at a dir with foreign content (file OR subdirectory) is refused
  (exit 2); a fresh leaf (parent exists) is created `0700` on demand.
- Sessions launched in a pool (via `new` AND `reply`/resume) keep their hook-driven
  state updates AND `hook.log` within that pool.
- `reap` honors `--pool`/`CCPOOL_POOL`; the system timer's default-only scope is
  documented.
- `doctor` reveals the active pool's root, db, socket name, and hook.log path.
- All tests above pass; `nix build .#ccpool` green.

## Open questions

None outstanding. Confirmed during brainstorm + review:

1. No config → built-in defaults (not inherit default pool). ✔
2. `doctor` shows pool context in v1. ✔
3. Move/destroy/cross-pool-views deferred. ✔
4. `hook.log` is per-pool (hook resolves `CCPOOL_POOL` without `config.Load`). ✔
5. Reap timer governs only the default pool in v1 (documented limitation +
   follow-up bead); `reap` itself honors `--pool`. ✔
