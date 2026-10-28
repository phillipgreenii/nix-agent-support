# pg-repo-runner

`pg-repo-runner` is the kernel and facade of the pg-router-managed async repo runners (design
`pg-router-async-runners`, origin bead `pg2-na2nr`; ADR 0108 in `docs/adr/`). It is a bash
`mkBashScript` package (`pg-repo-runner/pg-repo-runner.sh`, the kernel functions in
`pg-repo-runner/pg-repo-runner.bash`) with a home-manager module,
`phillipgreenii.programs.pg-repo-runner` (`home/programs/pg-repo-runner`).

This is bead `pg2-zj2v7m` (F1): the kernel, the verdict store, `status`, and stubs for everything
else. The role bodies land in later beads (B: `verify`, A: `sync`, C: `build`; the sources `tips`
and `workspace-tips`). Each of those stubs exits 2 with "not implemented".

**No repo is enabled for push by this package, its module defaults, its tests or any deployment
config in this repository.** `repos.<name>.push` defaults to `false` for every repo, the module
asserts that default, and the kernel in this version contains no push at all. Enabling a repo needs
an operator ruling recorded on a bead (ADR 0108, decision 1), and the first enablement runs dry-run
(decision 6).

## Commands

| Command          | What it does                                                                  |
| ---------------- | ----------------------------------------------------------------------------- |
| `tips`           | Source (stub, exits 2).                                                       |
| `workspace-tips` | Source (stub, exits 2).                                                       |
| `verify`         | Role: the kernel runs, then the stub exits 2.                                 |
| `sync`           | Role: the kernel runs, then the stub exits 2.                                 |
| `build`          | Role (workspace-wide, no repo item): the kernel runs, then the stub exits 2.  |
| `status`         | Prints the verdict store, the lease and the kill switch (`--json`, `--repo`). |

## The kernel (Template Method)

A role runs these steps in order and stops at the first that fails. `build` skips steps 1 to 3
(it names no repo).

1. **resolve.** `PG_ROUTER_ITEM_ID` (which pg-router always exports) is `<key>` or `<key>@<sha>`.
   `<key>` MUST be in the config allow-list (`repos.<key>`) AND be a repo key of
   `<workspace_root>/pn-workspace.toml`. Otherwise exit 2.
2. **path check.** The real path of `<workspace_root>/<key>` MUST equal
   `<real workspace_root>/<key>` and be the top level of a git repository. A symlinked key, a plain
   directory, or a path outside the workspace (in particular the employer's private monorepo, which
   is not a workspace repo) is refused with exit 2.
3. **role flag.** `repos.<key>.verify` (for `verify`) or `repos.<key>.sync` (for `sync`) MUST be
   true; if it is false the run is skipped and exits 0.
4. **kill switch.** `<state dir>/push.disabled` or the file named by `kill_switch` in the config
   exists: exit 9.
5. **load check.** The 1-minute load average MUST be strictly below the threshold
   (`thresholds.max_load`, else the core count): otherwise exit 9.
6. **single-flight lease.** One lease for all roles, taken without waiting (see below): held by a
   live process, exit 9.
7. **act**, then record a verdict, then release the lease.

Exit codes: `0` for any verdict (a red check is data, not a failure), `9` for "not now" (pg-router
re-offers the item until it expires), `2` for a usage error, a refusal or a stub, `1` for a
malfunction (unreadable config, no load reading, an unusable state directory).

### Lease

`$XDG_STATE_HOME/pg-repo-runner/lease` is a symlink whose target is `<pid>:<process start time>`.
`ln -s` is atomic and fails when the name exists, so creating it IS taking the lease. A lease is
stale when its pid is dead, or alive with a different start time (a recycled pid); a stale lease is
renamed aside (atomic, so one reclaimer wins), re-checked, and taken. This is the pattern
`pg-test-runner` uses for its run slots, without the wait and without the fail-open.

### Verdict store

`$XDG_STATE_HOME/pg-repo-runner/verdicts/<repo>/<sha>.<kind>.json`, with the fields `repo`, `sha`,
`kind` (`check` or `build`), `result` (`green`, `red`, `error`), `inputs` (`locked` or `override`),
`startedAt`, `finishedAt`, `exit`, `logPath`. `rr_verdict_write` builds the document in a temp file
in the same directory and renames it over the final name, so a reader sees the old verdict or the
new one, and a failed write leaves no temp file. The run log is
`$XDG_STATE_HOME/pg-repo-runner/run.log` (pg-router discards a role's stdout and stderr, so this file
is where a run is explained).

### nix

