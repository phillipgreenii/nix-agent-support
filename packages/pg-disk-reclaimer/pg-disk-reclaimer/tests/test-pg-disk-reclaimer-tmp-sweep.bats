#!/usr/bin/env bats
# Tests for pgdr-tmp-sweep.bash, the guarded idle sweep behind the registry items
# build-tmp and claude-code-tmp (bead pg2-rym7f, packet B of the build_tmp
# design in pg2-2qirl, decisions D3-D7, D13).
#
# Hermetic: every sweep runs against a throwaway root made with mktemp (a short
# path, because unix sockets are limited to 104 bytes) that carries the marker
# and mode 0700. The REAL find, rm, mv, ps and lsof are used; the only stand-ins
# are PATH shims that make ps or lsof fail, which is the thing being tested. The
# real build_tmp and claude_code_tmp are never touched.
#
# The library under test is $PGDR_TMP_SWEEP_LIB when set (the mutation proofs
# point it at a mutated copy), else the file next to the package.
#
# Toolchains: the suite runs against whatever find/rm/ps are first on PATH. The
# nix sandbox is GNU-only; on macOS run it twice (GNU coreutils first, then
# PATH=/usr/bin:/bin:/usr/sbin:/sbin:... for the BSD tools). One test below also
# re-runs a compact scenario with the BSD tools first when they exist.
bats_require_minimum_version 1.5.0

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  LIB="${PGDR_TMP_SWEEP_LIB:-$SCRIPTS_DIR/pgdr-tmp-sweep.bash}"
  FX="$(mktemp -d /tmp/pgts.XXXXXX 2>/dev/null || mktemp -d)"
  FX="$(cd -P "$FX" && pwd -P)"
  SHIM="$FX/shim"
  mkdir -p "$SHIM"
  PIDS="$FX/pids"
  : >"$PIDS"
}

teardown() {
  local p
  if [[ -f ${PIDS:-} ]]; then
    while read -r p; do
      [[ -n $p ]] && kill "$p" 2>/dev/null || true
    done <"$PIDS"
  fi
  chmod -R u+rwx "$FX" 2>/dev/null || true
  rm -rf "$FX"
}

# ----- helpers ----------------------------------------------------------------

need_ps() {
  local out
  if ! out="$(ps -axww -o command= 2>/dev/null)" || [[ -z $out ]]; then
    skip "ps is not usable here (sandbox?): run the suite outside the sandbox too"
  fi
  if ! out="$(lsof -n -w -d cwd -Fn 2>/dev/null)" || [[ -z $out ]]; then
    skip "lsof is not usable here (sandbox?): run the suite outside the sandbox too"
  fi
}

# ts_ago MINUTES: a touch -t timestamp MINUTES ago (GNU date, else BSD date).
ts_ago() {
  local e
  e=$(($(date +%s) - $1 * 60))
  if ! date -d "@$e" +%Y%m%d%H%M.%S 2>/dev/null; then
    date -r "$e" +%Y%m%d%H%M.%S
  fi
}

# age PATH MINUTES: set the mtime of PATH and of everything under it (so age a
# parent BEFORE the children that must differ).
age() {
  local ts
  ts="$(ts_ago "$2")"
  find "$1" -exec touch -h -t "$ts" {} +
}

# mkroot [basename] [marker]: a fresh root with the marker and mode 0700 -> ROOT
mkroot() {
  local b="${1:-build_tmp}" m="${2:-.build-tmp-root}"
  ROOT="$FX/$b"
  mkdir -m 0700 "$ROOT"
  : >"$ROOT/$m"
  chmod 0700 "$ROOT"
}

# sweep MODE ROOT MARKER BASE MIN DEPTH LIVENESS: run the library under
# set -euo pipefail (the CLI wrapper's own mode). SWEEP_PATH overrides PATH.
sweep() {
  run env PATH="${SWEEP_PATH:-$PATH}" "$BASH" -c 'set -euo pipefail; . "$1"; shift; pgdr_tmp_sweep "$@"' _ "$LIB" "$@"
}
bsweep() { sweep "$1" "$ROOT" .build-tmp-root build_tmp "$2" 1 none; }
csweep() { sweep "$1" "$ROOT" .claude-code-tmp-root claude_code_tmp "$2" 3 claude-session; }

# refute CMD...: fails the test when CMD succeeds (a bare `! cmd` does not trip
# errexit in bats, so it would silently pass).
refute() {
  if "$@"; then
    return 1
  fi
}

flat() {
  local s="$1"
  printf '%s' "${s//$'\n'/\\n}"
}
listed() { grep -qxF -- "would remove: $(flat "$1")" <<<"$output"; }
removed() { grep -qxF -- "removed: $(flat "$1")" <<<"$output"; }

# holder: a process that just waits (python3: ps shows its full argv, unlike
# `exec -a` on a coreutils multi-call binary).
holder() { exec python3 -I -c 'import time; time.sleep(300)' "$@"; }

# spawn TEXT: a live process whose argv contains TEXT.
spawn() {
  (holder "$1") >/dev/null 2>&1 &
  echo $! >>"$PIDS"
  for _ in $(seq 1 300); do
    [[ "$(ps -axww -o command= 2>/dev/null)" == *"$1"* ]] && return 0
  done
  return 1
}

# mksock PATH: a unix socket file at PATH that outlives its creator.
mksock() {
  python3 -I -c 'import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1])' "$1"
}

shim() { # shim NAME BODY: an executable PATH shim
  printf '#!/bin/sh\n%s\n' "$2" >"$SHIM/$1"
  chmod +x "$SHIM/$1"
}

uuid() { printf '%08x-0000-4000-8000-%012x\n' "$1" "$1"; }

# ----- the library itself -----------------------------------------------------

