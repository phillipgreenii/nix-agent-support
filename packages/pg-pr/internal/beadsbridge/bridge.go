// Package beadsbridge is the event handler that projects pg-pr's PR + process-
// feedback beads. It relocates the bead-orchestration that used to live inline
// in internal/sync. It creates the PR (merge-request) bead and the process-
// feedback bead, and cascade-closes on PR close. It does NOT create feedback
// beads — feedback now lives in internal/store.
package beadsbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// BeadClient is the subset of *beads.Client the bridge needs (narrow for tests).
type BeadClient interface {
	EnsureMergeRequest(ctx context.Context, title string, fields beads.MergeRequestFields) (string, bool, error)
	SetMergeRequestCoOwned(ctx context.Context, id string, coOwned bool) error
	SetMergeRequestCoOwnedWith(ctx context.Context, id string, coOwned bool, prefetched *beads.MergeRequest) error
	FindByRepoAndNumber(ctx context.Context, repo string, number int) (*beads.MergeRequest, error)
	CloseMergeRequest(ctx context.Context, id, reason string) error
	ListChildrenOfPR(ctx context.Context, prBeadID string) ([]string, error)
	CreateProcessingCycle(ctx context.Context, in beads.CreateProcessingCycleInput) (string, error)
	ResolveProcessingCycle(ctx context.Context, key, prBeadID string) (beads.ProcessingCycleState, error)
	AppendProcessingCycleNote(ctx context.Context, id, note, addLabel string, removeLabels []string) error
	CloseProcessingCycle(ctx context.Context, id, reason string) error
	CloseFeedback(ctx context.Context, id, reason string) error
	EnsureDraftReviewBead(ctx context.Context, prBeadID, title string, mine bool) (string, error)
	EnsureDraftReviewMineLabel(ctx context.Context, prBeadID string) error
	EnsureAttentionBead(ctx context.Context, prBeadID, title string) (string, error)
	CloseAttentionBead(ctx context.Context, prBeadID, reason string) error
	GetMergeRequest(ctx context.Context, id string) (*beads.MergeRequest, error)
	GetMergeRequestUncached(ctx context.Context, id string) (*beads.MergeRequest, error)
	SetPriority(ctx context.Context, id string, p int) error
	AddLabel(ctx context.Context, id, label string) error
	RemoveLabel(ctx context.Context, id, label string) error
}

