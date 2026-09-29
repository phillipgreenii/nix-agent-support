// Package userconfig is the new effect engine's user-level configuration
// (ADR 0075 "Ground truth"/P4/P7/P17) — a plain, flat settings struct for
// every security-relevant new-engine setting that is NOT itself a command,
// path, or target spec (those stay in internal/specfmt's three-layer
// Repository). It is deliberately its own small package rather than a home
// on internal/patheval or internal/pathspec: P4's TrustedExecPath (this
// packet) and a later packet's agentWritableConfig field (K7,
// internal/pathspec) both need to read/extend the SAME struct, and neither
// package should have to own it on the other's behalf.
//
// # Why not internal/specfmt's user-level directory
//
// specfmt.DefaultUserDir() (~/.config/claude-extended-tool-approver) is
// recursively walked by specfmt.Repository for "*.json" spec files, keyed by
// (Kind, Name) — dropping a flat settings file anywhere under that tree
// would make the Repository pick it up, fail Validate (an unrecognized/empty
// Kind), and report it as an Invalid spec on every load. This package's
// config is a different SHAPE of on-disk state (one flat struct, not a
// (Kind, Name)-keyed document), so it lives under its own directory
// (DefaultPath), keeping the new engine's flat config resolution
// independently auditable from both the old engine's XDG-based
// configrules.DefaultPath and the new engine's own spec directories.
//
// # Resolution rule (P7)
//
// DefaultPath resolves ONLY from the OS home directory (os.UserHomeDir) —
// never $XDG_CONFIG_HOME, never any other inherited environment variable.
// P7 requires every security-relevant new-engine setting (config locations,
// root lists, the trusted PATH, ...) come from this user-level config at a
// HOME-resolved location, not from the environment, so an agent-controlled
// shell cannot widen what the hook trusts merely by exporting a variable.
package userconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the new effect engine's user-level configuration (P17). Kept a
// plain exported struct with named fields — not an opaque or generated type
// — so a later packet can add its own field (K7's forward note: pathspec's
// agentWritableConfig lands here too) as a one-line diff.
type Config struct {
	// TrustedExecPath lists directories treated as the operator's trusted
	// PATH for P4's executable-identity check
	// (patheval.ResolveTrustedExecutable): an absolute argv0 is trusted only
	// when its realpath equals the realpath of <dir>/<basename> for some dir
	// listed here. In the operator's actual Nix deployment this is
	// ordinarily the current user's Nix profile bin directory and
	// /run/current-system/sw/bin (or the darwin equivalents) — but this
	// package hardcodes NO default of its own. An operator who has not
	// populated this field gets an empty trusted PATH, which — per P3,
	// "unknown never approves" — makes every absolute-path exec untrusted
	// rather than silently guessing a plausible-looking default location.
	TrustedExecPath []string `json:"trustedExecPath"`
}

// DefaultPath returns the new engine's user-level config file location,
// resolved from the OS home directory only (see package doc comment for why
// this is not under specfmt.DefaultUserDir()).
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("userconfig: resolving home directory: %w", err)
	}
	return filepath.Join(home, ".config", "claude-extended-tool-approver-engine", "config.json"), nil
}

// Load reads and parses the config file at path, returning a zero *Config
// (every field empty/nil, never nil itself) on any failure — a missing
// file, an unreadable file, or malformed JSON — mirroring
// configrules.Load's fail-safe-to-empty contract. A caller reading an empty
// Config falls back to its own safe default (for TrustedExecPath, an empty
// list, already the fail-closed answer per P3).
func Load(path string) *Config {
	data, err := os.ReadFile(path)
	if err != nil {
		return &Config{}
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &Config{}
	}
	return &cfg
}

// LoadDefault resolves DefaultPath and loads it, returning an empty *Config
// (never an error) even when the home directory cannot be resolved — the
// same fail-safe-to-empty behavior Load has for every other failure mode.
func LoadDefault() *Config {
	path, err := DefaultPath()
	if err != nil {
		return &Config{}
	}
	return Load(path)
}
