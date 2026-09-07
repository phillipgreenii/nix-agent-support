// migrate.go: the one-shot pg-pr -> pg-connector-pr-github disposition
// import (finding A19; ADR 0063 records the full decision). Design §6.1/
// §9.1 already decided WHAT moves ("the feedback-disposition store itself
// moves under the PR GitHub backend") and WHEN ("the disposition-store
// migration and the deletion of pg-pr's own feedback command group MUST
// land in the same cutover step, never split across two"); this file is
// the HOW: the concrete field mapping and the Go entry point a one-shot
// migration invocation calls at that cutover step.
//
// This deliberately does NOT open pg-pr's SQLite store directly — that
// would recreate exactly the FK/module dependency store.go's own top
// comment says this backend's self-contained module forbids. Instead the
// source of truth is pg-pr's own already-shipped, unchanged export path:
// `pg-pr feedback list <repo> <number> --json`, which already emits
// []store.Feedback as JSON with no tags (so its keys are the exported Go
// field names verbatim). LegacyFeedbackItem below decodes exactly the
// subset of those fields this migration needs; unrecognised keys in the
// source JSON are ignored by encoding/json, so this struct is intentionally
// narrower than pg-pr's own store.Feedback.
package internal

import (
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// LegacyFeedbackItem is the subset of one pg-pr `internal/store.Feedback`
// row's JSON encoding this migration reads. Field names and JSON keys
// mirror store.Feedback's own exported field names exactly (that struct
// carries no json tags, so encoding/json's default field-name-as-key
// behavior is what `pg-pr feedback list --json` actually emits) — keep
// these in sync with packages/pg-pr/internal/store/feedback.go's Feedback
// struct if that struct's field names ever change.
type LegacyFeedbackItem struct {
	// Kind restricts which pg-pr feedback rows carry a disposition this
	// store has a place for. Only the two comment-bearing kinds do;
	// ci-failure/review-request/jira-link/self-review dispositions have no
	// commentID to key this store's per-comment Dispositions map by, and
	// are out of scope for this backend's store (they belong to other
	// future connector types, not pr-github's).
	Kind string `json:"Kind"`
	// ExternalID is pg-pr's fallback comment identity when CommentNodeID
	// was never captured (see CommentNodeID below).
	ExternalID string `json:"ExternalID"`
	// CommentNodeID is GitHub's own GraphQL node id for the comment/thread
	// — the same id shape internal/github.go populates api.Comment.ID with
	// — and is preferred over ExternalID whenever both are present.
	CommentNodeID string `json:"CommentNodeID"`
	// DispositionAction is pg-pr's disposition_action column: "" (never
	// dispositioned), "will-fix", "wont-fix", or "no-action".
	DispositionAction string `json:"DispositionAction"`
}

// legacyCommentKinds is the set of pg-pr Feedback.Kind values that carry a
// per-comment disposition this store has a slot for (see
// LegacyFeedbackItem.Kind's doc comment).
var legacyCommentKinds = map[string]bool{
	"code-comment-thread": true,
	"pr-comments":         true,
}

// legacyDispositionMapping maps pg-pr's DispositionAction column values
// onto this backend's schema.Disposition enum. pg-pr's "" (never
// dispositioned) has no entry here — ImportLegacyDispositions skips it
// rather than writing schema.DispositionOpen, since "never written" and
// "explicitly set to open" are already distinct states in THIS store (see
// PRState's own doc comment in store.go): importing every undispositioned
// legacy row as an explicit "open" write would manufacture history pg-pr
// never actually recorded.
var legacyDispositionMapping = map[string]schema.Disposition{
	"will-fix":  schema.DispositionWillFix,
	"wont-fix":  schema.DispositionWontFix,
	"no-action": schema.DispositionNoAction,
}

// ImportLegacyDispositions applies pg-pr's exported feedback items for one
// PR (repo+number, matching whatever `pg-pr feedback list <repo> <number>
// --json` call produced items) into s, keyed by prID
// (formatPRID(repo, number) — the caller resolves and passes prID rather
// than this function re-deriving it, since the repo/number the caller
// scoped the pg-pr export call to IS that identity).
//
// Returns the count of dispositions actually imported. A per-item mapping
// failure (an unrecognised DispositionAction value — data this backend has
// never produced and pg-pr's own CLI-level validation in
// runFeedbackDisposition should have prevented, but a migration MUST NOT
// assume its input is clean) is collected and returned as a single joined
// error rather than aborting the whole import on the first bad row, so one
// unexpected row does not block every other PR's/comment's legitimate
// disposition from migrating.
//
// This is intentionally idempotent and safe to re-run: SetDisposition is a
// plain set/overwrite (store.go), and this function's own mapping is a pure
// function of its input, so importing the same export twice produces the
// same end state, never a duplicate or a second conflicting write.
func ImportLegacyDispositions(s *Store, prID string, items []LegacyFeedbackItem) (imported int, err error) {
	var errs []error
	for _, item := range items {
		if !legacyCommentKinds[item.Kind] {
			continue
		}
		if item.DispositionAction == "" {
			continue // never dispositioned in pg-pr — nothing to carry over
		}
		disposition, ok := legacyDispositionMapping[item.DispositionAction]
		if !ok {
			errs = append(errs, fmt.Errorf("migrate: pr %s: unrecognised legacy DispositionAction %q (comment %s)",
				prID, item.DispositionAction, legacyCommentID(item)))
			continue
		}
		commentID := legacyCommentID(item)
		if commentID == "" {
			errs = append(errs, fmt.Errorf("migrate: pr %s: dispositioned feedback item has neither CommentNodeID nor ExternalID set, cannot key this store's Dispositions map", prID))
			continue
		}
		if setErr := s.SetDisposition(prID, commentID, disposition); setErr != nil {
			errs = append(errs, fmt.Errorf("migrate: pr %s comment %s: %w", prID, commentID, setErr))
			continue
		}
		imported++
	}
	if len(errs) > 0 {
		return imported, joinErrors(errs)
	}
	return imported, nil
}

// legacyCommentID picks item's comment identity, preferring the GitHub
// node id (CommentNodeID) over pg-pr's own internal ExternalID fallback —
// see LegacyFeedbackItem's doc comment.
func legacyCommentID(item LegacyFeedbackItem) string {
	if item.CommentNodeID != "" {
		return item.CommentNodeID
	}
	return item.ExternalID
}

// joinErrors combines errs into one error whose message lists each cause —
// this module's go.mod (1.25.9) has errors.Join available, but a hand-rolled
// join keeps this file's error message format stable and readable without
// depending on fmt's "%w" multi-verb slice handling nuances.
func joinErrors(errs []error) error {
	msg := fmt.Sprintf("%d migration error(s):", len(errs))
	for _, e := range errs {
		msg += "\n  - " + e.Error()
	}
	return fmt.Errorf("%s", msg)
}
