// Package browser opens a set of URLs as ONE new desktop-browser window, one
// tab per URL, in the operator's existing browser profile.
//
// Ported from packages/pg-pr/internal/browser (this docket's Contract names
// it as a porting source for `pg-desk open`). The one deliberate change from
// the pg-pr original: OpenWindow here takes an explicit bin argument — the
// operator-configured `open.chrome_bin` (internal/config's OpenConfig) — in
// addition to pg-pr's own environment-variable override, because D10's
// composition rule (docs/behavior/pg-desk/README.md "Composition rule")
// requires the configured browser to always be read from a variable, never a
// Go string literal, at every call site (see cmd/pg-desk/composition_test.go's
// TestCompositionChokePointAllowsConfigDrivenBrowserExec). Precedence: an
// explicit non-empty bin argument wins; otherwise the pg-pr-style
// BinEnvVar override; otherwise DefaultBin.
//
// The implementation is per-GOOS. macOS + Google Chrome is the only supported
// target today (browser_darwin.go); every other platform returns
// ErrUnsupported rather than silently opening nothing.
package browser

import "errors"

// BinEnvVar and ProfileEnvVar name the environment variables that override
// which browser executable is launched and which profile it opens in.
//
// They live here rather than beside the darwin implementation that reads them
// because they are part of this package's contract to its callers — including
// test code on any platform, which points BinEnvVar at a path that cannot exist
// so a forgotten stub can never launch the operator's real browser.
const (
	BinEnvVar     = "PG_DESK_CHROME_BIN"
	ProfileEnvVar = "PG_DESK_CHROME_PROFILE"
)

// ErrUnsupported reports that this build has no window-opening implementation.
var ErrUnsupported = errors.New("opening a browser window is implemented only on macOS with Google Chrome")

// OpenWindow opens every url in one new browser window, one tab per url, in the
// operator's existing profile. bin, when non-empty, is the operator-configured
// browser binary (config's open.chrome_bin) and takes precedence over both the
// BinEnvVar override and DefaultBin. Passing no URLs is a no-op.
//
// It is a package var so a caller's tests can substitute a recorder rather than
// launching a real browser.
var OpenWindow = openWindow
