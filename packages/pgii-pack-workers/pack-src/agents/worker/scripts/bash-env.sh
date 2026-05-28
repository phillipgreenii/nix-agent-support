#!/usr/bin/env bash
# BASH_ENV for pgii-workers — ensures devbox-provided tools (most
# importantly `step` for ZR-Private SSH cert auth) reach PATH on every
# non-interactive bash invocation. Without this, Claude's `bash -c`
# tool calls run with only the supervisor's PATH and fail SSH cert
# auth with "step: command not found".
#
# We do two things on shell start:
#   1. Run `direnv export bash` to activate devbox env for the current
#      directory (no-op if no .envrc or direnv missing).
#   2. Walk up to find the nearest `.envrc` and prepend its sibling
#      `bin/` to PATH. ZipRecruiter's monorepo tools — including
#      `step` — live in `$WORKTREE/bin/`, which is not added by
#      devbox `shellenv` alone; only `.envrc.local` (written by the
#      zm post-checkout hook) usually adds it via
#      `zm-generate-monorepo-env`, but agents may run before/without
#      that hook so we prepend it directly as a safety net.
#
# We also override the `cd` builtin to repeat both steps after any
# directory change. Claude's bash tool calls frequently take the form
# `bash -c "cd $WORKTREE && git push"` — without this, the initial
# BASH_ENV pass runs in the caller's cwd (often the city root with no
# .envrc) and the `cd` lands in a worktree but with stale PATH.

[ -n "${_PGII_WORKER_BASH_ENV_LOADED:-}" ] && return 0
export _PGII_WORKER_BASH_ENV_LOADED=1

_pgii_worker_activate() {
  if command -v direnv >/dev/null 2>&1; then
    eval "$(direnv export bash 2>/dev/null)" || true
  fi
  local d=$PWD
  while [ "$d" != "/" ] && [ "$d" != "." ]; do
    if [ -f "$d/.envrc" ]; then
      if [ -d "$d/bin" ]; then
        case ":$PATH:" in
        *":$d/bin:"*) ;;
        *) export PATH="$d/bin:$PATH" ;;
        esac
      fi
      return 0
    fi
    d=$(dirname "$d")
  done
}

cd() {
  builtin cd "$@" || return
  _pgii_worker_activate
}

_pgii_worker_activate
