# shellcheck shell=bash
# Core logic (primary-branch resolution, signal gathering, strategy
# selection) as testable functions, split out of integrate-branch-support.sh.
# No top-level code — mkBashScript sources this file before the .sh body.

# resolve_primary_branch: print the repo's primary branch name.
# Order: pgii-integrate-branch.primaryBranch git config -> origin/HEAD
# (stripped of the "origin/" prefix) -> "main".
resolve_primary_branch() {
  local b
  b="$(git config --get pgii-integrate-branch.primaryBranch 2>/dev/null || true)"
  [ -n "$b" ] && {
    printf '%s' "$b"
    return
  }
  b="$(git symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null || true)"
  [ -n "$b" ] && {
    printf '%s' "${b#origin/}"
    return
  }
  printf 'main'
}

# canonical_root: print the absolute path to the MAIN working tree, i.e. the
# canonical clone, resolved even when invoked from inside a linked worktree.
# git-common-dir is the shared ".git" dir for the whole worktree set; its
# parent directory is the main worktree (verified true both when already
# inside the main tree and from a linked one). For the rare repo laid out
# with `--separate-git-dir`, git-common-dir's parent is not the main
# worktree; `git worktree list --porcelain | head -1` (strip the "worktree "
# prefix) is the more-robust alternative for that layout.
canonical_root() {
  local common_dir
  common_dir="$(git rev-parse --git-common-dir)"
  git -C "$(dirname "$common_dir")" rev-parse --show-toplevel
}

# canonical_core_worktree: print the value of core.worktree set in the
# CANONICAL clone's own .git/config (the shared config of the whole worktree
# set), or nothing when it is unset. A stray core.worktree there makes git LIE
# about the canonical clone: `rev-parse --show-toplevel` (and so canonical_root,
# and the CC fact) reports ANOTHER worktree's path and `git status` lists that
# worktree's files as untracked -- a phantom dirty tree that halts the land at
# FF-0a (bead pg2-4c4nv). Reads the config FILE directly (`git config --file`)
# so the answer is the ground truth rather than git's own possibly-lying
# opinion. Strictly read-only: it never unsets or edits the key (R-3).
canonical_core_worktree() {
  local common_dir
  common_dir="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" || return 0
  [ -f "$common_dir/config" ] || return 0
  git config --file "$common_dir/config" --get core.worktree 2>/dev/null || true
}

# canonical_branch: print the branch checked out in the canonical clone.
canonical_branch() {
  git -C "$(canonical_root)" symbolic-ref --short -q HEAD || echo "(detached)"
}

# canonical_dirty: print "true"/"false" for whether the canonical clone has
# uncommitted changes (tracked or untracked).
canonical_dirty() {
  if [ -n "$(git -C "$(canonical_root)" status --porcelain)" ]; then
    echo true
  else
    echo false
  fi
}

# detect_remote: print exactly two lines — (1) the resolved remote name,
# empty when there is none or it can't be resolved unambiguously; (2) an
# "ambiguous" reason note, empty unless line 1 is empty *because* of
# ambiguity (never because of a genuine absence of any remote). Two
# lines (rather than a bare name via a global side-effect) because this
# runs inside `$(...)` command substitutions, which fork a subshell — a
# global set inside the function would not survive back to the caller.
# Resolution order: (1) the CANONICAL clone's checked-out branch's upstream
# remote (git -C <canonical_root> rev-parse --abbrev-ref
# --symbolic-full-name @{upstream}, taking the remote portion before the
# first "/"); (2) if exactly one git remote exists, use it; (3) otherwise
# empty. Zero remotes is simply "no remote" (not ambiguous); two or more
# remotes with no upstream configured IS ambiguous (which remote would it
# be?) and reports so on line 2. Every git query is anchored to
# canonical_root so it is correct from a flake subdirectory and from inside
# a linked worktree (see the anchoring note in the body).
detect_remote() {
  local root upstream remotes count reason=""
  # Anchor every git query to the canonical clone, NOT the current working
  # directory. The tool may run from a flake SUBDIRECTORY (e.g. homelab's
  # nix/) inside a LINKED WORKTREE whose feature branch has no upstream. A
  # cwd-relative '@{upstream}' then resolves nothing and the fallback below
  # counts the repo's remotes -- with 2+ remotes that is reported as
  # "ambiguous", so remote comes back null even though the repo genuinely
  # pushes to a remote (homelab has origin+bitbucket and main tracks
  # origin/main). The canonical clone's checked-out branch is the one
  # integration lands/pushes, so ITS upstream is the remote we want; this
  # matches canonical_branch/canonical_dirty, which already anchor here.
  # git remote's output is repo-wide (shared config) so anchoring it is a
  # no-op for correctness, but kept consistent. See canonical_root.
  root="$(canonical_root)"
  upstream="$(git -C "$root" rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null || true)"
  if [ -n "$upstream" ]; then
    printf '%s\n\n' "${upstream%%/*}"
    return
  fi
  remotes="$(git -C "$root" remote 2>/dev/null || true)"
  count=0
  if [ -n "$remotes" ]; then
    # Pure-bash line count via a read loop, not `grep -c`/`wc -l`: grep and
    # wc are external commands that may be absent from a scrubbed PATH, so a
    # bash builtin avoids that coreutils dependency. (This is the external-
    # tool concern, distinct from the bash-version `mapfile` rationale
    # documented at the .sh call site.)
    while IFS= read -r _line; do
      [ -n "$_line" ] && count=$((count + 1))
    done <<<"$remotes"
  fi
  if [ "$count" -eq 1 ]; then
    printf '%s\n\n' "$remotes"
    return
  fi
  if [ "$count" -gt 1 ]; then
    reason="ambiguous remote: multiple remotes (${remotes//$'\n'/, }) and no upstream set for the current branch"
  fi
  printf '\n%s\n' "$reason"
}

