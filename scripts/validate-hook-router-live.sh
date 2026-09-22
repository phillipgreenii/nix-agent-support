#!/usr/bin/env bash
# validate-hook-router-live.sh — Tier 3 (ADR 0071 §4.0) live validation for
# the claude-hook-router plugin.
#
# ADR 0071 §4 Phase B, B4 (phillipgreenii-nix-agent-support): this is a
# named, scripted, DELIBERATELY NON-HERMETIC validation, not a `checks.*`
# gate. It cannot live inside a sandboxed nix build (no network, no way to
# purely-evaluate an external closed binary's behavior) and isn't meant to.
# B5 (same ADR section) explicitly excludes this script from
# `checks.*`/`nix flake check` — do NOT wire it in there.
#
# WHAT IT DOES: builds the Phase A-generated router plugin
# (`mkClaudeHookRouterPlugin`, phillipg-nix-repo-base `lib/claude-marketplace.nix`)
# over a small throwaway stub delegate, builds the Phase B router binary
# (`packages/claude-hook-router`), installs both into an ISOLATED
# `CLAUDE_CONFIG_DIR` (a fresh temp directory — the operator's real
# `~/.claude` is never touched or read except to copy the credentials file
# needed to authenticate), then runs one throwaway `claude -p` session that
# issues a real tool call and confirms the router actually dispatched to the
# stub delegate — the same technique ("a scripted throwaway `claude -p`
# session") this whole ADR used to establish every empirical fact it rests
# on (§1.2).
#
# WHY A BARE-NAME STUB DELEGATE, NOT A `${CLAUDE_PLUGIN_ROOT}`-relative one:
# `mkClaudeHookRouterPlugin`'s per-source copy step (the `surfaces` list in
# `lib/claude-marketplace.nix`) only vendors `commands`/`agents`/`skills`
# directories — it does NOT copy a hook source's own script files into
# `$out/vendored/<name>/`, even though `rewriteCommand` rewrites any
# `${CLAUDE_PLUGIN_ROOT}` occurrence in a source's hook command to point
# there. A source whose hook command is `${CLAUDE_PLUGIN_ROOT}/some-script`
# would therefore reference a path the generator never populates. This is a
# packet-A1 concern (out of scope for this script — see docket tc-rjzd3,
# packet tc-rjzd3.9's own report) and this script sidesteps it entirely by
# following the SAME convention every real plugin in this estate already
# uses (docs/claude-marketplaces.md Pattern 1: "Reference the hook command
# by bare name"): the stub delegate ships as a bare on-PATH command, never a
# plugin-root-relative path.
#
# WHAT COUNTS AS "FIRED": the stub delegate has `contract: "observe"` (its
# response is always discarded — ADR 0071 §2.4 — so it can never influence
# the real session's actual tool call) and, when invoked, writes a
# timestamped line to `$CLAUDE_PLUGIN_DATA/fired.marker`.
# `CLAUDE_PLUGIN_DATA` is guaranteed set for a dispatched delegate
# (`packages/claude-hook-router/internal/router/dispatch.go`'s
# `delegateEnv`, always deriving it from the router's own `CLAUDE_PLUGIN_DATA`
# — with a documented fallback to `${TMPDIR:-/tmp}/claude-hook-router` if
# Claude Code never sets one for the router itself), so this script checks
# both locations for that marker file after the session ends. Finding it is
# proof Claude Code actually: registered the plugin's `hooks.json`, invoked
# the real `claude-hook-router` binary for a real `PreToolUse` event, the
# binary read `router-config.json` and matched the delegate, and the
# delegate subprocess actually ran — i.e. that the router hook fires
# end-to-end for a real tool call, exactly what this script exists to prove.
#
# RUN CADENCE (documented per ADR 0071 §4 Phase B, B4): run this manually
# before landing any change to the Phase A generator
# (`mkClaudeHookRouterPlugin`) or the Phase B router runtime
# (`packages/claude-hook-router`), and after any Claude Code version bump.
# It is deliberately NOT run automatically by any gate, so it is never
# silently skipped just because it can't be sandboxed — running it is a
# manual, deliberate act every time.
#
# REQUIRES: `claude`, `nix`, `go`, `jq` on PATH; a working credentials file
# at `$HOME/.claude/.credentials.json` (copied, never modified, into the
# isolated config dir so the throwaway session can authenticate); and
# network access for the `claude -p` call itself (this script DOES make a
# real request to Claude's backend — that is the entire point of Tier 3).
#
# EXIT: 0 only on a confirmed pass. Non-zero on any detected failure
# (missing prerequisite, build failure, install failure, or the router
# hook never firing).
#
# Usage: scripts/validate-hook-router-live.sh

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log() { printf '[validate-hook-router-live] %s\n' "$*" >&2; }
fail() {
  printf '[validate-hook-router-live] FAIL: %s\n' "$*" >&2
  exit 1
}

