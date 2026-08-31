// Package beadsbridge is the event handler that projects pg-pr's PR + process-
// feedback beads. It relocates the bead-orchestration that used to live inline
// in internal/sync. It creates the PR (merge-request) bead and the process-
// feedback bead, and cascade-closes on PR close. It does NOT create feedback
// beads — feedback now lives in internal/store.
//
// It also no longer produces draft-review or attention beads (pg2-ynhr.5):
// that legacy review-workflow production (EnsureDraftReviewBead/
// EnsureDraftReviewMineLabel and the attention-bead projection) shipped off to
// pr-pool per ADR 0034 and epic pg2-ynhr. The pg-pr dashboard's OWN attention
// verdict (internal/snapshot.NeedsAttention, surfaced via internal/dashboard
// and `pg-pr pr open`) is UNRELATED and unaffected — this package never
// computed that verdict; it only ever projected a bead FROM it, and that
// projection is what pg2-ynhr.5 removes (FORK2a).
package beadsbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/prlock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// BeadClient is the subset of *beads.Client the bridge needs (narrow for tests).
type BeadClient interface {
	// FindByRepoAndNumberUncached and ReconcileMergeRequest together are the
	// read-once + single-write projection for the merge-request bead
	// (pg2-pz7y8): ONE fresh read, then AT MOST ONE combined create-or-update
	// carrying every desired mutation (fields, co-owned label,
	// conflict-priority/pbase label). See their doc comments in
	// pkg/beads/mergerequest.go.
	FindByRepoAndNumberUncached(ctx context.Context, repo string, prNumber int) (*beads.MergeRequest, error)
	ReconcileMergeRequest(ctx context.Context, existing *beads.MergeRequest, userTitle string, fields beads.MergeRequestFields, coOwned, hasConflict, actsAsMine bool) (id string, alreadyClosed bool, err error)
	FindByRepoAndNumber(ctx context.Context, repo string, number int) (*beads.MergeRequest, error)
	CloseMergeRequest(ctx context.Context, id, reason string) error
	ListChildrenOfPR(ctx context.Context, prBeadID string) ([]string, error)
	CreateProcessingCycle(ctx context.Context, in beads.CreateProcessingCycleInput) (string, error)
	ResolveProcessingCycle(ctx context.Context, key, prBeadID string) (beads.ProcessingCycleState, error)
	AppendProcessingCycleNote(ctx context.Context, id, note, addLabel string, removeLabels []string) error
	CloseProcessingCycle(ctx context.Context, id, reason string) error
	CloseFeedback(ctx context.Context, id, reason string) error
	// ListFeedbackChildrenOfCycle lists the feedback-bead children of a
	// process-feedback cycle bead — the parent-scoped query
	// CascadeCloseMergeRequest needs to close a cycle's feedback
	// grandchildren without enumerating every feedback bead in the
	// workspace (pg2-kij93).
	ListFeedbackChildrenOfCycle(ctx context.Context, cycleID string) ([]string, error)
}

// Handler is the beads event handler.
type Handler struct {
	client BeadClient
	// locks serializes the projection per PR identity (see Handle). Every Handler
	// points at the one process-wide projectionLocks — the field exists so the
	// dependency is visible at the call site, not so it can vary per Handler; a
	// per-Handler registry would defeat the whole mechanism (see projectionLocks).
	locks *keyedLock
	// fileLock is the cross-process extension of locks (bead pg2-4dz88.6.3):
	// every Handler points at the one process-wide prLock, for the identical
	// reason locks always points at projectionLocks rather than a fresh
	// per-Handler value — see prLock's doc comment.
	fileLock *prlock.Locker
}

