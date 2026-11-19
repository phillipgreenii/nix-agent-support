//go:build integration

package ccpool_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/ccpool/sessionmeta"
)

// TestSessionmeta_importable proves pg-router can build against and call ccpool's
// public sessionmeta package in-process (Option 2). The real orchestrator wiring
// is a separate pg-router bead; this only guards the dependency edge.
func TestSessionmeta_importable(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ccpool.db")
	s, err := sessionmeta.Open(db)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Set(context.Background(), "zr-x", "pool", "pg-router"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ids, err := s.ListByMeta(context.Background(), map[string]string{"pool": "pg-router"})
	if err != nil || len(ids) != 1 || ids[0] != "zr-x" {
		t.Fatalf("ListByMeta = (%v,%v), want [zr-x]", ids, err)
	}
}
