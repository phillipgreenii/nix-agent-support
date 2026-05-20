// Package output centralizes output-format resolution for the pg-pr CLI.
//
// The CLI exposes a global `--json` flag on subcommands plus a global
// `PGPR_OUTPUT` environment variable. When `PGPR_OUTPUT=json` and no
// `--json` flag is passed, JSON output is selected. The explicit
// `--json` flag wins when present (the cobra flag bool is true).
//
// Precedence:
//  1. --json flag set on the command line (flag == true) -> JSON
//  2. PGPR_OUTPUT=json env var, no --json flag           -> JSON
//  3. Otherwise                                          -> human-readable
//
// Because cobra's BoolVar exposes only the resolved value, we treat
// flag==true as "explicit JSON" and flag==false as "fall back to env".
// In practice `--json=false` is never used, so this simplification is
// safe and avoids needing pflag.Lookup() at every call site.
package output

import "os"

// EnvVar is the environment variable name consulted when the --json
// flag is not set.
const EnvVar = "PGPR_OUTPUT"

// Resolve returns true when JSON output should be emitted. flag is the
// value of the --json cobra flag (true when the user passed --json).
//
// If the flag is true, JSON wins. Otherwise the PGPR_OUTPUT env var is
// consulted; the value "json" (case-sensitive) selects JSON output.
func Resolve(flag bool) bool {
	if flag {
		return true
	}
	return os.Getenv(EnvVar) == "json"
}