// projectionLocks is the process-wide per-PR projection lock. It is
// PACKAGE-LEVEL, not a field New allocates, and that is REQUIRED rather than
// stylistic: the daemon's outbox dispatcher constructs a FRESH Handler (and a
// fresh per-repo bead client) on EVERY event — see newBeadsBridgeHandler in
// cmd/pg-pr/sync.go — so two concurrent dispatches never share a Handler
// instance. A per-Handler registry would hand each dispatch its own private lock
// and serialize nothing at all, while looking correct in a test that reuses one
// Handler. Keys carry the repo (`owner/name#123`), which is 1:1 with a bd
// workspace, so distinct repos cannot collide on one slot.
//
// SCOPE — IN-PROCESS ONLY, DELIBERATELY (bead pg2-35rl6). This closes the
// mechanism that produced the observed duplicates (one daemon, several goroutines
// on one shared outbox; Engine.Daemon holds an exclusive flock on daemon.lock, so
// two daemons cannot coexist). On its own it does NOT stop a SECOND pg-pr process
// racing a daemon tick: the one-shot `pg-pr sync` path takes no IN-PROCESS lock at
// all, and `pg-pr pr create` reaches EnsureMergeRequest without passing through
// this bridge. A post-create re-resolve that closes the loser was considered and
// NOT taken for the cross-process case either, for the same reason it fails
// in-process: it cannot be made airtight. The later creator always observes the
// earlier bead, but "who loses" must be decided from stored state both processes
// read identically, and the pairs this bug produces are created in the SAME
// SECOND (the production evidence: two process-feedback beads at 12:35:25Z with
// an identical fbsum digest). On a created_at tie the tiebreak can elect the
// LATER creator, whose peer may have already re-resolved and seen only itself —
// so neither closes and the duplicate survives. It would trade a total
// prevention for a partial cure plus a new close-what-we-just-wrote path, in a
// package whose FindDuplicateMergeRequests doc explicitly forbids a mutating
// collapse counterpart.
//
// The cross-process gap this in-process lock leaves open is closed by bead
// pg2-4dz88.6.3 — see prLock below. A per-key FILE lock (flock, the same
// mechanism the daemon already uses for its own single-instance daemon.lock) is
// held ACROSS OS PROCESSES for the identical key this lock guards: Handle
// acquires both (see Handle), and `pg-pr pr create` takes the cross-process lock
// around its own EnsureMergeRequest call (cmd/pg-pr/pr_write.go's
// mergeRequestLock) even though it never reaches this bridge. So a cross-process
// race can no longer produce a duplicate pair (INV-MR-1); the read-only
// `pg-pr sync duplicates` audit remains as a detector for any gap this analysis
// missed, not as the primary defense.
var projectionLocks = newKeyedLock()

// prLock is the cross-process extension of projectionLocks (bead pg2-4dz88.6.3):
// a per-PR-identity flock-backed lock (internal/prlock) held for the SAME key
// and the SAME span as projectionLocks (see Handle), but enforced by the kernel
// across OS PROCESS boundaries rather than only within this one. It exists
// because projectionLocks alone cannot help `pg-pr sync --pr/--repo` (a fresh
// process per invocation) or `pg-pr pr create` (which reaches EnsureMergeRequest
// directly, never through this bridge — see cmd/pg-pr/pr_write.go's own Acquire
// call around mergeRequestLock) resist racing a concurrently running daemon or a
// second CLI invocation.
//
// Package-level for the identical reason projectionLocks is package-level: a
// fresh Handler is built per event/invocation (see projectionLocks's doc
// comment), so a per-Handler field would defeat cross-process mutual exclusion
// the same way it would defeat the in-process case.
//
// Tests MUST override this with a Locker pointed at a t.TempDir() LockDir
// (mirrors prlock.Options.LockDir's own doc comment) — this package's TestMain
// (cross_process_lock_test.go) already does so for every test in this package;
// never exercise the real $XDG_RUNTIME_DIR/pg-pr/locks path from a test, where it
// could contend with (or silently synchronize against) a real daemon.
var prLock = prlock.New(prlock.Options{})

