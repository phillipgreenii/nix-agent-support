//go:build lockorder

package lockcheck

import "github.com/sasha-s/go-deadlock"

// Mutex is go-deadlock's Mutex in a build with the `lockorder` tag.
type Mutex = deadlock.Mutex
