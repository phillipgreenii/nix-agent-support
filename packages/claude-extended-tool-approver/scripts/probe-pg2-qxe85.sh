#!/usr/bin/env bash
# probe-pg2-qxe85.sh — verification probe for bead pg2-qxe85: finish ADR 0044's refusal
# census. pg2-d0ja3 converted 31 sites (safe-commands 23, git 8) and left the rest; this
# bead converts the 12 NON-GH remainder and leaves gh's 4 to a later pass (another agent
# owns internal/rules/gh/ concurrently, so qxe85 is only PARTIALLY complete).
#
# WHY IT MATTERS. Under-conversion is the APPROVAL-WIDENING direction. A site that means
# "I examined this and will not clear it" but reports ErrNotApplicable is read as an
# EXHAUSTION, and exhaustion is the half a consumer may act on to clear a body. So each
# unconverted site is a latent widening, not cosmetic debt.
#
# PER-RULE, NOT AGGREGATE. These rules sit EARLIER in the chain than safe-commands and
# git, so a floor here reaches more later-rule Approves and each rule owes its own
# before/after. Every section below names ONE rule, and every row is measured against BOTH
# a base binary (main @a064a73e, pre-patch) and the patched binary. An aggregate reading
# would hide which rule moved a row, which is exactly what ADR 0043's per-site ordering
# analysis exists to prevent.
#
# HOW PROVENANCE IS OBSERVED. hookio.Provenance is not serialized, so the hook JSON cannot
# show it. Two stderr channels can:
#
#   * `<rule> -> refused (floored at <d>, continuing): <reason>` — printed unconditionally
#     by engine.Evaluate's refusal branch. Its PRESENCE is the conversion, and the rule
#     name in it is the attribution.
#   * `TRACE <rule> -> abstain: <reason>` under CLAUDE_TOOL_APPROVER_TRACE=1 — on the base
#     binary the reason is the sentinel text "rule does not apply"; on the patched binary
#     it is the RESTORED reason. That pair is the fossil-to-fact transition the bead's
#     fourth acceptance criterion asks for.
#
# The DECISION column is measured too, because the acceptance gate is "no row moves in the
# less-restrictive direction". A NoOpinion floor can only demote a later Approve, so the
# admissible movements are approve->abstain/ask/reject and abstain->ask/reject; anything
# toward approve is a failure of the change, not of the probe.
#
# THREE SITES NEED A CONSUMER CONFIG. kubectl's scoped-approve and kc-exec-without-inner
# branches and monorepo's dangerous-env branch are unreachable with an empty rules.json
# (no dev-workspace prefix, no approved wrappers), so section 6 writes a SYNTHETIC
# rules.json into a temp XDG_CONFIG_HOME. Nothing outside the temp dir is read or written.
#
# ISOLATION (bead pg2-cbihz): both binaries are built from THIS worktree, XDG_DATA_HOME
# points at a throwaway directory so probe rows land in a scratch asks.db and never reach
# the shared production corpus, and this probe uses none of `evaluate`/`baseline`/`compare`
# (which open that corpus read-write).
set -uo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-qxe85-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

# BASE is the ref this branch forked from, PATCHED is this worktree. The base tree is
# EXPORTED with `git archive` into a temp directory rather than checked out: nothing in the
# canonical clone or this worktree is touched, and no branch is switched. A base build
# failure degrades the base columns to `n/a` instead of aborting, so the patched-side
# readings are still produced.
base_ref="${QXE85_BASE_REF:-main}"
base_src="$work/base-src"
base_bin="$work/ceta-base"
patched_bin="$work/ceta-patched"

echo "building patched binary from: $pkg_root"
(cd "$pkg_root" && go build -o "$patched_bin" ./cmd/claude-extended-tool-approver) || exit 1

echo "building base binary from:    $base_ref"
mkdir -p "$base_src"
# `git archive` run from inside a subdirectory exports that subtree with paths relative to
# it, so the module may land at $base_src OR at $base_src/packages/... depending on where
# this script is invoked from. Probe for go.mod rather than assuming either shape.
if ! git -C "$pkg_root" archive "$base_ref" | tar -x -C "$base_src"; then
  echo "  git archive $base_ref FAILED — base columns will read 'n/a'" >&2
  base_bin=""
else
  base_mod=""
  for candidate in "$base_src" "$base_src/packages/claude-extended-tool-approver"; do
    [[ -f "$candidate/go.mod" ]] && base_mod="$candidate"
  done
  if [[ -z $base_mod ]]; then
    echo "  base tree has no go.mod — base columns will read 'n/a'" >&2
    base_bin=""
  elif ! (cd "$base_mod" && go build -o "$base_bin" ./cmd/claude-extended-tool-approver); then
    echo "  base build FAILED — base columns will read 'n/a'" >&2
    base_bin=""
  fi
fi

export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

# probe_cwd is a REAL directory: the path evaluator classifies against a live filesystem,
# so a synthetic cwd would change every verdict below.
probe_cwd="$pkg_root"