@test "the library passes shellcheck" {
  command -v shellcheck >/dev/null
  shellcheck -s bash -e SC1091 "$LIB"
}

@test "the library is bash 3.2 and set -u friendly: sourcing under set -euo pipefail defines the entry point" {
  run env "$BASH" -c 'set -euo pipefail; . "$1"; declare -F pgdr_tmp_sweep >/dev/null' _ "$LIB"
  [ "$status" -eq 0 ]
}

# ----- build-tmp (depth 1) removal -------------------------------------------

@test "build-tmp: an old directory, regular file, dot-entry and empty directory are removed; the marker stays" {
  need_ps
  mkroot
  mkdir "$ROOT/olddir" "$ROOT/.dotdir" "$ROOT/emptydir"
  : >"$ROOT/olddir/f"
  : >"$ROOT/oldfile"
  : >"$ROOT/.dotfile"
  age "$ROOT/olddir" 1500
  age "$ROOT/.dotdir" 1500
  age "$ROOT/emptydir" 1500
  age "$ROOT/oldfile" 1500
  age "$ROOT/.dotfile" 1500
  age "$ROOT/.build-tmp-root" 100000
  bsweep apply 1440
  [ "$status" -eq 0 ]
  [ ! -e "$ROOT/olddir" ]
  [ ! -e "$ROOT/.dotdir" ]
  [ ! -e "$ROOT/emptydir" ]
  [ ! -e "$ROOT/oldfile" ]
  [ ! -e "$ROOT/.dotfile" ]
  [ -f "$ROOT/.build-tmp-root" ]
  removed "$ROOT/olddir"
  removed "$ROOT/.dotfile"
}

@test "build-tmp: names with spaces, a newline and a leading dash are handled" {
  need_ps
  mkroot
  local nl=$'nl\nname'
  mkdir "$ROOT/with space" "$ROOT/$nl" "$ROOT/-dash" "$ROOT/--"
  : >"$ROOT/with space/f"
  : >"$ROOT/$nl/f"
  : >"$ROOT/-dash/f"
  age "$ROOT/with space" 1500
  age "$ROOT/$nl" 1500
  age "$ROOT/-dash" 1500
  age "$ROOT/--" 1500
  bsweep dry 1440
  [ "$status" -eq 0 ]
  listed "$ROOT/with space"
  listed "$ROOT/$nl"
  listed "$ROOT/-dash"
  listed "$ROOT/--"
  [ -d "$ROOT/-dash" ]
  bsweep apply 1440
  [ "$status" -eq 0 ]
  [ ! -e "$ROOT/with space" ]
  [ ! -e "$ROOT/$nl" ]
  [ ! -e "$ROOT/-dash" ]
  [ ! -e "$ROOT/--" ]
}

@test "build-tmp: the dry run lists exactly what the real run removes, and removes nothing" {
  need_ps
  mkroot
  mkdir "$ROOT/a" "$ROOT/b" "$ROOT/fresh"
  : >"$ROOT/a/f"
  : >"$ROOT/c"
  age "$ROOT/a" 1500
  age "$ROOT/b" 1500
  age "$ROOT/c" 1500
  bsweep dry 1440
  [ "$status" -eq 0 ]
  local dry
  dry="$(grep '^would remove: ' <<<"$output" | sed 's/^would remove: //' | sort)"
  [ -d "$ROOT/a" ] && [ -d "$ROOT/b" ] && [ -f "$ROOT/c" ]
  bsweep apply 1440
  [ "$status" -eq 0 ]
  local real
  real="$(grep '^removed: ' <<<"$output" | sed 's/^removed: //' | sort)"
  [ -n "$dry" ]
  [ "$dry" = "$real" ]
  [ -d "$ROOT/fresh" ]
}

# ----- build-tmp skip list (D9a) ----------------------------------------------

@test "build-tmp: a top-level claude-* entry survives at both levels, dry and real, while a sibling of the same age goes" {
  need_ps
  local minutes
  for minutes in 1440 60; do
    rm -rf "$FX/build_tmp"
    mkroot
    mkdir -p "$ROOT/claude-501/proj/sess/scratchpad" "$ROOT/sibling"
    : >"$ROOT/claude-501/proj/sess/scratchpad/f"
    : >"$ROOT/sibling/f"
    age "$ROOT/claude-501" 100000
    age "$ROOT/sibling" 100000
    bsweep dry "$minutes"
    [ "$status" -eq 0 ]
    listed "$ROOT/sibling"
    refute grep -F "claude-501" <<<"$output"
    bsweep apply "$minutes"
    [ "$status" -eq 0 ]
    [ ! -e "$ROOT/sibling" ]
    [ -f "$ROOT/claude-501/proj/sess/scratchpad/f" ]
  done
}

# ----- build-tmp keeps --------------------------------------------------------

@test "build-tmp: an entry holding one fresh file is kept whole, and so is an entry whose own mtime is fresh" {
  need_ps
  mkroot
  mkdir "$ROOT/mixed" "$ROOT/freshdir"
  : >"$ROOT/mixed/old"
  : >"$ROOT/mixed/new"
  age "$ROOT/mixed/old" 100000
  age "$ROOT/mixed" 100000
  touch "$ROOT/mixed/new"
  age "$ROOT/freshdir" 1 # own mtime fresh, nothing inside
  bsweep apply 60
  [ "$status" -eq 0 ]
  [ -f "$ROOT/mixed/old" ]
  [ -f "$ROOT/mixed/new" ]
  [ -d "$ROOT/freshdir" ]
}

