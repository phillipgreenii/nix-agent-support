package classify

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// prFixture reads a synthetic fixture from testdata/.
func prFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// prDecodeMap decodes a fixture (or a marshaled mutation) into a generic map.
func prDecodeMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode facts: %v", err)
	}
	return m
}

// prObj returns the nested object at key (panics through the test on a
// missing or non-object value so a typo in a mutation fails loudly).
func prObj(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	o, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("fixture has no object %q", key)
	}
	return o
}

// prFirst returns the first object of the array at key inside m.
func prFirst(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	a, ok := m[key].([]any)
	if !ok || len(a) == 0 {
		t.Fatalf("fixture has no array %q", key)
	}
	o, ok := a[0].(map[string]any)
	if !ok {
		t.Fatalf("fixture array %q does not hold objects", key)
	}
	return o
}

// prMutated returns the base fixture with edit applied, marshaled back.
func prMutated(t *testing.T, base []byte, edit func(m map[string]any)) []byte {
	t.Helper()
	m := prDecodeMap(t, base)
	if edit != nil {
		edit(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal facts: %v", err)
	}
	return b
}

func prSnapshot(payload []byte) Snapshot {
	return Snapshot{Type: "pr", ID: "example/repo#7", Exists: true, Active: true, Payload: json.RawMessage(payload)}
}

func prKinds(rs []Record) []Kind {
	out := []Kind{}
	for _, r := range rs {
		out = append(out, r.Kind)
	}
	return out
}

// TestPRClassifierKinds pins the signal-to-kind mapping, one row per kind
// plus the no-change rows. Every row runs through the top-level Classify with
// Type "pr", so the registration from pr.go's init() is exercised too.
func TestPRClassifierKinds(t *testing.T) {
	base := prFixture(t, "pr_open_base.json")
	show := func(m map[string]any) map[string]any { return prObj(t, m, "pr_show") }

	cases := []struct {
		name string
		old  []byte
		new  []byte
		want []Kind
	}{
		{
			name: "opened: previous snapshot carried no pr_show state, new one is open",
			old:  prFixture(t, "pr_empty_show.json"),
			new:  base,
			want: []Kind{KindOpened},
		},
		{
			name: "reopened: closed to open",
			old:  prMutated(t, base, func(m map[string]any) { show(m)["state"] = "closed" }),
			new:  base,
			want: []Kind{KindReopened},
		},
		{
			name: "closed: open to closed",
			old:  base,
			new:  prMutated(t, base, func(m map[string]any) { show(m)["state"] = "closed" }),
			want: []Kind{KindClosed},
		},
		{
			name: "merged: open to closed with merged flag",
			old:  base,
			new: prMutated(t, base, func(m map[string]any) {
				show(m)["state"] = "closed"
				show(m)["merged"] = true
			}),
			want: []Kind{KindMerged},
		},
		{
			name: "merged: open to state merged",
			old:  base,
			new:  prMutated(t, base, func(m map[string]any) { show(m)["state"] = "merged" }),
			want: []Kind{KindMerged},
		},
		{
			name: "draft_changed",
			old:  base,
			new:  prMutated(t, base, func(m map[string]any) { show(m)["draft"] = true }),
			want: []Kind{KindDraftChanged},
		},
		{
			name: "head_changed: pr_show head_sha",
			old:  base,
			new: prMutated(t, base, func(m map[string]any) {
				show(m)["head_sha"] = "cccc333"
				m["head_sha"] = "cccc333"
			}),
			want: []Kind{KindHeadChanged},
		},
		{
			name: "base_changed: base branch",
			old:  base,
			new:  prMutated(t, base, func(m map[string]any) { show(m)["base"] = "release" }),
			want: []Kind{KindBaseChanged},
		},
		{
			name: "ci_changed: a run conclusion",
			old:  base,
			new: prMutated(t, base, func(m map[string]any) {
				prFirst(t, prObj(t, m, "ci"), "runs")["conclusion"] = "failure"
			}),
			want: []Kind{KindCiChanged},
		},
		{
			name: "ci_changed: a re-run (same run id, next attempt)",
			old:  base,
			new: prMutated(t, base, func(m map[string]any) {
				prFirst(t, prObj(t, m, "ci"), "runs")["attempt"] = 2
			}),
			want: []Kind{KindCiChanged},
		},
		{
			name: "ci_changed: checks_rollup",
			old:  base,
			new:  prMutated(t, base, func(m map[string]any) { show(m)["checks_rollup"] = "pending" }),
			want: []Kind{KindCiChanged},
		},
		{
			name: "mergeability_changed: UNKNOWN is a value like any other",
			old:  base,
			new:  prMutated(t, base, func(m map[string]any) { show(m)["mergeable"] = "UNKNOWN" }),
			want: []Kind{KindMergeabilityChanged},
		},
		{
			name: "review_changed: review_decision",
			old:  base,
			new:  prMutated(t, base, func(m map[string]any) { show(m)["review_decision"] = "APPROVED" }),
			want: []Kind{KindReviewChanged},
		},
		{
			name: "review_changed: a review's state",
			old:  base,
			new: prMutated(t, base, func(m map[string]any) {
				prFirst(t, show(m), "reviews")["state"] = "APPROVED"
			}),
			want: []Kind{KindReviewChanged},
		},
		{
			name: "review_changed: review_count",
			old:  base,
			new:  prMutated(t, base, func(m map[string]any) { show(m)["review_count"] = 2 }),
			want: []Kind{KindReviewChanged},
		},
		{
			name: "feedback_changed: comment_count",
			old:  base,
			new:  prMutated(t, base, func(m map[string]any) { show(m)["comment_count"] = 2 }),
			want: []Kind{KindFeedbackChanged},
		},
		{
			name: "feedback_changed: a top-level comment resolved",
			old:  base,
			new: prMutated(t, base, func(m map[string]any) {
				prFirst(t, show(m), "comments")["resolved"] = true
			}),
			want: []Kind{KindFeedbackChanged},
		},
		{
			name: "feedback_changed: a review-thread comment resolved",
			old:  base,
			new: prMutated(t, base, func(m map[string]any) {
				r := prFirst(t, show(m), "reviews")
				prFirst(t, r, "comments")["resolved"] = true
			}),
			want: []Kind{KindFeedbackChanged},
		},
		{
			name: "several signals at once come back sorted by kind",
			old:  base,
			new: prMutated(t, base, func(m map[string]any) {
				show(m)["head_sha"] = "cccc333"
				show(m)["checks_rollup"] = "failure"
				show(m)["draft"] = true
			}),
			want: []Kind{KindCiChanged, KindDraftChanged, KindHeadChanged},
		},
		{
			name: "no change: only the embedded as_of and stale differ",
			old:  base,
			new: prMutated(t, base, func(m map[string]any) {
				m["as_of"] = "2026-01-01T00:10:00Z"
				show(m)["as_of"] = "2026-01-01T00:10:00Z"
				show(m)["stale"] = true
				show(m)["updated_at"] = "2026-01-01T00:09:00Z"
				run := prFirst(t, prObj(t, m, "ci"), "runs")
				run["as_of"] = "2026-01-01T00:10:00Z"
				run["stale"] = false
			}),
			want: []Kind{},
		},
		{
			name: "no change: merge_state_status alone",
			old:  base,
			new:  prMutated(t, base, func(m map[string]any) { show(m)["merge_state_status"] = "BEHIND" }),
			want: []Kind{},
		},
		{
			name: "no change: base_sha alone",
			old:  base,
			new:  prMutated(t, base, func(m map[string]any) { show(m)["base_sha"] = "dddd444" }),
			want: []Kind{},
		},
		{
			name: "no change: a failed CI read is not a ci_changed",
			old:  base,
			new: prMutated(t, base, func(m map[string]any) {
				delete(m, "ci")
				m["degraded"] = "ci list"
			}),
			want: []Kind{},
		},
		{
			name: "no change: a partial CI read (a source degraded, runs missing) is not a ci_changed",
			old:  base,
			new: prMutated(t, base, func(m map[string]any) {
				ci := prObj(t, m, "ci")
				ci["runs"] = []any{}
				ci["sources"] = []any{map[string]any{"source": "ci-example", "status": "degraded", "count": 0}}
			}),
			want: []Kind{},
		},
		{
			name: "no change: CI present only on the new side is not a ci_changed",
			old:  prMutated(t, base, func(m map[string]any) { delete(m, "ci") }),
			new:  base,
			want: []Kind{},
		},
		{
			name: "no change: a stale CI answer (served from cache) is not a ci_changed",
			old:  base,
			new: prMutated(t, base, func(m map[string]any) {
				run := prFirst(t, prObj(t, m, "ci"), "runs")
				run["conclusion"] = "failure"
				run["stale"] = true
			}),
			want: []Kind{},
		},
		{
			name: "no change: an empty mergeable on one side is missing data",
			old:  base,
			new:  prMutated(t, base, func(m map[string]any) { delete(show(m), "mergeable") }),
			want: []Kind{},
		},
		{
			name: "no change: a missing draft field on one side is missing data",
			old:  prMutated(t, base, func(m map[string]any) { delete(show(m), "draft") }),
			new:  prMutated(t, base, func(m map[string]any) { show(m)["draft"] = true }),
			want: []Kind{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := prKinds(Classify(prSnapshot(tc.old), prSnapshot(tc.new)))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Classify = %v, want %v", got, tc.want)
			}
			for _, k := range got {
				if k == KindWorkChanged || k == KindLinkChanged {
					t.Fatalf("pr classifier emitted %q, which belongs to another packet", k)
				}
			}
		})
	}
}

