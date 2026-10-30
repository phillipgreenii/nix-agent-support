// Package legacyimport brings the old pg-connector-pr-github backend's posted-
// review sidecar files into the daemon's store, once. It is the only code of the
// daemon that names the old backend's state directory.
//
// The legacy home is, in order:
//
//  1. PG_CONNECTOR_PR_GITHUB_STATE_DIR;
//  2. $XDG_STATE_HOME/pg-connector-pr-github;
//  3. $HOME/.local/state/pg-connector-pr-github.
//
// read from the DAEMON's own environment through the getenv HomeFromEnv is given
// (a daemon MUST NOT read the environment of a client's call). The records are
// the files <home>/posted/<owner>__<repo>__<n>.json.
//
// Import runs in one transaction guarded by the store_meta key MarkerKey: when
// the key exists it does nothing; otherwise it inserts every file with INSERT OR
// IGNORE (the file's bytes as they are; a record already in the store wins) and
// writes the key in the same transaction. A failure rolls everything back and
// writes no marker, so the next start retries. The legacy files are never
// modified or deleted.
package legacyimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// MarkerKey is the store_meta key that records the import was done.
const MarkerKey = "posted_import_v1"

// EnvStateDir overrides the old backend's state home.
const EnvStateDir = "PG_CONNECTOR_PR_GITHUB_STATE_DIR"

const (
	serviceName = "pg-connector-pr-github"
	postedDir   = "posted"
)

// HomeFromEnv resolves the old backend's state home from the daemon's
// environment; "" means none could be determined.
func HomeFromEnv(getenv func(string) string) string {
	if getenv == nil {
		return ""
	}
	if d := getenv(EnvStateDir); d != "" {
		return d
	}
	if state := getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, serviceName)
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "state", serviceName)
	}
	return ""
}

// Result describes one Import.
type Result struct {
	// AlreadyDone is true when the marker existed and nothing was read.
	AlreadyDone bool
	// Imported is the number of rows inserted; Existing the number of files whose
	// record the store already had (the store wins); Skipped the number of
	// directory entries that are not sidecar files.
	Imported, Existing, Skipped int
}

// stemRE is "<owner>__<repo>__<n>": the owner has no underscore, the repo may
// contain any, and the number is the last "__" part.
var stemRE = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9.-]*)__([A-Za-z0-9][A-Za-z0-9._-]*)__([0-9]+)$`)

// ParseName splits a sidecar file name into its key.
func ParseName(name string) (owner, repo string, number int, ok bool) {
	stem, found := strings.CutSuffix(name, ".json")
	if !found {
		return "", "", 0, false
	}
	m := stemRE.FindStringSubmatch(stem)
	if m == nil {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(m[3])
	if err != nil || n <= 0 {
		return "", "", 0, false
	}
	return m[1], m[2], n, true
}

// Import runs the once-only import from home into st. An empty home is an error
// (nothing is written, no marker). A home without a posted directory has nothing
// to import and still writes the marker.
func Import(ctx context.Context, st *store.Store, home string) (Result, error) {
	var res Result
	if st == nil {
		return res, errors.New("legacyimport: no store")
	}
	if home == "" {
		return res, fmt.Errorf("legacyimport: no legacy state home could be determined (set %s, XDG_STATE_HOME or HOME in the daemon's environment)", EnvStateDir)
	}
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("legacyimport: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var marker string
	switch err := tx.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = ?`, MarkerKey).Scan(&marker); {
	case err == nil:
		res.AlreadyDone = true
		return res, nil
	case !errors.Is(err, sql.ErrNoRows):
		return res, fmt.Errorf("legacyimport: read marker: %w", err)
	}

	dir := filepath.Join(home, postedDir)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, fmt.Errorf("legacyimport: read %s: %w", dir, err)
	}
	now := time.Now().UnixMilli()
	for _, e := range entries {
		owner, repo, n, ok := ParseName(e.Name())
		if !ok || e.IsDir() {
			res.Skipped++
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return Result{}, fmt.Errorf("legacyimport: read %s: %w", e.Name(), err)
		}
		r, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO posted_review (owner, repo, number, state, updated_at) VALUES (?, ?, ?, ?, ?)`,
			owner, repo, n, string(data), now)
		if err != nil {
			return Result{}, fmt.Errorf("legacyimport: insert %s: %w", e.Name(), err)
		}
		if c, _ := r.RowsAffected(); c > 0 {
			res.Imported++
		} else {
			res.Existing++
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO store_meta (key, value) VALUES (?, ?)`, MarkerKey, strconv.FormatInt(now, 10)); err != nil {
		return Result{}, fmt.Errorf("legacyimport: write marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("legacyimport: commit: %w", err)
	}
	return res, nil
}
