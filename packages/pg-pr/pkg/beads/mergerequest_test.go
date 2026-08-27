package beads

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestEnsureMergeRequest_Validates(t *testing.T) {
	ctx := context.Background()
	c := NewClientWithRunner(&fakeRunner{})

	if _, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{}); err == nil {
		t.Fatalf("expected validation error on empty input")
	}
	if _, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "a/b"}); err == nil {
		t.Fatalf("expected validation error when pr_number is missing")
	}
}

// fakeRunner is a no-op runner used to test argument-validation paths that
// must fail BEFORE bd is invoked.
type fakeRunner struct{}

func (f *fakeRunner) Run(_ context.Context, _ ...string) (string, error) {
	return "", nil
}

func TestFindByRepoAndNumber_Validates(t *testing.T) {
	c := NewClientWithRunner(&fakeRunner{})
	if _, err := c.FindByRepoAndNumber(context.Background(), "", 1); err == nil {
		t.Fatalf("expected error on empty repo")
	}
	if _, err := c.FindByRepoAndNumber(context.Background(), "a/b", 0); err == nil {
		t.Fatalf("expected error on zero pr_number")
	}
}

// TestNewClientForRepo_SetsRunnerDir verifies the constructed Client's inner
// CLIRunner has the requested Dir, so bd will be invoked with that path as
// cwd and pick up the monorepo's `.beads/` workspace.
func TestNewClientForRepo_SetsRunnerDir(t *testing.T) {
	dir := "/tmp/some-monorepo-root"
	c := NewClientForRepo(dir)
	if c == nil {
		t.Fatalf("expected non-nil Client")
	} else if cli, ok := c.Runner.(*CLIRunner); !ok {
		t.Fatalf("expected runner to be *CLIRunner, got %T", c.Runner)
	} else if cli.Dir != dir {
		t.Fatalf("Dir: got %q want %q", cli.Dir, dir)
	}
}

// TestNewClientForRepo_EmptyDirMatchesNewClient documents that passing "" is
// equivalent to NewClient() — both yield a Client whose runner has no Dir
// and therefore inherits the process cwd.
func TestNewClientForRepo_EmptyDirMatchesNewClient(t *testing.T) {
	c := NewClientForRepo("")
	cli, ok := c.Runner.(*CLIRunner)
	if !ok {
		t.Fatalf("expected runner to be *CLIRunner, got %T", c.Runner)
	}
	if cli.Dir != "" {
		t.Fatalf("Dir: got %q want empty (cwd-discovered)", cli.Dir)
	}
}

// coOwnedRunner returns canned (empty) output and records calls, for
// asserting the exact `update <id> --add-label/--remove-label co-owned`
// arguments SetMergeRequestCoOwned sends to bd.
type coOwnedRunner struct {
	calls [][]string
}

func (r *coOwnedRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	return "", nil
}

func (r *coOwnedRunner) lastCall() []string {
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1]
}

// TestSetMergeRequestCoOwned asserts coOwned=true sends
// `update <id> --add-label co-owned` and coOwned=false sends
// `update <id> --remove-label co-owned`.
func TestSetMergeRequestCoOwned(t *testing.T) {
	t.Run("coOwned=true adds the label", func(t *testing.T) {
		r := &coOwnedRunner{}
		c := NewClientWithRunner(r)
		if err := c.SetMergeRequestCoOwned(context.Background(), "mr-1", true); err != nil {
			t.Fatalf("SetMergeRequestCoOwned: %v", err)
		}
		got := r.lastCall()
		want := []string{"update", "mr-1", "--add-label", "co-owned"}
		if len(got) != len(want) {
			t.Fatalf("call = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("call = %v, want %v", got, want)
			}
		}
	})

	t.Run("coOwned=false removes the label", func(t *testing.T) {
		r := &coOwnedRunner{}
		c := NewClientWithRunner(r)
		if err := c.SetMergeRequestCoOwned(context.Background(), "mr-1", false); err != nil {
			t.Fatalf("SetMergeRequestCoOwned: %v", err)
		}
		got := r.lastCall()
		want := []string{"update", "mr-1", "--remove-label", "co-owned"}
		if len(got) != len(want) {
			t.Fatalf("call = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("call = %v, want %v", got, want)
			}
		}
	})
}

// TestSetMergeRequestCoOwned_Validates asserts an empty id is rejected before
// bd is invoked.
func TestSetMergeRequestCoOwned_Validates(t *testing.T) {
	c := NewClientWithRunner(&fakeRunner{})
	if err := c.SetMergeRequestCoOwned(context.Background(), "", true); err == nil {
		t.Fatalf("expected validation error on empty id")
	}
}