// Option customizes a Handler. No options are defined today — the last one
// (WithoutDraftReviews, the NH3 review kill switch) was removed by pg2-ynhr.5
// once draft-review production itself was removed. The type/parameter is kept
// so a future option does not require changing New's signature.
type Option func(*Handler)

// New constructs the handler, applying any options.
func New(client BeadClient, opts ...Option) *Handler {
	h := &Handler{client: client, locks: projectionLocks, fileLock: prLock}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// FeedbackPayload is the JSON payload for feedback.created events. It is an
// ALIAS for the shared store type (which the emitters marshal), so there is one
// definition of the wire shape rather than a copy per package.
type FeedbackPayload = store.FeedbackPayload

// Handle implements event.Handler. Idempotent: re-dispatch under the
// at-least-once outbox must not duplicate beads.
//
// It is also SERIALIZED PER PR IDENTITY, because idempotence alone is not enough
// under concurrency (bead pg2-35rl6), and that serialization now spans BOTH an
// in-process lock (projectionLocks) AND a cross-process one (prLock, bead
// pg2-4dz88.6.3) — the latter closes the gap the former cannot: a second pg-pr OS
// process (the daemon and a one-shot `pg-pr sync --pr/--repo` invocation, or two
// overlapping one-shot invocations) racing the SAME PR identity. Every
// projection below is a check-then-create
// — ReconcileMergeRequest is handed a fresh read then creates-or-updates,
// ensureProcessFeedbackBead reads ResolveProcessingCycle then creates —
// and nothing at the bd layer rejects a second create for an identity that already has one
// (ProcessingCycleKey's doc calls two beads with the same key duplicates by
// definition, but only DECLARES the invariant). Two goroutines interleaved inside
// that read→decide→write window therefore both observe "none yet" and both write.
//
// The daemon reaches that state routinely: it runs two projecting workers (the
// mine and team queues, Daemon in internal/sync/daemon.go) plus a maintenance
// flusher, each calling flushOutbox on the SHARED outbox, and RunOutbox neither
// claims rows nor partitions them — so a PR that the user authored in a repo whose
// team query also covers it lands in both rosters, and one tick can drive two
// concurrent projections of the same key (in the limit, of the same outbox row).
//
// Both locks are taken ONCE for the whole event, spanning the entire
// read→decide→write section of every branch rather than the writes alone:
// locking only the create would let the second goroutine (or second process)
// finish its READ before the first's write and still decide to create.
// Different PRs take different keys and stay fully concurrent on EITHER lock,
// which is what keeps the two workers parallel on the 1m poll.
//
// An event whose payload carries no (repo, number) identity is projected
// WITHOUT either lock: there is nothing to serialize on, and the per-branch
// decoders below still report the malformed payload with their own error text.
func (h *Handler) Handle(ctx context.Context, e store.Event) error {
	key, ok := prIdentityKey(e.Payload)
	if !ok {
		return h.project(ctx, e)
	}
	release, err := h.locks.acquire(ctx, key)
	if err != nil {
		return fmt.Errorf("beadsbridge: await projection lock for %s: %w", key, err)
	}
	defer release()
	frelease, ferr := h.fileLock.Acquire(ctx, key)
	if ferr != nil {
		return fmt.Errorf("beadsbridge: await cross-process projection lock for %s: %w", key, ferr)
	}
	defer frelease()
	return h.project(ctx, e)
}

// prIdentityKey extracts the (repo, pr_number) identity every event payload
// carries at the top level — PRPayload and FeedbackPayload both
// spell it `repo` + `number` — and renders the lock key. It mirrors the
// repo-routing header decode in cmd/pg-pr/sync.go's dispatcher: a partial decode
// of the two fields needed, so one shape change cannot silently mis-key.
//
// A decode failure or a missing field returns ok=false rather than an error: the
// branch that owns the payload reports it, and a lock is not the place to
// re-litigate payload validity.
func prIdentityKey(payload []byte) (string, bool) {
	var head struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}
	if err := json.Unmarshal(payload, &head); err != nil || head.Repo == "" || head.Number == 0 {
		return "", false
	}
	return head.Repo + "#" + strconv.Itoa(head.Number), true
}

