#!/usr/bin/env bash
# beads-dolt-configure — Configure a bd project to use the shared Dolt server.
# Usage: beads-dolt-configure <database-name> [project-dir] [prefix]
#
# Examples:
#   beads-dolt-configure beads_monorepo /Volumes/ziprecruiter/monorepo zr
#   beads-dolt-configure beads_pg2 /Users/phillipg/phillipg_mbp pg2
#   beads-dolt-configure beads_test /tmp/test-project test

set -euo pipefail

DOLT_PORT="${BEADS_DOLT_PORT:-3307}"
DOLT_DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/beads-dolt"

db_name="${1:?Usage: beads-dolt-configure <database-name> [project-dir] [prefix]}"
if [[ ! $db_name =~ ^[a-zA-Z0-9_]+$ ]]; then
  echo "FATAL: database-name must contain only letters, numbers, and underscores" >&2
  exit 1
fi
project_dir="${2:-.}"
prefix="${3:-}"

echo "Configuring bd in ${project_dir} to use shared Dolt server"
echo "  Database:  ${db_name}"
echo "  Data dir:  ${DOLT_DATA_DIR}"
echo "  Port:      ${DOLT_PORT}"
echo ""

cd "$project_dir"

# Initialize .beads if it doesn't exist
if [ ! -d ".beads" ]; then
  if [ -n "$prefix" ]; then
    echo "  Initializing .beads with prefix '${prefix}'..."
    bd init --prefix "$prefix" --skip-hooks -q
  else
    echo "FATAL: .beads/ not found in ${project_dir} and no prefix provided for init"
    exit 1
  fi
fi

# Initialize database directory if it doesn't exist in the shared data dir
if [ ! -d "${DOLT_DATA_DIR}/${db_name}" ]; then
  echo "  Creating database ${db_name} in ${DOLT_DATA_DIR}..."
  mkdir -p "${DOLT_DATA_DIR}/${db_name}"
  (cd "${DOLT_DATA_DIR}/${db_name}" && dolt init --name "beads" --email "beads@localhost")
fi

# Switch from embedded to server mode if needed (bd dolt set refuses in embedded mode)
if jq -e '.dolt_mode == "embedded"' .beads/metadata.json >/dev/null 2>&1; then
  echo "  Switching from embedded to server mode..."
  jq '.dolt_mode = "server"' .beads/metadata.json >.beads/metadata.json.tmp &&
    mv .beads/metadata.json.tmp .beads/metadata.json
fi

# Configure bd to use the shared server (data-dir is managed by the server, not bd)
bd dolt set port "$DOLT_PORT"
bd dolt set database "$db_name"

echo ""
echo "  ✓ Configured (db=${db_name}, port=${DOLT_PORT})"

# Show final config
bd dolt show --json
