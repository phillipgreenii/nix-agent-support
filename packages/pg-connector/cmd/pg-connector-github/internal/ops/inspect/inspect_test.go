package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fixedTracker answers the keep-alive question from a fixed set.
type fixedTracker map[string]bool

func (f fixedTracker) InKeepAliveSet(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = f[id]
	}
	return out, nil
}

func newHandler(t *testing.T, tr Tracker) (scriptout.OpHandler, *store.Store) {
	t.Helper()
	st, _, err := store.Open(":memory:", store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h, err := Handler(Config{Store: st, Tracker: tr})
	if err != nil {
		t.Fatal(err)
	}
	return h, st
}

func seed(t *testing.T, st *store.Store, node, ref, via string, tomb bool) {
	t.Helper()
	ctx := context.Background()
	r, err := entity.ParseRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entity.NewIdentity(st, nil).Put(ctx, node, r); err != nil {
		t.Fatal(err)
	}
	row := store.EntityRow{Kind: entity.KindPR, ID: node}
	if tomb {
		row.TombstonedAt = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	}
	if err := st.PutEntity(ctx, row); err != nil {
		t.Fatal(err)
	}
	if via != "" {
		err := st.InTx(ctx, func(tx *store.Tx) error {
			_, err := tx.SetWatched(ctx, entity.KindPR, node, via, time.Date(2026, 10, 2, 3, 4, 5, 0, time.UTC))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func call(t *testing.T, h scriptout.OpHandler, args string) (Result, error) {
	t.Helper()
	v, err := h.Handle(context.Background(), json.RawMessage(args))
	if err != nil {
		return Result{}, err
	}
	return v.(Result), nil
}

func TestInspectAnswersTrackedWatchedViaAndTimeFromTheStore(t *testing.T) {
	h, st := newHandler(t, fixedTracker{"N1": true, "N2": true})
	seed(t, st, "N1", "o/r#1", "", false)
	seed(t, st, "N2", "o/r#2", "query:mine", false)
	seed(t, st, "N3", "o/r#3", "direct", true) // watched, tombstoned: not tracked

	res, err := call(t, h, `{"ids":["o/r#1","O/R#2","o/r#3","o/r#4"]}`)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Item{}
	for _, it := range res.Results {
		by[it.ID] = it
	}
	if it := by["o/r#1"]; !it.Known || !it.Tracked || it.Watched || it.WatchedVia != "" || it.WatchedAt != nil {
		t.Errorf("tracked only = %+v", it)
	}
	it := by["O/R#2"] // the reference is folded; the entry keeps the spelling asked
	if !it.Watched || it.WatchedVia != "query:mine" || it.WatchedAt == nil || it.WatchedAt.Format(time.RFC3339) != "2026-10-02T03:04:05Z" || it.Ref != "o/r#2" || it.NodeID != "N2" {
		t.Errorf("watched = %+v", it)
	}
	if it := by["o/r#3"]; !it.Tombstoned || it.Tracked || !it.Watched {
		t.Errorf("watched and tombstoned = %+v, want watched, tombstoned, not tracked", it)
	}
	if it := by["o/r#4"]; it.Known || it.Tracked || it.Watched || it.NodeID != "" {
		t.Errorf("unknown = %+v", it)
	}
	if want := (Counts{Requested: 4, Known: 3, Unknown: 1, Tracked: 2, Watched: 2}); res.Counts != want {
		t.Errorf("counts = %+v, want %+v", res.Counts, want)
	}
}

func TestInspectTakesTheSingleFormAndNodeIDsAndCountsAPRNamedTwiceOnce(t *testing.T) {
	h, st := newHandler(t, fixedTracker{"N1": true})
	seed(t, st, "N1", "o/r#1", "direct", false)
	res, err := call(t, h, `{"id":"o/r#1","ids":["N1","o/r#1"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 1 || res.Counts != (Counts{Requested: 1, Known: 1, Tracked: 1, Watched: 1}) {
		t.Errorf("result = %+v, want one entry, the PR counted once", res)
	}
	if res.Results[0].ID != "o/r#1" {
		t.Errorf("the entry is under %q, want the first spelling asked", res.Results[0].ID)
	}
}

func TestInspectRefusesAMalformedRequestWithInvalidArgument(t *testing.T) {
	h, _ := newHandler(t, fixedTracker{})
	many := make([]string, MaxIDs+1)
	for i := range many {
		many[i] = fmt.Sprintf("o/r#%d", i+1)
	}
	tooMany, _ := json.Marshal(map[string]any{"ids": many})
	for name, args := range map[string]string{
		"nothing":       `{}`,
		"empty ids":     `{"ids":[]}`,
		"blank id":      `{"ids":["o/r#1"," "]}`,
		"bad reference": `{"ids":["o/r#zero"]}`,
		"too many":      string(tooMany),
	} {
		if _, err := call(t, h, args); !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Errorf("%s: error = %v, want invalid_argument", name, err)
		}
	}
}
