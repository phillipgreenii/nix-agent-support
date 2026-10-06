# shellcheck shell=bash
# git-fixture-harness.bash — shared hermetic-by-construction bats git-fixture
# harness (design: pg2-gucfd, decided 2026-08-29; implementation: epic
# pg2-ljn47 / work packet pg2-ljn47.1).
#
# WHY THIS EXISTS: 17 bats suites across 4 repos hand-rolled their own git
# fixture with no GIT_DIR-family scrub at all, and the 12 that WERE protected
# copy-pasted the same six-line env-var-unset block into three different
# repos (pg2-jjlm8). Three separate passes over even the suites in THIS repo
# (pg2-klyn6, pg2-7hr6o, pg2-zjcp6) each closed one half of the problem and
# left the rest open — because enumerate-and-unset is only ever as complete
# as the enumeration. pg2-8wnhc's ruling ("hermetic by construction, not by
# scrubbing") is applied here the way it was already applied to the Go side's
# x/gittest (D2/D6 on pg2-67h4y): no fixture created by this library can
# reach a real repository or a real HOME, regardless of which env vars a
# future git version invents.
#
# THE THREE STRUCTURAL MECHANISMS (pg2-gucfd D2):
#   1. GIT_CEILING_DIRECTORIES pinned to a physical path that contains every
#      fixture this call creates. git's own repository-discovery walk-up
#      refuses to search — or search past — a ceiling directory, so a
#      fixture's real repo can never be shadowed by (or accidentally resolve
#      to) something outside the fixture tree, even from a non-repo
#      subdirectory a caller forgot to `-C` into explicitly.
#   2. gfh_reset_env rebuilds the exported environment from an ALLOWLIST, not
#      a denylist of known-leaky GIT_*/XDG_*/CI_* names. Every exported
#      variable not on the allowlist is unset — including any GIT_DIR-family
#      variable inherited from a linked-worktree hook environment (the exact
#      pg2-jjlm8/pg2-12795 mechanism), and including one this library's
#      author never heard of, because the allowlist names what MAY survive
#      rather than what must be removed.
#   3. HOME is a fresh, empty mktemp directory on every gfh_setup call, and
#      GIT_CONFIG_SYSTEM is pointed at /dev/null — the one config scope an
#      env reset cannot neutralise (a real /etc/gitconfig).
#
# FIXTURE IDENTITY (pg2-gucfd D3): every repo this library creates gets a
# PER-SUITE distinct identity under the domain "bashfixture.invalid" —
# deliberately different from the Go side's "gitfixture.invalid" (x/gittest),
# so a leaked identity string alone tells forensics which side produced it,
# and a distinct per-suite local part means a leak identifies its own
# offending suite without a workspace-wide grep.
#
# NO AUTOMATED ENFORCEMENT (pg2-gucfd D4): nothing gates a bats file on using
# this library. Suites adopt it by convention/review when touched — see the
# beads blocked on this one landing (pg2-31f13, pg2-whgx5, pg2-vn1nk) for the
# migration work.
#
# CONTRACT — see lib/tests/test-git-fixture-harness-lib.bats for the
# executable spec.
#
#   gfh_setup <suite-name>
#     One call per bats test, before any git call the test makes. Sets:
#       GFH_ROOT   — outer fixture root (mktemp -d); rm -rf'd by gfh_teardown
#       GFH_WORK   — the GIT_CEILING_DIRECTORIES boundary itself
#                    (GFH_ROOT/work); everything below is confined here
#       GFH_REPO   — a real, initialised, single-commit repo at
#                    GFH_WORK/repo, hooks disabled, identity
#                    "<suite-name>@bashfixture.invalid"
#       GFH_SUITE  — the suite name passed in
#       HOME                    (exported) — fresh empty dir under GFH_WORK
#       GIT_CEILING_DIRECTORIES (exported) — physical path of GFH_WORK
#       GIT_CONFIG_SYSTEM       (exported) — /dev/null
#     A caller needing anything beyond this minimal set (a mock PATH prefix,
#     XDG_STATE_HOME, a second GIT_CONFIG_GLOBAL, ...) exports it AFTER this
#     call returns — the same "opt in explicitly, never touch the real one"
#     contract every hermetic suite in this repo already follows.
#
#   gfh_teardown
#     rm -rf "$GFH_ROOT". Call from the test's own teardown().
#
#   gfh_init_repo <path> <suite-name> [--no-identity]
#     Lower-level primitive: git-init a repo at <path> with hooks disabled
#     and the per-suite fixture identity, WITHOUT touching HOME, the wider
#     exported environment, or GIT_CEILING_DIRECTORIES. For a caller that
#     needs a SECOND repository inside one gfh_setup call — gfh_setup itself
#     calls this for GFH_REPO. (A bare remote is gfh_init_bare, a clone is
#     gfh_clone; do not hand-roll either.)
#     ONE deliberate exception to "does not touch env" (pg2-510ya): it DOES
#     unset the GIT_DIR-family vars (GIT_DIR/GIT_WORK_TREE/GIT_COMMON_DIR/
#     GIT_INDEX_FILE/GIT_PREFIX/GIT_OBJECT_DIRECTORY) before its own git
#     calls, unconditionally — not only when gfh_setup already scrubbed them.
#     `-C <path>` does NOT protect against a leaked GIT_DIR: when GIT_DIR is
#     set, git's repository discovery is pinned there and bypasses `-C`
#     entirely, so an inherited GIT_DIR (e.g. from a linked-worktree hook
#     environment, the pg2-jjlm8/pg2-12795 mechanism) silently redirects
#     "init a fixture repo at <path>" onto the leaked real repo instead —
#     confirmed the exact corruption pg2-510ya reported. A caller that uses
#     this primitive standalone (its whole documented purpose) gets no other
#     scrub, so this one has to live here rather than depending on the caller
#     having called gfh_setup/gfh_reset_env first. gfh_init_bare and
#     gfh_clone below carry the same guard for the same reason.
#     --no-identity (pg2-1msck): "no identity resolvable" mode, for a test of
#     code that must handle a repo where git cannot determine an author. The
#     repo gets NO local user.email/user.name and user.useConfigOnly=true, so
#     git refuses to guess one from the hostname/GECOS either — a commit or
#     `git var GIT_AUTHOR_IDENT` in it fails deterministically. It can only
#     stay unresolved if nothing else supplies an identity: run gfh_setup
#     first (fresh HOME, XDG_CONFIG_HOME and GIT_AUTHOR_*/GIT_COMMITTER_*
#     scrubbed, system config neutralised) and do not export an identity
#     afterwards. <suite-name> is still required positionally but unused.
#
#   gfh_init_bare <path>
#     (pg2-1msck) Create a BARE repository at <path> — a push/fetch remote —
#     with branch main and hooks disabled (so no receive-side hook, planted or
#     templated, ever runs on a push into it). Same GIT_DIR-family guard as
#     gfh_init_repo. No identity: a bare repo has no commits of its own.
#     Keep <path> under GFH_WORK (the ceiling boundary), e.g.
#     "$GFH_WORK/remote.git".
#
#   gfh_clone <src> <dest> <suite-name> [--no-identity]
#     (pg2-1msck) Clone the repo (or bare remote) at <src> into <dest> with
#     hooks disabled — including DURING the clone, so a post-checkout hook
#     cannot fire — and the per-suite identity set locally, exactly as
#     gfh_init_repo would (--no-identity as above). Same GIT_DIR-family
#     guard. Cloning an empty bare remote is fine (the clone is unborn on
#     main). Keep <dest> under GFH_WORK.
#
#   gfh_save_env <VAR>... / gfh_restore_env
#     (pg2-1msck) Preserve exported variables across gfh_setup. gfh_reset_env
#     wipes every exported variable not on its allowlist, including ones a
#     suite legitimately needs: SCRIPTS_DIR, TEST_SUPPORT, and the nix-injected
#     tool paths a sandboxed bats check exports. The pattern, in setup():
#         gfh_save_env SCRIPTS_DIR MY_NIX_INJECTED_PATH   # BEFORE gfh_setup
#         gfh_setup "my-suite"
#         gfh_restore_env                                 # AFTER gfh_setup
#     gfh_restore_env re-exports each saved variable that was set when saved
#     and leaves unset any that was not. The saved copies live in unexported
#     GFH_SAVED_* variables, which gfh_reset_env never touches. (Doing it by
#     hand — copy to an unexported name, gfh_setup, copy back — is equivalent;
#     modules/pg-hooks/test-support/pg-hooks-test-helper.bash does that with
#     PGH_T_SAVED_*.) Restore ONLY what the suite needs: re-exporting a
#     GIT_*-family variable re-opens the leak the harness exists to close.
#
#   gfh_identity_email <suite-name> / gfh_identity_name <suite-name>
#     Pure functions printing the email/name half of the fixture identity.
#     Useful for a test asserting the identity it expects to see.
#
#   gfh_reset_env
#     Rebuilds the exported environment from the allowlist. gfh_setup calls
#     this itself; exposed separately for a caller that wants the scrub
#     without the rest of gfh_setup's repo-creation side effects.