@test "build-tmp: an entry containing a unix socket is kept however old; a FIFO too" {
  need_ps
  command -v python3 >/dev/null
  mkroot
  mkdir "$ROOT/sockent" "$ROOT/fifoent" "$ROOT/plain"
  mksock "$ROOT/sockent/s"
  mkfifo "$ROOT/fifoent/p"
  : >"$ROOT/plain/f"
  age "$ROOT/sockent" 100000
  age "$ROOT/fifoent" 100000
  age "$ROOT/plain" 100000
  bsweep dry 60
  [ "$status" -eq 0 ]
  listed "$ROOT/plain"
  refute listed "$ROOT/sockent"
  bsweep apply 60
  [ "$status" -eq 0 ]
  [ -S "$ROOT/sockent/s" ]
  [ -p "$ROOT/fifoent/p" ]
  [ ! -e "$ROOT/plain" ]
}

@test "build-tmp: a symlink entry is not followed and its target is untouched" {
  need_ps
  mkroot
  mkdir -p "$FX/outside/data"
  : >"$FX/outside/data/f"
  age "$FX/outside" 100000
  ln -s "$FX/outside" "$ROOT/link"
  touch -h -t "$(ts_ago 100000)" "$ROOT/link"
  bsweep dry 60
  [ "$status" -eq 0 ]
  refute listed "$ROOT/link"
  bsweep apply 60
  [ "$status" -eq 0 ]
  [ -L "$ROOT/link" ]
  [ -f "$FX/outside/data/f" ]
}

@test "build-tmp: an entry whose absolute path is in a live process's argv is kept" {
  need_ps
  mkroot
  mkdir "$ROOT/inuse" "$ROOT/idle"
  : >"$ROOT/inuse/x"
  age "$ROOT/inuse" 100000
  age "$ROOT/idle" 100000
  spawn "$ROOT/inuse/x"
  bsweep dry 60
  [ "$status" -eq 0 ]
  refute listed "$ROOT/inuse"
  listed "$ROOT/idle"
  bsweep apply 60
  [ "$status" -eq 0 ]
  [ -f "$ROOT/inuse/x" ]
  [ ! -e "$ROOT/idle" ]
}

@test "build-tmp: an entry that is a live process's cwd is kept" {
  need_ps
  mkroot
  mkdir "$ROOT/cwdent" "$ROOT/idle"
  age "$ROOT/cwdent" 100000
  age "$ROOT/idle" 100000
  (cd "$ROOT/cwdent" && holder) >/dev/null 2>&1 &
  echo $! >>"$PIDS"
  for _ in $(seq 1 300); do
    [[ "$(lsof -n -w -d cwd -Fn 2>/dev/null)" == *$'\n'"n$ROOT/cwdent"$'\n'* ]] && break
  done
  bsweep apply 60
  [ "$status" -eq 0 ]
  [ -d "$ROOT/cwdent" ]
  [ ! -e "$ROOT/idle" ]
}

# ----- idleness boundary ------------------------------------------------------

@test "idleness boundary at level 1 (1440 min): 23 h is kept, 25 h is removed" {
  need_ps
  mkroot
  mkdir "$ROOT/h23" "$ROOT/h25"
  age "$ROOT/h23" $((23 * 60))
  age "$ROOT/h25" $((25 * 60))
  bsweep apply 1440
  [ "$status" -eq 0 ]
  [ -d "$ROOT/h23" ]
  [ ! -e "$ROOT/h25" ]
}

@test "idleness boundary at level 3 (60 min): 59 min is kept, 61 min is removed" {
  need_ps
  mkroot
  mkdir "$ROOT/m59" "$ROOT/m61"
  age "$ROOT/m59" 59
  age "$ROOT/m61" 61
  bsweep apply 60
  [ "$status" -eq 0 ]
  [ -d "$ROOT/m59" ]
  [ ! -e "$ROOT/m61" ]
}

@test "the minutes parameter moves the boundary" {
  need_ps
  mkroot
  mkdir "$ROOT/e"
  age "$ROOT/e" 100
  bsweep dry 120
  refute listed "$ROOT/e"
  bsweep dry 90
  listed "$ROOT/e"
}

# ----- fail closed ------------------------------------------------------------