# detect_open_pr: print a JSON object ({number,state,url}) describing the
# OPEN pull request for the current branch, or nothing when there is no
# open PR, `gh` is absent, or the call fails for any reason. `gh` is an
# optional source — this must never fail the tool (spec §4.2 graceful
# degradation). A merged/closed PR is treated the same as "no PR" (the
# work is already integrated).
detect_open_pr() {
  local json state
  command -v gh >/dev/null 2>&1 || return 0
  json="$(gh pr view --json number,state,url 2>/dev/null)" || return 0
  state="$(printf '%s' "$json" | jq -r '.state // empty' 2>/dev/null)" || return 0
  [ "$state" = "OPEN" ] || return 0
  printf '%s' "$json"
}

# detect_mr_bead <remote> <branch>: print the id of an open merge-request-type
# bead that belongs to THIS repo and the CURRENT branch, or nothing when
# beads (`bd`) is absent, unreachable (e.g. no beads database for this
# repo), there is none, or the call errors for any reason. `bd` is an
# optional source -- this must never fail the tool (spec 4.2 graceful
# degradation).
#
# The beads tracker is shared across repos, so an unscoped lookup would let
# any repo's merge-request anchor flip every other repo to `pull-request`.
# pg-desk mints the anchor with metadata `repo` (the PR's repository, the
# `<repo>#<n>` of its title), `branch` (the PR head branch) and `state` (the
# PR state); see docs/adr/0009-pg-pr-bead-schema.md and
# packages/pg-desk/internal/sync/rules.go (anchor metadata). A bead is this
# repo's and branch's only when metadata.branch equals <branch> AND
# metadata.repo equals the remote URL's `owner/name` slug (or just its
# `name`) AND metadata.state is not merged/closed. A bead without those keys
# (e.g. a title-adopted anchor with no metadata) cannot be scoped and is
# ignored. bd's --json output is a bare array or a {"data":[...]} envelope
# depending on BD_JSON_ENVELOPE (beads-lifecycle J-1), so both are accepted.
detect_mr_bead() {
  local remote="${1:-}" branch="${2:-}" url slug name rest owner json
  [ -n "$remote" ] && [ -n "$branch" ] || return 0
  command -v bd >/dev/null 2>&1 || return 0
  url="$(git remote get-url "$remote" 2>/dev/null)" || return 0
  url="${url%/}"
  url="${url%.git}"
  name="${url##*[/:]}"
  rest="${url%[/:]*}"
  owner="${rest##*[/:]}"
  slug="$owner/$name"
  [ -n "$name" ] || return 0
  json="$(bd list --type=merge-request -n 0 --json 2>/dev/null)" || return 0
  printf '%s' "$json" | jq -r --arg branch "$branch" --arg slug "$slug" --arg name "$name" '
    (if type == "object" and has("data") then .data else . end)
    | if type == "array" then . else [] end
    | map(select(type == "object" and (.metadata | type) == "object"
        and .metadata.branch == $branch
        and (.metadata.repo == $slug or .metadata.repo == $name)
        and (((.metadata.state // "") | tostring | ascii_downcase) as $s
             | ($s != "merged" and $s != "closed"))))
    | .[0].id // empty' 2>/dev/null || true
}

# resolve_strategy: print exactly two lines — (1) the resolved strategy
# (empty for "cannot infer", i.e. JSON null); (2) a human-readable reason.
# Two lines for the same subshell-survival reason as detect_remote (this
# runs inside a `$(...)`/`< <(...)` caller).
#
# Implements the resolution order (spec §4.2/§4.3). The tool is advisory
# ONLY — it never emits ask/halt/none, and it never overrides a declared
# strategy even when that strategy looks infeasible; flagging the conflict
# in the reason is as far as the tool goes (§4.4: the agent decides):
#   1. declared `pgii-integrate-branch.strategy` wins outright. A declared
#      `pull-request` with no resolvable remote is kept as-is but flagged
#      infeasible in the reason.
#   2. else an ambiguous remote (detect_remote's line-2 reason) means the
#      tool cannot even resolve what "no remote" vs. "has a remote" means,
#      so it cannot infer either — reported as-is.
#   3. else no remote at all rules out `pull-request` outright ->
#      `ff-merge-to-main`.
#   4. else an open PR, or an open merge-request bead scoped to this repo
#      and branch (detect_mr_bead), -> `pull-request`.
#   5. else the tool cannot infer -> null (empty line 1).
# current_worktree_root: print the absolute path to the top level of the
# CURRENT worktree -- i.e. wherever this tool is invoked from, resolved to
# its worktree root even when invoked from a flake subdirectory. Contrast
# canonical_root, which always resolves to the MAIN worktree regardless of
# where the tool runs; this one tracks the caller's own location instead, so
# it equals canonical_root exactly when the caller is standing in the
# canonical clone. `git rev-parse --show-toplevel` (not a bare `pwd`) is
# deliberate: a hand-rolled `WT="$(pwd)"` preamble is only correct when the
# caller happens to be at the worktree's root already, and silently wrong
# from a subdirectory -- the same subdirectory-anchoring pitfall
# detect_remote's doc comment already describes for remote resolution.
current_worktree_root() {
  git rev-parse --show-toplevel
}

# current_branch: print the branch checked out in the CURRENT worktree
# (wherever this tool is invoked from). Contrast canonical_branch, which
# always reports the MAIN worktree's branch regardless of where the tool
# runs.
current_branch() {
  git symbolic-ref --short -q HEAD || echo "(detached)"
}

# current_dirty_yesno: print "yes"/"no" for whether the CURRENT worktree
# (wherever this tool is invoked from) has uncommitted changes (tracked or
# untracked). Contrast canonical_dirty, which always reports the MAIN
# worktree's dirty state and prints "true"/"false" for the JSON mode; this
# one is the plain-text form the --facts KEY=value block uses.
current_dirty_yesno() {
  if [ -n "$(git status --porcelain)" ]; then
    echo yes
  else
    echo no
  fi
}

# ahead_behind_primary <primary_branch>: print exactly two lines -- (1) AHEAD
# (commits reachable from HEAD but not from the primary branch), (2) BEHIND
# (commits reachable from the primary branch but not from HEAD). Two lines
# for the same subshell-survival reason as detect_remote/resolve_strategy
# (this runs inside a `< <(...)` process substitution).
#
# Resolves the primary branch NAME to a ref: prefers the local branch
# refs/heads/<primary> (the common case -- local branches are shared across
# every worktree of the same repo, so this resolves even from a linked
# worktree whose own checked-out branch differs); falls back to the
# remote-tracking ref refs/remotes/origin/<primary> (relevant right after a
# fresh clone, before any local branch of that name exists). Prints "0" for
# both when NEITHER ref resolves, or when HEAD itself is unborn -- this tool
# never fails outright over an optional/best-effort fact (the same
# graceful-degradation philosophy as detect_open_pr/detect_mr_bead).
ahead_behind_primary() {
  local primary="$1" ref
  if git rev-parse --verify -q "refs/heads/$primary" >/dev/null 2>&1; then
    ref="refs/heads/$primary"
  elif git rev-parse --verify -q "refs/remotes/origin/$primary" >/dev/null 2>&1; then
    ref="refs/remotes/origin/$primary"
  else
    printf '0\n0\n'
    return
  fi
  printf '%s\n' "$(git rev-list --count "$ref"..HEAD 2>/dev/null || echo 0)"
  printf '%s\n' "$(git rev-list --count HEAD.."$ref" 2>/dev/null || echo 0)"
}

# precommit_state <worktree root>: print the PRECOMMIT fact -- one of
# "bundle", "stale", "missing", "broken" -- from `pg-hooks status --porcelain`
# (spec 5.3: callers parse the porcelain, never prose):
#   present -> bundle    stale -> stale    missing -> missing   broken -> broken
#   relocated -> stale   (the bundle exists but must be rebuilt)
#   unreachable -> missing (git never runs the hooks from this clone)
# `pg-hooks status` encodes the state in its exit code too, so a non-zero exit
# is expected and ignored here. Anything else -- pg-hooks not on PATH, no
# `state=` line, or an unrecognized/retired state (e.g. an old pg-hooks's
# `legacy`) -- is "missing": the tool tolerates both older and newer pg-hooks
# output and never crashes on it. Read-only: it never inspects, links or
# regenerates an on-disk hook config.
precommit_state() {
  local wt="$1" out="" line state=""
  if command -v pg-hooks >/dev/null 2>&1; then
    out="$(cd "$wt" && pg-hooks status --porcelain 2>/dev/null)" || true
    while IFS= read -r line; do
      case "$line" in
      state=*) state="${line#state=}" ;;
      esac
    done <<<"$out"
    case "$state" in
    present)
      printf 'bundle'
      return
      ;;
    stale | relocated)
      printf 'stale'
      return
      ;;
    broken)
      printf 'broken'
      return
      ;;
    esac
  fi
  printf 'missing'
}