# ---------------------------------------------------------------------------
# hook_json <tool> <payload-json> — one PreToolUse envelope.
hook_json() {
  jq -cn --arg t "$1" --arg w "$probe_cwd" --argjson ti "$2" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-qxe85-probe",cwd:$w,
      permission_mode:"default",tool_name:$t,tool_input:$ti}'
}

bash_json() { jq -cn --arg c "$1" '{command:$c}'; }

# decision <bin> <tool> <payload> — the decision word; `abstain` for the empty object.
decision() {
  local bin=$1
  [[ -z $bin ]] && {
    printf 'n/a'
    return
  }
  hook_json "$2" "$3" | CLAUDE_TOOL_APPROVER_TRACE=0 "$bin" 2>/dev/null |
    jq -r '.hookSpecificOutput.permissionDecision // .permissionDecision // "abstain"'
}

# refusal_of <bin> <rule> <tool> <payload> — the refusal line this RULE printed, or NONE.
# Scoped to the named rule so a sibling rule's refusal can never be miscredited.
refusal_of() {
  local bin=$1 rule=$2
  [[ -z $bin ]] && {
    printf 'n/a'
    return
  }
  local stderr line
  stderr="$(hook_json "$3" "$4" | CLAUDE_TOOL_APPROVER_TRACE=1 "$bin" 2>&1 >/dev/null)"
  if ! printf '%s\n' "$stderr" | grep -q "^claude-extended-tool-approver: ${rule} -> refused"; then
    printf 'NONE'
    return
  fi
  line="$(printf '%s\n' "$stderr" |
    sed -n "s/^claude-extended-tool-approver: ${rule} -> refused (floored at [a-z]*, continuing): //p" |
    head -1)"
  printf '%s' "${line:-PRESENT (no rule-authored reason)}"
}

# traced_reason <bin> <rule> <tool> <payload> — what the TRACE line reports for this rule.
traced_reason() {
  local bin=$1 rule=$2
  [[ -z $bin ]] && {
    printf 'n/a'
    return
  }
  hook_json "$3" "$4" | CLAUDE_TOOL_APPROVER_TRACE=1 "$bin" 2>&1 >/dev/null |
    sed -n "s/^claude-extended-tool-approver: TRACE ${rule} -> [a-z]*: //p" | head -1
}

# site <rule> <site-label> <tool> <payload> — one census site, base vs patched.
site() {
  local rule=$1 label=$2 tool=$3 payload=$4
  printf '  site: %s\n' "$label"
  printf '        base    decision=%-8s refusal[%s]=%-8s trace=%s\n' \
    "$(decision "$base_bin" "$tool" "$payload")" \
    "$rule" "$(refusal_of "$base_bin" "$rule" "$tool" "$payload")" \
    "$(traced_reason "$base_bin" "$rule" "$tool" "$payload")"
  printf '        patched decision=%-8s refusal[%s]=%s\n' \
    "$(decision "$patched_bin" "$tool" "$payload")" \
    "$rule" "$(refusal_of "$patched_bin" "$rule" "$tool" "$payload")"
}

echo
echo "probe cwd: $probe_cwd"
echo "Read each site as a PAIR. The conversion is proven by base refusal=NONE + patched"
echo "refusal=<the restored reason>; the gate is that decision does not move toward approve."

echo
echo "=== 1. kubectl (3 of 5 sites; the other 2 need a consumer config — section 6) ==="
site kubectl 'exec verb outside a dev workspace' Bash "$(bash_json 'kubectl exec mypod -- ls /')"
site kubectl 'rollout with a mutating sub-verb' Bash "$(bash_json 'kubectl rollout restart deploy/x')"
site kubectl 'everything else (apply/delete/scale/...)' Bash "$(bash_json 'kubectl delete pod x')"
echo "  NOTE: the rollout site and the everything-else site share their RESTORED text"
echo "        verbatim ('modifying kubectl command (defer)') because the two fossil"
echo "        comments were identical. They are told apart by the INPUT above, not by the"
echo "        reason; restoring the text as recorded was the acceptance criterion."

echo
echo "=== 2. path-safety (3 sites) ==="
site path-safety 'Read of a path the evaluator will not clear' \
  Read '{"file_path":"/private/var/db/dslocal/x.plist"}'
site path-safety 'Write to a path the evaluator will not clear' \
  Write '{"file_path":"/usr/local/lib/x.txt","content":"y"}'
site path-safety 'Glob/Grep over a path the evaluator will not clear' \
  Grep '{"pattern":"x","path":"/private/var/db"}'
echo "  UNTOUCHED: the ADR 0041 agent-config write branch stays a TERMINAL NoOpinion."
site path-safety 'control — ADR 0041 site must NOT become a floor' \
  Write "$(jq -cn --arg p "$probe_cwd/.claude/settings.local.json" '{file_path:$p,content:"{}"}')"

echo
echo "=== 3. webfetch (1 site) ==="
site webfetch 'GitHub release binary download' \
  WebFetch '{"url":"https://github.com/o/r/releases/download/v1/b.tar.gz","prompt":"x"}'
echo "  control — an ordinary GitHub page must still approve:"
printf '        base=%-8s patched=%-8s\n' \
  "$(decision "$base_bin" WebFetch '{"url":"https://github.com/o/r","prompt":"x"}')" \
  "$(decision "$patched_bin" WebFetch '{"url":"https://github.com/o/r","prompt":"x"}')"