for bin in claude nix go jq; do
  command -v "$bin" >/dev/null 2>&1 || fail "required tool '$bin' not found on PATH"
done

if [ ! -f "$HOME/.claude/.credentials.json" ]; then
  # shellcheck disable=SC2016 # literal prose path, not command substitution
  fail 'no credentials at $HOME/.claude/.credentials.json — cannot authenticate the throwaway claude -p session (this script never reads/writes anything else under your real ~/.claude)'
fi

workdir="$(mktemp -d "${TMPDIR:-/tmp}/validate-hook-router-live.XXXXXX")"
# shellcheck disable=SC2329 # invoked indirectly via the trap below
cleanup() { rm -rf "$workdir"; }
trap cleanup EXIT

# --- 1. Build the Phase B router binary. `go build` (not `nix build`):
#     `packages/claude-hook-router` has no `packages.<system>` flake output
#     yet (verified empirically while writing this script — a real gap left
#     by packet B1, out of scope here), so building it plainly with `go`
#     keeps this script working regardless of that flake-wiring gap. ---
log "building claude-hook-router (go build)"
bin_dir="$workdir/bin"
mkdir -p "$bin_dir"
(
  cd "$repo_root/packages/claude-hook-router"
  GOBIN="$bin_dir" go build -o "$bin_dir/claude-hook-router" ./cmd/claude-hook-router
) || fail "go build of claude-hook-router failed"

# --- 2. The stub delegate: a bare on-PATH command (see header). ---
stub_name="validate-hook-router-stub-delegate"
stub_path="$bin_dir/$stub_name"
cat >"$stub_path" <<'STUB'
#!/usr/bin/env bash
# Throwaway PreToolUse observer for scripts/validate-hook-router-live.sh.
# Discards its input, proves it ran by appending to $CLAUDE_PLUGIN_DATA/fired.marker,
# and always abstains ({}) so it can never affect the real tool call.
cat >/dev/null
if [ -n "${CLAUDE_PLUGIN_DATA:-}" ]; then
  mkdir -p "$CLAUDE_PLUGIN_DATA"
  date -Iseconds >>"$CLAUDE_PLUGIN_DATA/fired.marker"
fi
echo '{}'
STUB
chmod +x "$stub_path"

# --- 3. The source plugin.json is stub-delegate; PreToolUse, match-all,
#     observe, command = the bare stub name from step 2. ---
src_dir="$workdir/stub-source"
mkdir -p "$src_dir/.claude-plugin" "$src_dir/hooks"
cat >"$src_dir/.claude-plugin/plugin.json" <<JSON
{ "name": "stub-delegate", "version": "0.0.1" }
JSON
cat >"$src_dir/hooks/hooks.json" <<JSON
{
  "hooks": {
    "PreToolUse": [
      {
        "hooks": [
          { "type": "command", "command": "$stub_name", "contract": "observe" }
        ]
      }
    ]
  }
}
JSON

# --- 4. Build the REAL Phase A generator output over that source — the
#     actual artifact this script installs, not a hand-rolled imitation. ---
gen_nix="$workdir/generate-plugin.nix"
cat >"$gen_nix" <<'NIX'
{ repoRoot, srcDir }:
let
  flake = builtins.getFlake (toString repoRoot);
  system = builtins.currentSystem;
  pkgs = import flake.inputs.nixpkgs { inherit system; };
  lib = pkgs.lib;
  builders = flake.inputs.phillipgreenii-nix-base.lib.mkClaudeMarketplaceBuilders {
    inherit pkgs lib;
  };
in
builders.mkClaudeHookRouterPlugin {
  name = "validate-hook-router-live-plugin";
  declared = "0.0.0";
  routerCommand = "claude-hook-router";
  sources = [
    {
      name = "stub-delegate";
      src = srcDir;
      includeHooks = true;
      priority = 0;
    }
  ];
}
NIX

log "building the Phase A-generated router plugin (mkClaudeHookRouterPlugin)"
plugin_out="$(
  nix build --impure -f "$gen_nix" \
    --argstr repoRoot "$repo_root" \
    --argstr srcDir "$src_dir" \
    --no-link --print-out-paths
)" || fail "nix build of the Phase A-generated plugin failed"
[ -n "$plugin_out" ] || fail "nix build produced no output path"