`rr_nix_run ARGS...` is the one way a runner starts nix: `nice -n <level> pg-nix-log-wrapped nix
ARGS...`, with every binary resolved by the absolute store path the nix build injects
(`RR_NICE_BIN`, `RR_NIX_LOG_WRAPPED_BIN`, `RR_NIX_BIN`; the config's `thresholds.nice_level`,
default 10). F1 offers it; the roles of the later beads call it.

## Configuration

The home module renders `$XDG_CONFIG_HOME/pg-repo-runner/config.toml` (override with
`PG_REPO_RUNNER_CONFIG`):

```toml
workspace_root = "/path/to/workspace"
kill_switch = "/path/to/state/pg-repo-runner/kill"

[thresholds]
max_load = 11.0 # omitted when unset: the core count
nice_level = 10

[repos.nix-example]
verify = false
sync = false
push = false
```

Every flag defaults to `false`. The allow-list is the ONLY source of repos a runner may touch. The
pg-router queries and roles that drive the commands belong to the deployment repository, not to this
package.

## Test seams (environment)

| Variable                  | Effect                                                         |
| ------------------------- | -------------------------------------------------------------- |
| `PG_REPO_RUNNER_CONFIG`   | Path of the config file.                                       |
| `PG_REPO_RUNNER_LOADAVG`  | Replaces the 1-minute load reading (the bats suites use this). |
| `PG_REPO_RUNNER_MAX_LOAD` | Replaces the load threshold.                                   |
| `PG_REPO_RUNNER_NCPU`     | Replaces the core count used as the default threshold.         |
| `XDG_STATE_HOME`          | Root of the state directory.                                   |

## Load threshold: the first calibration sample

The design asks the first bead to record the sampled 1-minute load while a nix check runs, as the
calibration input for `thresholds.max_load` (default: the core count).

Measured 2026-10-28 on the development Mac (`sysctl -n hw.ncpu` = 11). How: while this bead's own
targeted build ran, `nix build -L --no-link .#checks.aarch64-darwin.test-pg-repo-runner
.#checks.aarch64-darwin.test-pg-repo-runner-hm-render` (through `pg-nix-log-wrapped`, 57 bats tests plus
the module render check), `uptime` was sampled every 10 seconds, 11 samples from 01:18:14Z to
01:19:54Z.

| Quantity                        | Value                                                                                                   |
| ------------------------------- | ------------------------------------------------------------------------------------------------------- |
| 1-minute load, first sample     | 11.06                                                                                                   |
| 1-minute load, last sample      | 7.65                                                                                                    |
| 1-minute load, minimum, maximum | 7.65, 11.06                                                                                             |
| 1-minute load, mean of 11       | (11.06 + 10.67 + 9.96 + 9.74 + 9.33 + 8.99 + 8.46 + 8.18 + 7.69 + 7.96 + 7.65) / 11 = 99.69 / 11 = 9.06 |
| 5-minute load, first and last   | 40.90 and 31.23                                                                                         |

Reading it: the sample is NOT attributable to the runner. It is a small check build, not a
`nix flake check`, and the host was carrying other sessions' work (the 5-minute average of 31 to 41
shows the load that preceded the sample, and the 1-minute average was falling, from 11.06 to 7.65,
while the build ran). With the default threshold of 11 (the core count), a run would have been
admitted at 10 of the 11 samples and refused only at the first (11.06 is not below 11). A
`nix flake check` run by a runner will add its own load on top of whatever the host carries, so the
default admits only when the host is otherwise quieter than this sample. Treat the number as the
first data point for the operator's calibration, not as a conclusion; the F2 bead records the
measured duration and load of the real runner checks.

## Tests

- `pg-repo-runner/tests/test-pg-repo-runner.bats`: the real script against a real temporary
  workspace (a `pn-workspace.toml` and real git clones of bare local remotes), live and dead
  processes for the lease, the real verdict store. Covers the allow-list, the path check, the kill
  switch, the load check, the single-flight lease, stale-lease reclaim, atomic verdicts and `status`.
- `pg-repo-runner/tests/test-pg-repo-runner-lib.bats`: the kernel functions one at a time.
- `checks.<system>.test-pg-repo-runner`: both suites, built through `mkBashScript`.
- `checks.<system>.test-pg-repo-runner-hm-render`: evaluates the home module (push, verify and sync
  default to false for every repo, no case enables push, the assertions fire), builds the rendered
  `config.toml`, and runs the real package against it.

Run one check: `nix build .#checks.<system>.test-pg-repo-runner` (in the background, through
`pg-nix-log-wrapped`).
