# shellcheck shell=bash
# pgdr-tmp-sweep.bash: the guarded idle sweep behind the pg-disk-reclaimer
# registry items `build-tmp` and `claude-code-tmp` (bead pg2-rym7f, packet B of
# the build_tmp design in pg2-2qirl, decisions D3 to D7 and D13).
#
# ONE entry point, parameterised, so the registry commands only interpolate:
#
#   pgdr_tmp_sweep <dry|apply> <root> <marker-name> <basename> <idle-minutes> \
#                  <depth 1|3> <liveness none|claude-session>
#
# It is sourced by ABSOLUTE store path from registry commands of the form
#   : ; ( . <lib> && pgdr_tmp_sweep apply '<root>' ... )
# The `( )` subshell matters: the CLI runs removeCommand by a bare in-process
# `eval`, so a guard that fails must not exit the CLI and no assignment may
# clobber cmd_reclaim's locals. This file never calls `exit`.
#
# Contract (all of it is covered by tests/test-pg-disk-reclaimer-tmp-sweep.bats):
#
#   ROOT GUARD (D5), before anything is touched, any failure prints the reason
#   and returns 2: the root is a non-empty absolute path, is not itself a
#   symlink, resolves to a PHYSICAL path, is owned by the user ([ -O ]), is not
#   a forbidden or shallow root, has the asserted basename, mode 0700, and holds
#   its marker file (written by the activation step). The marker is what stops a
#   mis-set path that is user-owned and deep enough from being swept.
#
#   UNITS (D3). depth 1: every top-level entry. depth 3: every entry at
#   <root>/<a>/<b>/<c> plus every regular file at depth 1 and 2 (so nothing is a
#   permanent leak). A unit is IDLE when nothing under it, and not the unit
#   itself, has an mtime newer than the cutoff (find -mmin -N, no -type filter):
#   the whole unit is removed or kept. Symlinks are never followed and never
#   removed (a symlink unit is skipped). The root's marker and every .sweeping.*
#   name are never candidates. At depth 1 every top-level claude-* entry is also
#   skipped (D9a safeguard, in case Claude's tree is ever pointed here).
#
#   REMOVAL. capture liveness data once, check, rename the unit to
#   <root>/.sweeping.<pid>.<n>.<name> (always depth 1: same volume, atomic from
#   any depth), RE-check idleness there, then rm -rf; if the re-check finds
#   activity the unit is moved back and skipped. Any .sweeping.* left by an
#   interrupted run is removed at the START of every sweep. A failed move-back
#   leaves the unit quarantined (it is then removed by the next run); that
#   double fault is logged loudly and counts as a failure.
#
#   LIVENESS (D4), all fail closed: ps and lsof are captured ONCE before any
#   removal and a failed or empty capture returns 2 with nothing removed. A unit
#   is kept when it contains a socket or FIFO, when its absolute path occurs in
#   any process argv ([[ == *"$unit"* ]] on the captured text, never ps | grep),
#   or when any open-file path from lsof (cwd only at liveness none) is it or
#   under it. With liveness claude-session a unit is also kept when its
#   basename holds, as a whole token (boundary: any character outside
#   [0-9A-Za-z-]), a session uuid that is a whole token in the ps capture.
#   KNOWN GAP: a headless `claude -p` worker has a session directory whose uuid
#   is not in argv, so it is protected only by the idle cutoff; a path that is
#   only in a process ENVIRONMENT is not detected either.
#
#   OWNERSHIP. Entries not owned by the user are skipped with a log line and
#   do NOT count as failures. Per-unit removal failures are logged, the sweep
#   continues, and the exit status is 3 if a user-owned unit failed (D6).
#
#   EMPTY DIRECTORIES (depth 3). After the units, depth-2 then depth-1
#   directories that were idle for the cutoff BEFORE the sweep and are now empty
#   are removed with rmdir (never rm -rf). Judged before the sweep because
#   removing the last unit refreshes the directory's own mtime.
#
#   OUTPUT. dry prints "would remove: <path>" lines and removes nothing, the
#   list being exactly what apply would remove; apply prints "removed: <path>".
#   Diagnostics go to stderr. The last line is a summary. Status: 0 ok, 3 a
#   user-owned unit failed, 2 refused or aborted before anything was removed.
#
# Portability (D7): bash 3.2 syntax (no associative arrays), only flags common
# to BSD and GNU find/rm/mv/sed/tr (-mmin, -print0, -quit, -mindepth/-maxdepth,
# -perm 0700), tolerant of set -e/-u/pipefail (${VAR:-} everywhere, every
# fallible command is an `if` condition).

