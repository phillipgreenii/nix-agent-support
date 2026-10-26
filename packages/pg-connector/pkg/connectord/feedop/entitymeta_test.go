package feedop

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func TestEntityMetaOfStatesTheStoredFlag(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		ent   store.EntityRow
		found bool
		want  bool
	}{
		{"watched row", store.EntityRow{WatchedAt: at, WatchedVia: "direct"}, true, true},
		{"tracked-only row", store.EntityRow{}, true, false},
		{"no row", store.EntityRow{}, false, false},
		{"a stray flag on a row that was not found is not read", store.EntityRow{WatchedAt: at}, false, false},
	} {
		got, stated := EntityMetaOf(tc.ent, tc.found).WatchedState()
		if !stated || got != tc.want {
			t.Errorf("%s: (%v, stated %v), want %v stated", tc.name, got, stated, tc.want)
		}
	}
}