# The bash-side fixture-identity domain (D3).
GFH_IDENTITY_DOMAIN="bashfixture.invalid"

# Exported-variable names gfh_reset_env preserves across its rebuild.
# Everything else currently exported in the calling shell is unset.
#
#   - BATS_*  — bats' own machinery. gfh_reset_env runs IN-PROCESS with bats
#     (there is no subshell to reset instead), so unsetting these would break
#     `run`, `skip`, and tmpdir resolution for the rest of the current test
#     and every later test bats runs in the same file.
#   - GFH_*   — this library's own state (GFH_ROOT, GFH_WORK, GFH_REPO,
#     GFH_SUITE), so a caller can read them back after the reset.
#   - The bare-minimum shell/process plumbing git, bash and coreutils need to
#     keep functioning at all.
_gfh_allow_exact=(
  PATH
  SHELL
  TERM
  TMPDIR
  PWD
  OLDPWD
  IFS
  LANG
  LC_ALL
  LC_CTYPE
  USER
  LOGNAME
  SHLVL
)

# Rebuild the exported environment from the allowlist above. Idempotent and
# safe to call more than once. Enumerates EXPORTED shell variables by parsing
# `export -p` (a plain POSIX builtin, unconditionally present) rather than
# `compgen -e`: compgen is a bash programmable-completion builtin that some
# minimal bash builds -- notably nixpkgs' non-interactive `bash` package, as
# used inside a `nix build` sandbox -- do not compile in at all. There
# `compgen -e` fails with "command not found", `$(...)` silently expands to
# nothing, and the whole scrub becomes a no-op -- discovered via pg2-31f13's
# own regression-guard test, which caught the resulting leaked-GIT_DIR
# exactly because it built under `nix build`, not a plain interactive shell.
# Shell functions and unexported locals are never matched by `export -p`.
gfh_reset_env() {
  local line var keep allowed
  while IFS= read -r line; do
    # bash's `export -p` prints lines like `declare -x NAME=value`,
    # `declare -ax NAME=(...)` (exported array), or a bare `declare -x NAME`
    # for an exported-but-unset variable. Match any `-*x*` flag combination.
    if [[ $line =~ ^declare\ -[a-zA-Z]*x[a-zA-Z]*\ ([A-Za-z_][A-Za-z0-9_]*)(=|$) ]]; then
      var="${BASH_REMATCH[1]}"
    else
      continue
    fi
    case "$var" in
    BATS_* | GFH_*) continue ;;
    esac
    keep=0
    for allowed in "${_gfh_allow_exact[@]}"; do
      if [[ $var == "$allowed" ]]; then
        keep=1
        break
      fi
    done
    if [[ $keep -eq 0 ]]; then
      unset "$var"
    fi
  done < <(export -p)
}

