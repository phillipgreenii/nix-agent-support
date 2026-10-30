// Package faults is the crash-point seam. Production code calls At(name) at
// each named crash point; nothing is armed in production, so At is a no-op.
// Unit tests arm a point in-process with Arm; the integration binary built with
// the faults tag arms one from PG_CLAUDE_CODE_RECORDER_FAULT (see env.go).
package faults

import "sync"

var (
	mu    sync.RWMutex
	armed = map[string]func(){}
)

// Crash is the value an armed test handler panics with to simulate a crash.
type Crash struct{ Point string }

// At marks the crash point name. It is a no-op unless the point is armed.
func At(name string) {
	mu.RLock()
	h := armed[name]
	mu.RUnlock()
	if h != nil {
		h()
	}
}

// Arm installs handler for the named point and returns a disarm function.
func Arm(name string, handler func()) (disarm func()) {
	mu.Lock()
	armed[name] = handler
	mu.Unlock()
	return func() {
		mu.Lock()
		delete(armed, name)
		mu.Unlock()
	}
}

// ArmPanic arms the point so that reaching it panics with Crash{name}; use
// Recover in the test to catch it and then reopen on the same directories.
func ArmPanic(name string) (disarm func()) {
	return Arm(name, func() { panic(Crash{Point: name}) })
}

// Recover runs fn and reports whether it ended in an armed Crash panic. Any
// other panic propagates.
func Recover(fn func()) (crashed bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(Crash); ok {
				crashed = true
				return
			}
			panic(r)
		}
	}()
	fn()
	return false
}
