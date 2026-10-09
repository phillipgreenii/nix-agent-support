package engine

import (
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
)

// VerifyReport is what an offline check of a log found: the store's check
// and, when the log reads, the outcome of replaying its committed events.
type VerifyReport struct {
	// Check is the store's report: the log file, its size, the committed
	// lines and batches, the recovery the next start would perform (a torn
	// tail, an uncommitted batch at the end) and, in Problem, why a start
	// would refuse the log (a *store.CorruptError with its line, or a
	// *store.UnknownVersionError).
	Check store.CheckReport
	// Replayed is set when the committed events replayed into a possible
	// timeline. It is false when Check.Problem is set (nothing is replayed)
	// and when Invalid is.
	Replayed bool
	// Invalid is the finding of the replay when the timeline is impossible:
	// its specific code, its message and the stored events it names.
	Invalid *projection.Invalid
}

// OK reports whether a service would start on the log and replay it: no
// corruption, no unknown version, and the committed events replayed into a
// possible timeline. It agrees with Replayed: a replay that failed for a reason
// other than a finding (Verify's error) is not OK either. A recovery the next
// start would perform does not make a log fail.
func (r VerifyReport) OK() bool { return r.Check.Problem == nil && r.Invalid == nil && r.Replayed }

// Verify checks the log at path, the data directory or the log file itself,
// without taking the directory lock and without modifying anything, so it
// works while a service has the log open: the store's offline check, then a
// replay of the committed events. A problem with the log's content is in the
// report; the error is for a log that cannot be read at all.
func Verify(path string) (VerifyReport, error) {
	rep, err := store.Check(path)
	if err != nil {
		return VerifyReport{}, err
	}
	out := VerifyReport{Check: rep}
	if rep.Problem != nil {
		return out, nil
	}
	if _, err := projection.Replay(rep.Events); err != nil {
		var inv *projection.Invalid
		if errors.As(err, &inv) {
			out.Invalid = inv
			return out, nil
		}
		return out, fmt.Errorf("replaying the event log %s: %w", rep.Path, err)
	}
	out.Replayed = true
	return out, nil
}