// TestPRClassifierRegistered checks Register("pr", ...) ran from init().
func TestPRClassifierRegistered(t *testing.T) {
	if _, ok := Lookup("pr"); !ok {
		t.Fatal(`no classifier registered for "pr"`)
	}
}

func TestPRClassifyIdenticalSnapshotsIsEmpty(t *testing.T) {
	s := prSnapshot(prFixture(t, "pr_open_base.json"))
	if got := Classify(s, s); len(got) != 0 {
		t.Fatalf("Classify(s, s) = %v, want empty", got)
	}
	// Differing only in AsOf/ContentHash/HeadSHA metadata is still identical.
	n := s
	n.AsOf, n.ContentHash, n.HeadSHA = "2026-02-02T00:00:00Z", "h2", "sha2"
	if got := Classify(s, n); len(got) != 0 {
		t.Fatalf("metadata-only difference yielded %v, want empty", got)
	}
}

func TestPRFirstObservationYieldsOnlyReconcile(t *testing.T) {
	n := prSnapshot(prFixture(t, "pr_open_base.json"))
	want := []Kind{KindReconcile}

	first := n
	first.Exists = false
	if got := prKinds(Classify(first, n)); !reflect.DeepEqual(got, want) {
		t.Fatalf("no previous snapshot: got %v, want exactly %v", got, want)
	}

	inactive := prSnapshot(prMutated(t, prFixture(t, "pr_open_base.json"), func(m map[string]any) {
		prObj(t, m, "pr_show")["state"] = "closed"
	}))
	inactive.Active = false
	if got := prKinds(Classify(inactive, n)); !reflect.DeepEqual(got, want) {
		t.Fatalf("inactive previous snapshot: got %v, want exactly %v", got, want)
	}
}