# Print the EMAIL half of the per-suite fixture identity.
gfh_identity_email() {
  local suite="$1"
  printf '%s@%s\n' "$suite" "$GFH_IDENTITY_DOMAIN"
}

# Print the NAME half of the per-suite fixture identity.
gfh_identity_name() {
  local suite="$1"
  printf '%s fixture\n' "$suite"
}

# Unset the GIT_DIR family (pg2-510ya). See the gfh_init_repo CONTRACT entry.
_gfh_unset_git_dir_family() {
  unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_PREFIX GIT_OBJECT_DIRECTORY
}

# Apply the per-suite identity (D3) to the repo at $1, or — with
# --no-identity — configure it so no identity can resolve (pg2-1msck).
_gfh_apply_identity() {
  local repo="$1" suite="$2" mode="${3:-}"
  case "$mode" in
  "")
    command git -C "$repo" config user.email "$(gfh_identity_email "$suite")"
    command git -C "$repo" config user.name "$(gfh_identity_name "$suite")"
    ;;
  --no-identity)
    command git -C "$repo" config user.useConfigOnly true
    ;;
  *)
    echo "git-fixture-harness: unknown option '$mode' (expected --no-identity)" >&2
    return 2
    ;;
  esac
}

# Initialise a real git repository at $1 with hooks disabled (D2) and this
# library's per-suite identity (D3) as its LOCAL user.email/user.name. Does
# NOT touch HOME, the exported environment, or GIT_CEILING_DIRECTORIES —
# gfh_setup below calls this for GFH_REPO; a caller wanting an EXTRA repo
# under one gfh_setup call calls this directly for it (a bare remote is
# gfh_init_bare, a clone is gfh_clone). $3 = --no-identity: see the CONTRACT.
gfh_init_repo() {
  local repo="$1" suite="$2" mode="${3:-}"
  mkdir -p "$repo"
  # By-construction guard (pg2-510ya): see the CONTRACT block above this
  # function for why this is here unconditionally, not only relied on via
  # gfh_setup's earlier gfh_reset_env call. Idempotent with that path (these
  # are already unset there); load-bearing for a standalone caller, which is
  # this primitive's own documented, intended use.
  _gfh_unset_git_dir_family
  command git -C "$repo" init -q -b main
  # Hooks disabled (D2): core.hooksPath pointed at a non-directory means git
  # can never resolve a hook file under it, so no hook — planted, inherited,
  # or supplied by an init template — ever runs for this repo.
  command git -C "$repo" config core.hooksPath /dev/null
  _gfh_apply_identity "$repo" "$suite" "$mode"
}

