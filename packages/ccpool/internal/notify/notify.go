// Package notify emits an edge-triggered event when a session crosses into a
// notifying state (spec §10). ccpool does NOT own delivery: adapters route to
// none/exec/desktop. The hook computes the edge and calls Notify.
package notify

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Event struct {
	Name  string
	UUID  string
	State string
	CWD   string
}

type Notifier interface {
	Notify(e Event) error
}

// None drops events.
type None struct{}

func (None) Notify(Event) error { return nil }

// Exec runs a configured argv, passing event fields via environment.
type Exec struct{ Argv []string }

func (e Exec) Notify(ev Event) error {
	if len(e.Argv) == 0 {
		return nil
	}
	cmd := exec.Command(e.Argv[0], e.Argv[1:]...)
	cmd.Env = append(
		os.Environ(),
		"CCPOOL_NAME="+ev.Name,
		"CCPOOL_UUID="+ev.UUID,
		"CCPOOL_STATE="+ev.State,
		"CCPOOL_CWD="+ev.CWD,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("notify exec: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// FromConfig builds a Notifier. Unknown adapters fall back to None (a misconfig
// must never break a hook). command is a space-split argv for adapter=exec.
func FromConfig(adapter, command string) Notifier {
	switch adapter {
	case "exec":
		return Exec{Argv: strings.Fields(command)}
	case "desktop":
		return Desktop{}
	default: // "none" or anything unknown
		return None{}
	}
}

// ShouldNotify reports whether crossing prior→to should fire (edge + membership).
func ShouldNotify(on []string, prior, to string) bool {
	if prior == to {
		return false
	}
	for _, s := range on {
		if s == to {
			return true
		}
	}
	return false
}