@test "fail closed: a failing ps, an empty ps, a failing lsof and an empty lsof each abort non-zero and delete nothing" {
  need_ps
  mkroot
  mkdir "$ROOT/sentinel"
  age "$ROOT/sentinel" 100000
  local cmd body
  for cmd in ps lsof; do
    # failing with no output, succeeding with no output, and failing AFTER printing
    for body in 'exit 3' 'exit 0' 'echo n/x; exit 3'; do
      rm -f "$SHIM"/*
      shim "$cmd" "$body"
      SWEEP_PATH="$SHIM:$PATH" bsweep apply 60
      [ "$status" -ne 0 ]
      [ -d "$ROOT/sentinel" ]
      SWEEP_PATH="$SHIM:$PATH" bsweep dry 60
      [ "$status" -ne 0 ]
    done
  done
}

@test "fail closed: the claude-session capture of lsof also aborts when lsof fails, with or without output" {
  need_ps
  mkroot claude_code_tmp .claude-code-tmp-root
  mkdir -p "$ROOT/claude-1/p/s"
  age "$ROOT/claude-1" 100000
  local body
  for body in 'exit 3' 'exit 0' 'echo n/x; exit 3'; do
    shim lsof "$body"
    SWEEP_PATH="$SHIM:$PATH" csweep apply 60
    [ "$status" -ne 0 ]
    [ -d "$ROOT/claude-1/p/s" ]
  done
}

# ----- the root guard (D5) ----------------------------------------------------

# Every refusal below returns 2 and leaves a sentinel entry intact.
@test "root guard: an empty root, a relative root and a missing root are refused" {
  need_ps
  mkroot
  mkdir "$ROOT/sentinel"
  age "$ROOT/sentinel" 100000
  sweep apply "" .build-tmp-root build_tmp 60 1 none
  [ "$status" -eq 2 ]
  [[ $output == *"empty"* ]]
  cd "$FX"
  sweep apply build_tmp .build-tmp-root build_tmp 60 1 none
  [ "$status" -eq 2 ]
  [[ $output == *"not an absolute path"* ]]
  sweep apply "$FX/nowhere/build_tmp" .build-tmp-root build_tmp 60 1 none
  [ "$status" -eq 2 ]
  [ -d "$ROOT/sentinel" ]
}

@test "root guard: / and shallow roots are refused (pure check)" {
  run env "$BASH" -c 'set -euo pipefail; . "$1"
    for p in / /Volumes /tmp /private /private/tmp /a /a/b "" ; do
      pgdr_tmp_root_forbidden "$p" || { echo "NOT forbidden: $p"; exit 1; }
    done
    for p in /a/b/c /Volumes/x/build_tmp /private/var/folders/x/build_tmp; do
      HOME=/nonexistent pgdr_tmp_root_forbidden "$p" && { echo "forbidden: $p"; exit 1; }
    done
    exit 0' _ "$LIB"
  [ "$status" -eq 0 ]
}

@test "root guard: the real / is refused end to end" {
  need_ps
  sweep apply / .build-tmp-root build_tmp 60 1 none
  [ "$status" -eq 2 ]
  [[ $output == *"forbidden or too shallow"* ]] || [[ $output == *"basename"* ]] || [[ $output == *"not owned"* ]]
}

@test "root guard: HOME itself and an ancestor of HOME are refused" {
  need_ps
  mkroot
  mkdir "$ROOT/sentinel"
  age "$ROOT/sentinel" 100000
  HOME="$ROOT" sweep apply "$ROOT" .build-tmp-root build_tmp 60 1 none
  [ "$status" -eq 2 ]
  [[ $output == *"forbidden"* ]]
  mkdir "$ROOT/home"
  HOME="$ROOT/home" sweep apply "$ROOT" .build-tmp-root build_tmp 60 1 none
  [ "$status" -eq 2 ]
  [[ $output == *"forbidden"* ]]
  [ -d "$ROOT/sentinel" ]
}

@test "root guard: a symlinked root is refused" {
  need_ps
  mkroot
  mkdir "$ROOT/sentinel"
  age "$ROOT/sentinel" 100000
  ln -s "$ROOT" "$FX/link_build_tmp"
  sweep apply "$FX/link_build_tmp" .build-tmp-root build_tmp 60 1 none
  [ "$status" -eq 2 ]
  [[ $output == *"symlink"* ]]
  ln -s "$ROOT" "$FX/build_tmp_link"
  sweep apply "$FX/build_tmp_link/" .build-tmp-root build_tmp 60 1 none
  [ "$status" -eq 2 ]
  [ -d "$ROOT/sentinel" ]
}

@test "root guard: a root reached through a symlinked PARENT is swept at its physical path" {
  need_ps
  mkroot
  mkdir "$ROOT/old"
  age "$ROOT/old" 100000
  ln -s "$FX" "$FX/viaparent"
  sweep apply "$FX/viaparent/build_tmp" .build-tmp-root build_tmp 60 1 none
  [ "$status" -eq 0 ]
  [ ! -e "$ROOT/old" ]
  [[ $output == *"removed: $ROOT/old"* ]]
}

@test "root guard: a missing marker is refused and a sibling-repo-shaped directory is never swept" {
  need_ps
  mkroot
  rm "$ROOT/.build-tmp-root"
  mkdir "$ROOT/sentinel"
  age "$ROOT/sentinel" 100000
  bsweep apply 60
  [ "$status" -eq 2 ]
  [[ $output == *"marker"* ]]
  [ -d "$ROOT/sentinel" ]
  # a directory that looks like a sibling repository directory, user-owned, 0700, deep
  # enough, and the basename its caller asserts, but WITHOUT a marker
  mkdir -p "$FX/gitrepos/phillipg" && chmod 0700 "$FX/gitrepos/phillipg"
  mkdir -p "$FX/gitrepos/phillipg/repo/.git"
  age "$FX/gitrepos/phillipg" 100000
  sweep apply "$FX/gitrepos/phillipg" .build-tmp-root phillipg 60 1 none
  [ "$status" -eq 2 ]
  [[ $output == *"marker"* ]]
  [ -d "$FX/gitrepos/phillipg/repo/.git" ]
  # a marker that is a symlink does not count
  ln -s "$FX/gitrepos/phillipg/repo" "$FX/gitrepos/phillipg/.build-tmp-root"
  sweep apply "$FX/gitrepos/phillipg" .build-tmp-root phillipg 60 1 none
  [ "$status" -eq 2 ]
  [ -d "$FX/gitrepos/phillipg/repo/.git" ]
}

@test "root guard: a wrong mode is refused" {
  need_ps
  mkroot
  mkdir "$ROOT/sentinel"
  age "$ROOT/sentinel" 100000
  chmod 0755 "$ROOT"
  bsweep apply 60
  [ "$status" -eq 2 ]
  [[ $output == *"0700"* ]]
  chmod 0770 "$ROOT"
  bsweep apply 60
  [ "$status" -eq 2 ]
  chmod 0500 "$ROOT"
  bsweep apply 60
  [ "$status" -eq 2 ]
  chmod 0700 "$ROOT"
  [ -d "$ROOT/sentinel" ]
}

@test "root guard: a wrong basename is refused" {
  need_ps
  mkroot other
  mkdir "$ROOT/sentinel"
  age "$ROOT/sentinel" 100000
  sweep apply "$ROOT" .build-tmp-root build_tmp 60 1 none
  [ "$status" -eq 2 ]
  [[ $output == *"basename"* ]]
  [ -d "$ROOT/sentinel" ]
}

@test "root guard: a root the user does not own is refused (a real root-owned directory, and the ownership seam)" {
  need_ps
  local d found=""
  for d in /usr/bin /usr/lib /usr/libexec /nix/store /etc/ssh /usr/share; do
    if [ -d "$d" ] && [ ! -O "$d" ]; then
      found="$d"
      break
    fi
  done
  if [[ -n $found ]]; then
    sweep apply "$found" .build-tmp-root "${found##*/}" 60 1 none
    [ "$status" -eq 2 ]
    [[ $output == *"not owned"* ]]
  fi
  mkroot
  mkdir "$ROOT/sentinel"
  age "$ROOT/sentinel" 100000
  run env "$BASH" -c 'set -euo pipefail; . "$1"; pgdr_tmp_owned() { return 1; }; pgdr_tmp_sweep apply "$2" .build-tmp-root build_tmp 60 1 none' _ "$LIB" "$ROOT"
  [ "$status" -eq 2 ]
  [[ $output == *"not owned"* ]]
  [ -d "$ROOT/sentinel" ]
}

