# Runbook: verifying ceta still fires after enabling the Claude Code hook router

Operator-facing verification step for a real machine's migration onto ADR 0071's Claude Code
hook router. Design: `docs/adr/0071-claude-code-hook-router.md`. This step closes the "silent
foot-gun" gap the ADR's Phase C names explicitly: forgetting to enable the router, or enabling it
with ceta's delegate registration broken/missing, leaves ceta's protections dark with **no visible
symptom** until something that should have been blocked slips through. Run this immediately after
flipping `phillipgreenii.programs.claude-hook-router.enable = true` and applying the HM
configuration that migrates `claude-extended-tool-approver` ("ceta") onto the router (packet
`tc-rjzd3.12`).

```mermaid
flowchart TD
    ENABLE["enable programs.claude-hook-router.enable\n+ apply HM config"] --> S1["Step 1:\nrouter-config.json declares\nall 5 ceta events"]
    S1 --> S2["Step 2:\nTier-3 liveness check\n(scripts/validate-hook-router-live.sh)"]
    S2 --> S3["Step 3:\nceta round-trip check\n(direct PreToolUse probe)"]
    S3 -->|PASS: permissionDecision present| DONE["Migration verified\n-- ceta's protections fire under the router"]
    S3 -->|FAIL: bare {}| GAP["Silent foot-gun reproduced --\ndo not consider the migration done"]
    S3 --> S4["Step 4 (once):\nnegative control --\nprove the check isn't vacuous"]
```

## Prerequisites

- The HM configuration with `phillipgreenii.programs.claude-hook-router.enable = true` has been
  applied on this machine, so ceta's own direct marketplace registration is disabled
  (`phillipgreenii.programs.claude-code.marketplaces.overrides.claude-extended-tool-approver =
false`, applied automatically by packet `tc-rjzd3.12`'s graceful-degradation wiring) and its
  five hook events are instead contributed to `programs.claude-hook-router.delegates`.
- `claude-hook-router`, `claude-extended-tool-approver` and `jq` are on `PATH` (they are once the
  HM configuration above is active — both ship via `home.packages`, co-gated on
  `claude-code.enable`, matching the existing ceta/pg-pr precedent).
- A working `claude` CLI and credentials at `$HOME/.claude/.credentials.json`, only needed for
  Step 2 (the Tier-3 check makes a real network request to Claude's backend — see that script's
  own header for why this is deliberate and non-hermetic).

## Step 1 — confirm ceta's five events are registered

Find the router's rendered config (its exact on-disk location is Claude Code's own plugin-cache
layout, so search rather than hardcode a path — the same technique
`scripts/validate-hook-router-live.sh` uses for its `fired.marker` check):

```bash
router_config="$(find "${CLAUDE_CONFIG_DIR:-$HOME/.claude}" -name router-config.json 2>/dev/null | head -1)"
test -n "$router_config" || { echo "FAIL: no router-config.json found -- is the router enabled and applied?"; exit 1; }
jq -e '
  ((.PreToolUse // [])        | any(.name == "ceta" and .contract == "decide+rewrite")) and
  ((.PostToolUse // [])       | any(.name == "ceta" and .contract == "observe")) and
  ((.PermissionRequest // []) | any(.name == "ceta" and .contract == "observe")) and
  ((.PermissionDenied // [])  | any(.name == "ceta" and .contract == "observe")) and
  ((.SessionEnd // [])        | any(.name == "ceta" and .contract == "observe"))
' "$router_config" >/dev/null && echo "PASS: all 5 ceta events registered" || echo "FAIL: one or more ceta events missing from router-config.json"
```

**PASS**: prints `PASS: all 5 ceta events registered`. **FAIL**: prints the `FAIL` line — ceta's
delegate contribution did not land; stop here and re-check the HM configuration before continuing.

## Step 2 — Tier-3 liveness check (does the router fire at all under real Claude Code?)

```bash
scripts/validate-hook-router-live.sh
```

This is the existing ADR 0071 Phase B, B4 check (`scripts/validate-hook-router-live.sh`) — it
proves Claude Code itself registers the plugin and actually invokes the real
`claude-hook-router` binary for a real `PreToolUse` event, end to end, via one throwaway
`claude -p` session. It is deliberately not wired into any `checks.*`/`nix flake check` gate (not
sandboxable — real network, real credentials) and is meant to be run manually, exactly like this,
before landing any change to the router itself and after any Claude Code version bump. It uses a
generic stub delegate, so it does **not** by itself prove ceta's own decision reaches Claude Code
— that is Step 3.

**PASS**: the script prints `PASS: router hook fired end-to-end -- marker at ...` and exits 0.
**FAIL**: the script prints a `FAIL:` line and exits non-zero (missing prerequisite, build
failure, install failure, or the router hook never firing).