# PG_HOOKS_MISSING_NOTICE: the exact line spec 5.3 prescribes when pg-hooks is
# not on PATH (exit 127 semantics). FF-1b records it and continues.
PG_HOOKS_MISSING_NOTICE='pg-hooks not installed on this machine; ask the operator to run pn workspace apply'

# ---------------------------------------------------------------------------
# FF-1b pass reuse (bead pg2-wclb73). A pre-land run takes tens of minutes on a
# loaded host, and main usually advances under it, so the fast-forward race is
# lost and FF-3 re-runs the whole check. When the rebase changed nothing the
# hooks could see, a prior PASS is carried over instead. The rule is
# conservative: any doubt re-runs the hooks. It is OFF unless
# `git config pgii-integrate-branch.preLandReuse true` (any other value, an
# unreadable value, `--no-reuse`, and a nix build sandbox all mean OFF).
#
# A pass is recorded (pre_land_record_pass) only after `pg-hooks run pre-land`
# exited 0 with a clean worktree, HEAD == the branch tip and the same present
# hook bundle before and after the run. It is reused (pre_land_reuse_check)
# only when ALL of these hold:
#   1. the marker is current: right version, younger than the max age
#      (INTEGRATE_BRANCH_PRE_LAND_REUSE_MAX_AGE seconds, default 21600), and
#      recorded with the same hook bundle (its resolved real path);
#   2. the marker's base is an ancestor of the branch's new merge-base with the
#      primary branch (the branch moved forward, never rewound);
#   3. the branch's post-image is byte-identical: a hash over the
#      (new mode, new blob oid, path) tuples of `git diff --raw` base..FB. Blob
#      oids are rebase-invariant for files main did not touch, and unlike
#      `git patch-id` they do not ignore whitespace or hunk positions;
#   4. every branch-diff path is inside a Go module (nearest ancestor directory
#      holding go.mod, never the repo root), the tree has no go.work, and every
#      path main gained since the recorded base (git diff --name-only) lies in
#      a project directory (go.mod, package.json or pyproject.toml) that is NOT
#      in the diff's dependency component and is not a symlink target path. The
#      component starts at the diff's modules and follows, to a fixpoint and in
#      either direction, (a) every local `replace => ./x` / `=> ../x` of a
#      go.mod, and (b) every Go/script/data file of a module that names a
#      sibling project near a `..` path token (a test that builds or reads
#      `../pg-desk`, `filepath.Join("..", "pg-desk")`, ...). A go.mod `=>`
#      target that is neither local nor a versioned module fails closed. A path
#      main gained that is in no project (flake.nix, docs, scripts, a root file)
#      is shared, so it re-runs.
# bash 3.2 compatible: no associative arrays, no mapfile.
# ---------------------------------------------------------------------------
PRE_LAND_MARKER_VERSION=2

