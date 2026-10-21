# The `build_tmp` and `claude_code_tmp` roots

Two sibling home-manager modules, `phillipgreenii.programs.build-tmp` and
`phillipgreenii.programs.claude-code-tmp` (code: `home/programs/build-tmp`,
`home/programs/claude-code-tmp`, shared plumbing `lib/tmp-root.nix`), create a
temp root on a machine-specific volume and point tools at it:

- `build_tmp` holds Go build/test temp output (`GOTMPDIR`, the pg-test-runner
  root, and `TMPDIR` only when `exportTmpdir` is set).
- `claude_code_tmp` holds Claude Code's own temp tree (`CLAUDE_CODE_TMPDIR`).

The modules create the directory, the guarded environment and the CETA contribution
(packet A), and contribute the cleanup: one `pg-disk-reclaimer` registry item per root
(packet B, described under "Cleanup" below). The daily schedule and the host enablement
are later packets. Neither module is enabled for any host here.

## What a module does

- `path` has NO default and its last component MUST be `build_tmp` or
  `claude_code_tmp` (a Nix assertion). A generic module never names a volume.
- The activation step MUST create the directory only when its parent (the mount
  point) already exists, MUST refuse a symlink, MUST apply mode `0700` on every
  apply and MUST write the marker file (`.build-tmp-root` or
  `.claude-code-tmp-root`) that the sweep's root guard requires. Every failure
  prints a skip message and exits 0, so a `set -e` activation cannot abort an apply.
- The shell snippet (`programs.zsh.envExtra`, and `programs.bash.profileExtra` plus
  `bashrcExtra`, each gated on the home-manager shell flag) exports its variables
  only when the directory exists and is writable at shell start. It never exports
  `CETA_EXTRA_TEMP_ROOTS`. GUI and launchd-started processes do not read these
  files, and a shell that predates an apply keeps its old environment: check in a
  NEW shell.
- When the CETA module's options are declared and the root is enabled, the root is
  appended to BOTH `extraTempRoots` (temp classification) and `extraReadWriteRoots`
  (path-safety read/write), because temp classification alone gives no read or
  write opinion.
- Enabling a root MUST be accompanied by `phillipgreenii.programs.pg-disk-reclaimer.enable`
  (asserted): the registry that the cleanup is driven by is only written then.

## Cleanup: the registry items and the sweep

Each enabled root adds one `pg-disk-reclaimer` registry item (`build-tmp`,
`claude-code-tmp`) with two variants, selected by `reclaim --aggressiveness N [ID...]`
(highest variant `<= N`; a dry run unless `--apply`; no prompt at levels 1 and 3):

| Item              | Level 1                              | Level 3                             | Units judged                                                      |
| ----------------- | ------------------------------------ | ----------------------------------- | ----------------------------------------------------------------- |
| `build-tmp`       | idle `level1Minutes` (default 1440)  | idle `level3Minutes` (default 60)   | every top-level entry                                             |
| `claude-code-tmp` | idle `level1Minutes` (default 20160) | idle `level3Minutes` (default 2880) | `<root>/claude-<uid>/<project>/<session>` plus files at depth 1-2 |

Both commands call ONE function, `pgdr_tmp_sweep`, in
`packages/pg-disk-reclaimer/pg-disk-reclaimer/pgdr-tmp-sweep.bash`, sourced by absolute
store path inside a `( )` subshell (`: ; ( . <lib> && pgdr_tmp_sweep ... )`), because the CLI
runs `removeCommand` by an in-process `eval`. The idle minutes come from the module
options, so tuning one changes the command. The library header documents the whole
contract; in short:

