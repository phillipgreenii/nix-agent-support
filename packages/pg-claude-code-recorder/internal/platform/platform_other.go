//go:build !(darwin && cgo)

// Package platform holds the platform-specific code (non-darwin or CGO_ENABLED=0
// fallback: Linux builds with CGO_ENABLED=0 and uses inotify in later packets).
package platform

// CgoReady is false where the darwin cgo file is not built.
func CgoReady() bool { return false }
