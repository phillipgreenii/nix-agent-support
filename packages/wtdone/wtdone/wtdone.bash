# shellcheck shell=bash
# Core logic (canonical-root resolution, worktree-by-branch lookup, the lsof
# liveness guard, and the remaining-worktrees report) as testable functions,
# split out of wtdone.sh. No top-level code -- mkBashScript sources this file
# before the .sh body.

# canonical_root: print the absolute path to the MAIN working tree, i.e. the
# canonical clone, resolved even when wtdone is invoked from inside an
# existing linked worktree. Duplicated verbatim from
# integrate-branch-support.bash's function of the same name (wtnew's copy
# carries the identical note) -- this repo has no shared git-helpers library
# yet to depend on instead; keep the copies in sync if one is ever extracted.
# See integrate-branch-support.bash's own doc comment for the full rationale,
# including the --separate-git-dir caveat.
canonical_root() {
  local common_dir
  common_dir="$(git rev-parse --git-common-dir)"
  git -C "$(dirname "$common_dir")" rev-parse --show-toplevel
}

# wtdone_find_worktree <cc> <branch>: print the absolute path of the linked
# worktree in <cc> that has <branch> checked out, or nothing if no worktree
# has it checked out (already removed elsewhere, or never had one -- a plain
# local branch with no worktree). Deliberately resolved by ASKING GIT which
# worktree (if any) holds the branch, rather than assuming a directory-naming
# convention (e.g. wtnew's `.worktrees/<name>`): wtdone's four callers
# (cleanup-workforest, ff-merge-to-main's FF-4, wrap-up-session,
# drain-beads/pb-drain-isolate) each lay worktrees out differently -- a
# coordinated workforest set's members live at
# `<workspace_root>/.workforests/<branch>/<member>`, for instance, nowhere
# near `.worktrees/`. Querying `git worktree list --porcelain` works
# regardless of layout, and it is the SAME idiom this repo's own
# git-branch-maintenance.sh already uses (get_branch_worktree) for exactly
# this lookup.
wtdone_find_worktree() {
  local cc="$1" branch="$2"
  git -C "$cc" worktree list --porcelain | awk -v branch="$branch" '
    /^worktree / { path=$2 }
    /^branch refs\/heads\// && $0 == "branch refs/heads/"branch { print path; exit }
  '
}

# wtdone_anchored_processes <worktree>: print `lsof`'s RAW, UNFILTERED field
# report (`-F pcn`: one `p<pid>` / `c<command>` / `n<path>` line per field,
# plus `f` terminators) of every process whose CURRENT WORKING DIRECTORY (the
# "cwd" file descriptor, `-d cwd`) is anchored somewhere inside <worktree>
# (`+D`, recursive) -- empty when none. `-a` ANDs the two selectors together
# (lsof otherwise ORs selectors), so this reports exactly "processes cwd'd
# under this tree", not every process that merely has some open file under
# it. `+c 0` lifts lsof's 9-character command-name truncation, and `-F`
# (rather than the default table) keeps names that contain spaces, such as
# `Google Chrome Helper (Renderer)`, in ONE field and emits no COMMAND header
# row. Guarded: lsof exits nonzero (and prints nothing) when it finds no
# matching process, which is the common/expected case here, not a tool
# failure -- so its exit status is discarded and callers key off whether the
# OUTPUT is non-empty instead. This probe does NOT decide which processes
# block removal -- that is wtdone_classify_anchored's job (the allow-list).
wtdone_anchored_processes() {
  local worktree="$1"
  lsof -a -d cwd +D "$worktree" +c 0 -F pcn 2>/dev/null || true
}

