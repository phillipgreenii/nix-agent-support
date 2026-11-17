package supervisor

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/x/tmux"
)

// WriteLastExit records how id's session ended in <state dir>/last-exit/<id>.txt,
// replacing the previous record: the time, the pane's exit status and signal,
// the session's uptime and the pane's last lines (d.Tail, without its
// trailing blank lines). A session that crashes at once (expired auth, a
// removed flag, a bad environment value) so leaves its reason behind. The
// file is mode 0600 in a directory created 0700.
func WriteLastExit(stateDir, id string, d tmux.DeadInfo, uptime time.Duration, at time.Time) error {
	if !ValidID(id) {
		return fmt.Errorf("last exit: %q is not a valid assistant id ([a-z0-9-]+)", id)
	}
	signal := "none"
	if d.Signal != 0 {
		signal = fmt.Sprintf("%d (%s)", d.Signal, syscall.Signal(d.Signal))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "assistant: %s\n", id)
	fmt.Fprintf(&b, "exited: %s\n", at.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "status: %d\n", d.Status)
	fmt.Fprintf(&b, "signal: %s\n", signal)
	fmt.Fprintf(&b, "uptime: %s\n", uptime.Round(time.Millisecond))
	b.WriteString("last output:\n")
	if tail := strings.TrimRight(d.Tail, "\n"); tail != "" {
		b.WriteString(tail + "\n")
	}
	path := filepath.Join(stateDir, "last-exit", id+".txt")
	if err := writeFileAtomic(path, []byte(b.String())); err != nil {
		return fmt.Errorf("last exit: %w", err)
	}
	return nil
}
