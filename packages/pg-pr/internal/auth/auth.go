// Package auth checks credential availability for each configured provider.
// Phase 0 stub.
package auth

import (
	"context"
	"errors"
)

var ErrNotImplemented = errors.New("auth: not implemented in this phase")

// Status describes a single provider's auth state.
type Status struct {
	Provider string
	State    string // OK | MISSING | EXPIRED | INSUFFICIENT_SCOPES
	Detail   string
}

// CheckAll iterates configured providers and reports auth status.
func CheckAll(_ context.Context) ([]Status, error) { return nil, ErrNotImplemented }
