package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
)

// Fake backends are small shell scripts written to a fresh temp directory per
// test. On macOS the first exec of every newly written executable pays an
// endpoint-security scan (about 100 ms), which dominated this package's run
// time. installExec writes each distinct script body once per test process and
// hard-links it to the requested path, so every later exec of that body reuses
// the already-scanned inode. Scripts that need per-test state read it from the
// environment instead of embedding a per-test path in their body.
var (
	execCacheOnce sync.Once
	execCacheDir  string
	execCacheErr  error
	execCacheMu   sync.Mutex
)

// installExec makes path an executable file holding content.
func installExec(path, content string) error {
	execCacheOnce.Do(func() {
		execCacheDir, execCacheErr = os.MkdirTemp("", "pg-connector-fakes-")
	})
	if execCacheErr != nil {
		return execCacheErr
	}
	sum := sha256.Sum256([]byte(content))
	cached := filepath.Join(execCacheDir, hex.EncodeToString(sum[:]))

	execCacheMu.Lock()
	if _, err := os.Stat(cached); err != nil {
		if err := os.WriteFile(cached, []byte(content), 0o755); err != nil {
			execCacheMu.Unlock()
			return err
		}
	}
	execCacheMu.Unlock()

	_ = os.Remove(path)
	if err := os.Link(cached, path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o755)
}

// removeExecCache deletes the shared script cache; TestMain calls it.
func removeExecCache() {
	if execCacheDir != "" {
		_ = os.RemoveAll(execCacheDir)
	}
}
