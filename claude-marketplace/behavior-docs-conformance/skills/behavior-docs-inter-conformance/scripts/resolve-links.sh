#!/usr/bin/env bash
# resolve-links.sh — the DEREFERENCING half of D5's `[<uuid>](<remote-url>)`
# imports-table links (bead pg2-2oupw, deferred from WS-6/pg2-wr6lm.4).
# `resolve-imports.sh` PARSES the link and resolves identity against an OWNER
# set passed on its command line; it deliberately never follows the url (see
# its header). This script is the follow-through: for every D5 link cell in an
# implementer's `## External references` table, confirm the url's target still
# carries that UUID somewhere in its content.
#
# RULINGS THIS SCRIPT IMPLEMENTS (operator, 2026-08-14, pg2-ijtui, recorded on
# pg2-2oupw per S-1):
#
#   Q1 — the URL form (absolute GitHub blob/main) is KEPT exactly as authored.
#        This script never rewrites a link; it only reads through it.
#   Q2 — "local resolution is sufficient": resolve LOCALLY first (no network);
#        only on a local miss, attempt a REMOTE fetch; report a mismatch as
#        WARN — NEVER FAIL/error — because D5 makes the UUID authoritative and
#        the URL explicitly rot-tolerant (an unresolved URL is, by design, not
#        a correctness failure).
#
# THREE OUTCOMES per D5 link cell — never a fourth, and never an exit-1 FAIL:
#
#   ok (local)   — the url's target repo has a LOCAL checkout reachable from
#                  this invocation (itself, or a sibling under the discovered
#                  workspace root) and that checkout's file at the url's path
#                  contains the UUID somewhere in its content.
#   ok (remote)  — no usable local checkout was found (or its content did not
#                  carry the UUID), but a remote fetch of the url's raw content
#                  DID contain the UUID. Not a warning: Q2 only reserves WARN
#                  for "neither".
#   WARN         — resolves neither locally nor remotely. The url may simply be
#                  unpublished-yet (local main ahead of origin) or genuinely
#                  rotted; either way this is advisory, per Q2.
#
# A row whose owner-UUID cell is NOT a D5 link (the bare `<uuid>` shape, an
# `(external)` marker, or unparseable) has nothing to dereference and is
# SKIPPED here — `resolve-imports.sh` already reports format problems with
# such rows; this script's whole job starts only once a clean uuid+url pair
# exists (pg2-0pjvu built that parser first, precisely so this script could
# assume it).
#
# CHECKING IS UUID-PRESENCE-IN-CONTENT, NEVER A LINE ANCHOR. A UUID is minted
# once, on its carrier's OWN definition line, so "does this file's raw text
# contain this UUID anywhere" is exactly the identity check D5 wants — a line
# number would drift the moment surrounding content moves, which is precisely
# what a UUID exists to be immune to (INV-3).
#
# ============================================================================
# WHY THIS IS A SEPARATE SCRIPT, NOT A NEW PASS INSIDE resolve-imports.sh, AND
# WHY IT IS NOT WIRED INTO tests/behavior-docs-real-corpus.sh / its baseline
# ============================================================================
#
# tests/behavior-docs-real-corpus.sh is "ONE RUNNER, TWO CALLERS" (see its own
# header): `checks.test-behavior-docs-real-corpus` runs it against a COPY of
# this repo's own flake source inside the network-sandboxed nix build (no
# sibling repos, no network, ever — not even for an FOD, which would only fix
# the network half); the `behavior-docs-real-corpus` pre-commit hook runs the
# SAME script against the real working tree (siblings present, network
# reachable). Both callers are compared against ONE SHARED baseline file.
#
# A cross-repo D5 link (3 of the 9 today) genuinely CANNOT resolve locally
# under the sandboxed caller — the target repo's checkout simply is not
# there — so folding this check into that shared pipeline would force the
# baseline to record "WARN" for those rows, and that recorded baseline would
# then immediately conflict with the SAME script's other caller, where the
# sibling repo *is* on disk and the very same rows resolve with zero
# warnings. One ratchet cannot hold two structurally different, both-correct
# answers for the same finding. That is a property of WHERE the two callers
# run, not a bug either caller could fix, so the sound fix is to keep this
# check out of that shared ratchet entirely.
#
# MECHANISM CHOSEN: an OUT-OF-FLAKE RUNNER, not a fixed-output derivation.
#   - An FOD would only solve the NETWORK half of the sandbox gap (a FOD may
#     fetch, pinned to a fixed output hash). It does nothing for the SIBLING
#     REPO half — a second, genuinely different repo's checkout is not
#     something a single-derivation FOD can be handed without vendoring that
#     repo into this flake's source, which is not wanted for a rot-tolerant,
#     by-design-optional check.
#   - So this script runs OUTSIDE the nix sandbox entirely: wired as its own
#     pre-push, `language = "system"` pre-commit hook (see `flake.nix`,
#     `behavior-docs-links`), exactly like the existing `behavior-docs-real-corpus`
#     and `<module>-push-golangci` hooks — real filesystem, real (optional)
#     network, real workspace siblings. `nix flake check` never invokes it, so
#     it structurally cannot make that gate require network access; today, run
#     for real against this workspace, it needs no network either (all nine
#     links resolve locally — see the Gates section of the bead).
#
# Usage: resolve-links.sh [--timeout SECONDS] <implementer-set-dir>
# Env:   RESOLVE_LINKS_TIMEOUT  per-fetch network timeout in seconds (default 10);
#                               same as passing --timeout.
# Exit:  0 always, except a usage/environment error (2). A WARN finding is
#        NEVER a nonzero exit — see the Q2 ruling above.
set -euo pipefail
shopt -s nullglob
export LC_ALL=C

