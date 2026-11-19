#!/usr/bin/env bash
# Refresh the repository-root go.work.sum in workspace mode.
#
# update-locks.sh resolves every Go module with GOWORK=off, so its dependency
# bumps never write go.work.sum and it goes stale; the next workspace-mode go
# invocation (go-work-vet, an editor, golangci-lint) then rewrites it. This loads
# every package of every workspace module (tests included) in workspace mode,
# which makes go record the missing go.work.sum lines and nothing else. It must
# not touch any module go.mod or go.sum, so it never runs `go work sync`.
#
# Usage: refresh-go-work-sum.sh [--root DIR]
set -euo pipefail

root=""
while [ "$#" -gt 0 ]; do
  case "$1" in
  --root)
    root="${2:?--root needs a directory}"
    shift 2
    ;;
  *)
    echo "refresh-go-work-sum.sh: unknown argument: $1" >&2
    exit 2
    ;;
  esac
done
if [ -z "${root}" ]; then
  root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fi
if [ ! -f "${root}/go.work" ]; then
  echo "refresh-go-work-sum.sh: ${root}/go.work is missing" >&2
  exit 1
fi

cd "${root}"
GOWORK="${root}/go.work" go list -deps -test all >/dev/null
