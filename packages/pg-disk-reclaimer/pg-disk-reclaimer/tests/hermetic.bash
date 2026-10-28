# shellcheck shell=bash
# Shared test isolation for every pg-disk-reclaimer bats suite (bead pg2-kb5dby).
#
# Why: the tool reads its registry from ${XDG_CONFIG_HOME:-$HOME/.config} and runs
# the commands that registry names (`reclaim --apply` runs removeCommand), so a
# test that reaches the CLI with an AMBIENT environment can run the operator's real
# registry. That happened once, by hand, with an ambient XDG_CONFIG_HOME. Every
# suite therefore isolates its environment in setup() and then proves it did.
#
# Usage, in a suite's setup(), BEFORE sourcing pg-disk-reclaimer.bash (which reads
# its PGDR_* tunables at source time):
#
#   load 'hermetic'
#   setup() {
#     TEST_DIR="$(mktemp -d)"
#     pgdr_test_isolate "$TEST_DIR"
#     pgdr_test_assert_hermetic "$TEST_DIR"
#     ...
#   }
#
# The registry commands the suites install are `echo`/`touch` into the temp root;
# the program under test stays the real one (a mock of it would prove nothing).

# Exported PGDR_* variables that are test-harness inputs, not tool tunables, and so
# survive isolation: the REQUIRE switches (enforcement of skipped tiers) and the
# nix-injected sweep library path.
pgdr_test_is_harness_var() {
  case "$1" in
  PGDR_REQUIRE_* | PGDR_TMP_SWEEP_LIB) return 0 ;;
  *) return 1 ;;
  esac
}

# pgdr_test_isolate <root>: points every location the tool or its helpers write or
# read by environment at a fresh directory under <root>, leaves XDG_CONFIG_HOME
# unset (so the registry path is the default $HOME/.config/pg-disk-reclaimer under
# the temp HOME; a test that wants XDG sets its own, under <root>), and unsets
# every exported PGDR_* tunable that is not a harness variable.
pgdr_test_isolate() {
  local root="$1" name
  mkdir -p "$root/home" "$root/xdg-state" "$root/xdg-cache" "$root/xdg-data" "$root/tmp"
  export HOME="$root/home"
  export XDG_STATE_HOME="$root/xdg-state"
  export XDG_CACHE_HOME="$root/xdg-cache"
  export XDG_DATA_HOME="$root/xdg-data"
  export TMPDIR="$root/tmp"
  unset XDG_CONFIG_HOME
  while IFS= read -r name; do
    pgdr_test_is_harness_var "$name" || unset "$name"
  done < <(compgen -e | grep '^PGDR_' || true)
}

# pgdr_test_assert_hermetic <root>: returns 1 with a message, before any command
# under test can run, unless HOME, TMPDIR, the XDG state/cache/data homes and the
# registry path the tool would read are all under <root>, and no tunable PGDR_*
# variable is exported. A failing setup() fails its test.
pgdr_test_assert_hermetic() {
  local root="$1" name value registry_root
  for name in HOME TMPDIR XDG_STATE_HOME XDG_CACHE_HOME XDG_DATA_HOME; do
    value="${!name:-}"
    case "$value" in
    "$root"/*) ;;
    *)
      echo "not hermetic: $name='$value' is not under $root" >&2
      return 1
      ;;
    esac
  done
  registry_root="${XDG_CONFIG_HOME:-$HOME/.config}"
  case "$registry_root" in
  "$root"/*) ;;
  *)
    echo "not hermetic: the registry would be read from '$registry_root', not under $root" >&2
    return 1
    ;;
  esac
  while IFS= read -r name; do
    if ! pgdr_test_is_harness_var "$name"; then
      echo "not hermetic: $name is exported" >&2
      return 1
    fi
  done < <(compgen -e | grep '^PGDR_' || true)
  return 0
}
