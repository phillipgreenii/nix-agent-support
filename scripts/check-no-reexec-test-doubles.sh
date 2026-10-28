#!/usr/bin/env bash
# Guard against new re-exec test doubles (bead pg2-ty6mjm; shared command runner
# ADR in repo x, decision 5 and Appendix A bead 9; ADR 0111 here).
#
# Flags GO_WANT_*HELPER_PROCESS (the env var of the "re-execute the race-built test
# binary as a fake tool" pattern: about 1 s of race-runtime exit sleep per call) in a
# Go test file that a plain `go test ./...` compiles. Such a test MUST use the x
# command runner fake; a test whose subject is a real process MUST live in a file
# tagged `//go:build integration` (V-4). A file counts as tagged when its
# `//go:build` line names `integration`, `contract`, `smoke`, `hostile` or `corpus`
# without a leading `!` (the tags this repo already keeps out of the default build).
# A mention in a `//` comment line is not a use and is ignored.
#
# Existing uses that are neither migrated nor tagged are excused BY PATH in the
# allowlist, one `path | reason` line each (`#` starts a comment line). With no file
# arguments the whole tree is scanned and an allowlist entry that no longer matches
# (file gone, now tagged, or no longer using the name) is itself an error, so the
# list can only shrink. With file arguments (the pre-commit hook passes the staged
# `_test.go` files) only those files are checked.
#
# Usage: check-no-reexec-test-doubles.sh [--root <repo-root>] [--allowlist <file>] [file...]
#   --root       default: the current directory (paths are relative to it)
#   --allowlist  default: <script dir>/reexec-test-double-allowlist.txt
#
# Exits 0 when clean; 1 on a violation or stale allowlist entry; 2 on a usage error.
set -euo pipefail

root="$PWD"
allowlist="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/reexec-test-double-allowlist.txt"
files=()
while [ "$#" -gt 0 ]; do
  case "$1" in
  --root)
    root="${2:?--root needs a directory}"
    shift 2
    ;;
  --allowlist)
    allowlist="${2:?--allowlist needs a file}"
    shift 2
    ;;
  -h | --help)
    sed -n '2,/^set -/p' "$0" | sed '$d'
    exit 0
    ;;
  -*)
    echo "check-no-reexec-test-doubles: unknown option $1" >&2
    exit 2
    ;;
  *)
    files+=("$1")
    shift
    ;;
  esac
done

[ -d "$root" ] || {
  echo "check-no-reexec-test-doubles: --root $root is not a directory" >&2
  exit 2
}
[ -f "$allowlist" ] || {
  echo "check-no-reexec-test-doubles: allowlist $allowlist not found" >&2
  exit 2
}
cd "$root"

name_re='GO_WANT_[A-Z0-9_]*HELPER_PROCESS'
tag_re='^//go:build[[:space:]]+(.*[^![:alnum:]_])?(integration|contract|smoke|hostile|corpus)([^[:alnum:]_]|$)'

declare -A allowed=()
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in '' | '#'*) continue ;; esac
  path="${line%% | *}"
  reason="${line#* | }"
  if [ "$path" = "$line" ] || [ -z "${reason// /}" ]; then
    echo "check-no-reexec-test-doubles: allowlist line needs 'path | reason': $line" >&2
    exit 2
  fi
  allowed["$path"]=1
done <"$allowlist"

whole_tree=0
if [ "${#files[@]}" -eq 0 ]; then
  whole_tree=1
  while IFS= read -r f; do files+=("$f"); done < <(
    find . \( -name .git -o -name .worktrees -o -name vendor -o -name testdata -o -name node_modules \) -prune \
      -o -type f -name '*_test.go' -print | sed 's|^\./||' | sort
  )
fi

# uses_name FILE: succeeds when a non-comment line of FILE names the env var.
uses_name() { grep -E -- "$name_re" "$1" | grep -Evq '^[[:space:]]*//'; }

# is_tagged FILE: succeeds when the build constraint (before `package`) names a tag.
is_tagged() { awk '/^package /{exit} /^\/\/go:build[ \t]/{print}' "$1" | grep -Eq -- "$tag_re"; }

status=0
declare -A hit=()
for f in "${files[@]}"; do
  f="${f#./}"
  case "$f" in *_test.go) ;; *) continue ;; esac
  [ -f "$f" ] || continue
  case "/$f/" in */testdata/*) continue ;; esac
  uses_name "$f" || continue
  is_tagged "$f" && continue
  if [ -n "${allowed[$f]:-}" ]; then
    hit["$f"]=1
    continue
  fi
  echo "$f: names GO_WANT_*HELPER_PROCESS in an untagged test file. Use the x command runner fake for a stand-in tool, or move a real-process test behind //go:build integration (V-4); a pre-existing use may be listed in scripts/reexec-test-double-allowlist.txt with a reason." >&2
  status=1
done

if [ "$whole_tree" -eq 1 ]; then
  for p in "${!allowed[@]}"; do
    if [ -z "${hit[$p]:-}" ]; then
      echo "$p: listed in the allowlist but is gone, tagged, or no longer names GO_WANT_*HELPER_PROCESS; remove the entry" >&2
      status=1
    fi
  done
fi
exit "$status"