// Handler is the beads event handler.
type Handler struct {
	client BeadClient
	// suppressDraftReviews, when true, makes the pr.opened/pr.updated projection
	// skip EnsureDraftReviewBead (the NH3 kill switch, bead pg2-ynhr.11). All
	// other production (merge-request, attention, process-feedback) is
	// unaffected.
	suppressDraftReviews bool
	// locks serializes the projection per PR identity (see Handle). Every Handler
	// points at the one process-wide projectionLocks — the field exists so the
	// dependency is visible at the call site, not so it can vary per Handler; a
	// per-Handler registry would defeat the whole mechanism (see projectionLocks).
	locks *keyedLock
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
// two daemons cannot coexist). It does NOT stop a SECOND pg-pr process racing a
// daemon tick: the one-shot `pg-pr sync` path takes no lock at all, and
// `pg-pr pr create` reaches EnsureMergeRequest without passing through this
// bridge. Two mutual-exclusion mechanisms were considered and NOT taken:
//
//   - A post-create re-resolve that closes the loser cannot be made airtight. The
//     later creator always observes the earlier bead, but "who loses" must be
//     decided from stored state both processes read identically, and the pairs
//     this bug produces are created in the SAME SECOND (the production evidence:
//     two process-feedback beads at 12:35:25Z with an identical fbsum digest). On
//     a created_at tie the tiebreak can elect the LATER creator, whose peer may
//     have already re-resolved and seen only itself — so neither closes and the
//     duplicate survives. It would trade a total prevention for a partial cure
//     plus a new close-what-we-just-wrote path, in a package whose
//     FindDuplicateMergeRequests doc explicitly forbids a mutating collapse
//     counterpart.
//   - Making the gate a per-key FILE lock (flock, as the daemon already does for
//     its single-instance lock) WOULD cover both, but it moves this package from
//     pure logic over a bead client to something owning a runtime directory, with
//     its own failure modes and a cancellable LOCK_NB retry loop. That is a
//     design decision with a wider blast radius than this fix, and belongs in its
//     own bead alongside the alternative of enforcing the identity at the bd
//     layer.
//
// So a cross-process race can still produce a duplicate pair; it is reported by
// the read-only `pg-pr sync duplicates` audit, not silently absorbed.
var projectionLocks = newKeyedLock()

// Option customizes a Handler.
type Option func(*Handler)

// WithoutDraftReviews disables draft-review bead PRODUCTION on pr.opened/updated
// (the daemon's review kill switch, bead pg2-ynhr.11). Merge-request, attention,
// and process-feedback beads are still produced.
func WithoutDraftReviews() Option {
	return func(h *Handler) { h.suppressDraftReviews = true }
}

// New constructs the handler, applying any options.
func New(client BeadClient, opts ...Option) *Handler {
	h := &Handler{client: client, locks: projectionLocks}
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
// under concurrency (bead pg2-35rl6). Every projection below is a check-then-create
// — EnsureMergeRequest reads findByRepoPR then creates, ensureProcessFeedbackBead
// reads ResolveProcessingCycle then creates, EnsureDraftReviewBead and
// EnsureAttentionBead read their child list then create — and nothing at the bd
// layer rejects a second create for an identity that already has one
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
// The lock is taken ONCE for the whole event, spanning the entire read→decide→write
// section of every branch rather than the writes alone: locking only the create
// would let the second goroutine finish its READ before the first's write and
// still decide to create. Different PRs take different keys and stay fully
// concurrent, which is what keeps the two workers parallel on the 1m poll.
//
// An event whose payload carries no (repo, number) identity is projected WITHOUT
// the lock: there is nothing to serialize on, and the per-branch decoders below
// still report the malformed payload with their own error text.
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
	return h.project(ctx, e)
}

// prIdentityKey extracts the (repo, pr_number) identity every event payload
// carries at the top level — PRPayload, FeedbackPayload and AttentionPayload all
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
		mrID, alreadyClosed, err := h.client.EnsureMergeRequest(ctx, p.Title, beads.MergeRequestFields{
			Repo: p.Repo, PRNumber: p.Number, State: p.State, Branch: p.Branch,
			Base: p.Base, Author: p.Author, URL: p.URL, Draft: p.Draft,
			LastSyncedAt: p.LastSyncedAt,
		})
		if err != nil {
			return err
		}
		if alreadyClosed {
			return nil // closed PR bead: do not attach a draft-review under it
		}
		// FB-3: read the MR bead ONCE per tick and thread it into BOTH the
		// co-owned label diff AND the priority reconciler, instead of each
		// re-fetching it (which doubled the per-tick MR-bead reads → Dolt
		// connection churn).
		//
		// The read MUST be the UNCACHED one. Both consumers are
		// diff-before-write STATE DECISIONS — the FB-4 co-owned label diff and
		// reconcilePriority's priority/`pbase:<n>` baseline — and FB-5 excluded
		// exactly those from the per-tick TickCache because a snapshot taken at
		// tick start can predate a write issued earlier in the same tick. Reading
		// via GetMergeRequest here would make that freshness depend on whether a
		// cache happens to be attached to this client (today the bridge attaches
		// none, so it would be correct by accident and silently wrong the day one
		// is attached). Threading the read moves WHERE it happens, never WHETHER
		// it is fresh.
		mr, mrErr := h.client.GetMergeRequestUncached(ctx, mrID)
		// Keep the co-owned visibility label in sync with the current ownership
		// verdict — added when co-owned, removed otherwise. A nil-on-error mr
		// falls through to the write, exactly as SetMergeRequestCoOwned's own
		// read-failure path did (it swallowed the read error and wrote anyway).
		if err := h.client.SetMergeRequestCoOwnedWith(ctx, mrID, p.Ownership == "co-owned", mr); err != nil {
			return err
		}
		// reconcilePriority PROPAGATES a read error (its original behavior), so
		// surface mrErr here before reconciling — preserving the exact error
		// semantics of the two former in-function reads.
		if mrErr != nil {
			return mrErr
		}
		if err := h.reconcilePriority(ctx, mr, mrID, p.Ownership, p.HasConflict); err != nil {
			return err
		}
		// Emit the review work item. My PRs and co-owned PRs are reviewed even
		// while a GitHub draft; team PRs wait until the draft flag is removed
		// (which fires on the pr.updated that flips it). EnsureDraftReviewBead
		// is idempotent. When the review kill switch is on
		// (suppressDraftReviews), production is skipped entirely — the
		// merge-request bead above is still ensured.
		//
		// The acts-as-mine test goes through the SHARED predicate
		// ownership.ActsAsMine (mine OR co-owned), the same one replyposter,
		// snapshot.builder, sync.ingest and nudged below use — never a local
		// `!= "team"`. Over the closed 3-value set the two agree; they diverge on
		// an out-of-band/empty value, where ActsAsMine degrades to team-style
		// selection (a draft is skipped, not auto-reviewed) — the conservative
		// direction, matching pr-pool's copy of the predicate. (pg2-q2drf)
		mine := ownership.Ownership(p.Ownership).ActsAsMine()
		if !h.suppressDraftReviews && (mine || !p.Draft) {
			drID, err := h.client.EnsureDraftReviewBead(ctx, mrID, fmt.Sprintf("%s#%d", p.Repo, p.Number), mine)
			if err != nil {
				return err
			}
			// team->co-owned: an earlier team-style review bead must flip to mine.
			if p.Ownership == "co-owned" {
				if err := h.client.EnsureDraftReviewMineLabel(ctx, mrID); err != nil {
					return err
				}
			}
			_ = drID
			return nil
		}
		return nil
	case store.EventFeedbackCreated:
		var p FeedbackPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("beadsbridge: decode feedback payload: %w", err)
		}
		return h.ensureProcessFeedbackBead(ctx, p)
	case store.EventPRAttention:
		var p store.AttentionPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("beadsbridge: decode attention payload: %w", err)
		}
		return h.projectAttentionBead(ctx, p)
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
		PRBeadID:    mr.ID,
		Key:         key,
		Description: renderCycleDescription(key, p.Summary, predecessor),
		Mine:        p.Mine,
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

