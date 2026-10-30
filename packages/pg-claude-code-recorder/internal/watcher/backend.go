package watcher

// Hint is one filesystem event hint. Flags are never trusted for what changed:
// a hint only says "look at this path". MustScan asks for a full sweep
// (FSEvents MustScanSubDirs or a dropped-event flag, inotify queue overflow).
type Hint struct {
	Path     string
	MustScan bool
	EventID  uint64
}

// backend is the platform event source: FSEvents on darwin (cgo), inotify on
// Linux. A nil backend (other platforms, CGO_ENABLED=0 on darwin) leaves the
// watcher sweep-only.
type backend interface {
	// Start begins delivering hints for roots. replay=true replays history
	// from since; otherwise delivery starts from now.
	Start(roots []string, since uint64, replay bool, onHint func(Hint)) error
	Stop()
	// CanReplay reports whether the source keeps event history.
	CanReplay() bool
	// VolumeUUID identifies the volume holding path, or "" when unknown.
	VolumeUUID(path string) string
}
