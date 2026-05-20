package detectors

import (
	"strings"

	"github.com/phillipgreenii/claude-agents-tui/internal/labels"
)

// Agent maps Session.Model to agent.kind. Unknown models produce no
// label; agent.mode (interactive vs headless) is deferred until the
// daemon plumbs tty info through Session.
type Agent struct{}

func (Agent) Name() string { return "agent" }

func (Agent) Detect(s labels.Session) labels.Set {
	out := labels.Set{}
	switch {
	case strings.HasPrefix(s.Model, "claude-"):
		out["agent.kind"] = "claude"
	case strings.HasPrefix(s.Model, "codex-"):
		out["agent.kind"] = "codex"
	}
	return out
}