// project is Handle's body, run with the per-PR projection lock already held.
func (h *Handler) project(ctx context.Context, e store.Event) error {
	switch e.Type {
	case store.EventPROpened, store.EventPRUpdated:
		var p store.PRPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("beadsbridge: decode pr payload: %w", err)
		}
		// Read-once + single-write (pg2-pz7y8): ONE fresh read of the
		// merge-request bead's current state, then ONE combined create-or-update
		// (ReconcileMergeRequest) carrying every desired mutation — fields, the
		// co-owned label, and the conflict-priority/pbase label — computed
		// against that single snapshot. This replaces the former
		// EnsureMergeRequest → GetMergeRequestUncached → SetMergeRequestCoOwnedWith
		// → reconcilePriority chain (up to 2 reads and up to 4 separate bd calls
		// on one tick) with 1 read + at most 1 write.
		//
		// The read MUST be the UNCACHED one (FindByRepoAndNumberUncached). Both
		// the FB-4 co-owned label diff and the priority/`pbase:<n>` baseline are
		// diff-before-write STATE DECISIONS, and FB-5 excluded exactly those from
		// the per-tick TickCache because a snapshot taken at tick start can
		// predate a write issued earlier in the same tick. Calling the cached
		// FindByRepoAndNumber here would make that freshness depend on whether a
		// cache happens to be attached to this client (today the bridge attaches
		// none, so it would be correct by accident and silently wrong the day one
		// is attached).
		existing, err := h.client.FindByRepoAndNumberUncached(ctx, p.Repo, p.Number)
		if err != nil {
			return err
		}
		// The acts-as-mine test goes through the SHARED predicate
		// ownership.ActsAsMine (mine OR co-owned), the same one replyposter,
		// snapshot.builder, and sync.ingest use — never a local `!= "team"`. Over
		// the closed 3-value set the two agree; they diverge on an out-of-band/
		// empty value, where ActsAsMine degrades to team-style selection (a draft
		// is skipped, not auto-reviewed, and the priority nudge lowers rather
		// than raises) — the conservative direction, matching pr-pool's copy of
		// the predicate. (pg2-q2drf)
		mine := ownership.Ownership(p.Ownership).ActsAsMine()
		_, _, err = h.client.ReconcileMergeRequest(ctx, existing, p.Title, beads.MergeRequestFields{
			Repo: p.Repo, PRNumber: p.Number, State: p.State, Branch: p.Branch,
			Base: p.Base, Author: p.Author, URL: p.URL, Draft: p.Draft,
			LastSyncedAt: p.LastSyncedAt,
		}, p.Ownership == "co-owned", p.HasConflict, mine)
		return err
	case store.EventFeedbackCreated:
		var p FeedbackPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("beadsbridge: decode feedback payload: %w", err)
		}
		return h.ensureProcessFeedbackBead(ctx, p)
	case store.EventPRClosed, store.EventPRMerged:
		var p store.PRPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("beadsbridge: decode pr payload: %w", err)
		}
		return h.cascadeClose(ctx, p)
	}
	return nil
}

// fbsumLabelPrefix stashes the digest of the unaddressed-feedback SET a
// process-feedback bead already covers, as a `fbsum:<digest>` label. It mirrors
// the `pbase:<n>` idiom used by reconcilePriority: a marker label is the only
// per-bead state channel available to a stateless projection, it comes back on
// the same `bd list --json` read the lookup already performs, and comparing it
// makes the re-sync write a diff-before-write rather than an unconditional one.
const fbsumLabelPrefix = "fbsum:"

