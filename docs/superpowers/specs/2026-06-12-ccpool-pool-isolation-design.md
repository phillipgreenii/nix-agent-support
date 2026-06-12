# ccpool pool isolation — design

**Status**: Approved (brainstorm) — pending spec review
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

The _mechanism_ for isolation already exists — the contract harness (`newSandbox`)
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

- Moving/renaming/migrating a pool (path _is_ identity; a future `rename`/`migrate`
  command can handle it if ever needed).
- A `destroy` convenience (close-all + kill tmux server + `rm -rf`).
- Cross-pool views (`list --all-pools`).
- cwd-bound auto-detection of the active pool.

## Design

### Model: a pool is a directory

A pool is identified by a **directory path**; its canonical path _is_ its identity.
There is no registry and no naming scheme — the path is the name. This unifies the
two use cases for free:

- **Persistent**: a stable path you reuse (`~/pools/pr-pool`, or `$PWD/.ccpool`
  dropped in a repo's `.envrc` via direnv → that repo _is_ a pool).
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

A pool dir `P` is canonicalized (`EvalSymlinks` + `Abs` + `filepath.Clean`) before
any use, so `./pool`, `pool/`, and a symlinked path all map to the same pool. (ccpool
already canonicalizes cwd for trust keys — same pattern.) For a not-yet-created leaf,
canonicalize the **parent** then join the leaf.

`P` contains **only** ccpool-managed files:

| File                                     | Purpose                                                           |
| ---------------------------------------- | ----------------------------------------------------------------- |
| `config.toml` _(optional)_               | pool rules; absent → built-in compiled defaults                   |
| `store.db` (+ `-wal`, `-shm`)            | the pool's session store                                          |
| the lock file(s) `internal/lock` creates | per-pool flock (in default mode these live in `$XDG_RUNTIME_DIR`) |
| `hook.log`                               | per-pool hook/state diagnostics                                   |

The allowlist is exactly **the set of names ccpool itself writes** — the
implementation derives it from the same constants it uses to create them, never a
hand-maintained literal list, so the two cannot drift. Any other entry (including a
subdirectory) is "foreign".

The tmux socket is **not** stored in `P`. It is a named socket
`-L cc-<hash(canonical P)>` (e.g. `cc-` + first 16 hex of sha256 = ~19 chars, always
under the ~104-char Unix-socket limit) living in tmux's tmpdir, deterministically
tied to the pool path. **Prefix stays the constant `cc-`**: each pool has its own
tmux server (its own socket), so cross-pool prefix collisions are impossible.

### Config inheritance

**No `config.toml` in `P` → built-in compiled defaults** (`max_sessions=6`,
`idle_ttl=30m`, etc.) — _not_ the default pool's `config.toml`. A pool is
self-contained; it does not inherit the user's global config. (Confirmed judgment
call.)

### Validation & creation

On resolve in pool-dir mode:

1. Canonicalize parent; join leaf.
2. If the dir **exists**: every entry must be in the managed allowlist (the set of
   names ccpool writes — `config.toml`, `store.db` + `-wal`/`-shm`, the
   `internal/lock` file(s), `hook.log`). Any other entry → refuse:
   `not a ccpool pool dir: <path> contains <name>`.
   Exit code **2** (precondition/usage failure, consistent with `runNew`'s usage
   exits).
3. If the dir is **missing**: `mkdir` the leaf only. The parent must already exist,
   else error (guards typos like `--pool /pat/typo`). Never `mkdir -p`.
4. An empty (or allowlist-only) dir is valid; `store.db` + tmux socket are created on
   demand by normal operation.

### Hook re-entry (correctness lynchpin)

A launched claude session's hooks call bare `ccpool` (from the plugin) to report
state. For a session created in pool `P` to keep updating `P`'s db (not the default
pool's), `new`/`resume` inject `CCPOOL_POOL=<canonical P>` into the launched
session's environment, alongside the existing `CCPOOL_NAME`/`CCPOOL_UUID`. The
`ccpool hook` invocation then re-resolves the same pool via the precedence above.

Default-pool sessions leave `CCPOOL_POOL` unset → hooks resolve the default →
unchanged. Because path _is_ identity, a live session is bound to its pool path;
moving `P` while sessions are live orphans them (out of scope, see non-goals).

### Components (where it lands)

