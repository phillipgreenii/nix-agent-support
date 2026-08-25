package daemon

import (
	"context"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// stubPoller is a minimal PollerInterface implementation used by tick tests.
// Avoids needing to construct the full *poller.Poller with its many
// dependencies. Deliberately untagged (bead pg2-h05lt): nudger_lifecycle_test.go
// (unit) uses it too, so it must compile even when tick_integration_test.go's
// `//go:build integration` tag is absent (the default `go test ./...` build).
type stubPoller struct {
	snapshot func(ctx context.Context) (*aggregate.Tree, bool, error)
}

func (s *stubPoller) Snapshot(ctx context.Context) (*aggregate.Tree, bool, error) {
	return s.snapshot(ctx)
}
