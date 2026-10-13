// Package sidecar resolves the posted-review sidecar for the daemon: the
// posted.Store and posted.Locker that the write path (review_submit) and the
// pending group use.
//
// The sidecar is the old pg-connector-pr-github backend's own state, and it
// stays exactly where that backend keeps it. Until the cleanup phase it is the
// single source of truth for which review comments were posted, so a rollback to
// the old binary sees every review posted since cutover and the operator can
// still delete a record by hand. Its files and its cross-process flock keep the
// old names and locations under one state home:
//
//	<state home>/posted/<owner>__<repo>__<n>.json   the per-PR sidecar
//	<state home>/locks/<owner>__<repo>__<n>.lock    the per-PR flock
//
// A corrupt sidecar file stays a refusal: Store.Load wraps posted.ErrCorrupt and
// leaves the file where it is.
//
// The state home is, in order:
//
//  1. pr.posted_state_dir of the daemon's config (config.SettingPostedStateDir);
//  2. PG_CONNECTOR_PR_GITHUB_STATE_DIR;
//  3. $XDG_STATE_HOME/pg-connector-pr-github;
//  4. $HOME/.local/state/pg-connector-pr-github.
//
// Steps 2 to 4 are the old backend's own derivation (posted.StateHomeFromEnv).
// They read the DAEMON's environment, and only through the getenv that Open is
// given: a daemon MUST NOT read the environment of a client's call, so the one
// getenv ever passed is the daemon's own (os.Getenv, at start). Nothing in this
// package reads the process environment itself.
package sidecar

import (
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
)

// ErrNoStateHome is wrapped by the error Open returns when neither the setting
// nor the daemon's environment names a state home. The daemon refuses to start
// the write path then, instead of writing the sidecar somewhere the old backend
// would not look.
var ErrNoStateHome = errors.New("sidecar: no state home could be determined")

// Open resolves the sidecar's state home and returns the Store at
// <state home>/posted and the Locker at <state home>/locks (with the old
// backend's 60-second bounded wait). It resolves only: no directory is created
// until the first Save or Acquire, exactly as with the old backend.
//
// cfg is the loaded daemon config, whose pr.posted_state_dir (validated as an
// absolute path by config.Load) wins when set; a nil cfg is the same as an unset
// setting. getenv is the daemon's own environment, consulted only while the
// setting is unset; a nil getenv reads an empty environment. When no home can be
// determined, the error wraps ErrNoStateHome and names pr.posted_state_dir.
func Open(cfg *config.Config, getenv func(string) string) (posted.Store, posted.Locker, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	home := ""
	if cfg != nil {
		home = cfg.PR.PostedStateDir
	}
	if home == "" {
		home = posted.StateHomeFromEnv(getenv)
	}
	if home == "" {
		return posted.Store{}, posted.Locker{}, fmt.Errorf(
			"%w: set %s (an absolute path), or provide %s, XDG_STATE_HOME or HOME in the daemon's own environment",
			ErrNoStateHome, config.SettingPostedStateDir, posted.EnvStateDir,
		)
	}
	// Build both through the copied package's own constructors with the home
	// pinned as its override variable, so the layout (<home>/posted,
	// <home>/locks, the lock wait) is defined once, in the one place the sync
	// guard keeps identical to the old backend's.
	pinned := func(name string) string {
		if name == posted.EnvStateDir {
			return home
		}
		return ""
	}
	return posted.StoreFromEnv(pinned), posted.LockerFromEnv(pinned), nil
}