# pgdr_tmp_log: one diagnostic line on stderr.
pgdr_tmp_log() {
  printf 'pgdr-tmp-sweep: %s\n' "$*" >&2
}

# pgdr_tmp_flat: a path on one line (a newline in a name is shown as \n).
pgdr_tmp_flat() {
  local s="${1:-}"
  printf '%s' "${s//$'\n'/\\n}"
}

# pgdr_tmp_owned PATH: true when the current user owns PATH. A seam: the tests
# override it to exercise the not-owned refusal, which cannot be built
# hermetically without root.
pgdr_tmp_owned() {
  [ -O "${1:-}" ]
}

# pgdr_tmp_root_forbidden PHYSICAL-PATH: true when the path must never be swept.
# Shallow roots (fewer than three components) already cover /, /Volumes, /tmp,
# /private, /private/*, a volume's own top-level directory and every other
# top-level directory; the explicit names are kept so the intent reads. HOME and any ancestor of HOME are
# forbidden whatever their depth.
pgdr_tmp_root_forbidden() {
  local p="${1:-}" rest slashes n h home="${HOME:-}" home_phys=""
  [ -n "$p" ] || return 0
  case "$p" in
  / | /Volumes | /tmp | /private) return 0 ;;
  esac
  rest="${p#/}"
  slashes="${rest//[!\/]/}"
  n=$((${#slashes} + 1))
  if [ "$n" -lt 3 ]; then
    return 0
  fi
  if [ -n "$home" ]; then
    home_phys="$(CDPATH='' cd -P -- "$home" 2>/dev/null && pwd -P)" || home_phys=""
    for h in "${home%/}" "$home_phys"; do
      [ -n "$h" ] || continue
      [ "$p" = "$h" ] && return 0
      case "$h/" in "$p"/*) return 0 ;; esac
    done
  fi
  return 1
}

# pgdr_tmp_root_guard ROOT MARKER BASENAME: the D5 guard. On success sets
# _PGDR_TMP_ROOT to the physical root and returns 0; otherwise prints the reason
# and returns 2.
pgdr_tmp_root_guard() {
  local root="${1:-}" marker="${2:-}" base="${3:-}" phys
  _PGDR_TMP_ROOT=""
  if [ -z "$root" ]; then
    pgdr_tmp_log "refusing: the root path is empty"
    return 2
  fi
  case "$root" in
  /*) ;;
  *)
    pgdr_tmp_log "refusing: root '$root' is not an absolute path"
    return 2
    ;;
  esac
  while [ "$root" != / ] && [ "${root%/}" != "$root" ]; do
    root="${root%/}"
  done
  if [ -L "$root" ]; then
    pgdr_tmp_log "refusing: root '$root' is a symlink"
    return 2
  fi
  if [ ! -d "$root" ]; then
    pgdr_tmp_log "refusing: root '$root' is not a directory"
    return 2
  fi
  if ! phys="$(CDPATH='' cd -P -- "$root" 2>/dev/null && pwd -P)" || [ -z "$phys" ]; then
    pgdr_tmp_log "refusing: cannot resolve the physical path of '$root'"
    return 2
  fi
  if ! pgdr_tmp_owned "$phys"; then
    pgdr_tmp_log "refusing: '$phys' is not owned by the current user"
    return 2
  fi
  if pgdr_tmp_root_forbidden "$phys"; then
    pgdr_tmp_log "refusing: '$phys' is a forbidden or too shallow root"
    return 2
  fi
  if [ "${phys##*/}" != "$base" ]; then
    pgdr_tmp_log "refusing: '$phys' does not have the basename '$base'"
    return 2
  fi
  if [ -z "$(find "$phys" -maxdepth 0 -perm 0700 -print 2>/dev/null)" ]; then
    pgdr_tmp_log "refusing: '$phys' does not have mode 0700"
    return 2
  fi
  if [ -L "$phys/$marker" ] || [ ! -f "$phys/$marker" ] || ! pgdr_tmp_owned "$phys/$marker"; then
    pgdr_tmp_log "refusing: '$phys' has no marker file '$marker' (written by activation)"
    return 2
  fi
  _PGDR_TMP_ROOT="$phys"
  return 0
}

# pgdr_tmp_capture LIVENESS: capture ps and lsof ONCE, fail closed. Sets
# _PGDR_PS (argv text), _PGDR_LSOF (newline-wrapped open-file paths) and
# _PGDR_LIVE_UUIDS (newline-wrapped lowercase uuid tokens found in argv).
pgdr_tmp_capture() {
  local liveness="${1:-}" lsof_out
  _PGDR_PS=""
  _PGDR_LSOF=""
  _PGDR_LIVE_UUIDS=""
  if ! _PGDR_PS="$(ps -axww -o command= 2>/dev/null)"; then
    pgdr_tmp_log "refusing: 'ps -axww -o command=' failed (liveness unknown)"
    return 2
  fi
  if [ -z "$_PGDR_PS" ]; then
    pgdr_tmp_log "refusing: 'ps -axww -o command=' printed nothing (liveness unknown)"
    return 2
  fi
  if [ "$liveness" = claude-session ]; then
    if ! lsof_out="$(lsof -n -w -Fn 2>/dev/null)"; then
      pgdr_tmp_log "refusing: 'lsof -n -w -Fn' failed (liveness unknown)"
      return 2
    fi
  else
    if ! lsof_out="$(lsof -n -w -d cwd -Fn 2>/dev/null)"; then
      pgdr_tmp_log "refusing: 'lsof -n -w -d cwd -Fn' failed (liveness unknown)"
      return 2
    fi
  fi
  if [ -z "$lsof_out" ]; then
    pgdr_tmp_log "refusing: lsof printed nothing (liveness unknown)"
    return 2
  fi
  _PGDR_LSOF="$(printf '%s\n' "$lsof_out" | sed -n 's/^n//p')"
  _PGDR_LSOF=$'\n'"$_PGDR_LSOF"$'\n'
  if [ "$liveness" = claude-session ]; then
    _PGDR_LIVE_UUIDS="$(printf '%s\n' "$_PGDR_PS" | pgdr_tmp_uuid_tokens)"
    _PGDR_LIVE_UUIDS=$'\n'"$_PGDR_LIVE_UUIDS"$'\n'
  fi
  return 0
}

# pgdr_tmp_uuid_tokens: stdin -> the lowercase canonical-uuid tokens of it, one
# per line. A token is a maximal run of [0-9A-Za-z-]; only a token that IS a uuid
# counts, so a uuid inside a longer token (x-<uuid>, <uuid>-x, xx<uuid>yy) never does.
pgdr_tmp_uuid_tokens() {
  LC_ALL=C tr -c '0-9A-Za-z-' '\n' | LC_ALL=C tr '[:upper:]' '[:lower:]' |
    sed -n -E '/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/p'
}

# pgdr_tmp_fresh PATH MINUTES [inner]: true when something under PATH (or PATH
# itself) was modified within MINUTES, or when that cannot be told (find failed:
# fail closed, the unit is kept). With "inner" PATH's own mtime is ignored, used
# after the quarantine rename, which can refresh a directory's own mtime.
pgdr_tmp_fresh() {
  local p="${1:-}" minutes="${2:-}" out
  if [ "${3:-}" = inner ] && [ -d "$p" ]; then
    if ! out="$(find "$p" -mindepth 1 -mmin "-$minutes" -print -quit 2>/dev/null)"; then
      return 0
    fi
  else
    if ! out="$(find "$p" -mmin "-$minutes" -print -quit 2>/dev/null)"; then
      return 0
    fi
  fi
  [ -n "$out" ]
}

# pgdr_tmp_has_special PATH: true when PATH is or contains a socket or FIFO.
pgdr_tmp_has_special() {
  local out
  if ! out="$(find "${1:-}" \( -type s -o -type p \) -print -quit 2>/dev/null)"; then
    return 0
  fi
  [ -n "$out" ]
}

# pgdr_tmp_live UNIT NAME: true when a liveness guard keeps the unit.
pgdr_tmp_live() {
  local u="${1:-}" name="${2:-}" logical tok
  logical="$u"
  if [ "${_PGDR_ROOT_GIVEN:-}" != "${_PGDR_TMP_ROOT:-}" ]; then
    logical="${_PGDR_ROOT_GIVEN%/}${u#"$_PGDR_TMP_ROOT"}"
  fi
  if [[ $_PGDR_PS == *"$u"* ]] || [[ $_PGDR_PS == *"$logical"* ]]; then
    return 0
  fi
  if [[ $_PGDR_LSOF == *$'\n'"$u"$'\n'* ]] || [[ $_PGDR_LSOF == *$'\n'"$u"/* ]]; then
    return 0
  fi
  if [ "${_PGDR_LIVENESS:-}" = claude-session ] && [ "${#name}" -ge 36 ]; then
    while IFS= read -r tok; do
      [ -n "$tok" ] || continue
      if [[ $_PGDR_LIVE_UUIDS == *$'\n'"$tok"$'\n'* ]]; then
        return 0
      fi
    done <<<"$(printf '%s\n' "$name" | pgdr_tmp_uuid_tokens)"
  fi
  return 1
}

# pgdr_tmp_children DIR LEVEL: the entries of DIR (never . or ..), NUL-safe, into
# the array _PGDR_K<LEVEL> (one array per tree level, so nested loops do not
# overwrite each other; bash 3.2 has no namerefs).
pgdr_tmp_children() {
  local x
  case "${2:-}" in
  0) _PGDR_K0=() ;;
  1) _PGDR_K1=() ;;
  2) _PGDR_K2=() ;;
  *) _PGDR_K3=() ;;
  esac
  while IFS= read -r -d '' x; do
    case "${2:-}" in
    0) _PGDR_K0+=("$x") ;;
    1) _PGDR_K1+=("$x") ;;
    2) _PGDR_K2+=("$x") ;;
    *) _PGDR_K3+=("$x") ;;
    esac
  done < <(find "${1:-}" -mindepth 1 -maxdepth 1 -print0 2>/dev/null)
}

# pgdr_tmp_unit UNIT: judge, and in apply mode remove, one unit. Returns 0 when
# the unit was removed (or, in dry mode, would be), 1 when it is kept. Reads and
# updates the counters of pgdr_tmp_sweep (bash dynamic scope).
pgdr_tmp_unit() {
  local u="${1:-}" name q
  name="${u##*/}"
  case "$name" in .sweeping.*) return 1 ;; esac
  if [ -L "$u" ]; then
    return 1
  fi
  if ! pgdr_tmp_owned "$u"; then
    pgdr_tmp_log "skip (not owned by the current user): $(pgdr_tmp_flat "$u")"
    n_skipped=$((n_skipped + 1))
    return 1
  fi
  if pgdr_tmp_live "$u" "$name"; then
    [ -z "${PGDR_TMP_SWEEP_VERBOSE:-}" ] || pgdr_tmp_log "keep (live): $(pgdr_tmp_flat "$u")"
    n_kept=$((n_kept + 1))
    return 1
  fi
  if pgdr_tmp_has_special "$u"; then
    [ -z "${PGDR_TMP_SWEEP_VERBOSE:-}" ] || pgdr_tmp_log "keep (socket or FIFO): $(pgdr_tmp_flat "$u")"
    n_kept=$((n_kept + 1))
    return 1
  fi
  if pgdr_tmp_fresh "$u" "$_PGDR_IDLE"; then
    n_kept=$((n_kept + 1))
    return 1
  fi
  if [ "$_PGDR_MODE" = dry ]; then
    printf 'would remove: %s\n' "$(pgdr_tmp_flat "$u")"
    n_removed=$((n_removed + 1))
    return 0
  fi
  n_seq=$((n_seq + 1))
  q="$_PGDR_TMP_ROOT/.sweeping.${BASHPID:-$$}.$n_seq.${name:0:100}"
  if [ -e "$q" ] || [ -L "$q" ]; then
    pgdr_tmp_log "failed: quarantine name '$(pgdr_tmp_flat "$q")' is taken"
    n_failed=$((n_failed + 1))
    return 1
  fi
  if ! mv -- "$u" "$q"; then
    pgdr_tmp_log "failed: cannot quarantine $(pgdr_tmp_flat "$u")"
    n_failed=$((n_failed + 1))
    return 1
  fi
  if pgdr_tmp_fresh "$q" "$_PGDR_IDLE" inner; then
    if mv -- "$q" "$u"; then
      pgdr_tmp_log "keep (became active during the sweep): $(pgdr_tmp_flat "$u")"
      n_kept=$((n_kept + 1))
    else
      pgdr_tmp_log "FAILED to move an active unit back: $(pgdr_tmp_flat "$q") (was $(pgdr_tmp_flat "$u"))"
      n_failed=$((n_failed + 1))
    fi
    return 1
  fi
  if rm -rf -- "$q"; then
    printf 'removed: %s\n' "$(pgdr_tmp_flat "$u")"
    n_removed=$((n_removed + 1))
    return 0
  fi
  pgdr_tmp_log "failed: could not remove $(pgdr_tmp_flat "$u") (left as $(pgdr_tmp_flat "$q"), retried next run)"
  n_failed=$((n_failed + 1))
  return 1
}

# pgdr_tmp_dir_done DIR IDLE-PRE KEPT: after a depth-1 or depth-2 directory's
# entries were processed, rmdir it when it was idle beforehand and nothing in it
# was kept. Returns 0 when the directory is (or in dry mode would be) removed.
pgdr_tmp_dir_done() {
  local d="${1:-}" idle_pre="${2:-}" kept="${3:-1}"
  [ "$idle_pre" = 1 ] && [ "$kept" -eq 0 ] || return 1
  pgdr_tmp_owned "$d" || return 1
  if [ "$_PGDR_MODE" = dry ]; then
    printf 'would rmdir: %s\n' "$(pgdr_tmp_flat "$d")"
    n_dirs=$((n_dirs + 1))
    return 0
  fi
  if rmdir -- "$d" 2>/dev/null; then
    printf 'rmdir: %s\n' "$(pgdr_tmp_flat "$d")"
    n_dirs=$((n_dirs + 1))
    return 0
  fi
  return 1
}

# pgdr_tmp_idle_pre DIR: prints 1 when DIR's own mtime is older than the cutoff.
pgdr_tmp_idle_pre() {
  if pgdr_tmp_fresh_own "${1:-}"; then
    echo 0
  else
    echo 1
  fi
}

# pgdr_tmp_fresh_own DIR: true when DIR's own mtime is within the cutoff, or
# cannot be told. (Used for the empty-directory pass, where only the directory
# itself matters.)
pgdr_tmp_fresh_own() {
  local out
  if ! out="$(find "${1:-}" -maxdepth 0 -mmin "-$_PGDR_IDLE" -print 2>/dev/null)"; then
    return 0
  fi
  [ -n "$out" ]
}

# pgdr_tmp_sweep MODE ROOT MARKER BASENAME IDLE-MINUTES DEPTH LIVENESS
pgdr_tmp_sweep() {
  local LC_ALL=C
  local _PGDR_MODE="${1:-}" _PGDR_ROOT_GIVEN="${2:-}" marker="${3:-}" base="${4:-}"
  local _PGDR_IDLE="${5:-}" depth="${6:-}" _PGDR_LIVENESS="${7:-}"
  local _PGDR_TMP_ROOT="" _PGDR_PS="" _PGDR_LSOF="" _PGDR_LIVE_UUIDS=""
  local -a _PGDR_K0=() _PGDR_K1=() _PGDR_K2=() _PGDR_K3=()
  local n_removed=0 n_kept=0 n_skipped=0 n_failed=0 n_dirs=0 n_seq=0 rc
  local k0 k1 k2 k3 kept1 kept2 pre1 pre2

  case "$_PGDR_MODE" in dry | apply) ;; *)
    pgdr_tmp_log "usage: pgdr_tmp_sweep <dry|apply> <root> <marker> <basename> <idle-minutes> <1|3> <none|claude-session>"
    return 2
    ;;
  esac
  case "$depth" in 1 | 3) ;; *)
    pgdr_tmp_log "refusing: depth must be 1 or 3, not '$depth'"
    return 2
    ;;
  esac
  case "$_PGDR_LIVENESS" in none | claude-session) ;; *)
    pgdr_tmp_log "refusing: liveness must be none or claude-session, not '$_PGDR_LIVENESS'"
    return 2
    ;;
  esac
  case "$_PGDR_IDLE" in '' | *[!0-9]* | 0*)
    pgdr_tmp_log "refusing: idle minutes must be a positive integer, not '$_PGDR_IDLE'"
    return 2
    ;;
  esac
  case "$marker" in '' | */* | . | ..)
    pgdr_tmp_log "refusing: the marker must be a plain file name, not '$marker'"
    return 2
    ;;
  esac
  case "$base" in '' | */*)
    pgdr_tmp_log "refusing: the basename must be a plain name, not '$base'"
    return 2
    ;;
  esac

  pgdr_tmp_root_guard "$_PGDR_ROOT_GIVEN" "$marker" "$base" || return 2
  pgdr_tmp_capture "$_PGDR_LIVENESS" || return 2

  # An interrupted earlier run leaves .sweeping.* entries at depth 1; they are
  # idle by construction, so they are removed first.
  pgdr_tmp_children "$_PGDR_TMP_ROOT" 0
  for k0 in ${_PGDR_K0[@]+"${_PGDR_K0[@]}"}; do
    case "${k0##*/}" in .sweeping.*) ;; *) continue ;; esac
    if ! pgdr_tmp_owned "$k0"; then
      pgdr_tmp_log "skip (not owned by the current user): $(pgdr_tmp_flat "$k0")"
      n_skipped=$((n_skipped + 1))
    elif [ "$_PGDR_MODE" = dry ]; then
      printf 'would remove: %s\n' "$(pgdr_tmp_flat "$k0")"
      n_removed=$((n_removed + 1))
    elif rm -rf -- "$k0"; then
      printf 'removed: %s\n' "$(pgdr_tmp_flat "$k0")"
      n_removed=$((n_removed + 1))
    else
      pgdr_tmp_log "failed: could not remove the leftover $(pgdr_tmp_flat "$k0")"
      n_failed=$((n_failed + 1))
    fi
  done

  pgdr_tmp_children "$_PGDR_TMP_ROOT" 1
  for k1 in ${_PGDR_K1[@]+"${_PGDR_K1[@]}"}; do
    # Exempt at every depth: the root's marker and the quarantine names.
    case "${k1##*/}" in .sweeping.*) continue ;; esac
    [ "${k1##*/}" != "$marker" ] || continue
    if [ "$depth" = 1 ]; then
      # D9a safeguard: Claude's own per-user tree is never swept by this item.
      case "${k1##*/}" in claude-*) continue ;; esac
      pgdr_tmp_unit "$k1" || true
      continue
    fi

    # depth 3: a unit is <root>/<a>/<b>/<c>, or a regular file at depth 1 or 2.
    [ ! -L "$k1" ] || continue
    if [ -f "$k1" ]; then
      pgdr_tmp_unit "$k1" || true
      continue
    fi
    [ -d "$k1" ] || continue
    pre1="$(pgdr_tmp_idle_pre "$k1")"
    kept1=0
    pgdr_tmp_children "$k1" 2
    for k2 in ${_PGDR_K2[@]+"${_PGDR_K2[@]}"}; do
      case "${k2##*/}" in .sweeping.*)
        kept1=$((kept1 + 1))
        continue
        ;;
      esac
      if [ -L "$k2" ]; then
        kept1=$((kept1 + 1))
      elif [ -f "$k2" ]; then
        pgdr_tmp_unit "$k2" || kept1=$((kept1 + 1))
      elif [ -d "$k2" ]; then
        pre2="$(pgdr_tmp_idle_pre "$k2")"
        kept2=0
        pgdr_tmp_children "$k2" 3
        for k3 in ${_PGDR_K3[@]+"${_PGDR_K3[@]}"}; do
          case "${k3##*/}" in .sweeping.*)
            kept2=$((kept2 + 1))
            continue
            ;;
          esac
          if [ -L "$k3" ]; then
            kept2=$((kept2 + 1))
          else
            pgdr_tmp_unit "$k3" || kept2=$((kept2 + 1))
          fi
        done
        pgdr_tmp_dir_done "$k2" "$pre2" "$kept2" || kept1=$((kept1 + 1))
      else
        kept1=$((kept1 + 1))
      fi
    done
    pgdr_tmp_dir_done "$k1" "$pre1" "$kept1" || true
  done

  rc=0
  [ "$n_failed" -eq 0 ] || rc=3
  printf 'pgdr-tmp-sweep: %s %s: removed=%s empty-dirs=%s kept=%s not-owned=%s failed=%s\n' \
    "$_PGDR_MODE" "$(pgdr_tmp_flat "$_PGDR_TMP_ROOT")" "$n_removed" "$n_dirs" "$n_kept" "$n_skipped" "$n_failed"
  return "$rc"
}
