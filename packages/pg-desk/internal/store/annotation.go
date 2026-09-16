package store

import (
	"database/sql"
	"fmt"
)

// prEntityCommentID is the sentinel CommentID for an annotation row that
// carries the PR-level fields (Hidden, HiddenReason, WIP) rather than a
// per-comment disposition override. Empty string is never a valid GitHub
// comment id, so it cannot collide with a real one.
const prEntityCommentID = ""

// Annotation is one row of the annotation table, per the design doc's
// section 7.6. The table carries two logically distinct shapes, chosen
// apart by CommentID:
//
//   - CommentID == "" (prEntityCommentID): the PR-level annotation —
//     Hidden, HiddenReason, WIP.
//   - CommentID != "": a per-(PR, comment id) disposition override.
//
// Both shapes share SetBy/SetAt (who set the annotation and when) per the
// design doc's "with who set them".
//
// Human and agent annotations survive every pipeline run by construction:
// no pipeline stage (gather, interpret, the run pipeline — packet 6) calls
// UpsertAnnotation. Its only permitted callers are packet 8's CLI commands
// (hide/unhide/wip/feedback set) and packet 9's import-pg-pr-annotations
// command (this packet's Binding decisions section). This package itself
// never calls it.
type Annotation struct {
	Repo       string
	EntityType string
	EntityID   string
	CommentID  string // "" for the PR-level row; a comment id otherwise

	Hidden       *bool  // PR-level only
	HiddenReason string // PR-level only
	WIP          *bool  // PR-level only

	Disposition string // per-comment override only

	SetBy string
	SetAt string // RFC3339 timestamp
}

// UpsertAnnotation inserts or replaces the annotation row keyed by
// (Repo, EntityType, EntityID, CommentID). See the Annotation doc comment
// for who may call this.
func (s *Store) UpsertAnnotation(a Annotation) error {
	commentID := a.CommentID
	_, err := s.sql.Exec(
		`INSERT INTO annotation (repo, entity_type, entity_id, comment_id, hidden, hidden_reason, wip, disposition, set_by, set_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (repo, entity_type, entity_id, comment_id) DO UPDATE SET
		   hidden = excluded.hidden,
		   hidden_reason = excluded.hidden_reason,
		   wip = excluded.wip,
		   disposition = excluded.disposition,
		   set_by = excluded.set_by,
		   set_at = excluded.set_at`,
		a.Repo, a.EntityType, a.EntityID, commentID,
		nullableBool(a.Hidden), nullableString(a.HiddenReason), nullableBool(a.WIP),
		nullableString(a.Disposition), a.SetBy, a.SetAt,
	)
	if err != nil {
		return fmt.Errorf("store: upsert annotation (%s,%s,%s,%q): %w", a.Repo, a.EntityType, a.EntityID, commentID, err)
	}
	return nil
}

// GetPRAnnotation returns the PR-level annotation row (Hidden,
// HiddenReason, WIP) for (repo, entityType, entityID), or found=false if
// none exists. A thin convenience wrapper over GetAnnotation using the
// PR-level sentinel comment id.
func (s *Store) GetPRAnnotation(repo, entityType, entityID string) (annotation Annotation, found bool, err error) {
	return s.GetAnnotation(repo, entityType, entityID, prEntityCommentID)
}

// GetAnnotation returns the annotation row for
// (repo, entityType, entityID, commentID), or found=false if no such row
// exists. Pass "" for commentID to fetch the PR-level row.
func (s *Store) GetAnnotation(repo, entityType, entityID, commentID string) (annotation Annotation, found bool, err error) {
	var hidden, wip sql.NullBool
	var hiddenReason, disposition sql.NullString
	row := s.sql.QueryRow(
		`SELECT repo, entity_type, entity_id, comment_id, hidden, hidden_reason, wip, disposition, set_by, set_at
		 FROM annotation WHERE repo = ? AND entity_type = ? AND entity_id = ? AND comment_id = ?`,
		repo, entityType, entityID, commentID,
	)
	if err := row.Scan(&annotation.Repo, &annotation.EntityType, &annotation.EntityID, &annotation.CommentID,
		&hidden, &hiddenReason, &wip, &disposition, &annotation.SetBy, &annotation.SetAt); err != nil {
		if err == sql.ErrNoRows {
			return Annotation{}, false, nil
		}
		return Annotation{}, false, fmt.Errorf("store: get annotation (%s,%s,%s,%q): %w", repo, entityType, entityID, commentID, err)
	}
	if hidden.Valid {
		v := hidden.Bool
		annotation.Hidden = &v
	}
	if wip.Valid {
		v := wip.Bool
		annotation.WIP = &v
	}
	annotation.HiddenReason = hiddenReason.String
	annotation.Disposition = disposition.String
	return annotation, true, nil
}

// nullableBool maps a nil *bool to a SQL NULL.
func nullableBool(v *bool) any {
	if v == nil {
		return nil
	}
	return *v
}
