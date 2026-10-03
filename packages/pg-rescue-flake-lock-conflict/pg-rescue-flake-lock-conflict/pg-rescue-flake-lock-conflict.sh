# shellcheck shell=bash
# pg-rescue-flake-lock-conflict - deterministic pg-rescue failure handler
# (bead pg2-3ybxg; design: docs/superpowers/specs/2026-10-02-pg-rescue-design.md
# section 8.5). Resolves a rebase that stopped ONLY on a flake.lock conflict,
# with no model involved: take upstream's lock, recompute the conflicted
# inputs, stage, continue.

show_help() {
  cat <<'HELP'
pg-rescue-flake-lock-conflict: pg-rescue handler for a rebase stopped on flake.lock

Usage: pg-rescue-flake-lock-conflict [OPTIONS]

Run by pg-rescue as a failure handler, in the failed command's working
directory. It takes no arguments of its own.

If the repository is mid-rebase and flake.lock is the ONLY conflicted file, it:
  1. extracts the names of the conflicted inputs from the conflict markers;
  2. takes upstream's flake.lock (`git checkout --ours`: during a rebase,
     "ours" is the branch being rebased onto);
  3. runs `nix flake update <conflicted inputs>` (a bare `nix flake update`
     only when no names could be extracted; never `nix flake lock`, which
     leaves an already-pinned input stale);
  4. runs `git add flake.lock` and `GIT_EDITOR=true git rebase --continue`.

Outcome (reported with `pg-rescue result`):
  resolved  the rebase ran to completion ("relocked flake.lock")
  declined  not a rebase, flake.lock is not the only conflict, or the rebase
            stopped again on a later commit
  failed    (exit 1) nix or git itself failed; the working copy is left as-is

Options:
  -h, --help     Show this help message
  -v, --version  Show version information
HELP
}

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    show_help
    exit 0
    ;;
  *)
    # Exit 1, not 2: 2 would be read by the wrapper as `declined`.
    echo "pg-rescue-flake-lock-conflict: unexpected argument: $1" >&2
    exit 1
    ;;
  esac
done

# Handler-author idiom (design 8.5): stdout is reserved for the result JSON,
# so everything else (git's and nix's chatter included) goes to stderr.
exec 3>&1 1>&2

details_file=""
cleanup() { [[ -z $details_file ]] || rm -f "$details_file"; }
trap cleanup EXIT

# finish OUTCOME SUMMARY [DETAILS]: print the result and exit with its code.
# MUST be the last thing a path does: `pg-rescue result` exits.
finish() {
  local outcome="$1" summary="$2" details="${3:-}"
  if [[ -n $details ]]; then
    details_file="$(mktemp)"
    printf '%s\n' "$details" >"$details_file"
    pg-rescue result "$outcome" "$summary" --details-file "$details_file" >&3
  else
    pg-rescue result "$outcome" "$summary" >&3
  fi
}

die() {
  echo "pg-rescue-flake-lock-conflict: $1" >&2
  exit 1
}

# True while a rebase is stopped (either rebase backend).
rebase_in_progress() {
  local d
  for d in rebase-merge rebase-apply; do
    if [[ -d "$(git rev-parse --git-path "$d")" ]]; then
      return 0
    fi
  done
  return 1
}

top="$(git rev-parse --show-toplevel 2>/dev/null)" || {
  finish declined "Not inside a git work tree"
}
cd "$top" || die "cannot enter $top"

if ! rebase_in_progress; then
  finish declined "No rebase is in progress"
fi

# `--name-only` is unaffected by the external diff driver these repos
# configure. One path per line, relative to the toplevel.
conflicted="$(git diff --name-only --diff-filter=U)"
if [[ $conflicted != "flake.lock" ]]; then
  if [[ -z $conflicted ]]; then
    finish declined "No conflicted files" "The rebase is stopped but nothing is conflicted."
  fi
  finish declined "Conflict spans source files, not just flake.lock" "Conflicted files:
$conflicted"
fi

# The names MUST be extracted BEFORE the checkout below, which removes the
# conflict markers they are read from. Same extraction as
# integrate-branch:ff-merge-to-main: top-level `nodes` entries are indented
# exactly 4 spaces in flake.lock's pretty-printed JSON. `|| true`: grep exits 1
# when nothing matches, which pipefail would turn into a fatal error.
inputs_text="$(awk '/^<<<<<<<|^>>>>>>>/{c=!c; next} c' flake.lock |
  grep -E '^    "[^"]+": \{' | sed -E 's/^    "([^"]+)": \{.*/\1/' | sort -u | tr '\n' ' ' || true)"
read -r -a inputs <<<"$inputs_text"

# Upstream's flake.lock. During a rebase upstream is --ours (--theirs is the
# commit being replayed). Either side works: the relock below recomputes the
# conflicted pins from flake.nix rather than trusting the picked content; the
# checkout only hands `rebase --continue` a resolved, marker-free file.
git checkout --ours -- flake.lock

# NEVER `nix flake lock`: it only fills MISSING entries and leaves an
# already-pinned input at its stale revision. A targeted `nix flake update`
# refreshes only the conflicted inputs; the bare form refreshes EVERY input
# (unrelated third-party ones included) so it is the fallback ONLY when no
# names were extracted.
if [[ ${#inputs[@]} -gt 0 ]]; then
  echo "pg-rescue-flake-lock-conflict: nix flake update ${inputs[*]}"
  nix flake update "${inputs[@]}" || die "nix flake update ${inputs[*]} failed"
  relocked="${inputs[*]}"
else
  echo "pg-rescue-flake-lock-conflict: no input names found in the conflict markers; nix flake update"
  nix flake update || die "nix flake update failed"
  relocked="all inputs"
fi

git add flake.lock

# The relock happens BEFORE `rebase --continue` and needs no commit of its
# own. (ff-merge-to-main relocks after continuing and commits separately; this
# handler differs on purpose: the verify step re-runs `git pull --rebase`,
# which needs a clean tree.)
if GIT_EDITOR=true git rebase --continue; then
  continue_failed=false
else
  continue_failed=true
fi

if rebase_in_progress; then
  stopped_at="$(git rev-parse --short REBASE_HEAD 2>/dev/null || true)"
  subject=""
  if [[ -n $stopped_at ]]; then
    subject="$(git log -1 --format=%s "$stopped_at" 2>/dev/null || true)"
  fi
  conflicts="$(git diff --name-only --diff-filter=U || true)"
  finish declined "Rebase stopped again at ${stopped_at:-an unknown commit}${subject:+ ($subject)}" \
    "Relocked: $relocked
Conflicted files:
${conflicts:-none}"
fi

# Not stopped, but git refused to continue (for example a failing commit
# hook): that is a failure, not a resolution.
if [[ $continue_failed == true ]]; then
  die "git rebase --continue failed and no rebase is in progress"
fi

finish resolved "relocked flake.lock" "Took upstream's flake.lock (--ours) and ran nix flake update for: $relocked"
