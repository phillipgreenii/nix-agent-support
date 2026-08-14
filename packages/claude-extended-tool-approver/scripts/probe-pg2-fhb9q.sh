#!/usr/bin/env bash
# probe-pg2-fhb9q.sh — verification probe for bead pg2-fhb9q: credential-directory
# coverage becomes CONFIG-DRIVEN, so a path under a configured
# sandbox.filesystem.denyRead prefix is screened WITHOUT needing an entry in
# secretpath's hardcoded Go list.
#
# THE MEASURED DEFECT (main @93846155, isolated XDG_DATA_HOME, permission_mode
# default, cwd=/Users/phillipg/phillipg_mbp):
#
#   cat ~/.ssh/id_rsa          -> deny      (.ssh IS in secretpath's dir list)
#   cat ~/.aws/credentials     -> abstain
#   cat ~/.gnupg/secring.gpg   -> abstain
#   cat ~/.kube/config         -> abstain
#   cat ~/.netrc               -> abstain
#
# All five parent directories were ALREADY in the nix-managed
# sandbox.filesystem.denyRead that patheval.LoadSandboxFilesystemConfig loads and
# that the secrets rule already held an evaluator for. They abstained because
# secretpath.IsSecret GATED whether the deny-list was consulted at all — the LEXICAL
# list sat UPSTREAM of the CONFIGURED one. That inversion is the defect.
#
# TWO SECTIONS:
#
#  1. SYNTHETIC config, in a throwaway HOME. This is the section that PROVES the
#     acceptance criterion, because it can name `.kube` in denyRead — the real
#     machine config does not (see section 2). Read the deny/abstain pairs as the
#     whole point: the ONLY difference between a screened and an unscreened path
#     here is the config, not the code.
#  2. The REAL machine config, read-only. `.aws` / `.gnupg` are already in it, so
#     those rows show the live coverage this bead unlocks TODAY with no config edit.
#     `.kube` / `.docker` / `.netrc` are NOT in it yet, so those rows stay abstain
#     and the remaining coverage is a CONFIG EDIT owed in phillipg-nix-ziprecruiter
#     (machines/phillipg-mbp-02/default.nix, option claude-code.settings.sandbox).
#     That edit is DEFERRED: that canonical clone is dirty, and CLAUDE.md R-3
#     forbids an agent touching a dirty canonical clone.
#
# ISOLATION: XDG_DATA_HOME points at a throwaway directory on EVERY invocation, so
# probe rows land in a scratch asks.db and never reach the shared production corpus.
# NOTHING here runs `ceta evaluate` / `baseline` / `compare` — all three open the
# production asklog READ-WRITE (bead pg2-cbihz).
set -uo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-fhb9q-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver) || exit 1
export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

# probe <label> <cwd> <command> — prints the decision plus the deciding module.
probe() {
  local label="$1" cwd="$2" cmd="$3" out
  out="$(jq -cn --arg c "$cmd" --arg w "$cwd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-fhb9q-probe",cwd:$w,permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  local d m
  d="$(printf '%s' "$out" | jq -r '.hookSpecificOutput.permissionDecision // .permissionDecision // "abstain"' 2>/dev/null)"
  m="$(printf '%s' "$out" | jq -r '.hookSpecificOutput.module // .module // ""' 2>/dev/null)"
  printf '  %-46s -> %-8s %s\n' "$label" "$d" "${m:+($m)}"
}

# ---------------------------------------------------------------------------
# 1. SYNTHETIC config — the acceptance criterion.
# ---------------------------------------------------------------------------
synth="$work/home"
mkdir -p "$synth/.claude" "$synth/.kube" "$synth/.docker" "$synth/.aws" "$synth/notes"
: >"$synth/.kube/config"
: >"$synth/.docker/config.json"
: >"$synth/.aws/credentials"
: >"$synth/.netrc"
: >"$synth/notes/README.md"
jq -n --arg h "$synth" '{sandbox:{filesystem:{
  denyRead: [($h+"/.kube"), ($h+"/.docker"), ($h+"/.aws"), ($h+"/.netrc")],
  denyWrite:[($h+"/.kube"), ($h+"/.docker"), ($h+"/.aws"), ($h+"/.netrc")]}}}' >"$synth/.claude/settings.json"