@test "root guard: invalid parameters are refused" {
  need_ps
  mkroot
  mkdir "$ROOT/sentinel"
  age "$ROOT/sentinel" 100000
  sweep sweep "$ROOT" .build-tmp-root build_tmp 60 1 none
  [ "$status" -eq 2 ]
  sweep apply "$ROOT" .build-tmp-root build_tmp 0 1 none
  [ "$status" -eq 2 ]
  sweep apply "$ROOT" .build-tmp-root build_tmp abc 1 none
  [ "$status" -eq 2 ]
  sweep apply "$ROOT" .build-tmp-root build_tmp 60 2 none
  [ "$status" -eq 2 ]
  sweep apply "$ROOT" .build-tmp-root build_tmp 60 1 bogus
  [ "$status" -eq 2 ]
  sweep apply "$ROOT" ../x build_tmp 60 1 none
  [ "$status" -eq 2 ]
  [ -d "$ROOT/sentinel" ]
}

# ----- marker, quarantine ------------------------------------------------------

@test "the marker survives a sweep that is otherwise idle past the cutoff, and the next run still passes the guard" {
  need_ps
  mkroot
  mkdir "$ROOT/old"
  age "$ROOT/old" 100000
  age "$ROOT/.build-tmp-root" 100000
  bsweep apply 60
  [ "$status" -eq 0 ]
  [ -f "$ROOT/.build-tmp-root" ]
  [ ! -e "$ROOT/old" ]
  bsweep apply 60
  [ "$status" -eq 0 ]
  rm -rf "$FX/claude_code_tmp"
  mkroot claude_code_tmp .claude-code-tmp-root
  age "$ROOT/.claude-code-tmp-root" 100000
  csweep apply 60
  [ "$status" -eq 0 ]
  [ -f "$ROOT/.claude-code-tmp-root" ]
  csweep apply 60
  [ "$status" -eq 0 ]
}

@test "a leftover .sweeping.* is removed at the start of the next sweep, fresh or old, and is never itself a candidate" {
  need_ps
  mkroot
  mkdir -p "$ROOT/.sweeping.1.1.gone/inner" "$ROOT/.sweeping.2.2.fresh"
  age "$ROOT/.sweeping.1.1.gone" 100000
  bsweep dry 60
  [ "$status" -eq 0 ]
  listed "$ROOT/.sweeping.1.1.gone"
  listed "$ROOT/.sweeping.2.2.fresh"
  [ -d "$ROOT/.sweeping.1.1.gone" ]
  bsweep apply 60
  [ "$status" -eq 0 ]
  [ ! -e "$ROOT/.sweeping.1.1.gone" ]
  [ ! -e "$ROOT/.sweeping.2.2.fresh" ]
  [ -f "$ROOT/.build-tmp-root" ]
}

@test "a sweep killed between the quarantine rename and the removal leaves a depth-1 .sweeping.* that the next run cleans" {
  need_ps
  mkroot claude_code_tmp .claude-code-tmp-root
  mkdir -p "$ROOT/claude-1/proj/sess/scratchpad"
  : >"$ROOT/claude-1/proj/sess/scratchpad/f"
  age "$ROOT/claude-1" 100000
  shim rm 'kill -KILL "$PPID"; exit 1'
  SWEEP_PATH="$SHIM:$PATH" csweep apply 60
  [ "$status" -ne 0 ]
  # the unit was renamed to depth 1 of the root and the original is gone
  local q
  q="$(find "$ROOT" -mindepth 1 -maxdepth 1 -name '.sweeping.*' -print)"
  [ -n "$q" ]
  [ ! -e "$ROOT/claude-1/proj/sess" ]
  [ -f "$q/scratchpad/f" ]
  rm -f "$SHIM/rm"
  csweep apply 60
  [ "$status" -eq 0 ]
  [ -z "$(find "$ROOT" -mindepth 1 -maxdepth 1 -name '.sweeping.*' -print)" ]
}

@test "a unit that becomes active between the quarantine rename and the re-check is moved back and kept" {
  need_ps
  mkroot
  mkdir "$ROOT/busy"
  : >"$ROOT/busy/f"
  age "$ROOT/busy" 100000
  real_mv="$(command -v mv)"
  shim mv "for a in \"\$@\"; do last=\$a; done; if [ ! -e \"$FX/mv-once\" ]; then : >\"$FX/mv-once\"; $real_mv \"\$@\" && : >\"\$last/late\"; else exec $real_mv \"\$@\"; fi"
  SWEEP_PATH="$SHIM:$PATH" bsweep apply 60
  [ "$status" -eq 0 ]
  [ -f "$ROOT/busy/late" ]
  [ -f "$ROOT/busy/f" ]
  [ -z "$(find "$ROOT" -mindepth 1 -maxdepth 1 -name '.sweeping.*' -print)" ]
}