// ensureProcessFeedbackBead projects the process-feedback bead for ONE PR,
// keyed on (repo, pr_number) — never on the merge-request bead's id (pg2-onq1e).
//
// The projection is re-run on every tick, so all four branches below must be
// idempotent AND must write nothing when nothing changed:
//
//   - Nothing unaddressed (Summary says zero) → NO bead. This is what closes the
//     self-feeding loop: an agent's own reply comments and push are not
//     feedback, so they can no longer manufacture an empty bead asking the agent
//     to process itself.
//   - A live cycle exists → UPDATE it (append what is new), never create a
//     second one; and only when the feedback set actually changed.
//   - A predecessor was CLOSED covering the SAME feedback set → nothing new
//     arrived, so no successor.
//   - A predecessor was CLOSED and the set has changed → open a successor whose
//     description REFERENCES the predecessor, rather than a bare duplicate.
//
// Every lookup error PROPAGATES (swallowing it as "none open" is the documented
// duplicate-cycle bug).
func (h *Handler) ensureProcessFeedbackBead(ctx context.Context, p FeedbackPayload) error {
	mr, err := h.client.FindByRepoAndNumber(ctx, p.Repo, p.Number)
	if err != nil {
		return err
	}
	if mr == nil {
		return fmt.Errorf("beadsbridge: no merge-request bead for %s#%d", p.Repo, p.Number)
	}
	if mr.Status == "closed" {
		return nil // do not attach a live cycle under a closed PR bead
	}
	// A summary that POSITIVELY reports nothing unaddressed suppresses the bead.
	// A nil summary is "the emitter did not compute one" (a legacy outbox row);
	// those keep the pre-pg2-onq1e behaviour so an in-flight event still
	// projects rather than being silently dropped.
	if p.Summary != nil && p.Summary.Unaddressed == 0 {
		return nil
	}
	key := beads.ProcessingCycleKey(p.Repo, p.Number)
	state, err := h.client.ResolveProcessingCycle(ctx, key, mr.ID)
	if err != nil {
		return err // propagate — do NOT treat as "no open cycle"
	}
	digest := summaryDigest(p.Summary)
	if state.Open != nil {
		// Criterion: update the EXISTING open bead, appending what is new.
		// Unchanged set (or no digest to compare) ⇒ no write at all.
		if digest == "" || hasLabel(state.Open.Labels, fbsumLabelPrefix+digest) {
			return nil
		}
		return h.client.AppendProcessingCycleNote(ctx, state.Open.ID,
			renderCycleNote(p.Summary),
			fbsumLabelPrefix+digest,
			staleFbsumLabels(state.Open.Labels, digest))
	}
	predecessor := ""
	if state.Closed != nil {
		// A closed predecessor that already covered this exact set means the
		// feedback is not new — it was processed and closed. Re-opening for it
		// is precisely the churn this fix removes.
		if digest != "" && hasLabel(state.Closed.Labels, fbsumLabelPrefix+digest) {
			return nil
		}
		predecessor = state.Closed.ID
	}
	in := beads.CreateProcessingCycleInput{
		PRBeadID:      mr.ID,
		Key:           key,
		Description:   renderCycleDescription(key, p.Summary, predecessor),
		Mine:          p.Mine,
		PredecessorID: predecessor,
	}
	if digest != "" {
		in.Labels = []string{fbsumLabelPrefix + digest}
	}
	_, err = h.client.CreateProcessingCycle(ctx, in)
	return err
}

// summaryDigest returns the summary's set digest, or "" when there is none to
// compare (a legacy payload). An empty digest degrades every comparison to
// "cannot tell", which the callers treat conservatively: no extra write, no
// suppression of a genuinely-needed bead.
func summaryDigest(s *store.FeedbackSummary) string {
	if s == nil {
		return ""
	}
	return s.Digest
}