# --- 5. Wrap the generated plugin in a throwaway local-directory
#     marketplace (Pattern 1, docs/claude-marketplaces.md) and register +
#     install + enable it in an ISOLATED CLAUDE_CONFIG_DIR.
#
#     `source` MUST be a RELATIVE path (matching every real entry in this
#     repo's own claude-marketplace/.claude-plugin/marketplace.json, e.g.
#     "./bash-lsp") — the claude CLI's marketplace-entry schema rejects an
#     absolute path with "This plugin's marketplace entry is invalid:
#     source: Invalid input" (verified empirically against claude CLI
#     2.1.269: an absolute `/nix/store/...` source fails `claude plugin
#     install` even though `claude plugin marketplace add` accepts it; a
#     symlink under the marketplace dir referenced by a relative "./<name>"
#     source installs successfully and the plugin's real content — reached
#     through the symlink — is what actually gets copied into the plugin
#     cache). So symlink the nix store output into a name under the
#     marketplace dir and reference it relatively, rather than passing
#     $plugin_out directly. ---
marketplace_dir="$workdir/marketplace"
mkdir -p "$marketplace_dir/.claude-plugin"
plugin_link_name="validate-hook-router-live-plugin"
ln -s "$plugin_out" "$marketplace_dir/$plugin_link_name"
cat >"$marketplace_dir/.claude-plugin/marketplace.json" <<JSON
{
  "name": "validate-hook-router-live",
  "owner": { "name": "validate-hook-router-live" },
  "plugins": [
    {
      "name": "validate-hook-router-live-plugin",
      "source": "./$plugin_link_name",
      "description": "Throwaway ADR 0071 Tier 3 live-validation plugin"
    }
  ]
}
JSON

config_dir="$workdir/claude-config"
mkdir -p "$config_dir"
cp "$HOME/.claude/.credentials.json" "$config_dir/.credentials.json"
chmod 600 "$config_dir/.credentials.json"

project_dir="$workdir/project"
mkdir -p "$project_dir"

export CLAUDE_CONFIG_DIR="$config_dir"
export PATH="$bin_dir:$PATH"

log "registering the throwaway marketplace"
claude plugin marketplace add "$marketplace_dir" ||
  fail "claude plugin marketplace add failed"

log "installing + enabling the plugin"
claude plugin install "validate-hook-router-live-plugin@validate-hook-router-live" \
  --scope user -y ||
  fail "claude plugin install failed"

# --- 6. The live check: one throwaway claude -p session that issues a real
#     tool call. bypassPermissions matches this repo's own convention for a
#     throwaway/non-interactive session with a fixed, non-externally-derived
#     prompt (see docs/superpowers/plans/2026-06-23-pr-pool-deny-by-default-allowlist.md). ---
# shellcheck disable=SC2016 # backticks are literal prose, not command substitution
prompt='Run the shell command `echo hook-router-live-check` using your Bash tool. Do nothing else.'
session_log="$workdir/claude-session.log"
log "running the throwaway claude -p session"
if ! (cd "$project_dir" && claude -p "$prompt" --permission-mode bypassPermissions >"$session_log" 2>&1); then
  cat "$session_log" >&2
  fail "claude -p session exited non-zero"
fi

# --- 7. Confirm the router actually fired: look for the stub's marker file
#     under the isolated config dir (its real CLAUDE_PLUGIN_DATA location)
#     and under the router's documented no-CLAUDE_PLUGIN_DATA fallback. ---
# `|| true`: under `set -euo pipefail`, `find` exits nonzero when either
# candidate path doesn't exist (the CLAUDE_PLUGIN_DATA fallback location
# normally doesn't, since CLAUDE_PLUGIN_DATA is set for a dispatched
# delegate — see the header comment), and pipefail propagates that nonzero
# status through `| head -1` even though `head` itself succeeded and
# `$marker` was captured correctly. Without `|| true`, `set -e` would abort
# the script right here on a SUCCESSFUL run — before ever reaching the
# PASS/FAIL check below — discarding a correctly-found marker (verified
# empirically: the marker was found but the script still exited 1 with no
# PASS/FAIL output).
marker="$(
  find "$config_dir" "${TMPDIR:-/tmp}/claude-hook-router" \
    -name fired.marker 2>/dev/null | head -1
)" || true

if [ -z "$marker" ]; then
  cat "$session_log" >&2
  fail "router hook never fired: no fired.marker found under $config_dir or \${TMPDIR:-/tmp}/claude-hook-router"
fi

log "PASS: router hook fired end-to-end — marker at $marker"
cat "$marker" >&2
exit 0