# shellcheck disable=SC2034  # consumed by cell_uuid in the sourced lib/imports-row.bash
UUIDRE='[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}'
# The shared GFM imports-row/cell parser (trim/row_cell/cell_uuid/cell_url) —
# ONE definition with resolve-imports.sh, in `lib/imports-row.bash`. `cell_uuid`
# reads `$UUIDRE`, just set above.
# shellcheck source=../../../lib/imports-row.bash
. "$(dirname "${BASH_SOURCE[0]}")/../../../lib/imports-row.bash"

# sort_locs — canonical order for a multi-location finding list: by path, then
# by line NUMERICALLY. Same convention as resolve-imports.sh/trace-extract.sh/
# impl-traces.sh/reconcile-imports.sh (their header comments explain why: sorting
# the whole `path:line` string lexically puts `a.md:10` ahead of `a.md:9`, which
# is a second, quieter way for one finding to serialize two different ways).
# Used here to key-sort THIS run's own output lines so they are byte-identical
# regardless of glob or filesystem enumeration order.
sort_locs() { sort -t: -k1,1 -k2,2n -u; }

TIMEOUT="${RESOLVE_LINKS_TIMEOUT:-10}"
IMPL=""
while [ $# -gt 0 ]; do
  case "$1" in
  -h | --help)
    sed -n '2,60p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
    exit 0
    ;;
  --timeout)
    TIMEOUT="${2:?--timeout needs a value}"
    shift
    ;;
  --)
    shift
    break
    ;;
  -*)
    echo "unknown option: $1" >&2
    exit 2
    ;;
  *) IMPL="$1" ;;
  esac
  shift
done
[ $# -eq 0 ] || IMPL="${IMPL:-$1}"
[ -n "$IMPL" ] || {
  echo "usage: resolve-links.sh [--timeout SECONDS] <implementer-set-dir>" >&2
  exit 2
}
[ -d "$IMPL" ] || {
  echo "not a directory: $IMPL" >&2
  exit 2
}

# git_org_repo <repo-root> — print that repo's `origin` remote as `org/repo`
# (GitHub only — the D5 link form this script reads is a github.com blob url;
# widening to another host is a straightforward addition here, not a redesign).
# Accepts both the SSH (`git@github.com:org/repo.git`) and HTTPS
# (`https://github.com/org/repo[.git]`) remote forms. Empty output, rc 1, if
# `<repo-root>` is not a git repo or has no `origin` (never fatal to the caller).
git_org_repo() {
  local root="$1" url
  url=$(git -C "$root" remote get-url origin 2>/dev/null) || return 1
  case "$url" in
  git@github.com:*) url=${url#git@github.com:} ;;
  ssh://git@github.com/*) url=${url#ssh://git@github.com/} ;;
  https://github.com/*) url=${url#https://github.com/} ;;
  *) return 1 ;;
  esac
  url=${url%.git}
  [ -n "$url" ] || return 1
  printf '%s\n' "$url"
}

