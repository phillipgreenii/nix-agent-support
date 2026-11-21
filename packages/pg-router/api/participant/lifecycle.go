package participant

import (
	"fmt"
	"io"
)

// Lifecycle is a participant's lifecycle state (interfaces.md). Messages are
// accepted ONLY in Started: after started, before stopping (INV-INTF-1).
type Lifecycle int

const (
	Starting Lifecycle = iota
	Started
	Stopping
	Stopped
	Crashing
)

// String returns the lifecycle state's documented name (starting, started,
// stopping, stopped, crashing), so a diagnostic or a registry view reports the
// same names the behavior docs use rather than an opaque integer.
func (l Lifecycle) String() string {
	switch l {
	case Starting:
		return "starting"
	case Started:
		return "started"
	case Stopping:
		return "stopping"
	case Stopped:
		return "stopped"
	case Crashing:
		return "crashing"
	}
	return fmt.Sprintf("Lifecycle(%d)", int(l))
}

// Participant speaks the default CLI transport: it reads a JSON request from
// stdin and writes a JSON reply to stdout, returning a coarse exit code
// (INV-INTF-1). An implementation MAY instead speak gRPC or in-code and still
// conform so long as the message schema holds.
type Participant interface {
	Serve(subcommand string, stdin io.Reader, stdout io.Writer) (exitCode int)
}
