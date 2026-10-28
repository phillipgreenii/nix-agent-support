package changes

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// A model of the registration the daemon runs (Spec: EmitWatchedOnly): one PR
// goes through a random sequence of content changes, watches and evictions on a
// real store, and after every step the log and the flag must equal the model's.
// The model: a content change appends one row exactly when the PR is watched; a
// watch of an unwatched, known PR appends one added row and a watch of a watched
// one nothing; the flag, once set, stays until the entity is evicted, and an
// evicted PR that comes back is unwatched again.

func TestModel_AnUnwatchedPRNeverEmitsAndTheFlagIsStickyUntilEviction(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			r := newRigWithSpec(t, Spec())
			r.seedUnwatched(sample())
			var (
				watched  bool // the model's flag
				wantRows int  // the model's row count
				trail    []string
			)
			for step := 0; step < 120; step++ {
				switch op := rng.Intn(10); {
				case op < 5: // a content change of the summary
					r.clk.Advance(time.Second)
					title := fmt.Sprintf("title %d", step)
					r.must(r.changeSummary(func(s *summary.Summary) { s.Title = title }, nil))
					trail = append(trail, "change")
					if watched {
						wantRows++
					}
				case op < 8: // a watch of the entity (a read with the option, or a query)
					r.clk.Advance(time.Second)
					via := registry.ViaDirect
					if rng.Intn(2) == 0 {
						via = registry.ViaQuery + "mine"
					}
					res, err := r.e.WatchOne(context.Background(), r.st, engine.WatchRequest{ID: node, Ref: ref, Via: via, Mode: engine.WatchLate})
					r.must(err)
					trail = append(trail, "watch")
					if res.Newly == watched {
						t.Fatalf("seed %d, steps %v: a watch reported newly = %v while the model's flag is %v", seed, trail, res.Newly, watched)
					}
					if !watched {
						wantRows++ // known entity: one added row
					}
					watched = true
				default: // eviction, then the PR comes back unwatched
					r.must(r.st.DeleteEntity(context.Background(), LogKindPR, node))
					r.seedUnwatched(sample())
					trail = append(trail, "evict")
					watched = false
				}
				rows := r.changes()
				if len(rows) != wantRows {
					t.Fatalf("seed %d, steps %v: %d log rows, the model has %d", seed, trail, len(rows), wantRows)
				}
				got, err := entityWatched(r)
				r.must(err)
				if got != watched {
					t.Fatalf("seed %d, steps %v: the stored flag is %v, the model's is %v", seed, trail, got, watched)
				}
				for _, row := range rows {
					if !row.Watched {
						t.Fatalf("seed %d, steps %v: row %d was appended for an unwatched PR: %+v", seed, trail, row.Seq, row)
					}
				}
			}
		})
	}
}

func entityWatched(r *rig) (bool, error) {
	var out bool
	err := r.inTx(func(tx *store.Tx) error {
		var err error
		out, err = tx.IsWatched(context.Background(), LogKindPR, node)
		return err
	})
	return out, err
}