# ----- ownership and failures (D6) ---------------------------------------------

@test "an entry the user does not own is skipped with a log line and is not a failure" {
  need_ps
  mkroot
  mkdir "$ROOT/notmine" "$ROOT/mine"
  age "$ROOT/notmine" 100000
  age "$ROOT/mine" 100000
  run env "$BASH" -c 'set -euo pipefail; . "$1"
    pgdr_tmp_owned() { case "$1" in */notmine) return 1 ;; *) [ -O "$1" ] ;; esac; }
    pgdr_tmp_sweep apply "$2" .build-tmp-root build_tmp 60 1 none' _ "$LIB" "$ROOT"
  [ "$status" -eq 0 ]
  [[ $output == *"not owned"* ]]
  [ -d "$ROOT/notmine" ]
  [ ! -e "$ROOT/mine" ]
}

@test "an entry that cannot be removed is logged, the sweep continues, and the status is non-zero" {
  need_ps
  [ "$(id -u)" -ne 0 ] || skip "root ignores file modes"
  mkroot
  mkdir "$ROOT/aa_stuck" "$ROOT/zz_fine"
  : >"$ROOT/aa_stuck/f"
  age "$ROOT/aa_stuck" 100000
  age "$ROOT/zz_fine" 100000
  chmod 0555 "$ROOT/aa_stuck"
  bsweep apply 60
  [ "$status" -eq 3 ]
  [[ $output == *"failed:"* ]]
  [ ! -e "$ROOT/zz_fine" ]
  chmod -R u+rwx "$ROOT"
}

# ----- claude-code-tmp (depth 3) -----------------------------------------------

mkclaude() {
  mkroot claude_code_tmp .claude-code-tmp-root
}

@test "claude-code-tmp: a session directory idle past the cutoff is removed with its project directory; just inside it is kept" {
  need_ps
  mkclaude
  mkdir -p "$ROOT/claude-1/proj/oldsess/scratchpad" "$ROOT/claude-1/proj/newsess"
  : >"$ROOT/claude-1/proj/oldsess/scratchpad/f"
  : >"$ROOT/claude-1/proj/newsess/g"
  age "$ROOT/claude-1" 3000
  age "$ROOT/claude-1/proj/newsess" 2800
  csweep apply 2880
  [ "$status" -eq 0 ]
  [ ! -e "$ROOT/claude-1/proj/oldsess" ]
  [ -f "$ROOT/claude-1/proj/newsess/g" ]
  # the project directory still holds a kept unit: not removed; neither is claude-1
  [ -d "$ROOT/claude-1/proj" ]
  # level 1 (20160 min) keeps both, and the options drive the boundary
  csweep dry 20160
  refute grep -F "would remove" <<<"$output"
  csweep dry 2700
  listed "$ROOT/claude-1/proj/newsess"
}

@test "claude-code-tmp: structure: the now-empty project directory and an emptied claude-<uid> are rmdir'd, a non-empty one survives" {
  need_ps
  mkclaude
  mkdir -p "$ROOT/claude-1/proj/sess/scratchpad" "$ROOT/claude-2/proj/sess" "$ROOT/claude-2/keep/fresh"
  : >"$ROOT/claude-1/proj/sess/scratchpad/f"
  age "$ROOT/claude-1" 100000
  age "$ROOT/claude-2" 100000
  age "$ROOT/claude-2/keep/fresh" 1
  csweep dry 60
  [ "$status" -eq 0 ]
  listed "$ROOT/claude-1/proj/sess"
  grep -qxF "would rmdir: $ROOT/claude-1/proj" <<<"$output"
  grep -qxF "would rmdir: $ROOT/claude-1" <<<"$output"
  grep -qxF "would rmdir: $ROOT/claude-2/proj" <<<"$output"
  refute grep -F "would rmdir: $ROOT/claude-2/keep" <<<"$output"
  refute grep -qxF "would rmdir: $ROOT/claude-2" <<<"$output"
  csweep apply 60
  [ "$status" -eq 0 ]
  [ ! -e "$ROOT/claude-1" ]
  [ ! -e "$ROOT/claude-2/proj" ]
  [ -d "$ROOT/claude-2/keep/fresh" ]
  [ -d "$ROOT/claude-2" ]
  [ -f "$ROOT/.claude-code-tmp-root" ]
}

@test "claude-code-tmp: idle regular files at depth 1, 2 and 3 are removed, fresh ones are kept" {
  need_ps
  mkclaude
  mkdir -p "$ROOT/claude-1/proj/sess" "$ROOT/claude-1/keepdir/sess"
  : >"$ROOT/top.json"
  : >"$ROOT/claude-1/cache-break-state-x.json"
  : >"$ROOT/claude-1/proj/note.out"
  : >"$ROOT/claude-1/proj/sess/leaf.md"
  : >"$ROOT/claude-1/keepdir/fresh.md"
  age "$ROOT/top.json" 100000
  age "$ROOT/claude-1/cache-break-state-x.json" 100000
  age "$ROOT/claude-1/proj" 100000
  age "$ROOT/claude-1/keepdir/sess" 100000
  age "$ROOT/claude-1/keepdir" 100000
  age "$ROOT/claude-1" 100000
  touch "$ROOT/claude-1/keepdir/fresh.md"
  csweep apply 60
  [ "$status" -eq 0 ]
  [ ! -e "$ROOT/top.json" ]
  [ ! -e "$ROOT/claude-1/cache-break-state-x.json" ]
  [ ! -e "$ROOT/claude-1/proj/note.out" ]
  [ ! -e "$ROOT/claude-1/proj/sess/leaf.md" ]
  [ -f "$ROOT/claude-1/keepdir/fresh.md" ]
}