// TestSetPriority asserts SetPriority sends `update <id> -p <n>` to bd.
// Reuses coOwnedRunner (a generic calls-recorder already defined above) per
// controller guidance rather than inventing a new recorder or an assertCalled
// method on fakeRunner.
func TestSetPriority(t *testing.T) {
	r := &coOwnedRunner{}
	c := NewClientWithRunner(r)
	if err := c.SetPriority(context.Background(), "mr-1", 1); err != nil {
		t.Fatalf("SetPriority: %v", err)
	}
	got := r.lastCall()
	want := []string{"update", "mr-1", "-p", "1"}
	if len(got) != len(want) {
		t.Fatalf("call = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call = %v, want %v", got, want)
		}
	}
}

// TestSetPriority_Validates asserts an empty id is rejected before bd is
// invoked.
func TestSetPriority_Validates(t *testing.T) {
	r := &coOwnedRunner{}
	c := NewClientWithRunner(r)
	if err := c.SetPriority(context.Background(), "", 1); err == nil {
		t.Fatalf("expected validation error on empty id")
	}
	if len(r.calls) != 0 {
		t.Fatalf("expected no bd calls on validation failure, got %v", r.calls)
	}
}

// TestSetPriority_ClampsRange asserts out-of-range priorities are clamped to
// [0,4] rather than passed through or rejected.
func TestSetPriority_ClampsRange(t *testing.T) {
	t.Run("negative clamps to 0", func(t *testing.T) {
		r := &coOwnedRunner{}
		c := NewClientWithRunner(r)
		if err := c.SetPriority(context.Background(), "mr-1", -5); err != nil {
			t.Fatalf("SetPriority: %v", err)
		}
		got := r.lastCall()
		want := []string{"update", "mr-1", "-p", "0"}
		if len(got) != len(want) {
			t.Fatalf("call = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("call = %v, want %v", got, want)
			}
		}
	})

	t.Run("above 4 clamps to 4", func(t *testing.T) {
		r := &coOwnedRunner{}
		c := NewClientWithRunner(r)
		if err := c.SetPriority(context.Background(), "mr-1", 9); err != nil {
			t.Fatalf("SetPriority: %v", err)
		}
		got := r.lastCall()
		want := []string{"update", "mr-1", "-p", "4"}
		if len(got) != len(want) {
			t.Fatalf("call = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("call = %v, want %v", got, want)
			}
		}
	})
}

// TestAddLabel asserts AddLabel sends `update <id> --add-label <label>` to bd.
// Used by the conflict->priority reconciler (pg2-tsgkj) to stash the baseline
// priority in a `pbase:<n>` label.
func TestAddLabel(t *testing.T) {
	r := &coOwnedRunner{}
	c := NewClientWithRunner(r)
	if err := c.AddLabel(context.Background(), "mr-1", "pbase:2"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	got := r.lastCall()
	want := []string{"update", "mr-1", "--add-label", "pbase:2"}
	if len(got) != len(want) {
		t.Fatalf("call = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call = %v, want %v", got, want)
		}
	}
}

// TestRemoveLabel asserts RemoveLabel sends `update <id> --remove-label
// <label>` to bd. Used by the conflict->priority reconciler (pg2-tsgkj) to
// drop the `pbase:<n>` marker once the baseline priority is restored.
func TestRemoveLabel(t *testing.T) {
	r := &coOwnedRunner{}
	c := NewClientWithRunner(r)
	if err := c.RemoveLabel(context.Background(), "mr-1", "pbase:2"); err != nil {
		t.Fatalf("RemoveLabel: %v", err)
	}
	got := r.lastCall()
	want := []string{"update", "mr-1", "--remove-label", "pbase:2"}
	if len(got) != len(want) {
		t.Fatalf("call = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call = %v, want %v", got, want)
		}
	}
}

// TestBdIssueToMergeRequest_ParsesPriorityAndLabels pins the fix for
// bdIssueToMergeRequest previously dropping labels entirely and not
// surfacing priority at all.
func TestBdIssueToMergeRequest_ParsesPriorityAndLabels(t *testing.T) {
	iss := bdIssue{
		ID: "bd-2", Priority: 3, Labels: []string{"co-owned", "pbase:2"},
		Metadata: map[string]any{"repo": "o/r", "pr_number": float64(5)},
	}
	mr := bdIssueToMergeRequest(iss)
	if mr.Priority != 3 {
		t.Errorf("Priority = %d, want 3", mr.Priority)
	}
	if len(mr.Labels) != 2 {
		t.Errorf("Labels = %v, want 2", mr.Labels)
	}
	if mr.Labels[0] != "co-owned" || mr.Labels[1] != "pbase:2" {
		t.Errorf("Labels = %v, want [co-owned pbase:2]", mr.Labels)
	}
}

