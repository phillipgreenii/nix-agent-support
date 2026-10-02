// Package orphan lets a handler notice that the wrapper that started it is
// gone. A wrapper that is SIGKILLed (a calling tool timing out, a pg-router
// command role being killed) cannot clean up, and the handler it started would
// otherwise run on, unsupervised. Every reference handler calls Watch at
// startup; third-party handlers SHOULD do the equivalent.
package orphan

import (
	"os"
	"sync"
	"time"
)

// ExitCode is what a handler exits with once it finds itself orphaned. It is
// the generic failure code; nobody is left to read it.
const ExitCode = 1

// DefaultInterval is how often the parent is checked.
const DefaultInterval = time.Second

// Options are the injectable parts of Watch.
type Options struct {
	// Interval between parent checks; DefaultInterval when zero.
	Interval time.Duration
	// Getppid returns the current parent pid; os.Getppid when nil.
	Getppid func() int
	// Cleanup runs once, before Exit, when the handler is orphaned: kill the
	// child, remove scratch files. Nil means nothing to clean up.
	Cleanup func()
	// Exit ends the process; os.Exit when nil.
	Exit func(code int)
}

// Watch starts checking, every second, whether the process has been orphaned,
// and when it has, runs cleanup (which may be nil) and exits with ExitCode. It
// returns a stop function that ends the watch; calling it more than once is
// harmless. Call stop on the way out of the handler.
func Watch(cleanup func()) (stop func()) { return WatchWith(Options{Cleanup: cleanup}) }

// WatchWith is Watch with injectable parts.
//
// The process is orphaned when its parent pid is no longer the one it started
// with. That covers being re-parented to pid 1 (init or launchd) and also to a
// subreaper, which a literal "getppid() == 1" test would miss. A process that
// STARTS with parent 1 has no wrapper to lose, so it is never reported orphaned.
func WatchWith(o Options) (stop func()) {
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.Getppid == nil {
		o.Getppid = os.Getppid
	}
	if o.Exit == nil {
		o.Exit = os.Exit
	}
	parent := o.Getppid()

	done := make(chan struct{})
	var once sync.Once
	stop = func() { once.Do(func() { close(done) }) }

	go func() {
		tick := time.NewTicker(o.Interval)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				if o.Getppid() == parent {
					continue
				}
				if o.Cleanup != nil {
					o.Cleanup()
				}
				o.Exit(ExitCode)
				return
			}
		}
	}()
	return stop
}
