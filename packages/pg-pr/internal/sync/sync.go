// Package sync is the pg-pr sync engine. Phase 0 stub; full impl in Phase 1+.
package sync

import (
	"context"
	"errors"
)

var ErrNotImplemented = errors.New("sync: not implemented in this phase")

// Sync runs one full sync cycle across all configured repos.
func Sync(_ context.Context) error { return ErrNotImplemented }

// SyncPR runs a single-PR sync.
func SyncPR(_ context.Context, _ string, _ int) error { return ErrNotImplemented }
