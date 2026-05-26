# test_helper.bash — shared setup for pgii-packs/activation.sh bats tests.
#
# Conventions:
#   - $TMP is a per-test tmpdir, cleaned in teardown.
#   - $CITY is a city dir under $TMP (you create N of these).
#   - $SCRIPT is the absolute path to activation.sh.

bats_require_minimum_version 1.5.0

setup() {
  TMP="$(mktemp -d)"
  export TMP
  SCRIPT="${BATS_TEST_DIRNAME}/../activation.sh"
  export SCRIPT
  test -x "$SCRIPT" || {
    echo "activation.sh not executable: $SCRIPT" >&2
    exit 1
  }
}

teardown() {
  [ -n "${TMP:-}" ] && rm -rf "$TMP"
}

# mkCity NAME → echoes the city's path; creates city.toml seeded with arg2 (if given).
mkCity() {
  local name="$1"
  local seed="${2:-}"
  local dir="$TMP/$name"
  mkdir -p "$dir/.gc"
  if [ -n "$seed" ]; then
    printf '%s\n' "$seed" >"$dir/city.toml"
  else
    : >"$dir/city.toml"
  fi
  echo "$dir"
}

# blockExists CITY_TOML PACK_NAME → exit 0 if managed block for PACK_NAME exists.
blockExists() {
  grep -Fq "# BEGIN pgii-pack:$2 (managed)" "$1"
}

# blockPath CITY_TOML PACK_NAME → prints the store path inside the named block.
blockPath() {
  awk -v begin="# BEGIN pgii-pack:$2 (managed)" \
    -v end="# END pgii-pack:$2 (managed)" '
    $0 == begin { in_block = 1; next }
    $0 == end   { in_block = 0; next }
    in_block && /^path = / { gsub(/(^path = "|"$)/, ""); print; exit }
  ' "$1"
}

# packsJson NAME1 PATH1 [NAME2 PATH2 ...] → emits a JSON object.
packsJson() {
  local -a entries=()
  while [ $# -gt 0 ]; do
    entries+=("\"$1\":\"$2\"")
    shift 2
  done
  local IFS=,
  echo "{${entries[*]}}"
}

# citiesJson CITY1 [CITY2 ...] → emits a JSON array of paths.
citiesJson() {
  local -a entries=()
  while [ $# -gt 0 ]; do
    entries+=("\"$1\"")
    shift
  done
  local IFS=,
  echo "[${entries[*]}]"
}
