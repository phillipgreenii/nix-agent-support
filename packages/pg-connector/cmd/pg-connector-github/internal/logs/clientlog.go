package logs

import (
	"time"

	evlog "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ClientLog is the small log client mode appends to when it answers
// unavailable because the daemon cannot be reached (client.log.jsonl, Loki
// service_name pg-connector-github-client). The daemon cannot log that outage:
// it is the thing that is down, so the client records it, and a count of
// kind=request rows with outcome daemon_unreachable is the number of
// caller-visible outages.
//
// The client rotates the file itself, at MaxBytes (1 MiB), under the flock
// pkg/eventlog.Append takes on <file>.lock, so concurrent clients rotate once
// and lose no line. Append is the only writer; a nil *ClientLog is a no-op.
type ClientLog struct {
	Path     string
	MaxBytes int64
	// Version is the client's build, stamped into every row.
	Version string
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// NewClientLog returns the client log inside stateDir, rotating at
// ClientMaxBytes.
func NewClientLog(stateDir, version string) *ClientLog {
	return &ClientLog{Path: ClientLogPath(stateDir), MaxBytes: ClientMaxBytes, Version: version}
}

// DaemonUnreachable appends the row for one request the client could not hand
// to the daemon. started is when the client began the call; cause is the
// unavailable answer's message.
func (l *ClientLog) DaemonUnreachable(instance, op string, started time.Time, cause string) {
	if l == nil {
		return
	}
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	row := newRequestRow(ClientServiceName, l.Version, op, instance, started, now(),
		&scriptout.Error{Code: "unavailable", Message: cause})
	row.Outcome = OutcomeDaemonUnreachable
	row.Msg = op + " failed: " + OutcomeDaemonUnreachable
	max := l.MaxBytes
	if max <= 0 {
		max = ClientMaxBytes
	}
	evlog.WriteBestEffort(l.Path, max, row)
}