# PRE_LAND_NO_REUSE: the one-shot --no-reuse flag sets it (integrate-branch-support.sh).
PRE_LAND_NO_REUSE=""

pre_land_reuse_enabled() {
  local v
  [ -z "$PRE_LAND_NO_REUSE" ] || return 1
  # Inside a nix build the unit-test hook self-skips with exit 0, so a "pass"
  # there proves nothing: neither record nor reuse one.
  [ -z "${IN_NIX_BUILD:-}" ] && [ -z "${NIX_BUILD_TOP:-}" ] || return 1
  v="$(git config --type=bool --get pgii-integrate-branch.preLandReuse 2>/dev/null)" || return 1
  [ "$v" = true ]
}

# pre_land_marker_path <wt> <branch>
pre_land_marker_path() {
  local common
  common="$(git -C "$1" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" || return 1
  printf '%s/integrate-branch/pre-land-pass/%s' "$common" "${2//\//%}"
}

# pre_land_marker_get <file> <key>
pre_land_marker_get() {
  local line
  while IFS= read -r line; do
    case "$line" in
    "$2="*)
      printf '%s' "${line#"$2="}"
      return 0
      ;;
    esac
  done <"$1"
  return 0
}

# pre_land_diff_id <wt> <base> <fb>: hash of the (new mode, new blob oid, path)
# tuples of the branch diff; nothing for an empty diff.
pre_land_diff_id() {
  local tuples
  tuples="$(git -C "$1" -c core.quotepath=true diff --raw -z --no-renames --no-ext-diff --abbrev=40 "$2" "$3" 2>/dev/null |
    tr '\0' '\n' |
    awk 'NR % 2 == 1 { split($0, a, " "); mode = a[2]; oid = a[4]; next } { print mode, oid, $0 }')" || return 0
  [ -n "$tuples" ] || return 0
  printf '%s\n' "$tuples" | git hash-object --stdin 2>/dev/null || true
}

# pre_land_realpath <dir>: the symlink-free absolute path of a directory, or the input.
pre_land_realpath() {
  (cd -P "$1" 2>/dev/null && pwd -P) || printf '%s' "$1"
}

# pre_land_bundle_id <wt>: the resolved real path of a PRESENT hook bundle, else nothing.
pre_land_bundle_id() {
  local out line state="" bundle=""
  out="$(cd "$1" && pg-hooks status --porcelain 2>/dev/null)" || true
  while IFS= read -r line; do
    case "$line" in
    state=*) state="${line#state=}" ;;
    bundle=*) bundle="${line#bundle=}" ;;
    esac
  done <<<"$out"
  if [ "$state" = present ] && [ -n "$bundle" ]; then
    pre_land_realpath "$bundle"
  fi
  return 0
}

# pre_land_snapshot <wt> <branch>: `<HEAD sha>|<bundle id>|<clean yes/no>`, taken
# before and after the hook run; a record is made only when both match.
pre_land_snapshot() {
  local clean=no
  [ -z "$(git -C "$1" status --porcelain 2>/dev/null)" ] && clean=yes
  printf '%s|%s|%s' "$(git -C "$1" rev-parse HEAD 2>/dev/null)" "$(pre_land_bundle_id "$1")" "$clean"
}

