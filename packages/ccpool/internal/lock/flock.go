// Package lock provides a per-session advisory file lock (spec §15): a single
// writer per conversation, so a resume can't race a send and two writers can't
// corrupt one transcript.
package lock

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

type Flock struct{ dir string }

// New returns a Locker whose lockfiles live under dir (the runtime dir).
func New(dir string) *Flock { return &Flock{dir: dir} }

// Lock blocks until the per-name lock is held; the returned func releases it.
func (f *Flock) Lock(name string) (func(), error) {
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir lock dir: %w", err)
	}
	fl := flock.New(filepath.Join(f.dir, name+".lock"))
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("flock %q: %w", name, err)
	}
	return func() { _ = fl.Unlock() }, nil
}
