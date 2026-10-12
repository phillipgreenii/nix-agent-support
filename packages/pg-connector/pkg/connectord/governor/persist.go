package governor

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Store is the slice of the daemon's store the governor persists through: the
// budget table's accessors. *store.Store satisfies it.
//
// One row is appended per recorded origin call (and per reading fed in on its
// own), so a restart replays the window already spent: the governor never
// forgets the hour in progress, and a catch-up after an outage starts from
// what the table says was spent, not from zero.
type Store interface {
	AppendBudget(ctx context.Context, b store.BudgetSample) error
	BudgetSince(ctx context.Context, bucket string, since time.Time) ([]store.BudgetSample, error)
	PruneBudgetBefore(ctx context.Context, t time.Time) (int64, error)
}

var _ Store = (*store.Store)(nil)

// A budget row has no class column, and the background share must survive a
// restart too, so the governor stores the class inside the purpose column as
// "<purpose>:c<class>" (for example "query:c3"). The purpose part is the
// event log's vocabulary; a reader that wants per-purpose spend splits on the
// last ":c". A row without the suffix (not written by this package) decodes
// with class 0, which counts as background: unknown spend is never assumed to
// be the foreground's.
const classMarker = ":c"

func encodePurpose(class Class, p Purpose) string {
	return string(p) + classMarker + strconv.Itoa(int(class))
}

func decodePurpose(s string) (Class, Purpose) {
	i := strings.LastIndex(s, classMarker)
	if i < 0 {
		return 0, Purpose(s)
	}
	n, err := strconv.Atoi(s[i+len(classMarker):])
	if err != nil || n < 0 || n > int(ClassFill) {
		return 0, Purpose(s)
	}
	return Class(n), Purpose(s[:i])
}

// unknownRemaining is what a row stores in its remaining column when the call
// carried no reading (the column is NOT NULL).
const unknownRemaining = -1
