# shellcheck shell=bash

# Shared state conventions for bgrun/bgcheck/bgwait. A "job" is NAME plus four files in
# the state directory: NAME.log (combined output), NAME.pid (launcher subshell),
# NAME.exit (the payload's TRUE exit code, written by the launcher's wrapper),
# NAME.meta (session, command, cwd, start time, pid start time). The exit file is the
# only trustworthy completion signal: the pid dying without it means a crash/kill,
# and the log's tail proves nothing about exit status.
#
# NAME.meta is line-oriented `key: value`. `session:` (the CLAUDE_CODE_SESSION_ID
# that launched the job; omitted when unset) is written FIRST so no later free-text
# line can shadow it, and `pidstart:` (ps lstart of the recorded pid) is appended
# once the pid is known. Readers take the FIRST line carrying a key.

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

# bg_meta_get NAME KEY — the value of the FIRST `KEY: value` line of NAME.meta,
# nothing (exit 1) when the file or key is absent.
bg_meta_get() {
  local name="$1" key="$2" meta line
  meta="$(bg_meta_path "$name")"
  [[ -f $meta ]] || return 1
  while IFS= read -r line || [[ -n $line ]]; do
    if [[ $line == "$key: "* ]]; then
      printf '%s\n' "${line#"$key: "}"
      return 0
    fi
  done <"$meta"
  return 1
}

# bg_proc_start PID — the process's start time as ps prints it (stable for the
# life of the process, different for a process that later reuses the pid); empty
# when the pid is not alive.
bg_proc_start() {
  ps -p "$1" -o lstart= 2>/dev/null | sed -e 's/^ *//' -e 's/ *$//' || true
}

# bg_proc_command PID — the process's full command line; empty when not alive.
bg_proc_command() {
  ps -p "$1" -o command= 2>/dev/null || true
}

# bg_descendants PID — every transitive child of PID (PID itself excluded), one
# pid per line, deepest-first (a process is listed before its parent), so a
# caller signalling in order never orphans a child it could still have reached.
bg_descendants() {
  ps -A -o pid= -o ppid= 2>/dev/null | awk -v root="$1" '
    { kids[$2] = kids[$2] " " $1 }
    END {
      n = 0
      queue[n++] = root
      for (i = 0; i < n; i++) {
        m = split(kids[queue[i]], a, " ")
        for (j = 1; j <= m; j++) queue[n++] = a[j]
      }
      for (i = n - 1; i >= 1; i--) print queue[i]
    }'
}
