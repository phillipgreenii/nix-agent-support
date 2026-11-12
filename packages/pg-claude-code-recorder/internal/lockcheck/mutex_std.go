//go:build !lockorder

// Package lockcheck names the mutex type of the two locks whose order matters
// in the settle stage: the settler's run lock and the copier's lock (the order
// is run lock, then copier lock). A production build gets sync.Mutex itself, an
// alias with no cost. A build with the `lockorder` tag (the lock-order tests of
// internal/settle) gets go-deadlock's Mutex, which reports an inverted order and
// a lock that others wait on for too long. Only the tagged file imports
// go-deadlock, so the production binary does not link it.
package lockcheck

import "sync"

// Mutex is sync.Mutex in every build without the `lockorder` tag.
type Mutex = sync.Mutex