@test "claude-code-tmp: symlinks at any depth are never followed and a symlinked unit is skipped" {
  need_ps
  mkclaude
  mkdir -p "$FX/outside/d" "$ROOT/claude-1/proj/sess"
  : >"$FX/outside/d/f"
  age "$FX/outside" 100000
  ln -s "$FX/outside" "$ROOT/lnk1"
  ln -s "$FX/outside" "$ROOT/claude-1/lnk2"
  ln -s "$FX/outside" "$ROOT/claude-1/proj/lnk3"
  ln -s "$FX/outside" "$ROOT/claude-1/proj/sess/lnk4"
  age "$ROOT/claude-1" 100000
  find "$ROOT" -type l -exec touch -h -t "$(ts_ago 100000)" {} +
  csweep apply 60
  [ "$status" -eq 0 ]
  [ -L "$ROOT/lnk1" ]
  [ -L "$ROOT/claude-1/lnk2" ]
  [ -L "$ROOT/claude-1/proj/lnk3" ]
  [ -f "$FX/outside/d/f" ]
  # the session directory holding a symlink is a unit and goes whole (the link, not its target)
  [ ! -e "$ROOT/claude-1/proj/sess" ]
}

@test "claude-code-tmp: a non-uuid depth-3 directory is swept by idleness only" {
  need_ps
  mkclaude
  mkdir -p "$ROOT/bash-edit-diff/abc123/sub"
  : >"$ROOT/bash-edit-diff/abc123/sub/patch"
  age "$ROOT/bash-edit-diff" 100000
  csweep apply 60
  [ "$status" -eq 0 ]
  [ ! -e "$ROOT/bash-edit-diff" ]
}

@test "claude-code-tmp: a live session uuid as a whole token in argv keeps the unit, however idle" {
  need_ps
  mkclaude
  local live idle
  live="$(uuid 17)"
  idle="$(uuid 18)"
  mkdir -p "$ROOT/claude-1/proj/$live/scratchpad" "$ROOT/claude-1/proj/$idle/scratchpad"
  : >"$ROOT/claude-1/proj/$live/scratchpad/f"
  : >"$ROOT/claude-1/proj/$idle/scratchpad/f"
  age "$ROOT/claude-1" $((30 * 24 * 60))
  spawn "claude --session-id $live --settings x"
  csweep dry 20160
  [ "$status" -eq 0 ]
  refute listed "$ROOT/claude-1/proj/$live"
  listed "$ROOT/claude-1/proj/$idle"
  csweep apply 20160
  [ "$status" -eq 0 ]
  [ -f "$ROOT/claude-1/proj/$live/scratchpad/f" ]
  [ ! -e "$ROOT/claude-1/proj/$idle" ]
}

@test "claude-code-tmp: the uuid token boundary: -x and x- and xx..yy do not count, / and = do" {
  need_ps
  mkclaude
  local ua ub uc ud ue uf
  ua="$(uuid 201)" ub="$(uuid 202)" uc="$(uuid 203)" ud="$(uuid 204)" ue="$(uuid 205)" uf="$(uuid 206)"
  local u
  for u in "$ua" "$ub" "$uc" "$ud" "$ue" "$uf"; do
    mkdir -p "$ROOT/claude-1/proj/$u"
  done
  age "$ROOT/claude-1" 100000
  spawn "run $ua-x"
  spawn "run x-$ub"
  spawn "run xx${uc}yy"
  spawn "run /$ud/"
  spawn "run =$ue"
  spawn "run $uf"
  csweep apply 60
  [ "$status" -eq 0 ]
  [ ! -e "$ROOT/claude-1/proj/$ua" ]
  [ ! -e "$ROOT/claude-1/proj/$ub" ]
  [ ! -e "$ROOT/claude-1/proj/$uc" ]
  [ -d "$ROOT/claude-1/proj/$ud" ]
  [ -d "$ROOT/claude-1/proj/$ue" ]
  [ -d "$ROOT/claude-1/proj/$uf" ]
}

@test "claude-code-tmp: a unit with an open file held by a live process is kept even though its uuid is not in argv" {
  need_ps
  mkclaude
  local u
  u="$(uuid 300)"
  mkdir -p "$ROOT/claude-1/proj/$u/tasks" "$ROOT/claude-1/proj/other"
  : >"$ROOT/claude-1/proj/$u/tasks/out"
  age "$ROOT/claude-1" 100000
  (
    exec 3<"$ROOT/claude-1/proj/$u/tasks/out"
    holder
  ) >/dev/null 2>&1 &
  echo $! >>"$PIDS"
  for _ in $(seq 1 300); do
    [[ "$(lsof -n -w -Fn 2>/dev/null)" == *"$u/tasks/out"* ]] && break
  done
  csweep apply 60
  [ "$status" -eq 0 ]
  [ -f "$ROOT/claude-1/proj/$u/tasks/out" ]
  [ ! -e "$ROOT/claude-1/proj/other" ]
}

@test "claude-code-tmp: an idle unit whose path is in argv is kept (the path guard applies at depth 3)" {
  need_ps
  mkclaude
  mkdir -p "$ROOT/claude-1/proj/sess"
  : >"$ROOT/claude-1/proj/sess/f"
  age "$ROOT/claude-1" 100000
  spawn "$ROOT/claude-1/proj/sess/f"
  csweep apply 60
  [ "$status" -eq 0 ]
  [ -f "$ROOT/claude-1/proj/sess/f" ]
}

