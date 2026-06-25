#!/usr/bin/env bash
# create-child-bead.sh — create one right-sized child bead with the bd flags that
# the breakdown workflow always needs, so callers don't re-derive (and mis-derive)
# them. Run it from the pg-pr-break-down-work skill directory.
#
# Usage:
#   create-child-bead.sh <parent-id> <title> <body> [<context "key: value"> ...]
#
# Trailing args become a "Context (resolved during breakdown):" block appended to
# the description (Principle 2 — inject shared lookups once). Prints the new bead
# id on stdout. The caller ARMS the leaf separately, with the CONSUMER's label
# (worker-ready, ccpool, …), because the right label depends on who works it:
#   id=$(create-child-bead.sh zr-abc "test: foo" "Add tests for foo. Done when …" "target file: foo.go")
#   bd update "$id" --add-label worker-ready
#
# Why these flags:
#   --no-inherit-labels : a child must NOT silently inherit the parent's labels
#                         (e.g. worker-ready/human) — leaves are armed deliberately.
#   --force (fallback)  : bd rejects a child id whose prefix differs from the
#                         store default; --force allows the parent-derived prefix.
set -uo pipefail

parent="${1:?usage: create-child-bead.sh <parent-id> <title> <body> [context ...]}"
title="${2:?title required}"
body="${3:?body required}"
shift 3

desc="$body"
if [ "$#" -gt 0 ]; then
  desc="$body

Context (resolved during breakdown):"
  for kv in "$@"; do
    desc="$desc
- $kv"
  done
fi

# Try without --force first; retry with --force only if the create was rejected
# (typically a prefix mismatch on the parent-derived child id).
if out="$(bd create --type task --parent "$parent" --no-inherit-labels \
  --title "$title" --description "$desc" 2>&1)"; then
  :
else
  out="$(bd create --type task --parent "$parent" --no-inherit-labels --force \
    --title "$title" --description "$desc" 2>&1)" || {
    printf 'create-child-bead: bd create failed:\n%s\n' "$out" >&2
    exit 1
  }
fi

# Surface bd's full output on stderr (for logs); emit just the new id on stdout.
printf '%s\n' "$out" >&2
printf '%s\n' "$out" | grep -oE '[A-Za-z0-9]+-[A-Za-z0-9.]+' | head -1
