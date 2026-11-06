package runner

import (
	"os"
	"sync"
	"syscall"
)

// stagingRoot is a literal /tmp, never $TMPDIR: the analyst's sandbox can read
// /tmp but not the per-user temporary directory or the home directory.
const stagingRoot = "/tmp"

var umaskMu sync.Mutex

// makeStaging creates a fresh staging directory with an unguessable name
// (mkdtemp) under umask 077, so it is mode 0700.
func makeStaging() (string, error) {
	umaskMu.Lock()
	old := syscall.Umask(0o077)
	defer func() {
		syscall.Umask(old)
		umaskMu.Unlock()
	}()
	p, err := os.MkdirTemp(stagingRoot, "pg-review-")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(p, 0o700); err != nil {
		_ = os.RemoveAll(p)
		return "", err
	}
	return p, nil
}
