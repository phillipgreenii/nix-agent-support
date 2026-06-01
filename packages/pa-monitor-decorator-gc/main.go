// Command pa-monitor-decorator-gc is a Gas City–specific label
// decorator for pa-monitor. It is launched by the pa-monitor daemon
// at every metric tick with PA_MONITOR_DECORATE=1 in the environment
// and the session's JSON on stdin (see
// packages/pa-monitor/internal/labels/decorator.go in this repo).
//
// This is the SCAFFOLD only. The binary currently returns an empty
// labels object. The actual label mapping is documented in
// RESEARCH.md alongside this file and will be filled in after Phil
// and Claude review the proposal there.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// session mirrors the shape of labels.Session in
// packages/pa-monitor/internal/labels/labels.go. Kept as a local
// struct (rather than importing the pa-monitor module) so this
// package stays a standalone go.mod with no internal deps on
// pa-monitor — both packages reference the wire format, not Go types.
type session struct {
	ID    string            `json:"ID"`
	PID   int               `json:"PID"`
	CWD   string            `json:"CWD"`
	Env   map[string]string `json:"Env"`
	Model string            `json:"Model"`
}

// output is the JSON the pa-monitor daemon expects on our stdout:
// `{"labels": {key: value, ...}}`. An empty labels object is valid;
// it means "no decoration to add". The daemon merges this on top of
// the built-in detector labels (argument-wins semantics).
type output struct {
	Labels map[string]string `json:"labels"`
}

// run reads a session JSON document from r, computes a label set, and
// writes the JSON-encoded output to w. Factored out of main so tests
// can call it directly without shelling out.
//
// Contract (per pa-monitor's decorator runner):
//   - Must read all of stdin before writing.
//   - Must write a single JSON object on stdout.
//   - Non-zero exit / parse error / timeout -> daemon swallows our
//     output, so we have no obligation to surface stderr cleanly.
//     Still, we return errors here so tests can assert on them.
func run(r io.Reader, w io.Writer) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}

	var s session
	// Empty input is treated as an empty session — emit empty labels
	// rather than failing. The daemon may invoke us with `{}` during
	// startup sanity checks.
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("parse session JSON: %w", err)
		}
	}

	labels := decorate(s)
	if labels == nil {
		labels = map[string]string{}
	}

	enc := json.NewEncoder(w)
	if err := enc.Encode(output{Labels: labels}); err != nil {
		return fmt.Errorf("encode output: %w", err)
	}
	return nil
}

// decorate is where the Gas City label mapping will live. Today it
// returns an empty map — see RESEARCH.md for the proposed mapping.
// Keeping this as a named function gives the eventual implementation
// (and its unit tests) a single seam to extend.
func decorate(_ session) map[string]string {
	return map[string]string{}
}

func main() {
	// Per the decorator protocol the daemon always sets
	// PA_MONITOR_DECORATE=1. If we're invoked some other way (a user
	// running the binary by hand, a misconfigured wrapper) bail out
	// silently — same shape as the runner's swallow-and-warn so we
	// don't pollute logs.
	if os.Getenv("PA_MONITOR_DECORATE") != "1" {
		os.Exit(0)
	}

	if err := run(os.Stdin, os.Stdout); err != nil {
		// Decorator output is advisory; the runner swallows errors.
		// We surface them on stderr for ad-hoc debugging only.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