// ----------------------------------------------------------------------
// Diff-before-write (write-amplification elimination: pg2-ojqz5 FB-1/2/4)
// ----------------------------------------------------------------------

// mrDiffRunner is a fake Runner for the diff-before-write proofs. It returns a
// canned bd-list envelope for any READ (`bd list ...`) so the diff logic has a
// current stored state to compare against, and records EVERY call so a test can
// assert whether a WRITE (each of which, against real bd, produces a Dolt
// commit) was issued. Distinct from coOwnedRunner, which returns "" for every
// call (i.e. "bead not found") and so cannot exercise the already-in-desired-
// state skip path.
type mrDiffRunner struct {
	listJSON string
	calls    [][]string
}

func (r *mrDiffRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if len(args) > 0 && args[0] == "list" {
		return r.listJSON, nil
	}
	return "", nil
}

// writeCalls returns only the recorded calls that MUTATE bd state — the ones
// that, against real bd, produce a Dolt commit. Reads (`list`) are excluded.
func (r *mrDiffRunner) writeCalls() [][]string {
	var w [][]string
	for _, c := range r.calls {
		if len(c) == 0 {
			continue
		}
		switch c[0] {
		case "update", "create", "close", "dep":
			w = append(w, c)
		}
	}
	return w
}

// cannedList marshals issues into the bd 1.0.4+ list envelope parseBDList reads.
func cannedList(t *testing.T, issues ...bdIssue) string {
	t.Helper()
	env := struct {
		Data          []bdIssue `json:"data"`
		SchemaVersion int       `json:"schema_version"`
	}{Data: issues, SchemaVersion: 1}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal canned list: %v", err)
	}
	return string(b)
}

// storedMR is the current-state bead the daemon re-observes each refresh: an
// open merge-request whose metadata already reflects the upstream PR.
func storedMR() bdIssue {
	return bdIssue{
		ID: "mr-1", Title: "foo/bar#7", Status: "open", Type: "merge-request",
		Metadata: map[string]any{
			"repo":           "foo/bar",
			"pr_number":      float64(7),
			"state":          "open",
			"branch":         "feat/x",
			"base":           "main",
			"author":         "alice",
			"url":            "https://github.com/foo/bar/pull/7",
			"last_synced_at": "2020-01-01T00:00:00Z",
		},
	}
}

// storedMRWith returns storedMR() with its metadata patched: a key mapped to
// nil is DELETED (models a legacy bead with no "draft" key stored at all);
// any other value is set/overwritten. A sibling fixture rather than a
// mutation of storedMR() itself, since the existing no-op tests
// (TestEnsureMergeRequest_NoOpDoesNotWrite, TestReconcileMergeRequest_NoOpDoesNotWrite,
// ...) depend on storedMR()'s current shape.
func storedMRWith(patch map[string]any) bdIssue {
	iss := storedMR()
	md := make(map[string]any, len(iss.Metadata)+len(patch))
	for k, v := range iss.Metadata {
		md[k] = v
	}
	for k, v := range patch {
		if v == nil {
			delete(md, k)
			continue
		}
		md[k] = v
	}
	iss.Metadata = md
	return iss
}

// metadataArg extracts the JSON string value of a --metadata argument from a
// recorded bd call, e.g. []string{"update", "mr-1", "--metadata", `{...}`}.
func metadataArg(t *testing.T, call []string) string {
	t.Helper()
	for i, a := range call {
		if a == "--metadata" && i+1 < len(call) {
			return call[i+1]
		}
	}
	t.Fatalf("no --metadata argument found in call %v", call)
	return ""
}

