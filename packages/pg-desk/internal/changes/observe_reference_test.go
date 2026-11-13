package changes

import (
	"sort"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// status, doctor and /metrics all read Observe, so what it reports is what each
// shows. This pins the narrow reads Observe now makes (entity flow columns, an
// aggregate over change_log) to the full-row computation they replaced: the
// same records, max seq, active and due counts on a store with a mix of types,
// kinds, origins, inactive and unhydrated rows.
func TestObserveNarrowReadsMatchFullRowReference(t *testing.T) {
	st := store.OpenForTest(t)
	st.SeedLiveSizeForTest(t, 400, 300, 3000)
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	flow, err := Observe(st, now, 6*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	entities, err := st.ListEntities()
	if err != nil {
		t.Fatal(err)
	}
	if len(flow.Types) == 0 {
		t.Fatal("no types observed")
	}
	for _, tf := range flow.Types {
		recs, err := st.ListChangesAfter(tf.Type, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		type key struct{ kind, origin string }
		counts := map[key]int64{}
		var top int64
		for _, r := range recs {
			if r.Seq > top {
				top = r.Seq
			}
			for _, k := range r.Kinds {
				counts[key{k, r.Origin}]++
			}
		}
		var want []RecordCount
		for k, n := range counts {
			want = append(want, RecordCount{Kind: k.kind, Origin: k.origin, Count: n})
		}
		sort.Slice(want, func(i, j int) bool {
			if want[i].Kind != want[j].Kind {
				return want[i].Kind < want[j].Kind
			}
			return want[i].Origin < want[j].Origin
		})
		if tf.MaxSeq != top {
			t.Errorf("%s: MaxSeq %d, want %d", tf.Type, tf.MaxSeq, top)
		}
		if len(tf.Records) != len(want) {
			t.Fatalf("%s: %d records, want %d", tf.Type, len(tf.Records), len(want))
		}
		for i := range want {
			if tf.Records[i] != want[i] {
				t.Errorf("%s: record %d = %+v, want %+v", tf.Type, i, tf.Records[i], want[i])
			}
		}
		if got, w := tf.Active, ActiveCount(entities, tf.Type); got != w {
			t.Errorf("%s: Active %d, want %d", tf.Type, got, w)
		}
		if got, w := tf.Due, DueBacklog(entities, tf.Type, now, 6*time.Hour, 0); got != w {
			t.Errorf("%s: Due %d, want %d", tf.Type, got, w)
		}
	}
}