# parse_blob_url <url> — split a GitHub blob url into `org/repo|path`, or
# nothing (rc 1) if it is not that shape. The branch segment is deliberately
# NOT returned: local resolution only needs the target repo + path (it reads
# whatever is checked out, not necessarily that exact branch — Q1 kept the url
# pinned to `main` for human navigation, not for this script to enforce), and
# the remote fallback derives the raw-content url straight from the ORIGINAL
# url text (`to_raw_url`), not by reassembling these parts. Anything that is
# not this shape (a different host, an already-raw url, a non-blob GitHub url)
# has no local target this script knows how to derive, and falls straight to
# the remote fetch below.
parse_blob_url() {
  local url="$1" rest orgrepo tail path
  case "$url" in
  https://github.com/*/*/blob/*/*) ;;
  *) return 1 ;;
  esac
  rest=${url#https://github.com/}
  orgrepo=$(printf '%s' "$rest" | cut -d/ -f1,2)
  tail=${rest#*/*/blob/}
  path=${tail#*/}
  [ -n "$orgrepo" ] && [ -n "$path" ] || return 1
  printf '%s|%s\n' "$orgrepo" "$path"
}

# find_workspace_root <start-dir> — walk up from <start-dir> looking for a
# `pn-workspace.toml`, the pn-workspace convention this workspace already uses
# (see the global agent CLAUDE.md's own probes). Bounded at 8 levels so a
# missing marker fails fast rather than walking to `/`. Nothing here is
# ZipRecruiter- or repo-specific: any sibling-of-a-workspace-root layout works,
# and a run with no such marker (e.g. inside the nix build sandbox, which has
# no parent directories to find one in) simply returns rc 1 — local sibling
# resolution is unavailable, not an error.
find_workspace_root() {
  local d
  d=$(cd "$1" 2>/dev/null && pwd) || return 1
  for _ in 1 2 3 4 5 6 7 8; do
    [ -f "$d/pn-workspace.toml" ] && {
      printf '%s\n' "$d"
      return 0
    }
    [ "$d" = "/" ] && return 1
    d=$(dirname "$d")
  done
  return 1
}

# find_local_checkout <org/repo> <impl-repo-root> — print the local checkout
# root for <org/repo>, or nothing (rc 1). Prefers THIS invocation's own repo
# root over any sibling scan when <org/repo> is itself: a worktree may hold
# edits a sibling scan would not see (a checkout of the SAME repo is not
# interchangeable with a DIFFERENT checkout of it). Only when <org/repo> names
# a genuinely different repo does it search the discovered workspace root's
# immediate children by their own `origin`.
find_local_checkout() {
  local orgrepo="$1" implroot="$2" wsroot mine d o
  mine=$(git_org_repo "$implroot") || mine=''
  if [ -n "$mine" ] && [ "$mine" = "$orgrepo" ]; then
    printf '%s\n' "$implroot"
    return 0
  fi
  wsroot=$(find_workspace_root "$implroot") || return 1
  for d in "$wsroot"/*/; do
    d=${d%/}
    [ -d "$d/.git" ] || continue
    o=$(git_org_repo "$d") || continue
    if [ "$o" = "$orgrepo" ]; then
      printf '%s\n' "$d"
      return 0
    fi
  done
  return 1
}

# to_raw_url <blob-url> — the raw-content equivalent of a github.com blob url.
# Blob urls render through GitHub's markdown renderer, which DROPS HTML
# comments (`<!-- uuid: ... -->`) from the rendered page — exactly where every
# UUID in this method lives (INV-3's carrier convention) — so fetching the blob
# url itself would systematically miss every UUID and misreport every remote
# check as a rot WARN. `raw.githubusercontent.com` serves the literal source.
to_raw_url() {
  printf '%s' "$1" | sed -E 's#^https://github\.com/#https://raw.githubusercontent.com/#; s#/blob/#/#'
}

# fetch_url <url> — the url's content on stdout, or nothing (rc 1). Never
# fatal: a DNS failure, a blocked network (e.g. accidentally run inside a
# sandboxed build), or a missing fetch tool all land here, and the caller
# treats that identically to "remote did not confirm" — which is exactly the
# WARN-ceiling Q2 requires, not a script failure.
fetch_url() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --max-time "$TIMEOUT" "$1" 2>/dev/null
  elif command -v wget >/dev/null 2>&1; then
    wget -q -T "$TIMEOUT" -O - "$1" 2>/dev/null
  else
    return 1
  fi
}

implroot=$(git -C "$IMPL" rev-parse --show-toplevel 2>/dev/null) || implroot="$IMPL"

echo "=== resolve-links: D5 remote-url dereferencing for $IMPL ==="

checked=0
warns=0
# keyed_lines holds ONE element per finding: "<loc>\t<fully rendered line>".
# Rendering happens HERE, at emission time — never deferred to a printf template
# carried through the array — so there is nothing left to interpolate unsafely
# after the sort below.
keyed_lines=()

# Sorted, not a bare glob: this class of drift (an unsorted `*.md` glob making
# a finding list order depend on the filesystem, not on content) bit this same
# plugin once already (see resolve-imports.sh's WS-6 closing notes) — sort
# explicitly rather than trust glob/find order to already be byte-sorted.
mds=()
while IFS= read -r f; do mds+=("$f"); done < <(find "$IMPL" -maxdepth 1 -name '*.md' | sort)

for f in "${mds[@]}"; do
  relf=$(basename "$f")
  while IFS=$'\t' read -r lineno row; do
    case "$row" in
    '|'*) ;;
    *) continue ;;
    esac
    name=$(row_cell "$row" 1 | trim)
    [ "$name" = "Name" ] && continue
    printf '%s' "$name" | grep -qE '^:?-+:?$' && continue
    [ -z "$name" ] && continue

    uuidcell=$(row_cell "$row" -1 | trim)
    u=$(cell_uuid "$uuidcell")
    url=$(cell_url "$uuidcell")
    # Not a D5 link cell (bare uuid, `(external)`, or unparseable) — nothing to
    # dereference. resolve-imports.sh already reports a malformed link's shape
    # problem; this script only ever starts from a CLEAN uuid+url pair.
    [ -n "$u" ] && [ -n "$url" ] || continue

    checked=$((checked + 1))
    # Trailing colon bounds field 2 exactly at `$lineno` for `sort_locs`'s
    # `-k2,2n` — without it field 2 would run to the next colon INSIDE the
    # rendered line (e.g. "resolves locally: /path"), which numeric sort
    # tolerates (it reads only the leading digits) but is needless fragility.
    loc="$relf:$lineno:"
    line=""

    if orgrepo_path=$(parse_blob_url "$url"); then
      orgrepo=${orgrepo_path%%|*}
      path=${orgrepo_path#*|}
      if checkout=$(find_local_checkout "$orgrepo" "$implroot"); then
        target="$checkout/$path"
        if [ -f "$target" ] && grep -qF -- "$u" "$target"; then
          line=$(printf '  ok        %-22s (uuid %s resolves locally: %s)' "$name" "$u" "$target")
        fi
      fi
    fi

    if [ -z "$line" ]; then
      raw=$(to_raw_url "$url")
      content=$(fetch_url "$raw") || content=''
      if [ -n "$content" ] && printf '%s' "$content" | grep -qF -- "$u"; then
        line=$(printf '  ok        %-22s (uuid %s resolves remotely only — no usable local checkout: %s)' "$name" "$u" "$url")
      else
        line=$(printf '  WARN      %-22s (uuid %s resolves neither locally nor remotely — rot-tolerant per D5, advisory only: %s)' "$name" "$u" "$url")
        warns=$((warns + 1))
      fi
    fi

    keyed_lines+=("$loc"$'\t'"$line")
  done < <(
    awk '
      FNR==1 { insec=0 }
      toupper($0) ~ /^##[[:space:]]+EXTERNAL REFERENCES/ { insec=1; next }
      /^##[[:space:]]/ && insec { insec=0 }
      insec && /^\|/ { printf "%d\t%s\n", FNR, $0 }
    ' "$f"
  )
done

if [ "$checked" -eq 0 ]; then
  echo "  (no D5 [<uuid>](<url>) link cells found — nothing to dereference)"
else
  printf '%s\n' "${keyed_lines[@]}" | sort_locs | cut -f2-
fi

echo
echo "resolve-links: $checked link(s) checked, $warns WARN (rot-tolerant per D5 — never a FAIL)"
exit 0