// TestEnsureMergeRequest_DraftToReadyClearsStoredTrue is the pg2-4dz88.10
// regression test: a bead stored with draft:true, re-synced with
// Draft:false as the ONLY delta (state held constant so it can't mask the
// result), must issue exactly one write whose --metadata patch carries an
// EXPLICIT "draft":false — not merely omit the key (encodeMetadata's
// omitempty trap would otherwise leave the previously-stored true in place
// forever, since `bd update --metadata` merges rather than replaces).
func TestEnsureMergeRequest_DraftToReadyClearsStoredTrue(t *testing.T) {
	ctx := context.Background()
	r := &mrDiffRunner{listJSON: cannedList(t, storedMRWith(map[string]any{"draft": true}))}
	c := NewClientWithRunner(r)

	if _, alreadyClosed, err := c.EnsureMergeRequest(ctx, "foo/bar#7", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, State: "open", Draft: false,
	}); err != nil {
		t.Fatalf("EnsureMergeRequest: %v", err)
	} else if alreadyClosed {
		t.Fatalf("expected alreadyClosed=false")
	}

	w := r.writeCalls()
	if len(w) != 1 {
		t.Fatalf("expected exactly one write clearing draft, got %d: %v", len(w), w)
	}
	metaJSON := metadataArg(t, w[0])
	if !strings.Contains(metaJSON, `"draft":false`) {
		t.Fatalf("expected literal \"draft\":false in metadata JSON (omitempty trap), got %s", metaJSON)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(metaJSON), &decoded); err != nil {
		t.Fatalf("decode metadata JSON: %v", err)
	}
	draftVal, present := decoded["draft"]
	if !present {
		t.Fatalf("expected draft key present in decoded metadata, got %v", decoded)
	}
	if draftVal != false {
		t.Fatalf("expected decoded draft == false, got %v", draftVal)
	}
}

// TestEnsureMergeRequest_DraftTransitionTable is the transition table from the
// pg2-4dz88.10 design: every (stored draft, desired Draft) combination, with
// state held constant at "open" throughout so it can never mask the write
// decision. Row 1 is the bug (stored true, desired false, must write and
// clear). Rows 2/3 guard the opposite direction. Rows 4/5 are the no-op
// guarantee: steady state must still short-circuit to zero writes. Row 6 is
// the churn guard: a legacy bead with no "draft" key and a non-draft PR must
// NOT earn a gratuitous write just to materialize an explicit false — absent
// and false are the same state.
func TestEnsureMergeRequest_DraftTransitionTable(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	cases := []struct {
		name         string
		storedDraft  *bool // nil = key absent from stored metadata
		desiredDraft bool
		wantWrite    bool
		wantEncoded  bool // only checked when wantWrite
	}{
		{name: "row1_true_to_false_is_the_bug", storedDraft: boolPtr(true), desiredDraft: false, wantWrite: true, wantEncoded: false},
		{name: "row2_absent_to_true", storedDraft: nil, desiredDraft: true, wantWrite: true, wantEncoded: true},
		{name: "row3_false_to_true", storedDraft: boolPtr(false), desiredDraft: true, wantWrite: true, wantEncoded: true},
		{name: "row4_true_to_true_is_noop", storedDraft: boolPtr(true), desiredDraft: true, wantWrite: false},
		{name: "row5_false_to_false_is_noop", storedDraft: boolPtr(false), desiredDraft: false, wantWrite: false},
		{name: "row6_absent_to_false_is_noop", storedDraft: nil, desiredDraft: false, wantWrite: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			patch := map[string]any{}
			if tc.storedDraft != nil {
				patch["draft"] = *tc.storedDraft
			}
			r := &mrDiffRunner{listJSON: cannedList(t, storedMRWith(patch))}
			c := NewClientWithRunner(r)

			if _, _, err := c.EnsureMergeRequest(ctx, "foo/bar#7", MergeRequestFields{
				Repo: "foo/bar", PRNumber: 7, State: "open", Draft: tc.desiredDraft,
			}); err != nil {
				t.Fatalf("EnsureMergeRequest: %v", err)
			}

			w := r.writeCalls()
			if tc.wantWrite {
				if len(w) != 1 {
					t.Fatalf("expected exactly one write, got %d: %v", len(w), w)
				}
				metaJSON := metadataArg(t, w[0])
				var decoded map[string]any
				if err := json.Unmarshal([]byte(metaJSON), &decoded); err != nil {
					t.Fatalf("decode metadata JSON: %v", err)
				}
				draftVal, present := decoded["draft"]
				if !present {
					t.Fatalf("expected draft key present in decoded metadata, got %v", decoded)
				}
				if draftVal != tc.wantEncoded {
					t.Fatalf("encoded draft: got %v want %v", draftVal, tc.wantEncoded)
				}
			} else if len(w) != 0 {
				t.Fatalf("expected ZERO writes (steady state), got %d: %v", len(w), w)
			}
		})
	}
}

