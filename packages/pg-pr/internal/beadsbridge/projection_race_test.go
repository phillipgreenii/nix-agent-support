package beadsbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// projection_race_test.go reproduces the DUPLICATE-BEAD race (bead pg2-35rl6)
// deterministically, through the REAL production stack: two beadsbridge Handlers
// over two real *beads.Client instances that share ONE bd workspace, exactly the
// shape cmd/pg-pr/sync.go's dispatcher builds (a fresh Handler and a fresh
// per-repo client per event, all pointed at the same `.beads/`).
//
// WHY THE RACE IS REAL IN PRODUCTION: Engine.Daemon starts two projecting
// workers (mine + team) plus a maintenance flusher; each calls flushOutbox on the
// SHARED outbox, and store.RunOutbox neither claims rows nor partitions them. A
// PR the user authored in a repo whose team query also covers it is enqueued onto
// BOTH queues (detector.go's mineQ.enqueue / teamQ.enqueue), so one tick can put
// two goroutines into the same projection at the same time.
//
// HOW THE INTERLEAVE IS MADE DETERMINISTIC: the fake bd CLI holds a RENDEZVOUS on
// the one list command whose answer the create-decision is made from. Both
// goroutines must reach that read before either is released, so an unguarded
// read→decide→write window ALWAYS produces two creates — no timing luck. With the
// per-PR projection lock in place the second goroutine cannot reach the read
// until the first has finished writing, the rendezvous falls through on its
// timeout, and exactly one bead is created.

// arrivalIDKey carries a per-goroutine marker on the ctx that the bridge already
// threads all the way down into Runner.Run, so the rendezvous can tell the two
// callers apart without guessing at goroutine identity.
type arrivalIDKey struct{}

func withArrivalID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, arrivalIDKey{}, id)
}

func arrivalIDOf(ctx context.Context) string {
	s, _ := ctx.Value(arrivalIDKey{}).(string)
	return s
}

// rendezvous releases every arrival once `want` DISTINCT arrival ids have shown
// up, or after timeout — whichever comes first. The timeout is what lets the
// FIXED code pass: under the lock the peer never arrives, so the first (and only)
// arrival falls through and proceeds alone.
type rendezvous struct {
	mu      sync.Mutex
	seen    map[string]bool
	want    int
	ready   chan struct{}
	open    bool
	timeout time.Duration
}

func newRendezvous(want int, timeout time.Duration) *rendezvous {
	return &rendezvous{seen: map[string]bool{}, want: want, ready: make(chan struct{}), timeout: timeout}
}

func (r *rendezvous) arrive(id string) {
	if id == "" {
		return // not one of the racing callers
	}
	r.mu.Lock()
	r.seen[id] = true
	if len(r.seen) >= r.want && !r.open {
		r.open = true
		close(r.ready)
	}
	already := r.open
	ch := r.ready
	r.mu.Unlock()
	if already {
		return
	}
	select {
	case <-ch:
	case <-time.After(r.timeout):
	}
}

// wsBead is one bead in the fake workspace, marshalled in the shape
// `bd list --json` returns (the bd 1.0.4+ {"data":[...]} envelope).
type wsBead struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Status    string         `json:"status"`
	IssueType string         `json:"issue_type"`
	Priority  int            `json:"priority"`
	Desc      string         `json:"description,omitempty"`
	CreatedAt string         `json:"created_at,omitempty"`
	Labels    []string       `json:"labels,omitempty"`
	Metadata  map[string]any `json:"metadata"`
}

// fakeWorkspace is a mutex-guarded in-memory bd workspace serving the subset of
// `bd` the bridge drives: list (by type/status/id), create, update, close, and
// the parent-child dep verbs. Both racing clients share ONE instance, which is
// what makes it a workspace rather than a per-client stub.
type fakeWorkspace struct {
	mu     sync.Mutex
	beads  []*wsBead
	kids   map[string][]string // parent bead id -> child bead ids
	calls  [][]string
	nextID int

	// barrierOn selects the command the rendezvous fires on: the read whose
	// answer the create-decision is made from.
	barrierOn func(args []string) bool
	barrier   *rendezvous
}

func newFakeWorkspace(barrierOn func(args []string) bool) *fakeWorkspace {
	return &fakeWorkspace{
		kids:      map[string][]string{},
		barrierOn: barrierOn,
		barrier:   newRendezvous(2, 300*time.Millisecond),
	}
}

// seed installs a pre-existing bead (used to stand up the merge-request parent
// before racing the process-feedback projection).
func (w *fakeWorkspace) seed(b *wsBead) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.beads = append(w.beads, b)
}

// createsOf counts `bd create` invocations whose --title equals want. It is the
// duplicate detector: two creates for one identity IS the defect.
func (w *fakeWorkspace) createsOf(title string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, args := range w.calls {
		if len(args) == 0 || args[0] != "create" {
			continue
		}
		if got, ok := argValue(args, "--title"); ok && got == title {
			n++
		}
	}
	return n
}

