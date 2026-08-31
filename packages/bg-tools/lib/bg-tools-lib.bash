# shellcheck shell=bash

# Shared state conventions for bgrun/bgcheck. A "job" is NAME plus four files in
# the state directory: NAME.log (combined output), NAME.pid (launcher subshell),
# NAME.exit (the payload's TRUE exit code, written by the launcher's wrapper),
# NAME.meta (command, cwd, start time). The exit file is the only trustworthy
# completion signal: the pid dying without it means a crash/kill, and the log's
# tail proves nothing about exit status.

bg_state_dir() {
  if [[ -n ${BG_DIR:-} ]]; then
    echo "$BG_DIR"
  else
    echo "${TMPDIR:-/tmp}/pg-bg-${USER:-$(id -un)}"
  fi
}

# Names become filenames; restrict to a path-safe alphabet.
bg_validate_name() {
  [[ $1 =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]
}

bg_log_path() { echo "$(bg_state_dir)/$1.log"; }
bg_pid_path() { echo "$(bg_state_dir)/$1.pid"; }
bg_exit_path() { echo "$(bg_state_dir)/$1.exit"; }
bg_meta_path() { echo "$(bg_state_dir)/$1.meta"; }

# bg_status NAME — one line on stdout, one of:
#   DONE exit=<code>          NAME.exit exists (checked FIRST: the launcher
#                             subshell may briefly outlive the exit write)
#   RUNNING pid=<p> etime=<t> pid recorded and alive
#   EXITED unknown            pid recorded and dead, no exit file (crash/kill)
#   UNKNOWN                   nothing recorded for NAME (also exit 1)
bg_status() {
  local name="$1" pid etime pid_file exit_file
  pid_file="$(bg_pid_path "$name")"
  exit_file="$(bg_exit_path "$name")"
  if [[ -f $exit_file ]]; then
    echo "DONE exit=$(<"$exit_file")"
    return 0
  fi
  if [[ -f $pid_file ]]; then
    pid="$(<"$pid_file")"
    if kill -0 "$pid" 2>/dev/null; then
      etime="$(ps -p "$pid" -o etime= 2>/dev/null | tr -d ' ' || true)"
      echo "RUNNING pid=$pid etime=${etime:-?}"
      return 0
    fi
    echo "EXITED unknown"
    return 0
  fi
  echo "UNKNOWN"
  return 1
}

# bg_list_names — every NAME with a pid file in the state dir, one per line.
bg_list_names() {
  local dir f b
  dir="$(bg_state_dir)"
  [[ -d $dir ]] || return 0
  for f in "$dir"/*.pid; do
    [[ -e $f ]] || continue
    b="$(basename "$f")"
    echo "${b%.pid}"
  done
}