- **`internal/config`** — a pool-resolution step producing the effective paths
  (config path, db path, lock dir, state dir) + tmux socket name from a selected pool
  root (or default). `Load()` parameterized by the resolved root. New helpers:
  `ResolvePool(flag, env string) (PoolContext, error)`, `socketFor(canonicalPath)
string`, `validatePoolDir(path) error`.
- **`cmd/ccpool/main.go`** — parse a global `--pool` before subcommand dispatch; read
  `CCPOOL_POOL`; thread the resolved `PoolContext` into `buildService` / `config.Load`.
- **`cmd/ccpool/cancel.go` (`buildService`)** and every command's `config.Load` — use
  the resolved pool.
- **`internal/session/session.go`** — inject `CCPOOL_POOL` into the session env
  (`newSessionEnv`).
- **`cmd/ccpool/hook.go`** — resolve pool via the same precedence.
- **`cmd/ccpool/doctor.go`** — print the resolved pool context (root, db path, socket
  name) in both modes.

## Test plan (tests for everything)

### Unit — `internal/config`

- **Resolution precedence** (table-driven): flag-only, env-only, both (flag wins),
  neither (default). Assert the resolved root + that default mode yields the exact
  XDG paths (regression guard).
- **Canonicalization**: a symlinked path, a trailing-slash path, and a relative path
  to the same dir all resolve to one identity → identical socket name + db path.
- **Socket derivation**: deterministic per canonical path; two distinct paths →
  distinct sockets; generated name always `< 104` chars (incl. a deep/long path
  case).
- **Validation allowlist**: empty dir → OK; allowlist-only dir (each managed file)
  → OK; one foreign file → refuse with the exact error + the offending name; verify
  each managed filename is accepted (`config.toml`, `store.db`, `-wal`, `-shm`,
  `lock`, `hook.log`).
- **Creation**: missing leaf + existing parent → created (0700); missing parent →
  error (no `mkdir -p`); existing dir → not recreated/untouched.
- **No-config defaults**: a pool dir with no `config.toml` loads the built-in
  compiled defaults (assert `max_sessions=6`, `idle_ttl=30m`, etc.), not the default
  pool's config.

### Unit — `internal/session`

- **Env injection**: `newSessionEnv` includes `CCPOOL_POOL=<canonical P>` in pool-dir
  mode, alongside `CCPOOL_NAME`/`CCPOOL_UUID`; omitted (unset) in default mode.

### Unit — `cmd/ccpool`

- **Global `--pool` parse**: `--pool <dir>` consumed before dispatch; remaining args
  reach the subcommand intact; invalid pool dir → exit 2 with the validation error.
- **`hook` pool resolution**: `CCPOOL_POOL` set → hook resolves that pool; unset →
  default (regression).
- **`doctor` output**: prints root + db path + socket name in pool-dir mode; prints
  the XDG/default context in default mode.

### Integration — fake-claude + real tmux (the core isolation guarantee)

- Two distinct `--pool` dirs `A` and `B`. Create sessions in each (`--pool A new
alpha`, `--pool B new beta`). Assert:
  - `--pool A list` shows only `alpha`; `--pool B list` shows only `beta`.
  - the two pools use **distinct tmux sockets** (their servers are independent;
    `cc-<hashA>` ≠ `cc-<hashB>`).
  - `--pool A reap`/`close` does not touch `B`'s sessions.
  - the default pool (no flag) sees neither.
- Validation refusal end-to-end: `--pool <dir-with-a-foreign-file>` → exit 2.

### Gate

- `go test ./...` + `go vet ./...` (both tags) green.
- `nix build .#ccpool` green (hermetic go-test gate).
- A new fake-claude integration test (token-free) covers the isolation guarantee, so
  it runs in the normal suite without driving real claude. The existing
  `nix run .#ccpool-contract` real-claude suite is unaffected (pools are an
  orthogonal layer; default-mode contract scenarios stay green).

## Acceptance criteria

- A user can run two pools concurrently via `--pool <dir>` (or `CCPOOL_POOL`) with
  fully isolated stores + tmux servers, and differing rules via each pool's
  `config.toml`.
- No flag/env → byte-for-byte today's behavior (regression-tested).
- Pointing `--pool` at a dir with foreign content is refused (exit 2); a fresh leaf
  (parent exists) is created on demand.
- Sessions launched in a pool keep their hook-driven state updates within that pool.
- `doctor` reveals the active pool's root, db, and socket name.
- All tests above pass; `nix build .#ccpool` green.

## Open questions

None outstanding — judgment calls confirmed during brainstorm:

1. No config → built-in defaults (not inherit default pool). ✔
2. `doctor` shows pool context in v1. ✔
3. Move/destroy/cross-pool-views deferred. ✔
