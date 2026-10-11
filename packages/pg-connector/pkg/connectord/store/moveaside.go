package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// movedAsideInfix separates the store's file name from the move-aside
// timestamp: store.db.moved-aside-20261011T051800.123456789Z.
const movedAsideInfix = ".moved-aside-"

// movedAsideLayout is the timestamp format of a moved-aside file (UTC, nine
// fractional digits so two moves in one second never collide).
const movedAsideLayout = "20060102T150405.000000000Z"

// sidecars are the files SQLite keeps beside the main file; they move with
// it so a fresh store never inherits a stale WAL.
var sidecars = []string{"-wal", "-shm", "-journal"}

// moveAside renames the main file (and its sidecars) to a timestamped name
// and returns the new main-file path.
func moveAside(path string, now time.Time) (string, error) {
	var to string
	t := now.UTC()
	for i := 0; ; i++ {
		to = path + movedAsideInfix + t.Format(movedAsideLayout)
		if _, err := os.Lstat(to); errors.Is(err, os.ErrNotExist) {
			break
		}
		if i >= 1000 {
			return "", fmt.Errorf("no free moved-aside name for %s", path)
		}
		t = t.Add(time.Nanosecond)
	}
	if err := os.Rename(path, to); err != nil {
		return "", err
	}
	for _, sfx := range sidecars {
		if err := os.Rename(path+sfx, to+sfx); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return to, nil
}

// pruneMovedAside deletes moved-aside stores (and their sidecars) whose
// timestamp is older than expiry and returns the main-file paths it
// deleted. It is best effort: a file it cannot parse or delete is left.
func pruneMovedAside(path string, now time.Time, expiry time.Duration) []string {
	matches, err := filepath.Glob(escapeGlob(path) + movedAsideInfix + "*")
	if err != nil {
		return nil
	}
	var pruned []string
	for _, m := range matches {
		stamp := strings.TrimPrefix(m, path+movedAsideInfix)
		main := m
		for _, sfx := range sidecars {
			if strings.HasSuffix(stamp, sfx) {
				stamp = strings.TrimSuffix(stamp, sfx)
				main = strings.TrimSuffix(m, sfx)
				break
			}
		}
		at, err := time.Parse(movedAsideLayout, stamp)
		if err != nil || now.Sub(at) <= expiry {
			continue
		}
		if err := os.Remove(m); err != nil {
			continue
		}
		if main == m {
			pruned = append(pruned, m)
		}
	}
	return pruned
}

// escapeGlob quotes the glob metacharacters in a literal path.
func escapeGlob(s string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(s)
}
