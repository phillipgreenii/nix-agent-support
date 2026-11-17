package supervisor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
)

// An exit request is the file <state dir>/exit-requests/<id>. An assistant
// writes it by running `pg-assistants exit`; the daemon checks for it on each
// tick and stops the session gracefully. The daemon deletes it only once the
// session is gone (and before each start), never when it reads it, so a
// daemon restarted during the graceful stop still finds the request.

// ValidID reports whether id is a valid assistant id, [a-z0-9-]+. Every file
// named by an id is refused for any other id, since the id becomes part of a
// path.
func ValidID(id string) bool { return config.ValidID(id) }

// stateFile is the path of an assistant's file in a state-dir subdirectory:
// <stateDir>/<sub>/<id><ext>. It refuses a state dir that is not absolute (an
// empty or relative one would put the file under the process's working
// directory) and an id that is not valid.
func stateFile(stateDir, sub, id, ext string) (string, error) {
	if !filepath.IsAbs(stateDir) {
		return "", fmt.Errorf("state dir %q is not an absolute path", stateDir)
	}
	if !ValidID(id) {
		return "", fmt.Errorf("%q is not a valid assistant id ([a-z0-9-]+)", id)
	}
	return filepath.Join(stateDir, sub, id+ext), nil
}

func exitRequestPath(stateDir, id string) (string, error) {
	path, err := stateFile(stateDir, "exit-requests", id, "")
	if err != nil {
		return "", fmt.Errorf("exit request: %w", err)
	}
	return path, nil
}

// WriteExitRequest records an exit request for id at time at, replacing any
// earlier one. The file is mode 0600 in a directory created 0700.
func WriteExitRequest(stateDir, id string, at time.Time) error {
	path, err := exitRequestPath(stateDir, id)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(path, []byte(at.UTC().Format(time.RFC3339Nano)+"\n")); err != nil {
		return fmt.Errorf("exit request: %w", err)
	}
	return nil
}

// HasExitRequest reports whether an exit request for id is on file. It never
// deletes the request.
func HasExitRequest(stateDir, id string) (bool, error) {
	path, err := exitRequestPath(stateDir, id)
	if err != nil {
		return false, err
	}
	switch _, err := os.Stat(path); {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("exit request: %w", err)
	}
}

// ClearExitRequest deletes the exit request for id; nil when there is none.
func ClearExitRequest(stateDir, id string) error {
	path, err := exitRequestPath(stateDir, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("exit request: %w", err)
	}
	return nil
}