# pre_land_record_pass <wt> <branch> <snapshot taken before the run>: remember a
# passing run. Silent and best-effort: failing to record only costs a re-run.
pre_land_record_pass() {
  local wt="$1" fb="$2" before="$3" after primary base did bundle file tmp
  pre_land_reuse_enabled || return 0
  after="$(pre_land_snapshot "$wt" "$fb")"
  [ "$before" = "$after" ] || return 0
  case "$after" in *"|yes") ;; *) return 0 ;; esac
  [ "${after%%|*}" = "$(git -C "$wt" rev-parse "$fb" 2>/dev/null)" ] || return 0
  bundle="${after#*|}"
  bundle="${bundle%|*}"
  [ -n "$bundle" ] || return 0
  primary="$(cd "$wt" && resolve_primary_branch)"
  base="$(git -C "$wt" merge-base "$primary" "$fb" 2>/dev/null)" || return 0
  did="$(pre_land_diff_id "$wt" "$base" "$fb")"
  [ -n "$did" ] || return 0
  file="$(pre_land_marker_path "$wt" "$fb")" || return 0
  mkdir -p "$(dirname "$file")" 2>/dev/null || return 0
  tmp="$file.$$"
  {
    printf 'version=%s\n' "$PRE_LAND_MARKER_VERSION"
    printf 'diff_id=%s\n' "$did"
    printf 'base=%s\n' "$base"
    printf 'bundle=%s\n' "$bundle"
    printf 'epoch=%s\n' "$(date +%s)"
  } >"$tmp" 2>/dev/null && mv -f "$tmp" "$file" 2>/dev/null || rm -f "$tmp"
  return 0
}

# pre_land_normalize <dir> <relative path>: join and collapse `.`/`..`; prints
# nothing when the result leaves the repo root.
pre_land_normalize() {
  local joined="$1/$2" part out="" IFS=/
  local -a parts stack
  read -r -a parts <<<"$joined"
  stack=()
  for part in "${parts[@]}"; do
    case "$part" in
    "" | .) ;;
    ..)
      [ "${#stack[@]}" -gt 0 ] || return 0
      unset 'stack[${#stack[@]}-1]'
      stack=("${stack[@]+"${stack[@]}"}")
      ;;
    *) stack+=("$part") ;;
    esac
  done
  out="${stack[*]+"${stack[*]}"}"
  printf '%s' "$out"
}

# pre_land_project_root <marker dirs file> <path>: the nearest ancestor
# directory of <path> listed in the file (never the repo root), or nothing.
pre_land_project_root() {
  local d="${2%/*}"
  [ "$d" != "$2" ] || return 0
  while [ -n "$d" ]; do
    if grep -qxF -- "$d" "$1"; then
      printf '%s' "$d"
      return 0
    fi
    [ "$d" != "${d%/*}" ] || break
    d="${d%/*}"
  done
  return 0
}

# pre_land_replace_targets <wt> <rev> <module dir>: one line per `=>` of the
# module's go.mod, `L <relative path>` for a local target (quotes stripped),
# `V` for a versioned module, `U` for anything else (which fails closed).
pre_land_replace_targets() {
  local line tok rest
  git -C "$1" show "$2:$3/go.mod" 2>/dev/null |
    grep -v '^[[:space:]]*//' |
    sed -n 's/^[^=]*=>[[:space:]]*//p' |
    while IFS= read -r line; do
      tok="${line%%[[:space:]]*}"
      rest="${line#"$tok"}"
      tok="${tok//[\"\`]/}"
      case "$tok" in
      . | .. | ./* | ../*) printf 'L %s\n' "$tok" ;;
      *)
        if [[ $rest =~ ^[[:space:]]+v[0-9] ]]; then
          printf 'V\n'
        else
          printf 'U\n'
        fi
        ;;
      esac
    done
}

# pre_land_scan_edges <wt> <rev> <module dir> <markers file> <out file>: add an
# edge `<module> <project>` for every project whose directory name appears within
# three lines of a `..` path token in the module's code, scripts or data files.
pre_land_scan_edges() {
  local wt="$1" rev="$2" g="$3" text s b ext
  local -a specs
  specs=()
  for ext in go sh bash bats py js ts nix toml yaml yml json mod; do
    specs+=(":(glob)$g/**/*.$ext")
  done
  specs+=(":(glob)$g/**/Makefile")
  text="$(git -C "$wt" grep -hI -C 3 -E '(^|[^.])\.\.(/|["'"'"'`,)]|$)' "$rev" -- "${specs[@]}" 2>/dev/null)" || true
  [ -n "$text" ] || return 0
  while IFS= read -r s; do
    [ -n "$s" ] && [ "$s" != "$g" ] || continue
    b="${s##*/}"
    if printf '%s\n' "$text" | grep -qE "(^|[^[:alnum:]_.-])$b([^[:alnum:]_-]|\$)"; then
      printf '%s\t%s\n' "$g" "$s" >>"$5"
    fi
  done <"$4"
}

