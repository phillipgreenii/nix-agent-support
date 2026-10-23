package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

// backupInfix separates the store's file name from the backup's schema version
// and timestamp: store.db.pre-schema-3-20261011T051800Z.
const backupInfix = ".pre-schema-"

// backupLayout is the timestamp format of a backup (UTC, whole seconds); a
// second backup of the same second gets a numeric -2, -3 suffix.
const backupLayout = "20060102T150405Z"

// backupStore writes a consistent copy of the open database to target. It is a
// package-level variable so a test can make exactly this step fail.
var backupStore = vacuumInto

// vacuumInto copies db to target with VACUUM INTO. The file name of VACUUM INTO
// is an SQL expression, so it is bound as a parameter, never spliced into the
// statement.
func vacuumInto(db *sql.DB, target string) error {
	_, err := db.Exec("VACUUM INTO ?", target)
	return err
}

// backupPath returns a free <path>.pre-schema-<from>-<timestamp> name.
func backupPath(path string, from int, now time.Time) (string, error) {
	base := fmt.Sprintf("%s%s%d-%s", path, backupInfix, from, now.UTC().Format(backupLayout))
	to := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(to); errors.Is(err, os.ErrNotExist) {
			return to, nil
		} else if err != nil {
			return "", err
		}
		if i > 1000 {
			return "", fmt.Errorf("no free backup name for %s", path)
		}
		to = fmt.Sprintf("%s-%d", base, i)
	}
}

// backupBeforeMigrating writes the verified pre-migration copy of a populated
// store and returns its path. The copy is opened and must report the same
// PRAGMA user_version as the original; a copy that does not is deleted and an
// error returned. The errors are plain: they MUST NOT take Open's move-aside
// path, which would lose the store the copy exists to protect.
func backupBeforeMigrating(db *sql.DB, path string, from int, now time.Time) (string, error) {
	to, err := backupPath(path, from, now)
	if err != nil {
		return "", fmt.Errorf("store: back up %s before migrating: %w", path, err)
	}
	if err := backupStore(db, to); err != nil {
		_ = os.Remove(to)
		return "", fmt.Errorf("store: back up %s to %s before migrating: %w", path, to, plainError{err})
	}
	if err := verifyBackup(to, from); err != nil {
		_ = os.Remove(to)
		return "", fmt.Errorf("store: verify the backup %s of %s: %w", to, path, plainError{err})
	}
	return to, nil
}

// verifyBackup opens the copy and checks it reports user_version want.
func verifyBackup(path string, want int) error {
	cp, err := sql.Open("sqlite", "file:"+uriPath(path)+"?mode=ro")
	if err != nil {
		return err
	}
	defer func() { _ = cp.Close() }()
	var got int
	if err := cp.QueryRow("PRAGMA user_version").Scan(&got); err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("the copy reports schema version %d, want %d", got, want)
	}
	return nil
}

// plainError carries a message but hides the wrapped chain, so a SQLite
// corruption code inside it cannot make Open treat a failed backup as a corrupt
// store and move it aside.
type plainError struct{ err error }

func (e plainError) Error() string { return e.err.Error() }