echo '=== 1. SYNTHETIC config naming .kube/.docker/.aws/.netrc in denyRead ==='
echo '    MUST CHANGE: abstain -> deny. None of these is in secretpath, so before'
echo '    this bead the configured deny-list was never consulted for them at all.'
(
  export HOME="$synth"
  probe 'cat ~/.kube/config (THE criterion)' "$synth" 'cat ~/.kube/config'
  probe 'cat <abs>/.kube/config' "$synth" "cat $synth/.kube/config"
  probe 'cat ~/.docker/config.json' "$synth" 'cat ~/.docker/config.json'
  probe 'cat ~/.aws/credentials' "$synth" 'cat ~/.aws/credentials'
  probe 'cat ~/.netrc (a deny-listed FILE)' "$synth" 'cat ~/.netrc'
  echo
  echo '    MUST NOT CHANGE: under HOME but under no deny prefix — the arm must not'
  echo '    sweep the whole home directory in. (`allow` rather than abstain because'
  echo '    the synthetic HOME is also the cwd and so the projectRoot; what matters'
  echo '    is that it is NOT deny.)'
  probe 'cat ~/notes/README.md' "$synth" 'cat ~/notes/README.md'
  probe 'cat ~/.kubeconfig-backup (prefix sibling)' "$synth" 'cat ~/.kubeconfig-backup'
  echo
  echo '    BLAST-RADIUS BOUND: a BARE WORD is never absolutized into the cwd, so a'
  echo '    deny-listed cwd cannot turn `kubectl get secrets` into a hard reject.'
  probe 'kubectl get secrets' "$synth/.kube" 'kubectl get secrets'
)
echo

# ---------------------------------------------------------------------------
# 2. REAL machine config — what lands today vs. what is still owed.
# ---------------------------------------------------------------------------
#
# THE CWD IS THE BEAD'S MEASURED ONE and it is load-bearing, not incidental. With
# cwd=$HOME, patheval.DetectProjectRoot finds no `.git` and FALLS BACK to the cwd,
# so the whole home directory becomes projectRoot / PathReadWrite and safecmds
# APPROVES `cat ~/anything` — which hides the very distinction this section makes.
# The workspace root is outside $HOME's zone in the same way the bead measured it.
real_cwd="/Users/phillipg/phillipg_mbp"
echo '=== 2. REAL machine config (~/.claude/settings.json), read-only ==='
echo "    cwd=$real_cwd (the bead's measured cwd — see the note in this script)"
echo '    ALREADY in denyRead — these are the rows this bead unlocks with NO config'
echo '    edit (abstain -> deny):'
probe 'cat ~/.aws/credentials' "$real_cwd" 'cat ~/.aws/credentials'
probe 'cat ~/.gnupg/secring.gpg' "$real_cwd" 'cat ~/.gnupg/secring.gpg'
echo
echo '    NOT YET in denyRead — still abstain. Closing these is the DEFERRED config'
echo '    edit in phillipg-nix-ziprecruiter (dirty canonical clone; CLAUDE.md R-3):'
probe 'cat ~/.kube/config' "$real_cwd" 'cat ~/.kube/config'
probe 'cat ~/.docker/config.json' "$real_cwd" 'cat ~/.docker/config.json'
probe 'cat ~/.netrc' "$real_cwd" 'cat ~/.netrc'
echo
echo '    UNCHANGED — the lexical arms, which this bead does not touch:'
probe 'cat ~/.ssh/id_rsa (lexical AND deny-listed)' "$real_cwd" 'cat ~/.ssh/id_rsa'
probe 'cat ~/.claude/.credentials (lexical only)' "$real_cwd" 'cat ~/.claude/.credentials'
probe 'cat README.md' "$pkg_root" 'cat README.md'
echo
echo '    PRE-EXISTING, NOT THIS BEAD: with cwd=$HOME the projectRoot fallback makes'
echo '    the whole home directory read-write, so an UNCONFIGURED credential dir'
echo '    auto-APPROVES. The deny-list still wins over the zone, which is why the'
echo '    configured rows below stay deny — that contrast is the argument for'
echo '    finishing the deferred config edit rather than relying on the zone model.'
probe 'cwd=$HOME: cat ~/.kube/config (unconfigured)' "$HOME" 'cat ~/.kube/config'
probe 'cwd=$HOME: cat ~/.aws/credentials (configured)' "$HOME" 'cat ~/.aws/credentials'
echo

echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"

