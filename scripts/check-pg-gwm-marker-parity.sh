#!/usr/bin/env bash
# Asserts the pg-gwm exception marker name is spelled identically in the always-on rules
# text (R-4/R-5) and in the tool approver's pggwmworktree rule (the ExceptionEnv constant
# that its deny message prints). Usage: check-pg-gwm-marker-parity.sh <rules.md> <rule.go>
set -euo pipefail
rules="${1:?rules markdown file}"
rule_go="${2:?pggwmworktree rule.go}"

marker="$(sed -n 's/^const ExceptionEnv = "\(.*\)"$/\1/p' "$rule_go")"
if [ -z "$marker" ]; then
  echo "FAIL: no 'const ExceptionEnv' in $rule_go" >&2
  exit 1
fi
if ! [[ "$marker" =~ ^[A-Z][A-Z0-9_]*$ ]]; then
  echo "FAIL: marker '$marker' in $rule_go is not an upper-case identifier" >&2
  exit 1
fi
if ! grep -qF -- "$marker" "$rules"; then
  echo "FAIL: marker '$marker' (hook) does not appear in $rules" >&2
  exit 1
fi
# Every PG_GWM_* token in the rules must be exactly the hook's marker.
bad="$(grep -oE 'PG_GWM_[A-Z0-9_]*' "$rules" | sort -u | grep -vxF -- "$marker" || true)"
if [ -n "$bad" ]; then
  echo "FAIL: $rules names a pg-gwm marker the hook does not implement: $bad" >&2
  exit 1
fi
# The rule text must also name the three prohibited routes.
for lit in EnterWorktree 'git worktree add' bin/worktree; do
  grep -qF -- "$lit" "$rules" || {
    echo "FAIL: $rules lacks '$lit'" >&2
    exit 1
  }
done
echo "ok: $marker"