// hasLabel reports whether labels contains want.
func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// staleFbsumLabels lists the `fbsum:` labels that are NOT the current digest, so
// the update that adds the new marker drops the old ones in the same call and at
// most one marker is ever present.
func staleFbsumLabels(labels []string, keepDigest string) []string {
	var out []string
	for _, l := range labels {
		if strings.HasPrefix(l, fbsumLabelPrefix) && l != fbsumLabelPrefix+keepDigest {
			out = append(out, l)
		}
	}
	return out
}

// renderCycleDescription builds the bead body. It MUST carry substance — the
// count and kind of unaddressed findings, and who raised them — so a drain
// session can triage from the bead alone instead of hitting the VCS API first.
// Copying the title verbatim into the description (the old behaviour) is what
// made the duplicated beads indistinguishable and unreviewable.
func renderCycleDescription(key string, s *store.FeedbackSummary, predecessorID string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Unaddressed reviewer feedback on %s.\n", key)
	if s == nil {
		// No summary available (legacy event): say so rather than implying zero.
		b.WriteString("\nFeedback breakdown unavailable for this event; run `pg-pr feedback list` for the current items.\n")
	} else {
		b.WriteString("\n" + renderCycleNote(s) + "\n")
	}
	if predecessorID != "" {
		fmt.Fprintf(&b, "\nSupersedes closed process-feedback bead %s (that cycle was completed; "+
			"the items above were not covered by it).\n", predecessorID)
	}
	return b.String()
}

// renderCycleNote renders the one-block summary appended to a bead on each
// genuine change: the total, the per-kind breakdown, and the raising logins.
// Kinds are emitted in sorted order so the same set always renders identically
// (an unstable rendering would defeat the digest comparison).
func renderCycleNote(s *store.FeedbackSummary) string {
	if s == nil {
		return "Feedback breakdown unavailable."
	}
	kinds := make([]string, 0, len(s.ByKind))
	for k := range s.ByKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s x%d", k, s.ByKind[k]))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d unaddressed item(s)", s.Unaddressed)
	if len(parts) > 0 {
		fmt.Fprintf(&b, ": %s", strings.Join(parts, ", "))
	}
	b.WriteString(".")
	if len(s.Reviewers) > 0 {
		fmt.Fprintf(&b, "\nRaised by: %s.", strings.Join(s.Reviewers, ", "))
	}
	return b.String()
}

// cascadeCloseFeedbackReason renders the reason recorded on a feedback bead
// closed purely because its parent process-feedback cycle reached a terminal
// state. It is DELIBERATELY distinct from the reason the cycle/PR bead
// itself closes with (e.g. "pr-closed", "upstream-merged"): the feedback item
// was never individually read, let alone triaged, and its close reason MUST
// say so (pg2-kij93's acceptance criteria). Blurring the two is exactly what
// made the 1,303-bead backlog this bug produced unreadable after the fact —
// a later reader could not tell "adjudicated non-actionable" from "never
// looked at".
func cascadeCloseFeedbackReason(cycleID, cycleReason string) string {
	return fmt.Sprintf(
		"cascade-closed: never individually triaged (parent process-feedback cycle %s closed: %s)",
		cycleID, cycleReason)
}

// cascadeClose closes the PR bead and its descendants: resolves the
// merge-request bead and the close reason from the PR payload, then hands
// off to CascadeCloseMergeRequest for the actual cascade.
func (h *Handler) cascadeClose(ctx context.Context, p store.PRPayload) error {
	mr, err := h.client.FindByRepoAndNumber(ctx, p.Repo, p.Number)
	if err != nil || mr == nil {
		return err
	}
	reason := "pr-closed"
	if p.Merged {
		reason = "upstream-merged"
	}
	return h.CascadeCloseMergeRequest(ctx, mr.ID, reason)
}