- A unit is IDLE when nothing in it, and not the unit itself, was modified within the
  cutoff (`find -mmin`, `mtime` only): the whole unit is removed or kept. Symlinks are
  never followed or removed. The root's marker and every `.sweeping.*` name are never
  candidates; at depth 1 a top-level `claude-*` entry is skipped unconditionally (a
  safeguard in case Claude's tree is ever pointed at `build_tmp`).
- The root guard refuses (exit 2, nothing removed) unless the root is an absolute,
  non-symlink directory owned by the user, at least three path components deep, not
  `$HOME` or an ancestor of it, with the asserted basename, mode `0700` and its marker file.
  The marker is what stops a mis-set, user-owned path from being swept.
- Liveness is captured once (`ps -axww -o command=`, `lsof -n -w -Fn`; cwd only for
  `build-tmp`) and a failed or empty capture aborts the sweep, fail closed. A unit is kept
  when it holds a socket or FIFO, its path is in a process's argv, or an open file lies
  under it; `claude-code-tmp` also keeps a unit whose basename holds, as a whole token
  (boundary: any character outside `[0-9A-Za-z-]`), a session uuid that is a whole token
  in the `ps` capture (`--session-id <uuid>`, `--resume <uuid>`).
- A removal renames the unit to `<root>/.sweeping.<pid>.<n>.<name>` (depth 1, same
  volume), re-checks idleness there, then `rm -rf`s it; activity found by the re-check moves
  it back. A leftover `.sweeping.*` from an interrupted run is removed at the start of the
  next sweep. Empty depth-2 then depth-1 directories that were idle before the sweep are
  `rmdir`ed.
- A removal that fails is logged and the sweep continues; the exit status is 3 when a
  user-owned unit failed. Entries the user does not own are skipped and logged.

**Known gap (stated, not closed):** a headless `claude -p` worker (for example the
`pg-desk` workers) gets a session directory whose uuid is NOT in its argv, so the live
session guard does not see it; only the idle cutoff protects it. The same holds for
`--resume <id> --fork-session` if the new directory name is not in argv (untested). A path
that appears only in a process's ENVIRONMENT (a `TMPDIR=<root>/...` handed to `go test`)
is not detected either. The open-file capture closes part of this gap: a session whose
background tasks hold `tasks/*.output` open is kept.

Tests: the sweep in `tests/test-pg-disk-reclaimer-tmp-sweep.bats` (the package's own check,
real `find`/`rm`/`mv`/`ps`/`lsof` on throwaway roots, run under `set -euo pipefail`), and
the rendered registry through the real CLI in `tests/tmp-root-registry.bats` (the check
`test-tmp-root-registry`). The nix sandbox is GNU-only and may deny `ps`/`lsof`, in which
case the cases that need them skip; run both suites outside the sandbox on macOS, once with
GNU coreutils first on `PATH` and once with the BSD tools first
(`PATH=/usr/bin:/bin:/usr/sbin:/sbin:...`).

## Facts the roots depend on (tested 2026-10-21)

The volume that holds these roots is a different APFS volume from the one holding
`$HOME` and `/var/folders`. Commands run, with the observed result:

```bash
diskutil info /Volumes/<vol> | grep -E 'File System Personality|Mount Point'
# File System Personality: Case-sensitive APFS

W="$(mktemp -d /Volumes/<vol>/<fixture>.XXXXXX)"; B="$(mktemp -d)"   # B is under /var/folders
touch "$W/f"
ln "$W/f" "$B/hl"                    # ln: failed to create hard link ... Cross-device link
python3 -I -c 'import os,sys; os.rename(sys.argv[1], sys.argv[2])' "$W/f" "$B/rn"
                                     # OSError: [Errno 18] Cross-device link (EXDEV)
touch "$W/Case" "$W/case" "$B/Case" "$B/case"
ls "$W" "$B"                         # W holds both names; B holds only one
```

- `rename(2)` and hard links between the two volumes fail with `EXDEV`. A tool that
  renames from `$TMPDIR` into the home volume may break or fall back to a copy,
  which is why `exportTmpdir` defaults to false.
- The root's volume is case-sensitive while the home volume is not.
- `atime` is not a reliable idleness signal here, so cleanup is `mtime`-based.

## Spotlight, Time Machine and file watchers

A directory that receives constantly created and deleted artifacts is churn for
indexers and backup. Checked on the machine this was written on, for BOTH roots
(they sit on the same volume, so the answer is the same):

```bash
mdutil -s /Volumes/<vol>
# /System/Volumes/Data/Volumes/<vol>: Indexing and searching disabled.

tmutil isexcluded /Volumes/<vol>          # [Excluded]  /Volumes/<vol>
tmutil destinationinfo                    # tmutil: No destinations configured.

# per-directory exclusion mechanics, on a throwaway fixture under the volume:
tmutil addexclusion "$W"; xattr "$W"      # com.apple.metadata:com_apple_backup_excludeItem
tmutil removeexclusion "$W"; xattr "$W"   # (empty)
```

- Spotlight indexing was already disabled for the volume and the volume was already
  excluded from Time Machine (no backup destination was configured either), so no
  per-directory exclusion was applied or needed. If a host's volume is indexed or
  backed up, the per-directory form is `tmutil addexclusion <root>` (a sticky
  attribute on the directory, which an `rm -rf` of the root loses) and a Spotlight
  Privacy entry; renaming the root to a `.noindex` name is NOT done, because the
  basename is asserted.
- File watchers (watchman, fsmonitor, IDE watchers) were NOT tested. An unexpected
  load from them on a root's volume is a follow-up for the verification after apply.

The real `build_tmp` and `claude_code_tmp` directories were not created while this
was written; the checks above ran on fixtures only.

## Tests

`checks.<system>.test-tmp-root-modules` evaluates both modules against stubs
(`tests/tmp-root-modules.nix`) and runs the rendered snippets and activation scripts
under `tests/tmp-root-env.bats` (bash and zsh, `env -i`, a real temporary directory)
and `tests/tmp-root-activation.bats` (a fixture parent directory).
