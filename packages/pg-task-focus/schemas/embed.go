// Package schemas embeds the JSON Schemas that define the wire formats of
// pg-task-focus: the event log line and the configuration file. The files are
// the contract; this package only carries them into the binary.
package schemas

import _ "embed"

//go:embed event.schema.json
var event []byte

//go:embed config.schema.json
var config []byte

// Event returns the JSON Schema (2020-12) of one event log line. The caller
// owns the returned slice.
func Event() []byte { return append([]byte(nil), event...) }

// Config returns the JSON Schema (2020-12) of the configuration file. The
// caller owns the returned slice.
func Config() []byte { return append([]byte(nil), config...) }

//go:embed cli.schema.json
var cli []byte

// CLI returns the JSON Schema (2020-12) of the command-line client's --json
// output: every document a client verb prints, as a $defs entry (State,
// Result, DryRunResult, Events, Problem, CheckOutput, ConfigCheckOutput).
// The entries that come from the HTTP API are derived from api/openapi.yaml,
// and a test fails when they drift. The caller owns the returned slice.
func CLI() []byte { return append([]byte(nil), cli...) }
