// Package browser opens a set of URLs as ONE new desktop-browser window, one
// tab per URL, in the operator's existing browser profile.
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
	BinEnvVar     = "PGPR_CHROME_BIN"
	ProfileEnvVar = "PGPR_CHROME_PROFILE"
)

// ErrUnsupported reports that this build has no window-opening implementation.
var ErrUnsupported = errors.New("opening a browser window is implemented only on macOS with Google Chrome")

// OpenWindow opens every url in one new browser window, one tab per url, in the
// operator's existing profile. Passing no URLs is a no-op.
//
// It is a package var so a caller's tests can substitute a recorder rather than
// launching a real browser.
var OpenWindow = openWindow