// TestEnsureMergeRequest_DraftIdempotentAfterConvergence guards against a
// regression where the pg2-4dz88.10 fix over-corrects into re-writing every
// tick: a runner whose listJSON already reflects the POST-write converged
// state (stored draft == desired draft) must record ZERO writes, and running
// the identical tick a SECOND time in a row must still record zero — not
// just "the first time happened to no-op".
func TestEnsureMergeRequest_DraftIdempotentAfterConvergence(t *testing.T) {
	for _, desiredDraft := range []bool{true, false} {
		t.Run(fmt.Sprintf("draft=%v steady state", desiredDraft), func(t *testing.T) {
			ctx := context.Background()
			r := &mrDiffRunner{listJSON: cannedList(t, storedMRWith(map[string]any{"draft": desiredDraft}))}
			c := NewClientWithRunner(r)
			fields := MergeRequestFields{Repo: "foo/bar", PRNumber: 7, State: "open", Draft: desiredDraft}

			if _, _, err := c.EnsureMergeRequest(ctx, "foo/bar#7", fields); err != nil {
				t.Fatalf("first pass: %v", err)
			}
			if w := r.writeCalls(); len(w) != 0 {
				t.Fatalf("first pass (already converged) expected zero writes, got %v", w)
			}

			// Second pass: an identical tick against the same already-converged
			// state must remain a no-op, not accumulate a write.
			if _, _, err := c.EnsureMergeRequest(ctx, "foo/bar#7", fields); err != nil {
				t.Fatalf("second pass: %v", err)
			}
			if w := r.writeCalls(); len(w) != 0 {
				t.Fatalf("second pass expected ZERO write calls (steady state), got %v", w)
			}
		})
	}
}

// TestEnsureMergeRequest_NoOpDoesNotWrite is the core FB-1/FB-2 proof: a refresh
// whose ONLY delta from the stored bead is a fresh last_synced_at (exactly what
// the per-minute daemon produces) issues NO bd write/commit — killing the 428k
// no-op 'nothing to commit' commits.
func TestEnsureMergeRequest_NoOpDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	r := &mrDiffRunner{listJSON: cannedList(t, storedMR())}
	c := NewClientWithRunner(r)

	id, alreadyClosed, err := c.EnsureMergeRequest(ctx, "foo/bar#7", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, State: "open", Branch: "feat/x", Base: "main",
		Author: "alice", URL: "https://github.com/foo/bar/pull/7",
		LastSyncedAt: "2026-07-25T12:00:00Z", // the ONLY "change": a new poll timestamp
	})
	if err != nil {
		t.Fatalf("EnsureMergeRequest: %v", err)
	}
	if id != "mr-1" || alreadyClosed {
		t.Fatalf("id=%q alreadyClosed=%v, want mr-1/false", id, alreadyClosed)
	}
	if w := r.writeCalls(); len(w) != 0 {
		t.Fatalf("expected ZERO bd writes for a last_synced_at-only refresh, got %v", w)
	}
}

// TestEnsureMergeRequest_RealChangeWritesOnce proves a refresh that changes a
// REAL field (state open->ready) still issues exactly one bd update/commit, and
// that the fresh last_synced_at rides along with it.
func TestEnsureMergeRequest_RealChangeWritesOnce(t *testing.T) {
	ctx := context.Background()
	r := &mrDiffRunner{listJSON: cannedList(t, storedMR())}
	c := NewClientWithRunner(r)

	if _, _, err := c.EnsureMergeRequest(ctx, "foo/bar#7", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, State: "ready", // real change
		LastSyncedAt: "2026-07-25T12:00:00Z",
	}); err != nil {
		t.Fatalf("EnsureMergeRequest: %v", err)
	}
	w := r.writeCalls()
	if len(w) != 1 {
		t.Fatalf("expected exactly one bd write on a real change, got %d: %v", len(w), w)
	}
	if w[0][0] != "update" || w[0][1] != "mr-1" {
		t.Fatalf("expected `update mr-1 --metadata ...`, got %v", w[0])
	}
}

// TestSetMergeRequestCoOwned_SkipsWhenAlreadyInDesiredState proves the daemon's
// per-tick co-owned re-assertion issues NO bd write when the label already
// matches the desired state (FB-4). The current labels are read from the
// scripted list; the diff then suppresses the redundant add/remove-label.
func TestSetMergeRequestCoOwned_SkipsWhenAlreadyInDesiredState(t *testing.T) {
	ctx := context.Background()

	t.Run("label present, desired co-owned -> no write", func(t *testing.T) {
		iss := storedMR()
		iss.Labels = []string{"co-owned"}
		r := &mrDiffRunner{listJSON: cannedList(t, iss)}
		c := NewClientWithRunner(r)
		if err := c.SetMergeRequestCoOwned(ctx, "mr-1", true); err != nil {
			t.Fatalf("SetMergeRequestCoOwned: %v", err)
		}
		if w := r.writeCalls(); len(w) != 0 {
			t.Fatalf("expected no write when already co-owned, got %v", w)
		}
	})

	t.Run("label absent, desired not-co-owned -> no write", func(t *testing.T) {
		r := &mrDiffRunner{listJSON: cannedList(t, storedMR())} // no labels
		c := NewClientWithRunner(r)
		if err := c.SetMergeRequestCoOwned(ctx, "mr-1", false); err != nil {
			t.Fatalf("SetMergeRequestCoOwned: %v", err)
		}
		if w := r.writeCalls(); len(w) != 0 {
			t.Fatalf("expected no write when already not-co-owned, got %v", w)
		}
	})
}