## Step 3 — the ceta round-trip check (does ceta's actual decision reach the router's output?)

This is the specific check packet `tc-rjzd3.12` ("C2") used as its own explicit acceptance
check — restated here as a reusable operator step. It feeds one synthetic `PreToolUse` `Write`
event straight to the real, currently-installed `claude-hook-router` binary, using the real
`router-config.json` Step 1 found, with the real `claude-extended-tool-approver` binary as the
dispatched delegate:

```bash
router_config="$(find "${CLAUDE_CONFIG_DIR:-$HOME/.claude}" -name router-config.json 2>/dev/null | head -1)"
plugin_root="$(dirname "$router_config")"
probe_dir="$(mktemp -d)"
probe_file="$probe_dir/verify-ceta-router-migration-probe.txt"

CLAUDE_PLUGIN_ROOT="$plugin_root" \
CLAUDE_PLUGIN_DATA="$probe_dir/plugin-data" \
CLAUDE_PROJECT_DIR="$probe_dir" \
claude-hook-router >"$probe_dir/router-output.json" <<EOF
{"hook_event_name":"PreToolUse","tool_name":"Write","cwd":"$probe_dir","tool_input":{"file_path":"$probe_file","content":"probe"}}
EOF

cat "$probe_dir/router-output.json"
jq -e '.permissionDecision == "allow"' "$probe_dir/router-output.json" >/dev/null \
  && echo "PASS: ceta's real decision (allow) round-tripped through the router" \
  || echo "FAIL: expected a nonzero permissionDecision (allow) -- see output printed above"
rm -rf "$probe_dir"
```

**What counts as PASS**: the router's stdout is `{"permissionDecision":"allow"}` — proof that
ceta's real `PreToolUse` handler ran (its `path-safety` rule approves an ordinary write inside the
probe's own project directory), emitted its normal nested `hookSpecificOutput` envelope, and the
router correctly unwrapped and relayed it (this is the exact envelope-unwrap `claude-hook-router`
needed the `tc-6sfia` fix for — see that bead and packet `tc-rjzd3.12`'s closeout for the
regression this reproduces).

**What counts as FAIL — the silent foot-gun**: the router's stdout is the bare `{}` abstain
response instead. This is the dangerous case named in this runbook's own Objective: a plain write
that should have been evaluated (and approved, denied, or asked about) by ceta's real rule chain
instead produces no decision at all, with nothing in the router's own output to distinguish "ceta
genuinely abstained" from "ceta's contribution never reached the router" — which is exactly why
this check pins a scenario (an ordinary write inside an allowed project directory) where ceta's
`path-safety` rule is _always_ expected to emit an explicit `allow`, never abstain. Any deviation
from `{"permissionDecision":"allow"}` on this specific scenario is the visible failure signal;
treat the migration as **not** verified and do not proceed until it is `PASS`.

## Step 4 — negative control (run once, to prove Step 3 actually detects breakage)

Confirm Step 3's check is not passing vacuously by deliberately breaking the registration and
re-running it. Temporarily edit the rendered `router-config.json` found in Step 1 to drop ceta's
`PreToolUse` entry (e.g. `jq 'del(.PreToolUse)' "$router_config" > "$router_config.tmp" && mv
"$router_config.tmp" "$router_config"` — this is the router's OWN rendered artifact, safe to
edit for a one-off drill; re-apply the HM configuration afterwards to restore it), then re-run
Step 3's command against the same probe scenario.

**Expected result of the drill**: Step 3's check now prints a bare `{}` and reports `FAIL:
expected a nonzero permissionDecision (allow) -- see output printed above` — confirming the check
does distinguish "ceta's delegate is wired" from "ceta's delegate silently vanished," rather than
passing regardless of whether ceta is actually registered. Re-apply the real HM configuration (or
otherwise restore `router-config.json`) before leaving this drill — do not leave the router config
edited by hand.

## Rollback

Set `phillipgreenii.programs.claude-hook-router.enable = false` and re-apply. Per the
graceful-degradation decision (ADR 0071's Implementation plan, Phase C, C0), ceta's HM module falls back to its own
direct marketplace hook registration unchanged whenever the router is disabled — this is always
available as a real rollback path, at the cost of ceta's HM module maintaining both registration
code paths.

## Related

- Design: `docs/adr/0071-claude-code-hook-router.md`
- Docket: `tc-rjzd3` (packets C1 `tc-rjzd3.11`, C2 `tc-rjzd3.12`, C3 `tc-rjzd3.13` — this runbook)
- Tier-3 general liveness script: `scripts/validate-hook-router-live.sh`
- Hermetic regression coverage for the router's decision-envelope unwrap: `packages/claude-hook-router/tests/test-claude-hook-router-e2e.bats`
