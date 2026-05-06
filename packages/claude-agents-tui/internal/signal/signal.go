package signal

import "errors"

// ErrNotImplemented is returned by stub Signalers not yet wired up.
var ErrNotImplemented = errors.New("signaler not implemented for this terminal")

// Signaler injects keyboard input into the terminal session hosting a process.
type Signaler interface {
	Name() string
	// Detect returns true if pid is running inside this terminal environment.
	Detect(pid int) bool
	// Send injects text followed by Enter into the terminal hosting pid.
	Send(pid int, text string) error
}

// ResolveSignaler returns the first Signaler whose Detect returns true for pid,
// or nil if none match.
func ResolveSignaler(signalers []Signaler, pid int) Signaler {
	for _, s := range signalers {
		if s.Detect(pid) {
			return s
		}
	}
	return nil
}

// DefaultSignalers returns the standard ordered list of Signalers.
// TmuxSignaler is tried first; stubs follow for future implementation.
func DefaultSignalers() []Signaler {
	return []Signaler{
		&TmuxSignaler{},
		&CmuxSignaler{},
		&GhosttySignaler{},
		&VSCodeSignaler{},
	}
}