// TestSetMergeRequestCoOwned_WritesOnChange proves a genuine co-owned transition
// still issues exactly one add/remove-label write.
func TestSetMergeRequestCoOwned_WritesOnChange(t *testing.T) {
	ctx := context.Background()

	t.Run("label absent, desired co-owned -> add-label", func(t *testing.T) {
		r := &mrDiffRunner{listJSON: cannedList(t, storedMR())}
		c := NewClientWithRunner(r)
		if err := c.SetMergeRequestCoOwned(ctx, "mr-1", true); err != nil {
			t.Fatalf("SetMergeRequestCoOwned: %v", err)
		}
		w := r.writeCalls()
		if len(w) != 1 || w[0][2] != "--add-label" || w[0][3] != "co-owned" {
			t.Fatalf("expected one `update mr-1 --add-label co-owned`, got %v", w)
		}
	})

	t.Run("label present, desired not-co-owned -> remove-label", func(t *testing.T) {
		iss := storedMR()
		iss.Labels = []string{"co-owned"}
		r := &mrDiffRunner{listJSON: cannedList(t, iss)}
		c := NewClientWithRunner(r)
		if err := c.SetMergeRequestCoOwned(ctx, "mr-1", false); err != nil {
			t.Fatalf("SetMergeRequestCoOwned: %v", err)
		}
		w := r.writeCalls()
		if len(w) != 1 || w[0][2] != "--remove-label" || w[0][3] != "co-owned" {
			t.Fatalf("expected one `update mr-1 --remove-label co-owned`, got %v", w)
		}
	})
}

// ---------------------------------------------------------------------------
// ReconcileMergeRequest: read-once + single-write projection (pg2-pz7y8).
// ---------------------------------------------------------------------------

// reconcileHarness runs the exact two-call sequence beadsbridge.Handler.project
// runs for one pr.opened/updated tick: ONE fresh read (FindByRepoAndNumberUncached),
// then ONE combined create-or-update (ReconcileMergeRequest). Tests below
// exercise this pair together, then inspect the recording runner's calls to
// prove the read/write counts and the combined args.
func reconcileHarness(ctx context.Context, c *Client, repo string, pr int, userTitle string, fields MergeRequestFields, coOwned, hasConflict, actsAsMine bool) (string, bool, error) {
	existing, err := c.FindByRepoAndNumberUncached(ctx, repo, pr)
	if err != nil {
		return "", false, err
	}
	return c.ReconcileMergeRequest(ctx, existing, userTitle, fields, coOwned, hasConflict, actsAsMine)
}

// splitReadsWrites partitions recorded runner calls into `list` reads and
// every other (mutating) verb.
func splitReadsWrites(calls [][]string) (reads, writes int) {
	for _, c := range calls {
		if len(c) == 0 {
			continue
		}
		if c[0] == "list" {
			reads++
		} else {
			writes++
		}
	}
	return reads, writes
}

// mrDiffCreateRunner is mrDiffRunner plus a fixed ID returned for `create`
// calls, so ReconcileMergeRequest's not-found (create) path can be exercised
// without a real bd workspace.
type mrDiffCreateRunner struct {
	mrDiffRunner
	createID string
}

func (r *mrDiffCreateRunner) Run(ctx context.Context, args ...string) (string, error) {
	out, err := r.mrDiffRunner.Run(ctx, args...)
	if len(args) > 0 && args[0] == "create" {
		return r.createID, err
	}
	return out, err
}

