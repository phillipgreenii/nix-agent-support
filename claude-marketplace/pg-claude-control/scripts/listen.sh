#!/usr/bin/env bash
# pg-claude-control listener. Spawned by the control mod as a child of the claude process, so
# $PPID is the Claude's pid. Holds <pid>.fifo, opened read-write (a writer never blocks and never
# sees EOF), and prints every line it reads to stdout for the mod. Events go to stderr as
# "<name>: <detail>". It starts no child process while idle.
#
# State directory: PG_CLAUDE_CONTROL_STATE_DIR, else $XDG_STATE_HOME/pg-claude-control, else
# ~/.local/state/pg-claude-control. The inbox is <state>/inbox (mode 0700, user-owned, never a
# symbolic link); the FIFO is mode 0600; payloads/ is mode 0700 (its files are 0600).
#
# Exit 20 (an app code, not a sysexits one): the inbox directory was refused (the mod does not respawn). File identity uses `test
# -ef`, never BSD `stat -f`: the PATH here resolves stat to GNU coreutils.
set -u
umask 077

parent=$PPID
state=${PG_CLAUDE_CONTROL_STATE_DIR:-${XDG_STATE_HOME:-${HOME:-}/.local/state}/pg-claude-control}
inbox=$state/inbox
fifo=$inbox/$parent.fifo

refuse() {
  printf 'inbox_dir_refused: %s\n' "$1" >&2
  exit 20
}

prepare_dir() { # <dir>
  if [ -L "$1" ]; then refuse "$1 is a symbolic link"; fi
  if [ -e "$1" ]; then
    [ -d "$1" ] || refuse "$1 is not a directory"
    [ -O "$1" ] || refuse "$1 is not owned by this user"
  else
    mkdir -p "$1" || refuse "cannot create $1"
  fi
  chmod 700 "$1" || refuse "cannot tighten $1"
}

# cmp=1 when `test -ef` can tell the FIFO from another file through /dev/fd/3 (Linux; macOS's
# fdescfs reports another device number, so there `-ef` is always false and only `-p` is used).
cmp=0

make_fifo() {
  rm -f "$fifo"
  mkfifo "$fifo" || return 1
  chmod 600 "$fifo"
  exec 3<>"$fifo"
  cmp=0
  if [ "$fifo" -ef /dev/fd/3 ]; then cmp=1; fi
}

# True while $fifo is still the file this listener holds open.
ours() {
  [ -p "$fifo" ] || return 1
  if [ "$cmp" = 1 ]; then [ "$fifo" -ef /dev/fd/3 ]; fi
}

cleanup() {
  # remove the FIFO only when it is still ours
  if ours; then rm -f "$fifo"; fi
}

prepare_dir "$state"
prepare_dir "$inbox"
prepare_dir "$inbox/payloads"

trap cleanup EXIT
trap 'exit 0' TERM INT HUP

make_fifo || {
  printf 'fifo_create_failed: %s\n' "$fifo" >&2
  exit 1
}

while :; do
  # the parent (the Claude) died, even by SIGKILL: leave, removing the FIFO
  kill -0 "$parent" 2>/dev/null || exit 0
  if IFS= read -r -t 1 line <&3; then
    printf '%s\n' "$line" || exit 0
  else
    rc=$?
    if [ "$rc" -le 128 ]; then exit 0; fi # EOF cannot happen on a read-write FIFO
    # one-second health check: the FIFO deleted or replaced by another file is recreated
    if ! ours; then
      exec 3<&-
      make_fifo || exit 1
    fi
  fi
done
