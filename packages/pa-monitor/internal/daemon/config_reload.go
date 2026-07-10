package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"os"

	"github.com/phillipgreenii/pa-monitor/internal/labels"
)

// configReloader re-reads the daemon's config file each tick and rebuilds the
// decorator pipeline when the file's content changes.
//
// Motivation (bead pg2-r1f1j.8): the daemon loads config ONCE at startup
// (cmd/pa-monitor/daemon.go) and has no reload path. `pn workspace apply`
// restarts the daemon around the same time home-manager writes
// ~/.config/pa-monitor/config.toml, so the daemon can boot BEFORE the file
// exists (or before the [[decorator]] block is added), leaving
// workspace.scope stuck at the DefaultScope ("personal"). A config-only change
// (adding/removing a decorator) also never restarts the launchd agent, whose
// restart is keyed on the package hash, not the config. Polling the config
// fingerprint each tick closes both gaps without a manual restart.
type configReloader struct {
	path   string
	lastFP string
	// rebuild re-loads the config and returns the fresh decorator pipeline as
	// FailableDetectors (already adapted via labels.AsFailable). It returns an
	// error when the config cannot be loaded/parsed this tick.
	rebuild func() ([]labels.FailableDetector, error)
}

// reloadIfChanged returns the freshly-rebuilt decorator pipeline and true when
// the config file's fingerprint changed since the last call. Because lastFP
// starts empty, the FIRST call always rebuilds — this deterministically closes
// the boot race regardless of what the daemon read at startup.
//
// A rebuild error is reported as (nil, false) and does NOT advance the
// fingerprint, so the working pipeline is preserved and the next tick retries.
func (r *configReloader) reloadIfChanged() ([]labels.FailableDetector, bool) {
	fp := fileFingerprint(r.path)
	if fp == r.lastFP {
		return nil, false
	}
	decs, err := r.rebuild()
	if err != nil {
		return nil, false
	}
	r.lastFP = fp
	return decs, true
}

// fileFingerprint returns a hex SHA-256 of the file's content, following
// symlinks (home-manager renders the config as a symlink into /nix/store and
// re-points it on change). An absent/unreadable file yields "" — distinct from
// any real content hash — so a config that appears later is detected as a
// change.
func fileFingerprint(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
