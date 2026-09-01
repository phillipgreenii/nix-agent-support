# shellcheck shell=bash
# git-fixture-harness.bash -- shared hermetic-by-construction bats git-fixture
# harness (design: pg2-gucfd, decided 2026-08-29; bug: pg2-31f13).
#
# VENDORED COPY: this is a byte-for-byte copy of the canonical library landed
# in phillipg-nix-repo-base at lib/scripts/git-fixture-harness.bash (commit
# ba09073, work packet pg2-ljn47.1). agent-support cannot yet consume that as
# a real cross-repo nix package because, at the time pg2-31f13 was
# implemented, ba09073 existed only in repo-base's local canonical clone (not
# yet pushed to its GitHub remote), so agent-support's `phillipgreenii-nix-base`
# flake input could not lock onto it. Once repo-base pushes and this repo's
# flake.lock is bumped past that rev, migrate every consumer of THIS file onto
# `inputs.phillipgreenii-nix-base.packages.${system}.git-fixture-harness`
# (mirroring how `basePkgs.pnwf`/`basePkgs.wsplan` are threaded through
# flake.nix's overlay) and delete the three vendored copies (this one, the
# integrate-branch-support one, and the top-level tests/support one).
#
# WHY THIS EXISTS: see pg2-67h4y (full mechanism write-up) and pg2-gucfd (the
# bash/bats design decision) for the defect class: a `git commit` issued from
# a LINKED WORKTREE exports GIT_DIR/GIT_INDEX_FILE (and friends) into the
# pre-commit hook environment, and every child process -- including a bats run
# launched by that hook -- inherits them. `-C <dir>`, cmd.Dir, `cd`, and an
# explicit path argument all lose to those env vars; git's own repository
# discovery consults the environment FIRST. A bats fixture with no isolation
# at all can therefore re-init, reconfigure, or commit onto the REAL canonical
# repository instead of its own throwaway tempdir.
#
# THE THREE STRUCTURAL MECHANISMS (pg2-gucfd D2), applied here exactly as in
# the canonical library:
#   1. GIT_CEILING_DIRECTORIES pinned to a physical path containing every
#      fixture this call creates -- git's own discovery walk refuses to
#      search past a ceiling directory, so a fixture can never be shadowed by
#      (or resolve to) something outside its own tree.
#   2. gfh_reset_env rebuilds the exported environment from an ALLOWLIST, not
#      a denylist of known-leaky GIT_*/XDG_*/CI_* names -- every exported
#      variable not on the allowlist is unset, including any GIT_DIR-family
#      variable inherited from a linked-worktree hook environment, and
#      including one this library's author never heard of.
#   3. HOME is a fresh, empty mktemp directory on every gfh_setup call, and
#      GIT_CONFIG_SYSTEM is pointed at /dev/null.
#
# FIXTURE IDENTITY (pg2-gucfd D3): every repo this library creates gets a
# PER-SUITE distinct identity under the domain "bashfixture.invalid".
#
# NO AUTOMATED ENFORCEMENT (pg2-gucfd D4): nothing gates a bats file on using
# this library; suites adopt it by convention/review when touched.
#
# CONTRACT:
#
#   gfh_setup <suite-name>
#     One call per bats test, before any git call the test makes. Sets:
#       GFH_ROOT   -- outer fixture root (mktemp -d); rm -rf'd by gfh_teardown
#       GFH_WORK   -- the GIT_CEILING_DIRECTORIES boundary itself
#                    (GFH_ROOT/work); everything below is confined here
#       GFH_REPO   -- a real, initialised, single-commit repo at
#                    GFH_WORK/repo, hooks disabled, identity
#                    "<suite-name>@bashfixture.invalid"
#       GFH_SUITE  -- the suite name passed in
#       HOME                    (exported) -- fresh empty dir under GFH_WORK
#       GIT_CEILING_DIRECTORIES (exported) -- physical path of GFH_WORK
#       GIT_CONFIG_SYSTEM       (exported) -- /dev/null
#     A caller needing anything beyond this minimal set exports it AFTER this
#     call returns -- gfh_reset_env has already scrubbed everything else not
#     on the allowlist, including any pre-existing exported var the caller's
#     own setup() computed earlier (e.g. SCRIPTS_DIR injected by a nix
#     check) -- capture such a value into a plain (non-exported) local
#     BEFORE calling gfh_setup, then re-export it after.
#
#   gfh_teardown
#     rm -rf "$GFH_ROOT". Call from the test's own teardown().
#
#   gfh_init_repo <path> <suite-name>
#     Lower-level primitive: git-init a repo at <path> with hooks disabled
#     and the per-suite fixture identity, WITHOUT touching HOME, env, or
#     GIT_CEILING_DIRECTORIES.
#
#   gfh_identity_email <suite-name> / gfh_identity_name <suite-name>
#     Pure functions printing the email/name half of the fixture identity.
#
#   gfh_reset_env
#     Rebuilds the exported environment from the allowlist, without the rest
#     of gfh_setup's repo-creation side effects (e.g. for a caller that only
#     needs the scrub, not a whole fresh repo -- see
#     tests/behavior-docs-resolve-links.bats).

# The bash-side fixture-identity domain (D3).
GFH_IDENTITY_DOMAIN="bashfixture.invalid"

# Exported-variable names gfh_reset_env preserves across its rebuild.
# Everything else currently exported in the calling shell is unset.
#
#   - BATS_*  -- bats' own machinery. gfh_reset_env runs IN-PROCESS with bats
#     (there is no subshell to reset instead), so unsetting these would break
#     `run`, `skip`, and tmpdir resolution for the rest of the current test.
#   - GFH_*   -- this library's own state, so a caller can read it back.
#   - The bare-minimum shell/process plumbing git, bash and coreutils need.
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

# Initialise a real git repository at $1 with hooks disabled (D2) and this
# library's per-suite identity (D3) as its LOCAL user.email/user.name. Does
# NOT touch HOME, the exported environment, or GIT_CEILING_DIRECTORIES --
# gfh_setup below calls this for GFH_REPO.
gfh_init_repo() {
  local repo="$1" suite="$2"
  mkdir -p "$repo"
  command git -C "$repo" init -q -b main
  # Hooks disabled (D2): core.hooksPath pointed at a non-directory means git
  # can never resolve a hook file under it, so no hook ever runs.
  command git -C "$repo" config core.hooksPath /dev/null
  command git -C "$repo" config user.email "$(gfh_identity_email "$suite")"
  command git -C "$repo" config user.name "$(gfh_identity_name "$suite")"
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
  # but still entirely inside this call's own private mktemp tree) to prove
  # the ceiling holds, without any risk to concurrently running tests.
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