// CascadeCloseMergeRequest closes the merge-request bead id and its FULL
// descendant tree — every process-feedback cycle beneath it, AND every
// feedback bead beneath EACH of those cycles — with reason.
//
// It is exported so every path that closes a merge-request bead, not only
// the pr.closed/pr.merged event path (cascadeClose above), can run the
// IDENTICAL cascade. cmd/pg-pr's `pr close` command closes a merge-request
// bead directly — a synchronous CLI action that bypasses the event/outbox
// system entirely — and any other close path, present or future, has the
// same obligation (pg2-kij93 defect 2): a close path that does not cascade
// leaves cycles, and their feedback, orphaned under a closed PR bead
// forever, since ensureProcessFeedbackBead's closed-parent guard means
// nothing will ever revisit them once the PR bead is closed.
//
// Two levels are closed, not one (defect 1): ListChildrenOfPR finds the
// process-feedback cycles (and any other direct child, e.g. an action bead —
// this method is as type-blind about direct children as the pre-fix code
// was), and ListFeedbackChildrenOfCycle finds each cycle's feedback
// grandchildren. A grandchild is closed with cascadeCloseFeedbackReason, NOT
// the bare reason the cycle/PR bead itself gets.
//
// A cycle is closed only once ALL of its feedback children closed
// successfully. If any feedback close fails, that cycle is deliberately left
// OPEN rather than closed anyway: an open task-type bead is visible to
// normal bd sweeps (bd ready included), whereas a closed cycle with orphaned
// feedback underneath it reproduces the exact invisible defect this bug
// fixes — feedback beads are `hooked` status, excluded from bd ready. The
// merge-request bead itself is always attempted regardless of descendant
// failures, matching the pre-fix behavior of unconditionally closing it.
//
// Every failure is collected and SURFACED, not discarded (defect 3): a
// partial cascade returns a non-nil error naming what did not close, rather
// than returning nil as if the cascade fully completed.
//
// Idempotent: CloseFeedback/CloseProcessingCycle/CloseMergeRequest are each
// individually idempotent (a no-op on an already-closed bead), so
// re-running the cascade over an already-closed subtree closes nothing new
// and returns nil.
func (h *Handler) CascadeCloseMergeRequest(ctx context.Context, mrID, reason string) error {
	if mrID == "" {
		return errors.New("beadsbridge: cascade-close: merge-request id required")
	}
	cycles, err := h.client.ListChildrenOfPR(ctx, mrID)
	if err != nil {
		return fmt.Errorf("beadsbridge: cascade-close %s: list children: %w", mrID, err)
	}
	var failures []error
	for _, cycle := range cycles {
		feedback, ferr := h.client.ListFeedbackChildrenOfCycle(ctx, cycle)
		if ferr != nil {
			failures = append(failures, fmt.Errorf("list feedback children of %s: %w", cycle, ferr))
			continue
		}
		feedbackReason := cascadeCloseFeedbackReason(cycle, reason)
		allFeedbackClosed := true
		for _, fb := range feedback {
			if cerr := h.client.CloseFeedback(ctx, fb, feedbackReason); cerr != nil {
				failures = append(failures, fmt.Errorf("close feedback %s (cycle %s): %w", fb, cycle, cerr))
				allFeedbackClosed = false
			}
		}
		if !allFeedbackClosed {
			// Leave the cycle open — see the doc comment above.
			continue
		}
		if cerr := h.client.CloseProcessingCycle(ctx, cycle, reason); cerr != nil {
			failures = append(failures, fmt.Errorf("close cycle %s: %w", cycle, cerr))
		}
	}
	if cerr := h.client.CloseMergeRequest(ctx, mrID, reason); cerr != nil {
		failures = append(failures, fmt.Errorf("close merge-request %s: %w", mrID, cerr))
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("beadsbridge: cascade-close %s: %d descendant/self close(s) failed: %w",
		mrID, len(failures), errors.Join(failures...))
}

// compile-time check: *beads.Client must satisfy BeadClient.
var _ BeadClient = (*beads.Client)(nil)