# pre_land_reuse_check <wt> <branch>: decide whether a recorded pass carries
# over. Prints one `FF-1b: ...` line saying why. Returns 0 to reuse, 1 to re-run.
pre_land_reuse_check() {
  local wt="$1" fb="$2" file primary newbase oldbase did_old did_new bundle_old bundle_new
  local epoch now max_age tmp markers gomods edges droots comp unrec p r g rel target changed n_count kind
  pre_land_reuse_enabled || return 1
  file="$(pre_land_marker_path "$wt" "$fb")" || return 1
  [ -f "$file" ] || return 1
  if [ "$(pre_land_marker_get "$file" version)" != "$PRE_LAND_MARKER_VERSION" ]; then
    echo "FF-1b: re-running hooks (recorded pass has an unknown format)"
    return 1
  fi
  epoch="$(pre_land_marker_get "$file" epoch)"
  now="$(date +%s)"
  max_age="${INTEGRATE_BRANCH_PRE_LAND_REUSE_MAX_AGE:-21600}"
  case "$epoch$max_age" in *[!0-9]* | "")
    echo "FF-1b: re-running hooks (recorded pass has no usable age)"
    return 1
    ;;
  esac
  if [ $((now - epoch)) -gt "$max_age" ]; then
    echo "FF-1b: re-running hooks (recorded pass is older than ${max_age}s)"
    return 1
  fi
  primary="$(cd "$wt" && resolve_primary_branch)"
  newbase="$(git -C "$wt" merge-base "$primary" "$fb" 2>/dev/null)" || return 1
  oldbase="$(pre_land_marker_get "$file" base)"
  if ! git -C "$wt" merge-base --is-ancestor "$oldbase" "$newbase" 2>/dev/null; then
    echo "FF-1b: re-running hooks (recorded base ${oldbase:0:9} is not an ancestor of the new base ${newbase:0:9})"
    return 1
  fi
  did_old="$(pre_land_marker_get "$file" diff_id)"
  did_new="$(pre_land_diff_id "$wt" "$newbase" "$fb")"
  if [ -z "$did_new" ] || [ "$did_old" != "$did_new" ]; then
    echo "FF-1b: re-running hooks (the branch's changed files differ from the recorded pass)"
    return 1
  fi
  bundle_old="$(pre_land_marker_get "$file" bundle)"
  bundle_new="$(pre_land_bundle_id "$wt")"
  if [ -z "$bundle_new" ] || [ "$bundle_old" != "$bundle_new" ]; then
    echo "FF-1b: re-running hooks (the hook bundle changed since the recorded pass)"
    return 1
  fi
  changed="$(git -C "$wt" -c core.quotepath=true diff --no-ext-diff --no-renames --name-only "$oldbase" "$newbase" 2>/dev/null)" || return 1
  if [ -z "$changed" ]; then
    echo "FF-1b: reused the pre-land pass recorded on base ${oldbase:0:9}; main gained no files since (diff ${did_new:0:12}); pg-hooks not re-run"
    return 0
  fi

  tmp="$(mktemp -d 2>/dev/null)" || return 1
  markers="$tmp/markers"
  gomods="$tmp/gomods"
  edges="$tmp/edges"
  droots="$tmp/droots"
  comp="$tmp/comp"
  unrec="$tmp/unrec"
  # Project directories of the tree being landed (repo root never counts).
  git -C "$wt" ls-tree -r --name-only "$fb" 2>/dev/null >"$tmp/tree" || {
    rm -rf "$tmp"
    return 1
  }
  if grep -qE '(^|/)go\.work(\.sum)?$' "$tmp/tree"; then
    rm -rf "$tmp"
    echo "FF-1b: re-running hooks (the repo has a go.work, so modules are coupled)"
    return 1
  fi
  grep -E '/(go\.mod|package\.json|pyproject\.toml)$' "$tmp/tree" | sed 's|/[^/]*$||' | sort -u >"$markers" || true
  grep -E '/go\.mod$' "$tmp/tree" | sed 's|/go\.mod$||' | sort -u >"$gomods" || true
  : >"$edges"
  : >"$droots"
  : >"$unrec"
  # Every branch-diff path must sit in a Go module.
  while IFS= read -r p; do
    [ -n "$p" ] || continue
    r="$(pre_land_project_root "$markers" "$p")"
    if [ -z "$r" ] || ! grep -qxF -- "$r" "$gomods"; then
      rm -rf "$tmp"
      echo "FF-1b: re-running hooks (branch path $p is not inside a Go module, so shared checks may read it)"
      return 1
    fi
    printf '%s\n' "$r" >>"$droots"
  done < <(git -C "$wt" -c core.quotepath=true diff --no-ext-diff --no-renames --name-only "$newbase" "$fb" 2>/dev/null)
  sort -u "$droots" -o "$droots"
  [ -s "$droots" ] || {
    rm -rf "$tmp"
    return 1
  }
  # Edges between projects, read from the tree being landed: go.mod local
  # replaces, and sibling projects named near a `..` path token.
  while IFS= read -r g; do
    [ -n "$g" ] || continue
    while IFS=' ' read -r kind rel; do
      case "$kind" in
      L)
        target="$(pre_land_normalize "$g" "$rel")"
        [ -n "$target" ] && printf '%s\t%s\n' "$g" "$target" >>"$edges"
        ;;
      U) printf '%s\n' "$g" >>"$unrec" ;;
      esac
    done < <(pre_land_replace_targets "$wt" "$fb" "$g")
    pre_land_scan_edges "$wt" "$fb" "$g" "$markers" "$edges"
  done <"$gomods"
  # Fixpoint: connected component of the diff's modules, edges both ways.
  cp "$droots" "$comp"
  n_count=-1
  while [ "$(wc -l <"$comp")" -ne "$n_count" ]; do
    n_count="$(wc -l <"$comp")"
    while IFS="$(printf '\t')" read -r g target; do
      if grep -qxF -- "$g" "$comp"; then
        printf '%s\n' "$target" >>"$comp"
      fi
      if grep -qxF -- "$target" "$comp"; then
        printf '%s\n' "$g" >>"$comp"
      fi
    done <"$edges"
    sort -u "$comp" -o "$comp"
  done
  # A go.mod `=>` target nobody recognises, or a symlink, inside the component.
  while IFS= read -r g; do
    if grep -qxF -- "$g" "$unrec"; then
      rm -rf "$tmp"
      echo "FF-1b: re-running hooks ($g/go.mod has a replace target that is neither local nor a versioned module)"
      return 1
    fi
    if git -C "$wt" ls-tree -r "$fb" -- "$g" 2>/dev/null | grep -q '^120000 '; then
      rm -rf "$tmp"
      echo "FF-1b: re-running hooks ($g holds a symlink, which may point outside it)"
      return 1
    fi
  done <"$comp"
  # Every path main gained must be in a project outside the component.
  while IFS= read -r p; do
    [ -n "$p" ] || continue
    r="$(pre_land_project_root "$markers" "$p")"
    if [ -z "$r" ]; then
      rm -rf "$tmp"
      echo "FF-1b: re-running hooks (main gained $p, a shared path outside every project)"
      return 1
    fi
    while IFS= read -r g; do
      case "$p" in
      "$g" | "$g"/*)
        rm -rf "$tmp"
        echo "FF-1b: re-running hooks (main gained $p, inside $g which the branch diff depends on)"
        return 1
        ;;
      esac
    done <"$comp"
  done <<<"$changed"
  n_count="$(printf '%s\n' "$changed" | wc -l | tr -d ' ')"
  printf 'FF-1b: reused the pre-land pass recorded on base %s (now %s): the branch diff %s is unchanged and the %s files main gained lie outside the diff modules (%s) and the projects they replace, are replaced by or name near a path token; pg-hooks not re-run\n' \
    "${oldbase:0:9}" "${newbase:0:9}" "${did_new:0:12}" "$n_count" "$(paste -sd, "$droots")"
  rm -rf "$tmp"
  return 0
}

# pre_land_check <worktree root> <branch>: FF-1b. Run `pg-hooks run pre-land
# <branch>` from the worktree root and map its exit status (spec 5.3):
#   0   -> 0
#   10  -> 10 (a hook failed; the caller reports
#              stopped:precommit-branch-diff-failed)
#   13  -> 0  (no hook bundle: pg-hooks has already
#              printed its one `pg-hooks: no hook bundle ...` notice on stderr,
#              which the land outcome relays verbatim; the land continues)
#   127 -> 0  (pg-hooks missing: print PG_HOOKS_MISSING_NOTICE on stdout; the
#              land continues)
#   any other status (2 usage, 12 broken bundle, 11 skipped, 1 ...) is
#   returned unchanged so the caller halts rather than land unchecked.
# Never links, copies or regenerates a hook config.
pre_land_check() {
  local wt="$1" fb="$2" rc=0 before=""
  if ! command -v pg-hooks >/dev/null 2>&1; then
    printf '%s\n' "$PG_HOOKS_MISSING_NOTICE"
    return 0
  fi
  if pre_land_reuse_check "$wt" "$fb"; then
    return 0
  fi
  if pre_land_reuse_enabled; then
    before="$(pre_land_snapshot "$wt" "$fb")"
  fi
  (cd "$wt" && pg-hooks run pre-land "$fb") || rc=$?
  case "$rc" in
  0)
    pre_land_record_pass "$wt" "$fb" "$before"
    return 0
    ;;
  13) return 0 ;;
  127)
    printf '%s\n' "$PG_HOOKS_MISSING_NOTICE"
    return 0
    ;;
  *) return "$rc" ;;
  esac
}

# NIX_FALLBACK_DIRS_DEFAULT: where a system-installed nix lives when the
# caller's PATH does not carry it (a pg-router pool worker's PATH is only the
# ccpool tool store paths plus /etc/profiles/per-user/*/bin and /usr/bin, so a
# bare `nix` is not found even though /run/current-system/sw/bin/nix exists;
# bead pg2-hrw24). Colon-separated, tried in order.
NIX_FALLBACK_DIRS_DEFAULT="/run/current-system/sw/bin:/nix/var/nix/profiles/default/bin:/etc/profiles/per-user/${USER:-nobody}/bin"

