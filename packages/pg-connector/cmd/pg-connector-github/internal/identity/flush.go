package identity

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// MeToken is the query-expression token that means "the viewer": a stored
// result whose expression contains it is only valid for the identity that ran
// it.
const MeToken = "@me"

// QueryStore is the part of the store the query flush needs. *store.Store
// satisfies it.
type QueryStore interface {
	ListQueries(ctx context.Context, configuredOnly bool) ([]store.QueryRow, error)
	DeleteQuery(ctx context.Context, key string) error
}

var _ QueryStore = (*store.Store)(nil)

// ExprResolver returns the expressions behind a stored query key: for a
// configured query its configured expression(s), for any other key nil. The
// key text itself is always inspected as well, so an ad-hoc key that carries
// its expression needs no resolver entry.
type ExprResolver func(key string) []string

// UsesMe reports whether a stored query key depends on the viewer: the key or
// any expression the resolver gives for it contains "@me".
func UsesMe(key string, resolve ExprResolver) bool {
	if strings.Contains(key, MeToken) {
		return true
	}
	if resolve == nil {
		return false
	}
	for _, e := range resolve(key) {
		if strings.Contains(e, MeToken) {
			return true
		}
	}
	return false
}

// FlushMeQueries returns the flush that deletes every stored query result
// (configured and ad-hoc alike) that depends on the viewer and leaves the
// others. Register it with Tracker.RegisterFlush. Entities the dropped queries
// listed stay: they are public GitHub data, not identity-scoped.
func FlushMeQueries(st QueryStore, resolve ExprResolver) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		rows, err := st.ListQueries(ctx, false)
		if err != nil {
			return fmt.Errorf("list queries: %w", err)
		}
		for _, r := range rows {
			if !UsesMe(r.Key, resolve) {
				continue
			}
			if err := st.DeleteQuery(ctx, r.Key); err != nil {
				return fmt.Errorf("delete query %q: %w", r.Key, err)
			}
		}
		return nil
	}
}