func TestPRKindsArePureFunctionOfOldAndNew(t *testing.T) {
	base := prFixture(t, "pr_open_base.json")
	old := prSnapshot(base)
	new := prSnapshot(prMutated(t, base, func(m map[string]any) {
		s := prObj(t, m, "pr_show")
		s["head_sha"] = "cccc333"
		s["review_decision"] = "APPROVED"
		s["state"] = "closed"
		s["merged"] = true
	}))
	oldPayload := bytes.Clone(old.Payload)
	newPayload := bytes.Clone(new.Payload)

	first := Classify(old, new)
	want := []Kind{KindHeadChanged, KindMerged, KindReviewChanged}
	if !reflect.DeepEqual(prKinds(first), want) {
		t.Fatalf("got %v, want %v", prKinds(first), want)
	}
	for i := 0; i < 20; i++ {
		if got := Classify(old, new); !reflect.DeepEqual(got, first) {
			t.Fatalf("call %d: got %v, want %v", i, got, first)
		}
	}
	if !bytes.Equal(old.Payload, oldPayload) || !bytes.Equal(new.Payload, newPayload) {
		t.Fatal("classification mutated an input payload")
	}
}

func TestPRDegradedBackendNeverYieldsRemovedOrClosed(t *testing.T) {
	base := prFixture(t, "pr_open_base.json")
	old := prSnapshot(base)

	noClosedOrRemoved := func(t *testing.T, got []Record) {
		t.Helper()
		for _, r := range got {
			if r.Kind == KindClosed || r.Kind == KindRemoved {
				t.Fatalf("got %v, which includes %q", prKinds(got), r.Kind)
			}
		}
	}

	t.Run("pr_show absent in a degraded snapshot", func(t *testing.T) {
		n := prSnapshot(prFixture(t, "pr_degraded_no_show.json"))
		n.Degraded = true
		got := Classify(old, n)
		noClosedOrRemoved(t, got)
		if len(got) != 0 {
			t.Fatalf("got %v, want empty: nothing can be inferred from missing data", prKinds(got))
		}
	})
	t.Run("pr_show absent, snapshot not flagged degraded", func(t *testing.T) {
		got := Classify(old, prSnapshot(prFixture(t, "pr_degraded_no_show.json")))
		if len(got) != 0 {
			t.Fatalf("got %v, want empty", prKinds(got))
		}
	})
	t.Run("pr_show empty object", func(t *testing.T) {
		got := Classify(old, prSnapshot(prFixture(t, "pr_empty_show.json")))
		if len(got) != 0 {
			t.Fatalf("got %v, want empty", prKinds(got))
		}
	})
	t.Run("pr_show null", func(t *testing.T) {
		n := prSnapshot(prMutated(t, base, func(m map[string]any) { m["pr_show"] = nil }))
		if got := Classify(old, n); len(got) != 0 {
			t.Fatalf("got %v, want empty", prKinds(got))
		}
	})
	t.Run("degraded snapshot whose pr_show says closed", func(t *testing.T) {
		n := prSnapshot(prMutated(t, base, func(m map[string]any) { prObj(t, m, "pr_show")["state"] = "closed" }))
		n.Degraded = true
		noClosedOrRemoved(t, Classify(old, n))
	})
	t.Run("facts.degraded set and pr_show says closed", func(t *testing.T) {
		n := prSnapshot(prMutated(t, base, func(m map[string]any) {
			prObj(t, m, "pr_show")["state"] = "closed"
			m["degraded"] = "pr files"
		}))
		noClosedOrRemoved(t, Classify(old, n))
	})
	t.Run("control: healthy closed does yield closed", func(t *testing.T) {
		n := prSnapshot(prMutated(t, base, func(m map[string]any) { prObj(t, m, "pr_show")["state"] = "closed" }))
		if got := prKinds(Classify(old, n)); !reflect.DeepEqual(got, []Kind{KindClosed}) {
			t.Fatalf("got %v, want [closed]", got)
		}
	})
	t.Run("a removed-event payload (removed_state only) yields nothing", func(t *testing.T) {
		n := prSnapshot([]byte(`{"removed_state":"not_found"}`))
		got := Classify(old, n)
		noClosedOrRemoved(t, got)
		if len(got) != 0 {
			t.Fatalf("got %v, want empty", prKinds(got))
		}
	})
}