# The default allow-list of process kinds whose presence in a worktree
# BLOCKS its removal (operator ruling, Phillip, 2026-10-06, bead pg2-qs7lp):
# a session/agent (claude), VCS (git), shells and scripting (bash zsh sh
# python*), editors that can hold unsaved work (vim nvim emacs) and builds
# that can be mid-flight (go nix). `node` is deliberately absent -- the
# language servers that motivated the allow-list are node processes. Entry
# syntax: see wtdone_command_blocks.
WTDONE_DEFAULT_BLOCKING_COMMANDS="claude git bash zsh sh python* vim nvim emacs go nix"

# wtdone_normalize_command <name>: print <name> normalized for allow-list
# comparison: ONE leading `.` stripped, ONE trailing `-wrapped` stripped,
# then lowercased. lsof reports the kernel COMM name, not argv[0], so a
# nix-wrapped program shows as `.claude-wrapped` even though `ps -o comm`
# says `claude`; both normalize to `claude`.
wtdone_normalize_command() {
  local name="$1"
  name="${name#.}"
  name="${name%-wrapped}"
  printf '%s' "$name" | tr '[:upper:]' '[:lower:]'
}

# wtdone_command_blocks <name>: the blocking predicate (Specification
# pattern). Succeeds iff the normalized <name> matches an entry of the
# allow-list: $WTDONE_BLOCKING_COMMANDS when set to something non-blank,
# otherwise WTDONE_DEFAULT_BLOCKING_COMMANDS (an unset OR empty/blank value
# means the default, never "block nothing"). The list is space-separated;
# an entry ending in `*` is a case-insensitive PREFIX match (`python*`
# matches `python3.13` and macOS's `Python`), every other entry is an exact
# match after normalization. The list is split with `read -ra`, never by an
# unquoted expansion, so `python*` is never glob-expanded against the cwd.
wtdone_command_blocks() {
  local norm list entry prefix
  local -a entries
  norm="$(wtdone_normalize_command "$1")"
  list="${WTDONE_BLOCKING_COMMANDS:-}"
  [[ $list =~ [^[:space:]] ]] || list="$WTDONE_DEFAULT_BLOCKING_COMMANDS"
  read -ra entries <<<"$list"
  for entry in "${entries[@]}"; do
    entry="$(printf '%s' "$entry" | tr '[:upper:]' '[:lower:]')"
    if [[ $entry == *'*' ]]; then
      prefix="${entry%\*}"
      [[ $norm == "$prefix"* ]] && return 0
    else
      [[ $norm == "$entry" ]] && return 0
    fi
  done
  return 1
}

# wtdone_classify_anchored: read wtdone_anchored_processes' `-F pcn` output on
# stdin and print one line per anchored process, `block <pid> <command>` when
# wtdone_command_blocks says its name is on the allow-list, else `ignore <pid>
# <command>` (the command is the remainder of the line, so a name with
# spaces stays whole). Field-oriented, NOT table parsing: a `p` line starts a
# process, `c` names it, every other field (`f`, `n`) is irrelevant here.
wtdone_classify_anchored() {
  local line pid="" name=""
  while IFS= read -r line || [[ -n $line ]]; do
    case "$line" in
    p*)
      _wtdone_emit_classified "$pid" "$name"
      pid="${line#p}"
      name=""
      ;;
    c*) name="${line#c}" ;;
    *) ;;
    esac
  done
  _wtdone_emit_classified "$pid" "$name"
}

# _wtdone_emit_classified <pid> <name>: print the classified line for one
# process; a no-op when <pid> is empty (before the first `p` record).
_wtdone_emit_classified() {
  local pid="$1" name="$2"
  [[ -n $pid ]] || return 0
  if wtdone_command_blocks "$name"; then
    printf 'block %s %s\n' "$pid" "$name"
  else
    printf 'ignore %s %s\n' "$pid" "$name"
  fi
}

# wtdone_remaining_worktrees <cc>: print `git worktree list`'s report for
# <cc>, for step 6's "remaining worktrees" line. Thin, named wrapper purely
# so the lib tests can assert on it directly instead of shelling `git`
# themselves.
wtdone_remaining_worktrees() {
  local cc="$1"
  git -C "$cc" worktree list
}