# ===========================================================================
# MEASURED 2026-08-14 on branch drain/ceta-wave2. `base` is the same worktree with
# ONLY this bead's three files reverted to HEAD (other agents' concurrent wave-2
# changes present in both), so the delta below is this bead's alone.
#
#   SECTION 1 (synthetic config)                   base      -> patched
#     cat ~/.kube/config                           ALLOW     -> deny
#     cat <abs>/.kube/config                       ALLOW     -> deny
#     cat ~/.docker/config.json                    ALLOW     -> deny
#     cat ~/.aws/credentials                       ALLOW     -> deny
#     cat ~/.netrc                                 ALLOW     -> deny
#     cat ~/notes/README.md                        allow     -> allow
#     cat ~/.kubeconfig-backup                     allow     -> allow
#     kubectl get secrets (cwd under a deny tree)  allow     -> allow
#     Read  ~/.kube/config                         deny      -> deny
#     Write ~/.kube/config                         deny      -> deny
#
#   TWO THINGS TO READ OFF SECTION 1, both worth more than the headline:
#
#   (a) THE BASE VERDICT IS `ALLOW`, NOT `abstain`. In this fixture the synthetic
#       HOME is also the cwd, so DetectProjectRoot's fallback makes it the
#       projectRoot and safecmds AUTO-APPROVED `cat ~/.kube/config` outright. The
#       bead measured `abstain` because its cwd was the workspace root, outside
#       $HOME's zone. So the deny-list is the ONLY control that holds when an agent
#       is started in $HOME — the zone model does not — and section 2's
#       `cwd=$HOME` pair pins exactly that contrast (unconfigured: allow -> allow;
#       configured: allow -> deny).
#   (b) THE `Read`/`Write` TOOL ROWS DO NOT MOVE — they were already `deny` at
#       base, because pathsafety consults the same deny-list for the FILE and
#       SEARCH tools. A Bash ARGUMENT path never reaches pathsafety, so BASH IS
#       THE WHOLE OF THE MEASURED GAP. The config arm is applied uniformly across
#       tool kinds anyway (the ruling says "ANY path"), which for the file tools
#       moves only the deciding module (pathsafety -> secrets), never the verdict.
#
#   SECTION 2 (real machine config, cwd=/Users/phillipg/phillipg_mbp)
#     cat ~/.aws/credentials                       abstain   -> deny
#     cat ~/.gnupg/secring.gpg                     abstain   -> deny
#     cat ~/.kube/config                           abstain   -> abstain  (owed)
#     cat ~/.docker/config.json                    abstain   -> abstain  (owed)
#     cat ~/.netrc                                 abstain   -> abstain  (owed)
#     cat ~/.ssh/id_rsa                            deny      -> deny
#     cat ~/.claude/.credentials                   ask       -> ask
#     cat README.md                                allow     -> allow
#     cwd=$HOME cat ~/.kube/config (unconfigured)  allow     -> allow
#     cwd=$HOME cat ~/.aws/credentials (configured) allow    -> deny
#
#   AND ONE ROW THAT IS NOT A CREDENTIAL AT ALL:
#     cat ~/Documents/notes.txt                    abstain   -> deny
#   ~/Documents is in this machine's denyRead alongside ~/Pictures, ~/finance,
#   ~/Google Drive, ~/My Drive, ~/.Trash and ~/Library/{Keychains,Cookies}. Those
#   are privacy entries rather than credential stores, so making the deny-list
#   authoritative screens them too. That is CORRECT — the user deny-listed them —
#   and it is the LARGEST expected movement class in a corpus replay, so do not
#   read a `~/Documents` transition as a defect.
#
# THE WHOLE-CORPUS REPLAY IS NOT RUN HERE — it needs a SECOND tree to diff against,
# so it cannot live in a one-tree script. The recipe is at the bottom of
# scripts/probe-pg2-wq3ki.sh. What a replay of THIS bead should look for:
#   - ZERO transitions toward `allow`. The config arm can only ever convert an
#     Abstain into a Reject, so any row that became less restrictive is a defect;
#   - transitions toward `deny` ONLY on rows whose reason names a path under a
#     configured denyRead/denyWrite prefix. On this machine that list is broader
#     than credentials — it also holds ~/Documents, ~/Pictures, ~/finance,
#     ~/Google Drive, ~/My Drive, ~/.Trash and ~/Library/{Keychains,Cookies} — so
#     a `cat ~/Documents/...` row moving abstain -> deny is CORRECT (the user
#     deny-listed it) and is the largest expected movement class. Sample it and
#     confirm each moved row's path really is under a configured prefix;
#   - ZERO movement on rows with no path-shaped argument.
