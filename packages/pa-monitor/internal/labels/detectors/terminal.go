// Package detectors implements built-in label detectors that map session
// context to stable label keys. See labels package docs for the contract.
package detectors

import "github.com/phillipgreenii/pa-monitor/internal/labels"

// Terminal classifies the terminal multiplexer hosting the session.
type Terminal struct{}

func (Terminal) Name() string { return "terminal" }

func (Terminal) Detect(s labels.Session) labels.Set {
	if s.Env["CMUX_WORKSPACE_ID"] != "" {
		return labels.Set{"workspace.terminal": "cmux"}
	}
	if s.Env["TMUX"] != "" {
		return labels.Set{"workspace.terminal": "tmux"}
	}
	return labels.Set{"workspace.terminal": "direct"}
}