// TestReconcileMergeRequest_NoOpDoesNotWrite proves a refresh whose fields,
// co-owned state, and conflict state ALL already match stored state issues
// exactly ONE read and ZERO writes — the read-once/write-once no-op case is
// now cheaper than before the refactor (previously a no-change tick still
// spent a SECOND read via the old GetMergeRequestUncached).
func TestReconcileMergeRequest_NoOpDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	r := &mrDiffRunner{listJSON: cannedList(t, storedMR())}
	c := NewClientWithRunner(r)

	id, alreadyClosed, err := reconcileHarness(ctx, c, "foo/bar", 7, "foo/bar#7", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, State: "open", Branch: "feat/x", Base: "main",
		Author: "alice", URL: "https://github.com/foo/bar/pull/7",
		LastSyncedAt: "2026-07-25T12:00:00Z", // FB-1: last_synced_at-only is not a change
	}, false, false, true) // not co-owned, no conflict — matches stored (no labels)
	if err != nil {
		t.Fatalf("reconcileHarness: %v", err)
	}
	if id != "mr-1" || alreadyClosed {
		t.Fatalf("id=%q alreadyClosed=%v, want mr-1/false", id, alreadyClosed)
	}
	reads, writes := splitReadsWrites(r.calls)
	if reads != 1 {
		t.Fatalf("expected exactly 1 read, got %d: %v", reads, r.calls)
	}
	if writes != 0 {
		t.Fatalf("expected ZERO writes for a full no-op tick, got %d: %v", writes, r.calls)
	}
}

// TestReconcileMergeRequest_ClosedBeadNoWrites proves an existing CLOSED bead
// short-circuits BEFORE any field/co-owned/priority diff is even attempted —
// not merely that the diffs happen to no-op. This is the precise contract
// TestPROpenedClosedParentSkipsDraftReview (internal/beadsbridge) used to pin
// at the bridge-fake level; it now lives here, where the short-circuit
// actually executes.
func TestReconcileMergeRequest_ClosedBeadNoWrites(t *testing.T) {
	ctx := context.Background()
	iss := storedMR()
	iss.Status = "closed"
	r := &mrDiffRunner{listJSON: cannedList(t, iss)}
	c := NewClientWithRunner(r)

	// coOwned=true and hasConflict=true would, on an OPEN bead, both demand a
	// write. On a closed bead neither may be attempted.
	id, alreadyClosed, err := reconcileHarness(ctx, c, "foo/bar", 7, "foo/bar#7", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, State: "ready",
	}, true, true, true)
	if err != nil {
		t.Fatalf("reconcileHarness: %v", err)
	}
	if !alreadyClosed || id != "mr-1" {
		t.Fatalf("id=%q alreadyClosed=%v, want mr-1/true", id, alreadyClosed)
	}
	_, writes := splitReadsWrites(r.calls)
	if writes != 0 {
		t.Fatalf("expected ZERO writes for a closed bead, got %d: %v", writes, r.calls)
	}
}

// TestReconcileMergeRequest_CombinedChangeSingleWrite proves a tick that needs
// ALL THREE mutations at once — a real field change, a co-owned label flip,
// and a first-conflict priority nudge — still issues exactly ONE combined
// `bd update` call, not three separate ones.
func TestReconcileMergeRequest_CombinedChangeSingleWrite(t *testing.T) {
	ctx := context.Background()
	iss := storedMR()
	iss.Priority = 2 // no labels yet: not co-owned, no pbase baseline
	r := &mrDiffRunner{listJSON: cannedList(t, iss)}
	c := NewClientWithRunner(r)

	id, alreadyClosed, err := reconcileHarness(ctx, c, "foo/bar", 7, "foo/bar#7", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, State: "ready", // real field change (open -> ready)
	}, true, true, true) // desired co-owned=true, conflict=true, mine
	if err != nil {
		t.Fatalf("reconcileHarness: %v", err)
	}
	if alreadyClosed || id != "mr-1" {
		t.Fatalf("id=%q alreadyClosed=%v, want mr-1/false", id, alreadyClosed)
	}
	reads, writes := splitReadsWrites(r.calls)
	if reads != 1 || writes != 1 {
		t.Fatalf("expected 1 read + 1 combined write, got reads=%d writes=%d: %v", reads, writes, r.calls)
	}
	w := r.writeCalls()
	joined := strings.Join(w[0], " ")
	if w[0][0] != "update" || w[0][1] != "mr-1" {
		t.Fatalf("expected `update mr-1 ...`, got %v", w[0])
	}
	if !strings.Contains(joined, "--metadata") {
		t.Fatalf("expected the field change folded into the combined write, got %v", w[0])
	}
	if !strings.Contains(joined, "--add-label pbase:2") {
		t.Fatalf("expected the pbase baseline stash folded into the combined write, got %v", w[0])
	}
	if !strings.Contains(joined, "--add-label co-owned") {
		t.Fatalf("expected the co-owned label folded into the combined write, got %v", w[0])
	}
	if !strings.Contains(joined, "-p 1") {
		t.Fatalf("expected the nudged priority (2 -> 1, mine conflict) folded into the combined write, got %v", w[0])
	}
}