@test "claude-code-tmp: the dry run lists exactly what the real run removes" {
  need_ps
  mkclaude
  mkdir -p "$ROOT/claude-1/p1/s1" "$ROOT/claude-1/p1/s2" "$ROOT/claude-1/p2/s3"
  : >"$ROOT/claude-1/f.json"
  age "$ROOT/claude-1" 100000
  touch "$ROOT/claude-1/p2/s3/fresh"
  csweep dry 60
  [ "$status" -eq 0 ]
  local dry real
  dry="$(grep -E '^would (remove|rmdir): ' <<<"$output" | sed -E 's/^would (remove|rmdir): /X /' | sort)"
  csweep apply 60
  [ "$status" -eq 0 ]
  real="$(grep -E '^(removed|rmdir): ' <<<"$output" | sed -E 's/^(removed|rmdir): /X /' | sort)"
  [ -n "$dry" ]
  [ "$dry" = "$real" ]
}

# ----- execution model: the real CLI ---------------------------------------------

# A registry with three items, in order: one whose guard fails, one that sweeps
# a real fixture root and succeeds, and an ordinary one.
write_registry() {
  mkdir -p "$FX/xdg/pg-disk-reclaimer"
  local lib="$LIB" good="$ROOT" bad="$FX/gitrepos/phillipg"
  jq -n --arg lib "$lib" --arg good "$good" --arg bad "$bad" --arg fx "$FX" '
    def cmd($mode; $root; $marker; $base):
      ": ; ( . " + ($lib | @sh) + " && pgdr_tmp_sweep " + $mode + " " + ($root | @sh) + " " + $marker + " " + $base + " 60 1 none )";
    [
      { id: "guarded", description: "guard fails", path: $fx, displayCommand: "echo x", sizeCommand: "echo 100",
        variants: [ { aggressiveness: 1, variantDescription: "v",
          dryRunCommand: cmd("dry"; $bad; ".build-tmp-root"; "phillipg"),
          removeCommand: cmd("apply"; $bad; ".build-tmp-root"; "phillipg") } ] },
      { id: "real", description: "sweeps a fixture", path: $good, displayCommand: "echo x", sizeCommand: "echo 4096",
        variants: [ { aggressiveness: 1, variantDescription: "v",
          dryRunCommand: cmd("dry"; $good; ".build-tmp-root"; "build_tmp"),
          removeCommand: cmd("apply"; $good; ".build-tmp-root"; "build_tmp") } ] },
      { id: "other", description: "ordinary", path: $fx, displayCommand: "echo x", sizeCommand: "echo 2048",
        variants: [ { aggressiveness: 1, variantDescription: "v",
          dryRunCommand: "echo other-dry", removeCommand: "echo OTHER-RAN" } ] }
    ]' >"$FX/xdg/pg-disk-reclaimer/registry.json"
}

cli() {
  run env XDG_CONFIG_HOME="$FX/xdg" PATH="${SWEEP_PATH:-$PATH}" "$BASH" -euo pipefail "$SCRIPTS_DIR/pg-disk-reclaimer.sh" "$@"
}

@test "execution model: a failing guard does not exit the CLI, the other items still run, and the total is not clobbered" {
  need_ps
  command -v jq >/dev/null
  mkroot
  mkdir "$ROOT/old"
  age "$ROOT/old" 100000
  mkdir -p "$FX/gitrepos/phillipg/repo" && chmod 0700 "$FX/gitrepos/phillipg"
  write_registry
  cli reclaim --aggressiveness 1 --apply
  # (a) the CLI itself kept going: its own exit status comes from the failed item
  [ "$status" -eq 1 ]
  [[ $output == *"remove command for 'guarded' exited non-zero"* ]]
  # (b) the items after the failure still ran and the total line printed
  [[ $output == *"OTHER-RAN"* ]]
  [ ! -e "$ROOT/old" ]
  [[ $output == *"total reclaimed"* ]]
  # (c) the loop's locals survived: 4096K + 2048K = 6.0M, the failed item excluded
  [[ $output == *"total reclaimed: 6.0M (2 sized, 0 unknown); 1 item(s) failed, excluded from total"* ]]
  [ -d "$FX/gitrepos/phillipg/repo" ]
}

@test "execution model: the default is a dry run and a failing guard there is reported without exiting" {
  need_ps
  command -v jq >/dev/null
  mkroot
  mkdir "$ROOT/old"
  age "$ROOT/old" 100000
  mkdir -p "$FX/gitrepos/phillipg" && chmod 0700 "$FX/gitrepos/phillipg"
  write_registry
  cli reclaim --aggressiveness 1
  [ "$status" -eq 1 ]
  [[ $output == *"would remove: $ROOT/old"* ]]
  [[ $output == *"other-dry"* ]]
  [ -d "$ROOT/old" ]
}

# ----- toolchains ----------------------------------------------------------------

@test "toolchains: the BSD tools first on PATH give the same result (darwin only; the nix sandbox is GNU-only)" {
  need_ps
  if /usr/bin/find --version >/dev/null 2>&1 || [[ ! -x /bin/ps ]]; then
    skip "no BSD userland here"
  fi
  mkroot
  mkdir "$ROOT/old" "$ROOT/fresh" "$ROOT/sp ace"
  : >"$ROOT/old/f"
  age "$ROOT/old" 100000
  age "$ROOT/sp ace" 100000
  SWEEP_PATH="/usr/bin:/bin:/usr/sbin:/sbin" bsweep apply 60
  [ "$status" -eq 0 ]
  [ ! -e "$ROOT/old" ]
  [ ! -e "$ROOT/sp ace" ]
  [ -d "$ROOT/fresh" ]
  mkroot claude_code_tmp .claude-code-tmp-root
  mkdir -p "$ROOT/claude-1/p/s"
  age "$ROOT/claude-1" 100000
  SWEEP_PATH="/usr/bin:/bin:/usr/sbin:/sbin" csweep apply 60
  [ "$status" -eq 0 ]
  [ ! -e "$ROOT/claude-1" ]
}
