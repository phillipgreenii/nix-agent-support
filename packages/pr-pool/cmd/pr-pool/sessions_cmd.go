package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/ccpool/sessionmeta"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
)

// sessionRow is one pr-pool session resolved from session metadata.
type sessionRow struct {
	ExternalID string
	Bead       string
	Role       string
}

// metaReader is the read subset of sessionmeta.Store used by the sessions view.
type metaReader interface {
	ListByMeta(ctx context.Context, filters map[string]string) ([]string, error)
	Meta(ctx context.Context, externalID string) (map[string]string, error)
}

// collectPoolSessions finds this pool's sessions (ListByMeta on prpool.pool) and
// expands each one's bead/role (Meta). external_ids come back sorted.
func collectPoolSessions(ctx context.Context, r metaReader) ([]sessionRow, error) {
	ids, err := r.ListByMeta(ctx, map[string]string{ccpool.MetaKeyPool: ccpool.PoolName})
	if err != nil {
		return nil, err
	}
	rows := make([]sessionRow, 0, len(ids))
	for _, id := range ids {
		m, err := r.Meta(ctx, id)
		if err != nil {
			return nil, err
		}
		rows = append(rows, sessionRow{ExternalID: id, Bead: m[ccpool.MetaKeyBead], Role: m[ccpool.MetaKeyRole]})
	}
	return rows, nil
}

// renderSessions writes the human-readable session list.
func renderSessions(w io.Writer, rows []sessionRow) {
	_, _ = fmt.Fprintf(w, "pool sessions (%d):\n", len(rows))
	for _, s := range rows {
		_, _ = fmt.Fprintf(w, "  - %-40s bead=%-12s role=%s\n", s.ExternalID, s.Bead, s.Role)
	}
}

// runSessions implements `pr-pool sessions`: list this pool's sessions with bead/role
// resolved from ccpool session metadata. Read-only; opens the SAME pool `ccpool new`
// writes to (CCPOOL_POOL-honoring, matching ccpool's own Load(); OpenPool("") would
// instead use LoadForPool which ignores CCPOOL_POOL).
func runSessions() int {
	store, err := sessionmeta.OpenPool(os.Getenv("CCPOOL_POOL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "sessions:", err)
		return exitGeneric
	}
	defer func() { _ = store.Close() }()
	rows, err := collectPoolSessions(context.Background(), store)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sessions:", err)
		return exitGeneric
	}
	renderSessions(os.Stdout, rows)
	return exitOK
}