// projectAttentionBead ensures or closes the teammate-attention bead for a PR
// from the shared needsAttention verdict carried in the event. Re-run every tick
// (the projector re-emits from persisted facts), so both ensure and close are
// idempotent and a dropped fire-once event self-heals on the next tick (R1).
// Mirrors ensureProcessFeedbackBead: the FindByRepoAndNumber error PROPAGATES,
// and a closed parent suppresses opening a new child.
func (h *Handler) projectAttentionBead(ctx context.Context, p store.AttentionPayload) error {
	mr, err := h.client.FindByRepoAndNumber(ctx, p.Repo, p.Number)
	if err != nil {
		return err
	}
	if mr == nil {
		return fmt.Errorf("beadsbridge: no merge-request bead for %s#%d", p.Repo, p.Number)
	}
	if mr.Status == "closed" {
		return nil // do not attach/reopen an attention child under a closed PR bead
	}
	if p.Need {
		_, err := h.client.EnsureAttentionBead(ctx, mr.ID, fmt.Sprintf("%s#%d", p.Repo, p.Number))
		return err
	}
	return h.client.CloseAttentionBead(ctx, mr.ID, "attention-cleared")
}

// cascadeClose closes the PR bead and its descendants.
func (h *Handler) cascadeClose(ctx context.Context, p store.PRPayload) error {
	mr, err := h.client.FindByRepoAndNumber(ctx, p.Repo, p.Number)
	if err != nil || mr == nil {
		return err
	}
	reason := "pr-closed"
	if p.Merged {
		reason = "upstream-merged"
	}
	children, err := h.client.ListChildrenOfPR(ctx, mr.ID)
	if err != nil {
		return err
	}
	for _, child := range children {
		_ = h.client.CloseProcessingCycle(ctx, child, reason)
	}
	return h.client.CloseMergeRequest(ctx, mr.ID, reason)
}

const pbaseLabelPrefix = "pbase:"

// reconcilePriority nudges the merge-request bead's priority on conflict and
// reverts it when the conflict clears, statelessly. The pre-adjustment priority
// is stashed in a `pbase:<n>` label so a repeated conflicting tick is a no-op
// and a clear restores the exact baseline. mine/co-owned raise (−1, clamp 0);
// team lowers (+1, clamp 4). (pg2-tsgkj)
//
// mr is the ALREADY-fetched merge-request bead (FB-3): the caller reads it once
// per tick and threads it here rather than reconcilePriority re-fetching it. A
// nil mr (not found) is a no-op, matching the prior in-function read that
// returned nil on a nil bead. mrID is still passed explicitly for the label /
// priority writes.
func (h *Handler) reconcilePriority(ctx context.Context, mr *beads.MergeRequest, mrID, ownershipStr string, hasConflict bool) error {
	if mr == nil {
		return nil
	}
	baseline, hasBaseline := parsePbase(mr.Labels)

	switch {
	case hasConflict && !hasBaseline:
		// First conflicting tick: stash current priority, then nudge.
		desired := nudged(mr.Priority, ownershipStr)
		// Stash unconditionally — even when the priority is already clamped at the
		// boundary (desired == mr.Priority) — so a later clear is a no-op-safe restore.
		if err := h.client.AddLabel(ctx, mrID, pbaseLabelPrefix+strconv.Itoa(mr.Priority)); err != nil {
			return err
		}
		if desired != mr.Priority {
			return h.client.SetPriority(ctx, mrID, desired)
		}
		return nil
	case hasConflict && hasBaseline:
		return nil // already adjusted this conflict episode — idempotent no-op
	case !hasConflict && hasBaseline:
		// Conflict cleared: restore baseline, drop the marker.
		if mr.Priority != baseline {
			if err := h.client.SetPriority(ctx, mrID, baseline); err != nil {
				return err
			}
		}
		return h.client.RemoveLabel(ctx, mrID, pbaseLabelPrefix+strconv.Itoa(baseline))
	default:
		return nil // no conflict, no baseline — nothing to do
	}
}

// nudged returns the conflict-adjusted priority: mine/co-owned raise (toward 0),
// team lower (toward 4). Clamped to [0,4].
func nudged(p int, ownershipStr string) int {
	if ownership.Ownership(ownershipStr).ActsAsMine() {
		if p > 0 {
			return p - 1
		}
		return 0
	}
	if p < 4 {
		return p + 1
	}
	return 4
}

// parsePbase extracts the stashed baseline priority from a `pbase:<n>` label.
func parsePbase(labels []string) (int, bool) {
	for _, l := range labels {
		if strings.HasPrefix(l, pbaseLabelPrefix) {
			if n, err := strconv.Atoi(strings.TrimPrefix(l, pbaseLabelPrefix)); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// compile-time check: *beads.Client must satisfy BeadClient.
var _ BeadClient = (*beads.Client)(nil)
