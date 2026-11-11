// Package cutover is the per-machine switch that hands CETA's recording over to
// the claude-tool-tracker. It is OFF unless the machine's Home Manager module sets
// trackerCutover, which exports EnvVar=1 around the binary: a machine nobody has
// checked keeps every byte of CETA's own recording.
//
// With the switch on, CETA stops writing tool_decisions, outcomes and
// decision_trace_entries, stops flushing rule_errors to its database (they are
// ceta.rule_errors events by then), and defaults --source to the tracker. Its
// database stays readable for history (--source ceta).
package cutover

import "os"

// EnvVar is the environment variable the Home Manager wrapper exports.
const EnvVar = "CETA_TRACKER_CUTOVER"

// Enabled reports whether the cut-over switch is on. Only the exact value "1"
// turns it on; anything else (unset, empty, "0", "true") is off, so a stray value
// can never silently stop CETA's recording.
func Enabled() bool { return os.Getenv(EnvVar) == "1" }