// Run answers one bd invocation, holding the answer to the barrier command until
// the peer has taken ITS OWN snapshot of the same state.
//
// The rendezvous is entered AFTER the read has been served and the workspace
// mutex released, not before — and that ordering is what makes the test
// deterministic rather than a coin flip. Gating on ARRIVAL instead lets the
// released goroutine run its create while the peer is still queued on the
// workspace mutex, so the peer's read observes the bead and no duplicate appears
// (observed: the unguarded code "passed" ~half the time). Gating on the ANSWER
// pins both decisions to the pre-create state, which is precisely the production
// interleave: two projections that each read before either wrote.
func (w *fakeWorkspace) Run(ctx context.Context, args ...string) (string, error) {
	out, err := w.serve(args)
	if w.barrierOn != nil && w.barrierOn(args) {
		w.barrier.arrive(arrivalIDOf(ctx))
	}
	return out, err
}

func (w *fakeWorkspace) serve(args []string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, args)

	switch {
	case args[0] == "create":
		w.nextID++
		b := &wsBead{
			ID:        fmt.Sprintf("bead-%d", w.nextID),
			Status:    "open",
			CreatedAt: "2026-08-12T12:35:25Z",
			Metadata:  map[string]any{},
		}
		b.Title, _ = argValue(args, "--title")
		b.Desc, _ = argValue(args, "-d")
		if t, ok := argValue(args, "--type"); ok {
			b.IssueType = t
		}
		b.Labels = argValues(args, "-l")
		if m, ok := argValue(args, "--metadata"); ok {
			_ = json.Unmarshal([]byte(m), &b.Metadata)
		}
		w.beads = append(w.beads, b)
		return b.ID, nil

	case args[0] == "update":
		return "", nil

	case args[0] == "close":
		for _, b := range w.beads {
			if b.ID == args[1] {
				b.Status = "closed"
			}
		}
		return "", nil

	case args[0] == "dep" && len(args) > 1 && args[1] == "add":
		child, parent := args[2], args[3]
		w.kids[parent] = append(w.kids[parent], child)
		return "", nil

	case args[0] == "dep" && len(args) > 1 && args[1] == "list":
		out := make([]map[string]string, 0, len(w.kids[args[2]]))
		for _, id := range w.kids[args[2]] {
			out = append(out, map[string]string{"id": id})
		}
		return marshalEnvelope(out)

	case args[0] == "list":
		wantID, byID := argValue(args, "--id")
		wantType, byType := argValue(args, "--type")
		wantStatus, byStatus := argValue(args, "--status")
		// `bd list` shows only OPEN beads unless --all, an explicit --status, or
		// an --id selection says otherwise.
		openOnly := !byStatus && !byID && !hasArg(args, "--all")
		var out []*wsBead
		for _, b := range w.beads {
			if byID && b.ID != wantID {
				continue
			}
			if byType && b.IssueType != wantType {
				continue
			}
			if byStatus && b.Status != wantStatus {
				continue
			}
			if openOnly && b.Status != "open" {
				continue
			}
			out = append(out, b)
		}
		return marshalEnvelope(out)
	}
	return "", fmt.Errorf("fakeWorkspace: unhandled bd %s", strings.Join(args, " "))
}

func marshalEnvelope(data any) (string, error) {
	b, err := json.Marshal(map[string]any{"data": data, "schema_version": 1})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// argValue reads a flag value in either `--flag value` or `--flag=value` form.
func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"="), true
		}
	}
	return "", false
}

// argValues collects every value of a REPEATED flag (bd's `-l label`).
func argValues(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// raceTwoProjections dispatches the same event through TWO Handlers over TWO
// clients sharing one workspace, concurrently, and waits for both.
func raceTwoProjections(t *testing.T, ws *fakeWorkspace, e store.Event) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// A fresh client AND a fresh Handler per dispatch — exactly what
			// newBeadsBridgeHandler in cmd/pg-pr/sync.go builds for every event.
			h := New(beads.NewClientWithRunner(ws))
			ctx := withArrivalID(context.Background(), fmt.Sprintf("goroutine-%d", i))
			errs[i] = h.Handle(ctx, e)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("projection %d failed: %v", i, err)
		}
	}
}

