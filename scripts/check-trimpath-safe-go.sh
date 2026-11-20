#!/usr/bin/env bash
# Guard Go code against lookups that only work without -trimpath.
#
# -trimpath rewrites every recorded source path to <import path>/<file>.go and
# leaves the compiled-in GOROOT empty. A file path derived from the call stack
# (runtime.Caller, runtime.Callers, runtime.CallersFrames, a frame's File) is then
# not a real path, and go/importer's default importer looks for the go command
# under the empty GOROOT. A test helper MUST instead resolve files from the
# working directory (go test runs a test binary in the directory of the package
# under test), embed them with go:embed, or build by import path; GOROOT comes
# from `go env GOROOT`.
#
# Flags a line of a Go file that contains any of
#   runtime.Caller(  runtime.Callers(  CallersFrames  .Frame.File  importer.Default(
# unless the same line carries the allow marker `// trimpath-ok: <reason>` (the
# reason MUST be non-empty), for a use that does not derive a path or that sets
# GOROOT first. A line whose first non-blank text is `//` is a comment, not a
# use, and is ignored. Build tags do not matter: every Go file is scanned.
#
# With file arguments (the pre-commit hook passes the staged .go files) only
# those files are checked. With none, every .go file under the root is scanned,
# skipping .git, .worktrees, .workforests, .claude/worktrees, vendor and
# node_modules.
#
# Usage: check-trimpath-safe-go.sh [--root <repo-root>] [file...]
#   --root  default: the current directory (paths are relative to it)
#
# Exits 0 when clean; 1 on a violation; 2 on a usage error.
set -euo pipefail

root="$PWD"
files=()
while [ "$#" -gt 0 ]; do
  case "$1" in
  --root)
    root="${2:?--root needs a directory}"
    shift 2
    ;;
  -h | --help)
    sed -n '2,/^set -/p' "$0" | sed '$d'
    exit 0
    ;;
  --)
    shift
    files+=("$@")
    break
    ;;
  -*)
    echo "check-trimpath-safe-go: unknown option $1" >&2
    exit 2
    ;;
  *)
    files+=("$1")
    shift
    ;;
  esac
done

[ -d "$root" ] || {
  echo "check-trimpath-safe-go: --root $root is not a directory" >&2
  exit 2
}
cd "$root"

if [ "${#files[@]}" -eq 0 ]; then
  while IFS= read -r f; do files+=("$f"); done < <(
    find . \( -name .git -o -name .worktrees -o -name .workforests -o -path ./.claude/worktrees -o -name vendor -o -name node_modules \) -prune \
      -o -type f -name '*.go' -print | sed 's|^\./||' | sort
  )
fi

comment_re='^[[:space:]]*//'
marker_re='//[[:space:]]*trimpath-ok:[[:space:]]*[^[:space:]]'

go_files=()
for f in "${files[@]}"; do
  f="${f#./}"
  case "$f" in *.go) ;; *) continue ;; esac
  [ -f "$f" ] || continue
  go_files+=("$f")
done
[ "${#go_files[@]}" -gt 0 ] || exit 0

status=0
# One grep over every file (xargs batches them): a grep per file costs a fork
# each, which is most of a whole-tree run. -H keeps the file name on every hit.
while IFS= read -r hit; do
  f="${hit%%:*}"
  rest="${hit#*:}"
  lineno="${rest%%:*}"
  text="${rest#*:}"
  [[ "$text" =~ $comment_re ]] && continue
  [[ "$text" =~ $marker_re ]] && continue
  echo "$f:$lineno: derives a path or GOROOT from compiled-in data, which -trimpath rewrites (runtime.Caller/Callers, CallersFrames, .Frame.File or importer.Default). Resolve the file from the working directory, go:embed it or build by import path; or, if this use derives no path, end the line with // trimpath-ok: <reason>." >&2
  status=1
done < <(printf '%s\0' "${go_files[@]}" | xargs -0 grep -n -H -F -e 'runtime.Caller(' -e 'runtime.Callers(' -e 'CallersFrames' -e '.Frame.File' -e 'importer.Default(' -- || true)
exit "$status"
