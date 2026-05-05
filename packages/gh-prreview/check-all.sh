#!/usr/bin/env bash
# Check script for gh-prreview
# Usage: ./check-all.sh [--no-fix] [--quick] [--suppress-coverage-check]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# shellcheck source=../../lib/python-checks/check-lib.sh
CHECK_LIB_DIR="${CHECK_LIB_DIR:-$(cd "$SCRIPT_DIR/../../lib/python-checks" && pwd)}"
source "$CHECK_LIB_DIR/check-lib.sh"

PACKAGE_NAME="gh-prreview"
SRC_DIR="src"

check_parse_flags "$@"
detect_runner
check_header
check_ruff_format
check_ruff_lint
check_mypy

if [ "$QUICK_MODE" = true ]; then
  check_pytest tests/unit
else
  check_pytest
fi

check_footer
