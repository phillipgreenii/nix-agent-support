#!/bin/sh
# Doctor check: HACK 2 (hack-autoclose-completed-mols sweeper).
#
# HACK 2 retires when `gc bd close` fires the on_close hook chain like
# `bd close` does, OR when builtin formulas update their report step to
# use `bd close`.
#
# Exit 0 = HACK 2 still needed (the workaround should stay)
# Exit 1 = HACK 2 retirement candidate (verify and remove the sweeper)

bd_version=$(bd --version 2>/dev/null | head -1 | awk '{print $3}')
gc_version=$(gc version 2>/dev/null | head -1)

case "$bd_version" in
1.0.*)
  echo "bd $bd_version, gc $gc_version: HACK 2 still needed"
  echo "(gc bd close bypasses on_close hook in bd 1.0.x — verified 2026-05-14)"
  exit 0
  ;;
*)
  echo "bd $bd_version, gc $gc_version: HACK 2 retirement candidate"
  echo "Test before removing:"
  echo "  1. Create parent + child beads with parent-child dep"
  echo "  2. gc bd close <child>"
  echo "  3. If parent auto-closed within ~10s, the on_close hook fires"
  echo "  4. If confirmed, delete orders/hack-autoclose-completed-mols.toml + script"
  exit 1
  ;;
esac