echo
echo "=== 4. docker (1 site) ==="
site docker 'unparseable mount spec on a docker run --rm' \
  Bash "$(bash_json 'docker run --rm -v :::: alpine sh -c "ls /"')"

echo
echo "=== 5. claude-tools (1 site) ==="
site claude-tools 'plan-mode transition tool (deliberate allowlist exclusion)' \
  ExitPlanMode '{}'
site claude-tools 'the same, other spelling' EnterPlanMode '{}'
echo "  control — an allowlisted first-party tool must still approve:"
printf '        base=%-8s patched=%-8s\n' \
  "$(decision "$base_bin" TodoWrite '{}')" "$(decision "$patched_bin" TodoWrite '{}')"

echo
echo "=== 6. SYNTHETIC-CONFIG sites: kubectl scoped-approve + kc-exec, monorepo (3 sites) ==="
echo "    An empty rules.json cannot reach these branches, so a throwaway XDG_CONFIG_HOME"
echo '    supplies the minimum consumer data. Nothing outside $work is read or written.'
export XDG_CONFIG_HOME="$work/xdg-config"
mkdir -p "$XDG_CONFIG_HOME/claude-extended-tool-approver"
cat >"$XDG_CONFIG_HOME/claude-extended-tool-approver/rules.json" <<'JSON'
{
  "kubectl": {
    "executableAliases": ["kc"],
    "execVerbs": ["exe"],
    "scopedApproveVerbs": ["restart"],
    "devWorkspaceFlags": ["--ws"],
    "devWorkspacePrefix": "probe-dev-"
  },
  "monorepo": {
    "approvedCommands": ["probe-wrapper"],
    "dangerousEnvByWrapper": { "probe-wrapper": ["PROBE_DANGER"] }
  }
}
JSON
site kubectl 'scoped-approve verb outside a dev workspace' \
  Bash "$(bash_json 'kc restart --ws other-workspace')"
site kubectl 'kc exec with no inner command after --' \
  Bash "$(bash_json 'kc exe --ws probe-dev-me')"
site monorepo 'approved wrapper carrying a dangerous env var' \
  Bash "$(bash_json 'PROBE_DANGER=1 probe-wrapper build')"
echo "  control — the same wrapper WITHOUT the dangerous var must still approve:"
printf '        base=%-8s patched=%-8s\n' \
  "$(decision "$base_bin" Bash "$(bash_json 'probe-wrapper build')")" \
  "$(decision "$patched_bin" Bash "$(bash_json 'probe-wrapper build')")"
echo
echo "=== 6b. THE FLOOR HAS TEETH: a later rule's Approve is demoted (the ONLY movement) ==="
echo "    Sections 1-6 all read abstain->abstain, because on the real chain no rule after"
echo "    kubectl/monorepo approves these leaves — so the conversion changes PROVENANCE"
echo "    without moving a verdict, which is the intended result. That alone does not prove"
echo "    the floor is load-bearing, so this section CONSTRUCTS the case it is for: a"
echo "    consumer config in which build-tools (which runs AFTER both rules) approves the"
echo "    very leaf they refuse. This is a SYNTHETIC config, not a measured production row;"
echo "    it demonstrates the MECHANISM and its DIRECTION."
cat >"$XDG_CONFIG_HOME/claude-extended-tool-approver/rules.json" <<'JSON'
{
  "buildtools": { "approvedTools": ["kubectl", "probe-wrapper"] },
  "monorepo": {
    "approvedCommands": ["probe-wrapper"],
    "dangerousEnvByWrapper": { "probe-wrapper": ["PROBE_DANGER"] }
  }
}
JSON
printf '  %-42s %-14s %s\n' 'leaf' 'base' 'patched'
for c in 'kubectl delete pod x' 'PROBE_DANGER=1 probe-wrapper build' 'probe-wrapper build'; do
  printf '  %-42s %-14s %s\n' "$c" \
    "$(decision "$base_bin" Bash "$(bash_json "$c")")" \
    "$(decision "$patched_bin" Bash "$(bash_json "$c")")"
done
echo "    EXPECTED: the first two move allow->abstain (MORE restrictive — a refused leaf can"
echo "    no longer be cleared by a later rule), and the third, which nobody refuses, does"
echo "    not move. A row moving the other way would mean the change is wrong."
unset XDG_CONFIG_HOME

echo
echo "=== 7. NOT CONVERTED, recorded so the census is not lost ==="
echo "    gh's 4 sites (internal/rules/gh/gh.go:132,183,188,213) are owned by a"
echo "    concurrent agent this wave and are deliberately left as ErrNotApplicable."
echo "    Each is still a latent widening; qxe85 is PARTIALLY complete until they land."
echo
echo "    DECLINED with an in-code reason (not fossils, and not conversions):"
echo '      kubectl.go  operation == ""     — no verb classified, so nothing was examined'
echo "      kubectl.go  r.exprEval == nil    — construction state, not a judgement"
echo "      pathsafety.go switch default     — no path classified"

echo
echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"
