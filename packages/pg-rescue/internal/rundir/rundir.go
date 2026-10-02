// Package rundir creates a run's private directory under the state root.
// Keeping, removing and pruning run directories belongs to later work; this
// package covers startup only: the run id, the state-root layout and the
// exclusive mkdir whose failure is a wrapper error.
package rundir

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// IDPattern matches a run id: a UTC timestamp followed by 32 random bits.
var IDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)

const maxCollisionRetries = 16

// StateRoot returns ${XDG_STATE_HOME:-~/.local/state}/pg-rescue. An empty
// XDG_STATE_HOME counts as unset.
func StateRoot(getenv func(string) string, home string) string {
	if x := getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "pg-rescue")
	}
	return filepath.Join(home, ".local", "state", "pg-rescue")
}

// NewID builds a run id from the clock reading and 4 bytes from rnd.
func NewID(now time.Time, rnd io.Reader) (string, error) {
	var b [4]byte
	if _, err := io.ReadFull(rnd, b[:]); err != nil {
		return "", fmt.Errorf("random source: %w", err)
	}
	return fmt.Sprintf("%s-%x", now.UTC().Format("20060102T150405Z"), b), nil
}

// Create makes <stateRoot>/runs/<run-id> with mode 0700 (the state root and
// runs/ are 0700 too, whatever the umask). The final mkdir fails if the
// directory exists, and Create then retries with a new id.
func Create(stateRoot string, now func() time.Time, rnd io.Reader) (id, dir string, err error) {
	runs := filepath.Join(stateRoot, "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		return "", "", fmt.Errorf("cannot create run directory root %s: %w", runs, err)
	}
	for _, d := range []string{stateRoot, runs} {
		if err := os.Chmod(d, 0o700); err != nil {
			return "", "", fmt.Errorf("cannot make %s private: %w", d, err)
		}
	}
	for range maxCollisionRetries {
		id, err := NewID(now(), rnd)
		if err != nil {
			return "", "", err
		}
		dir := filepath.Join(runs, id)
		err = os.Mkdir(dir, 0o700)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", "", fmt.Errorf("cannot create run directory %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", "", fmt.Errorf("cannot make %s private: %w", dir, err)
		}
		return id, dir, nil
	}
	return "", "", fmt.Errorf("cannot create a unique run directory under %s after %d attempts", runs, maxCollisionRetries)
}