// TestReconcileMergeRequest_CreatesWhenAbsent proves the not-found path issues
// exactly ONE `bd create` call, with a conflict-nudged priority and a co-owned
// label already folded in when the very first tick already carries them —
// reproducing what a subsequent read of the freshly created bead would show,
// without spending that read (see bdDefaultPriority's doc).
func TestReconcileMergeRequest_CreatesWhenAbsent(t *testing.T) {
	ctx := context.Background()
	r := &mrDiffCreateRunner{mrDiffRunner: mrDiffRunner{listJSON: cannedList(t)}, createID: "mr-new-1"}
	c := NewClientWithRunner(r)

	id, alreadyClosed, err := reconcileHarness(ctx, c, "foo/bar", 8, "foo/bar#8", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 8, State: "open",
	}, true, true, true) // co-owned + conflict already true on the first tick
	if err != nil {
		t.Fatalf("reconcileHarness: %v", err)
	}
	if alreadyClosed {
		t.Fatalf("expected alreadyClosed=false on create")
	}
	if id != "mr-new-1" {
		t.Fatalf("id=%q, want mr-new-1", id)
	}
	reads, writes := splitReadsWrites(r.calls)
	if reads != 1 || writes != 1 {
		t.Fatalf("expected 1 read + 1 create, got reads=%d writes=%d: %v", reads, writes, r.calls)
	}
	create := r.calls[len(r.calls)-1]
	if create[0] != "create" {
		t.Fatalf("expected a create call, got %v", create)
	}
	joined := strings.Join(create, " ")
	if !strings.Contains(joined, "-l pbase:2,co-owned") {
		t.Fatalf("expected combined labels (pbase baseline + co-owned) in the create call, got %v", create)
	}
	if !strings.Contains(joined, "-p 1") {
		t.Fatalf("expected the nudged priority (bdDefaultPriority=2 -> 1, mine conflict) in the create call, got %v", create)
	}
}

// TestMergeRequestPriorityDelta pins the pure conflict-priority decision
// (relocated verbatim from internal/beadsbridge/bridge.go's former
// reconcilePriority — see pg2-pz7y8) across its four cases: mine raises and
// stashes, team lowers and stashes, a repeated conflicting tick is a no-op,
// and a clear restores the exact baseline.
func TestMergeRequestPriorityDelta(t *testing.T) {
	cases := []struct {
		name                string
		curPriority         int
		curLabels           []string
		actsAsMine          bool
		hasConflict         bool
		wantAdd, wantRemove []string
		wantPriority        int
		wantSetPriority     bool
	}{
		{
			name:        "mine first conflict raises and stashes baseline",
			curPriority: 2, curLabels: nil, actsAsMine: true, hasConflict: true,
			wantAdd: []string{"pbase:2"}, wantPriority: 1, wantSetPriority: true,
		},
		{
			name:        "team first conflict lowers and stashes baseline",
			curPriority: 2, curLabels: nil, actsAsMine: false, hasConflict: true,
			wantAdd: []string{"pbase:2"}, wantPriority: 3, wantSetPriority: true,
		},
		{
			name:        "repeated conflict is idempotent no-op",
			curPriority: 1, curLabels: []string{"pbase:2"}, actsAsMine: true, hasConflict: true,
			wantSetPriority: false,
		},
		{
			name:        "conflict cleared restores exact baseline",
			curPriority: 1, curLabels: []string{"pbase:2"}, actsAsMine: true, hasConflict: false,
			wantRemove: []string{"pbase:2"}, wantPriority: 2, wantSetPriority: true,
		},
		{
			name:        "no conflict no baseline is a no-op",
			curPriority: 2, curLabels: nil, actsAsMine: true, hasConflict: false,
			wantSetPriority: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addLabels, removeLabels, priority, setPriority := mergeRequestPriorityDelta(tc.curPriority, tc.curLabels, tc.actsAsMine, tc.hasConflict)
			if !equalStringSlices(addLabels, tc.wantAdd) {
				t.Errorf("addLabels: got %v want %v", addLabels, tc.wantAdd)
			}
			if !equalStringSlices(removeLabels, tc.wantRemove) {
				t.Errorf("removeLabels: got %v want %v", removeLabels, tc.wantRemove)
			}
			if setPriority != tc.wantSetPriority {
				t.Errorf("setPriority: got %v want %v", setPriority, tc.wantSetPriority)
			}
			if setPriority && priority != tc.wantPriority {
				t.Errorf("priority: got %d want %d", priority, tc.wantPriority)
			}
		})
	}
}

// equalStringSlices compares two string slices treating nil and empty as equal.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
