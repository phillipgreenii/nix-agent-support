package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// The key/value annotation API (schema version 2 only). It sits beside the
// old per-column API in annotation.go, which keeps working on version 1.
//
// Every write appends an annotation_changed change_log record in the same
// transaction, so a failed append rolls the annotation write back. An
// annotation write does NOT bump entity.version: the record carries the
// entity's CURRENT version, so it names a snapshot that exists. Annotating
// an entity that has no entity row is an error and writes nothing. A
// version of 0 (a migrated, never-rehydrated entity) is a valid current
// version.

// ChangeKindAnnotationChanged is the change_log kind every annotation write
// appends.
const ChangeKindAnnotationChanged = "annotation_changed"

// Reserved annotation keys. Keys with a parameter are built with the
// Key* helpers below.
const (
	// AnnotationHidden's value is JSON {"value": true|false, "reason": string|null}.
	AnnotationHidden = "hidden"
	// AnnotationWIP's value is "true" or "false".
	AnnotationWIP = "wip"
	// AnnotationForceReview marks an entity a person wants reviewed regardless of deciders.
	AnnotationForceReview = "force_review"
	// AnnotationReadyToLand marks an entity ready to land.
	AnnotationReadyToLand = "ready_to_land"

	annotationDispositionPrefix = "disposition."
	annotationSuppressPrefix    = "suppress."
	annotationDeciderPrefix     = "decider."
)

// Disposition values for a disposition.<comment_id> annotation.
const (
	DispositionWillFix  = "will-fix"
	DispositionWontFix  = "wont-fix"
	DispositionNoAction = "no-action"
)

// KeyDisposition returns the key of a per-comment disposition annotation.
func KeyDisposition(commentID string) string { return annotationDispositionPrefix + commentID }

// KeySuppress returns the key of a per-kind suppression annotation.
func KeySuppress(kind string) string { return annotationSuppressPrefix + kind }

// KeyDecider returns the key of a decider-owned annotation,
// decider.<name>.<k>.
func KeyDecider(name, k string) string { return annotationDeciderPrefix + name + "." + k }

// KVAnnotation is one row of the key/value annotation table.
type KVAnnotation struct {
	Repo       string
	EntityType string
	EntityID   string
	Key        string
	Value      string
	Origin     string
	SetBy      string
	SetAt      string // RFC3339 timestamp
}

// ErrNoEntity is returned (matchable with errors.Is) when an annotation
// write names an entity that has no entity row.
var ErrNoEntity = errors.New("entity not found")

// SetAnnotation inserts or replaces the annotation a.Key on the entity and
// appends an annotation_changed change_log record (origin a.Origin, at
// a.SetAt) in the same transaction. It errors, writing nothing, when the
// entity has no row.
func (s *Store) SetAnnotation(a KVAnnotation) error {
	return s.annotationTx(a.Repo, a.EntityType, a.EntityID, a.Key, a.Origin, a.SetAt, func(tx *sql.Tx) (bool, error) {
		_, err := tx.Exec(
			`INSERT INTO annotation (repo, entity_type, entity_id, key, value, origin, set_by, set_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (repo, entity_type, entity_id, key) DO UPDATE SET
			   value = excluded.value,
			   origin = excluded.origin,
			   set_by = excluded.set_by,
			   set_at = excluded.set_at`,
			a.Repo, a.EntityType, a.EntityID, a.Key, a.Value, a.Origin, a.SetBy, a.SetAt,
		)
		return true, err
	})
}

// DeleteAnnotation removes the annotation key from the entity and, when a
// row was removed, appends an annotation_changed record (origin, at) in the
// same transaction. It reports whether a row existed. It errors, writing
// nothing, when the entity has no row.
func (s *Store) DeleteAnnotation(repo, entityType, entityID, key, origin, at string) (bool, error) {
	removed := false
	err := s.annotationTx(repo, entityType, entityID, key, origin, at, func(tx *sql.Tx) (bool, error) {
		res, err := tx.Exec(
			`DELETE FROM annotation WHERE repo = ? AND entity_type = ? AND entity_id = ? AND key = ?`,
			repo, entityType, entityID, key,
		)
		if err != nil {
			return false, err
		}
		n, err := res.RowsAffected()
		removed = n > 0
		return removed, err
	})
	return removed, err
}

// annotationTx runs mutate inside a transaction that first requires the
// entity row (capturing its current version). When mutate reports a change,
// the annotation_changed record is appended before commit.
func (s *Store) annotationTx(repo, entityType, entityID, key, origin, at string, mutate func(tx *sql.Tx) (changed bool, err error)) error {
	if err := s.RequireNewSchema(); err != nil {
		return err
	}
	wrap := func(err error) error {
		return fmt.Errorf("store: annotation %q on (%s,%s,%s): %w", key, repo, entityType, entityID, err)
	}
	tx, err := s.sql.Begin()
	if err != nil {
		return wrap(err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	var version int64
	err = tx.QueryRow(
		`SELECT version FROM entity WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		repo, entityType, entityID,
	).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return wrap(ErrNoEntity)
	}
	if err != nil {
		return wrap(err)
	}

	changed, err := mutate(tx)
	if err != nil {
		return wrap(err)
	}
	if changed {
		if s.betweenAnnotationAndAppend != nil {
			if err := s.betweenAnnotationAndAppend(); err != nil {
				return wrap(err)
			}
		}
		if _, err := s.AppendChangeLogTx(tx, ChangeRecord{
			Repo: repo, EntityType: entityType, EntityID: entityID,
			Version: version, Kinds: []string{ChangeKindAnnotationChanged}, Origin: origin, At: at,
		}); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return wrap(err)
	}
	return nil
}

// GetKVAnnotation returns one entity's annotation for key, or found=false.
func (s *Store) GetKVAnnotation(repo, entityType, entityID, key string) (KVAnnotation, bool, error) {
	if err := s.RequireNewSchema(); err != nil {
		return KVAnnotation{}, false, err
	}
	var a KVAnnotation
	err := s.sql.QueryRow(
		`SELECT repo, entity_type, entity_id, key, value, origin, set_by, set_at
		 FROM annotation WHERE repo = ? AND entity_type = ? AND entity_id = ? AND key = ?`,
		repo, entityType, entityID, key,
	).Scan(&a.Repo, &a.EntityType, &a.EntityID, &a.Key, &a.Value, &a.Origin, &a.SetBy, &a.SetAt)
	if errors.Is(err, sql.ErrNoRows) {
		return KVAnnotation{}, false, nil
	}
	if err != nil {
		return KVAnnotation{}, false, fmt.Errorf("store: get annotation %q (%s,%s,%s): %w", key, repo, entityType, entityID, err)
	}
	return a, true, nil
}

// ListKVAnnotations returns every annotation of one entity, ordered by key.
func (s *Store) ListKVAnnotations(repo, entityType, entityID string) ([]KVAnnotation, error) {
	if err := s.RequireNewSchema(); err != nil {
		return nil, err
	}
	rows, err := s.sql.Query(
		`SELECT repo, entity_type, entity_id, key, value, origin, set_by, set_at
		 FROM annotation WHERE repo = ? AND entity_type = ? AND entity_id = ? ORDER BY key`,
		repo, entityType, entityID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list annotations (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []KVAnnotation
	for rows.Next() {
		var a KVAnnotation
		if err := rows.Scan(&a.Repo, &a.EntityType, &a.EntityID, &a.Key, &a.Value, &a.Origin, &a.SetBy, &a.SetAt); err != nil {
			return nil, fmt.Errorf("store: scan annotation (%s,%s,%s): %w", repo, entityType, entityID, err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate annotations (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
