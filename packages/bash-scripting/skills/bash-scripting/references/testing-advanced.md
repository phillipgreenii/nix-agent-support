# Advanced Testing Patterns

Read this when writing tests for scripts that source libraries, or when building shared test infrastructure for a module.

## Standard test_helper.bash

A copy-ready template lives in `assets/test_helper.bash`. Drop it into `test-support/` (module-level) or alongside tests (per-script). Key responsibilities:

- Resolve `SCRIPTS_DIR` (injected by nix check, fall back to relative path)
- Resolve `LIB_PATH` (composed file from nix, or source directory locally)
- Create `TEST_DIR="$(mktemp -d)"`, override `HOME`, clean up in `teardown`

## Loading shared test support

Tests in a module directory load the shared helper like this:

```bash
if [[ -n ${TEST_SUPPORT:-} ]]; then
  source "$TEST_SUPPORT/test_helper.bash"
else
  source "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper.bash"
fi
```

The framework exports `TEST_SUPPORT` in nix check derivations and copies `*.bash` from the test-support directory alongside tests so `load test_helper` resolves.

## Library wrapper pattern

Scripts that source libraries need a wrapper for testing that replicates the builder's composition (the builder sources `.bash` libraries before the script's `.sh`):

```bash
create_cmd_wrapper() {
  local resolved_lib
  if [[ -d ${LIB_PATH} ]]; then
    resolved_lib="${LIB_PATH}/module-lib.bash"
  else
    resolved_lib="${LIB_PATH%%:*}"  # take first if colon-separated
  fi

  cat >"$TEST_DIR/run_cmd" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
script_name="\$1"
shift
source "${resolved_lib}"
source "${SCRIPTS_DIR}/\${script_name}.sh"
WRAPPER
  chmod +x "$TEST_DIR/run_cmd"
}
```

Use in tests: `run "$TEST_DIR/run_cmd" my-command arg1 arg2`

## Mock isolation gotcha

If the script under test runs `git clean -fd`, mocks placed inside the test's git working directory get deleted mid-test. Put mocks in a **separate** temp directory:

```bash
MOCK_BIN=$(mktemp -d)
cat > "$MOCK_BIN/mock-cmd" <<'EOF'
#!/usr/bin/env bash
echo "mocked"
EOF
chmod +x "$MOCK_BIN/mock-cmd"
export PATH="$MOCK_BIN:$PATH"
```

## Bats footguns

- Don't shadow bats built-ins like `fail` or `skip` with custom helpers — bats crashes silently. Use names like `failing_step`, `mock_failure`.
- Don't test `--version` — the builder injects the handler at build time, and tests run against raw source.