// TestConcurrentPRProjectionCreatesOneMergeRequestBead is the merge-request half
// of the race. EnsureMergeRequest reads findByRepoPR and then creates; the
// rendezvous holds both goroutines at that read, so without the per-PR lock both
// see "no bead for o/r#7" and both `bd create --type=merge-request`.
func TestConcurrentPRProjectionCreatesOneMergeRequestBead(t *testing.T) {
	ws := newFakeWorkspace(func(args []string) bool {
		return args[0] == "list" && hasArg(args, "--type=merge-request")
	})

	payload, err := json.Marshal(store.PRPayload{
		Repo: "o/r", Number: 7, Title: "add a thing", Ownership: "mine",
		State: "open", Branch: "feat", Base: "main", Author: "alice",
		URL: "https://example.invalid/o/r/pull/7", LastSyncedAt: "2026-08-12T12:35:20Z",
	})
	if err != nil {
		t.Fatalf("marshal pr payload: %v", err)
	}

	raceTwoProjections(t, ws, store.Event{Type: store.EventPROpened, Payload: payload})

	if got := ws.createsOf("o/r#7: add a thing"); got != 1 {
		t.Fatalf("merge-request beads created for o/r#7: got %d, want 1 — the resolve→create window in EnsureMergeRequest is unguarded", got)
	}
	// The draft-review child hangs off the same unguarded window
	// (EnsureDraftReviewBead reads its child list, then creates).
	if got := ws.createsOf("draft-review: o/r#7"); got != 1 {
		t.Fatalf("draft-review beads created for o/r#7: got %d, want 1", got)
	}
}

// TestConcurrentFeedbackProjectionCreatesOneProcessFeedbackBead is the
// process-feedback half — the family observed duplicated in production
// (zr-7oixl / zr-xivnw, same instant, IDENTICAL fbsum digest label). The
// identical digest is the tell: both goroutines hashed the same feedback set and
// each compared it against a bead the other had not committed yet, so the
// existing digest guard could not fire. The rendezvous reproduces exactly that.
func TestConcurrentFeedbackProjectionCreatesOneProcessFeedbackBead(t *testing.T) {
	ws := newFakeWorkspace(func(args []string) bool {
		return args[0] == "list" && hasArg(args, "--type=task") && hasArg(args, "--status=open")
	})
	// The merge-request parent already exists (the outbox projects pr.opened
	// before feedback.created — see concurrency_test.go's ordering invariant).
	ws.seed(&wsBead{
		ID: "mr-1", Title: "o/r#8: add a thing", Status: "open",
		IssueType: "merge-request", CreatedAt: "2026-08-12T12:30:00Z",
		Metadata: map[string]any{"repo": "o/r", "pr_number": 8, "state": "open"},
	})

	payload, err := json.Marshal(store.FeedbackPayload{
		Repo: "o/r", Number: 8, Mine: false,
		Summary: &store.FeedbackSummary{
			Unaddressed: 2,
			ByKind:      map[string]int{"code-comment-thread": 2},
			Reviewers:   []string{"bob"},
			Digest:      "8c6a0a4b36ac",
		},
	})
	if err != nil {
		t.Fatalf("marshal feedback payload: %v", err)
	}

	raceTwoProjections(t, ws, store.Event{Type: store.EventFeedbackCreated, Payload: payload})

	if got := ws.createsOf("process-feedback: o/r#8"); got != 1 {
		t.Fatalf("process-feedback beads created for o/r#8: got %d, want 1 — the ResolveProcessingCycle→CreateProcessingCycle window is unguarded", got)
	}
}

// TestConcurrentProjectionsOfDifferentPRsStayConcurrent guards the fix against
// over-serialization: the two daemon workers MUST keep making progress on
// DIFFERENT PRs at the same time. The rendezvous is the assertion — it only
// releases when BOTH goroutines reach the read, so if the lock were global the
// second could not arrive and both would stall on the timeout.
func TestConcurrentProjectionsOfDifferentPRsStayConcurrent(t *testing.T) {
	ws := newFakeWorkspace(func(args []string) bool {
		return args[0] == "list" && hasArg(args, "--type=merge-request")
	})

	var wg sync.WaitGroup
	for i, number := range []int{11, 12} {
		payload, err := json.Marshal(store.PRPayload{
			Repo: "o/r", Number: number, Title: "t", Ownership: "mine",
			State: "open", Branch: "feat", Base: "main", Author: "alice",
			LastSyncedAt: "2026-08-12T12:35:20Z",
		})
		if err != nil {
			t.Fatalf("marshal pr payload: %v", err)
		}
		wg.Add(1)
		go func(i int, payload []byte) {
			defer wg.Done()
			h := New(beads.NewClientWithRunner(ws))
			ctx := withArrivalID(context.Background(), fmt.Sprintf("goroutine-%d", i))
			if err := h.Handle(ctx, store.Event{Type: store.EventPROpened, Payload: payload}); err != nil {
				t.Errorf("projection %d failed: %v", i, err)
			}
		}(i, payload)
	}

	start := time.Now()
	wg.Wait()
	// Both arrived at the rendezvous ⇒ it released immediately instead of waiting
	// out its 300ms timeout. A global lock would push this past the timeout.
	if elapsed := time.Since(start); elapsed >= 300*time.Millisecond {
		t.Fatalf("projections of DIFFERENT PRs did not overlap (took %s, rendezvous timed out) — the lock is serializing across keys", elapsed)
	}
	if got := ws.createsOf("o/r#11: t"); got != 1 {
		t.Fatalf("o/r#11 merge-request creates: got %d, want 1", got)
	}
	if got := ws.createsOf("o/r#12: t"); got != 1 {
		t.Fatalf("o/r#12 merge-request creates: got %d, want 1", got)
	}
}