# reinstall_needs_nix <command>: succeed when the command text invokes a bare
# `nix` (one that needs PATH lookup) as a command word: at the start or after
# whitespace or a shell separator, and followed by whitespace or the end. A path
# component (/nix/store/..., /nix/var/nix/builds/...), a dashed name (a repo
# directory like phillipgreenii-nix-x) and an absolute /path/to/nix do not count.
reinstall_needs_nix() {
  [[ $1 =~ (^|[[:space:];\&\|\(\`])nix([[:space:]]|$) ]]
}

# find_nix_dir: print the directory holding an executable `nix` from the
# fallback list (INTEGRATE_BRANCH_NIX_FALLBACK_DIRS overrides the default; the
# override exists for tests), or nothing when none has one. Only consulted when
# `command -v nix` already failed.
find_nix_dir() {
  local dirs="${INTEGRATE_BRANCH_NIX_FALLBACK_DIRS-$NIX_FALLBACK_DIRS_DEFAULT}" d
  local -a parts
  IFS=: read -r -a parts <<<"$dirs"
  for d in "${parts[@]}"; do
    if [ -n "$d" ] && [ -x "$d/nix" ]; then
      printf '%s' "$d"
      return 0
    fi
  done
  return 0
}

# bundle_refresh <old primary sha>: FF-4 (spec 4.5, D5). Run from the canonical
# clone AFTER a successful ff-merge. When the landed diff (<old>..HEAD)
# touches a stamp input -- flake.lock, flake.nix, or a stampPaths entry of the
# current bundle's meta.json -- start the canonical clone's recorded
# `reinstall` command in the background through bgrun. Prints exactly one
# `FF-4: bundle refresh ...` line and ALWAYS returns 0: a refresh that is
# skipped, cannot start, or later fails is reported, never a failed land.
bundle_refresh() {
  local old="$1" common pgdir top gen="" cmd="" repo job out rc=0 nixdir=""
  local -a inputs=(flake.lock flake.nix)
  local p touched

  common="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" || common=""
  top="$(git rev-parse --show-toplevel 2>/dev/null)" || top=""
  if [ -z "$common" ] || [ -z "$top" ]; then
    echo "FF-4: bundle refresh skipped (not inside a git working tree)"
    return 0
  fi
  pgdir="$common/pg-hooks"
  if [ ! -f "$pgdir/reinstall" ]; then
    echo "FF-4: bundle refresh skipped (no $pgdir/reinstall: this clone has no hook bundle)"
    return 0
  fi
  if [ -f "$pgdir/current" ] && [ ! -L "$pgdir/current" ]; then
    IFS= read -r gen <"$pgdir/current" || true
  fi
  if [[ $gen =~ ^gen-[0-9]+$ ]] && [ -f "$pgdir/$gen/bundle/meta.json" ]; then
    while IFS= read -r p; do
      [ -n "$p" ] && inputs+=("$p")
    done < <(jq -r '(.stampPaths // [])[]' "$pgdir/$gen/bundle/meta.json" 2>/dev/null || true)
  fi
  if ! touched="$(git -C "$top" diff --no-ext-diff --name-only "$old" HEAD -- "${inputs[@]}" 2>/dev/null)"; then
    echo "FF-4: bundle refresh skipped (cannot diff $old..HEAD)"
    return 0
  fi
  if [ -z "$touched" ]; then
    echo "FF-4: bundle refresh not needed (the landed diff touches no stamp input)"
    return 0
  fi
  IFS= read -r cmd <"$pgdir/reinstall" || true
  if [ -z "$cmd" ]; then
    echo "FF-4: bundle refresh skipped ($pgdir/reinstall is empty)"
    return 0
  fi
  # The recorded reinstall command is `nix run ...`. A caller whose PATH lacks
  # nix (a pg-router pool worker) would otherwise get a background job that
  # dies with exit 127 in the bgrun log while this line claims "started": resolve
  # nix here and say so loudly, in this command's own output, when it is absent.
  if reinstall_needs_nix "$cmd" && ! command -v nix >/dev/null 2>&1; then
    nixdir="$(find_nix_dir)"
    if [ -z "$nixdir" ]; then
      echo "FF-4: bundle refresh NOT started (nix is not on PATH and was not found in ${INTEGRATE_BRANCH_NIX_FALLBACK_DIRS-$NIX_FALLBACK_DIRS_DEFAULT}); run in the background with nix on PATH: $cmd"
      return 0
    fi
    printf -v cmd 'export PATH=%q:"$PATH"; %s' "$nixdir" "$cmd"
  fi
  if ! command -v bgrun >/dev/null 2>&1; then
    echo "FF-4: bundle refresh NOT started (bgrun is not on PATH); run in the background: $cmd"
    return 0
  fi
  repo="$(basename "$top")"
  job="pg-hooks-refresh-${repo//[^A-Za-z0-9._-]/-}"
  out="$(bgrun "$job" -- bash -c "$cmd" 2>&1)" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FF-4: bundle refresh NOT started (bgrun $job exited $rc: $out); run in the background: $cmd"
    return 0
  fi
  echo "FF-4: bundle refresh started in the background ($out); check with: bgcheck $job"
  return 0
}

resolve_strategy() {
  local declared="$1" remote="$2" remote_reason="$3" open_pr_json="$4" mr_bead="$5"

  if [ -n "$declared" ]; then
    if [ "$declared" = "pull-request" ] && [ -z "$remote" ]; then
      printf '%s\ndeclared strategy is '"'"'pull-request'"'"' but no remote could be resolved -- pull request is infeasible; the agent decides how to proceed\n' "$declared"
      return
    fi
    printf '%s\ndeclared strategy: %s (pgii-integrate-branch.strategy)\n' "$declared" "$declared"
    return
  fi

  if [ -n "$remote_reason" ]; then
    printf '\n%s\n' "$remote_reason"
    return
  fi

  if [ -z "$remote" ]; then
    printf 'ff-merge-to-main\nno remote configured -- a pull request is impossible\n'
    return
  fi

  if [ -n "$open_pr_json" ] && [ "$open_pr_json" != "null" ]; then
    printf 'pull-request\nan open pull request was found for the current branch\n'
    return
  fi

  if [ -n "$mr_bead" ]; then
    printf 'pull-request\nan open merge-request bead for this repo and branch was found: %s\n' "$mr_bead"
    return
  fi

  printf '\nremote present, no open PR or scoped merge-request bead, and no strategy declared -- cannot infer a strategy\n'
}