# Create a bare repository at $1 (a push/fetch remote), branch main, hooks
# disabled (pg2-1msck). No identity: a bare repo has no commits of its own.
gfh_init_bare() {
  local repo="$1"
  mkdir -p "$repo"
  _gfh_unset_git_dir_family
  command git init -q --bare -b main "$repo"
  command git -C "$repo" config core.hooksPath /dev/null
}

# Clone $1 into $2 with hooks disabled — including during the clone itself —
# and the per-suite identity (or --no-identity) set locally (pg2-1msck).
gfh_clone() {
  local src="$1" dest="$2" suite="$3" mode="${4:-}"
  _gfh_unset_git_dir_family
  command git clone -q -c core.hooksPath=/dev/null "$src" "$dest" 2>/dev/null || return
  command git -C "$dest" config core.hooksPath /dev/null
  _gfh_apply_identity "$dest" "$suite" "$mode"
}

# Save the named exported variables so they survive gfh_setup's allowlist
# reset (pg2-1msck). Call BEFORE gfh_setup. A variable that is unset is
# recorded as unset.
gfh_save_env() {
  local name
  GFH_SAVED_NAMES=("$@")
  for name in "$@"; do
    if [[ -n ${!name+x} ]]; then
      printf -v "GFH_SAVED_VAL_$name" '%s' "${!name}"
      printf -v "GFH_SAVED_SET_$name" '%s' 1
    else
      printf -v "GFH_SAVED_SET_$name" '%s' 0
    fi
  done
}

# Re-export what gfh_save_env saved (pg2-1msck). Call AFTER gfh_setup.
gfh_restore_env() {
  local name set_var val_var
  for name in ${GFH_SAVED_NAMES[@]+"${GFH_SAVED_NAMES[@]}"}; do
    set_var="GFH_SAVED_SET_$name"
    val_var="GFH_SAVED_VAL_$name"
    if [[ ${!set_var:-0} == 1 ]]; then
      export "$name=${!val_var}"
    else
      unset "$name"
    fi
  done
}

# Full harness setup for ONE bats test. See the CONTRACT block above this
# function for exactly what it sets.
gfh_setup() {
  local suite="$1"
  # shellcheck disable=SC2034  # read by callers after gfh_setup returns, not in this file
  GFH_SUITE="$suite"

  GFH_ROOT="$(mktemp -d)"

  gfh_reset_env

  # GFH_WORK, not GFH_ROOT itself, is the ceiling boundary: this lets a
  # regression test plant a decoy repository IN GFH_ROOT (above the ceiling,
  # but still entirely inside this call's own private mktemp tree — never
  # touching the shared system temp directory) to prove the ceiling holds,
  # without any risk to concurrently running tests or processes.
  GFH_WORK="$GFH_ROOT/work"
  mkdir -p "$GFH_WORK"

  GIT_CEILING_DIRECTORIES="$(cd "$GFH_WORK" && pwd -P)"
  export GIT_CEILING_DIRECTORIES

  # The one config scope an env reset cannot neutralise: a real
  # /etc/gitconfig is a file, not an environment variable.
  GIT_CONFIG_SYSTEM=/dev/null
  export GIT_CONFIG_SYSTEM

  HOME="$GFH_WORK/home"
  mkdir -p "$HOME"
  export HOME

  GFH_REPO="$GFH_WORK/repo"
  gfh_init_repo "$GFH_REPO" "$suite"
  printf 'initial\n' >"$GFH_REPO/file.txt"
  command git -C "$GFH_REPO" add file.txt
  command git -C "$GFH_REPO" commit -q -m "initial"
}

# Tear down everything gfh_setup created for this test. Call from teardown().
gfh_teardown() {
  if [[ -n ${GFH_ROOT:-} ]]; then
    rm -rf "$GFH_ROOT"
  fi
}
