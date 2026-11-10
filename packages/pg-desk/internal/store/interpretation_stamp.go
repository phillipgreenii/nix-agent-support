package store

import (
	"errors"
	"fmt"
)

// The interpretation stamp: which build and which interpretation-relevant
// configuration computed an interpretation row (docs/behavior/pg-desk/
// reinterpret.md, "The stamp"; store-schema.md, "The interpretation stamp").

// interpStampColumn is the nullable interpretation column that holds the
// stamp. NULL means "written before stamping" and differs from every stamp.
const interpStampColumn = "interp_version"

// FailedStampSuffix follows a stamp on a row that failed to re-interpret
// (INV-REINTERPRET-8): the row then differs from the running stamp, so it is
// not recorded as current, but the behind query skips it under that stamp.
const FailedStampSuffix = "!failed"

// ErrNoInterpStamp is returned by a query that compares against the handle's
// stamp when the handle has none: with no stamp every row would be "behind"
// forever, so the caller must set one (SetInterpStamp) first.
var ErrNoInterpStamp = errors.New("store: no interpretation stamp set on this handle")

// ensureInterpretationStamp adds interpretation.interp_version when it is
// absent. Like ensureIssueFeedStorage it is an additive step run from Open,
// guarded by pragma table_info, idempotent, tolerant of a concurrent opener,
// and it NEVER touches user_version (it is not a rung of the version ladder:
// a rung would mis-stamp user_version). The cutover only drops sync_error from
// interpretation and does not rebuild it, so the column survives it. A file
// with no interpretation table at all (nothing migrated) is left alone.
func ensureInterpretationStamp(s *Store) error {
	have, err := s.tableColumns("interpretation")
	if err != nil {
		return err
	}
	if len(have) == 0 || have[interpStampColumn] {
		return nil
	}
	if _, err := s.sql.Exec(`ALTER TABLE interpretation ADD COLUMN ` + interpStampColumn + ` TEXT`); err != nil {
		// A second process may have added it between the check and the ALTER;
		// the column being present now is success.
		again, cerr := s.tableColumns("interpretation")
		if cerr != nil || !again[interpStampColumn] {
			return fmt.Errorf("add interpretation.%s: %w", interpStampColumn, err)
		}
	}
	return nil
}

// interpShape reports the two facts every interpretation query decides from
// before it is issued: whether the store is on schema 2 and whether the table
// has the stamp column.
func (s *Store) interpShape() (newSchema, haveStamp bool, err error) {
	newSchema, err = s.isNewSchema()
	if err != nil {
		return false, false, err
	}
	haveStamp, err = s.hasInterpStampColumn()
	if err != nil {
		return false, false, err
	}
	return newSchema, haveStamp, nil
}

// hasInterpStampColumn reports whether the interpretation table has the stamp
// column: false for a store a read-only or raw open reached before any Open of
// the binary that adds it. Called before a query is issued, never while a
// result set is open.
func (s *Store) hasInterpStampColumn() (bool, error) {
	have, err := s.tableColumns("interpretation")
	if err != nil {
		return false, err
	}
	return have[interpStampColumn], nil
}

// SetInterpStamp sets the stamp this handle writes into every interpretation
// row it writes and compares against in the behind query. The empty string
// clears it (a handle with no stamp writes NULL, which is right for read-only
// tools and tests). It is safe to call while the handle is shared.
func (s *Store) SetInterpStamp(stamp string) {
	if stamp == "" {
		s.interpStamp.Store(nil)
		return
	}
	s.interpStamp.Store(&stamp)
}

// InterpStamp returns the handle's stamp, or "" when none is set.
func (s *Store) InterpStamp() string {
	if p := s.interpStamp.Load(); p != nil {
		return *p
	}
	return ""
}

// InterpretationKey identifies one interpretation row.
type InterpretationKey struct {
	Repo       string
	EntityType string
	EntityID   string
}

// InterpretationsBehind returns the keys of the `pr` interpretation rows of
// repo whose stamp differs from the handle's stamp (a NULL stamp differs from
// every stamp), skipping rows already marked failed under that stamp
// (stamp + FailedStampSuffix). At most limit keys are returned (limit <= 0
// means no limit).
//
// known is false, with no error, when the answer cannot be given: the store is
// not on schema 2, or its interpretation table has no stamp column yet (a
// reader that never ran Open). "Unknown" is never "none behind": callers (the
// status line) report it as unknown. The handle must carry a stamp, else
// ErrNoInterpStamp.
//
// The query reads only the small interpretation table, joined to entity
// WITHOUT selecting facts, so a pass that finds nothing is cheap. The order
// reaches open PRs first without reading facts: entity.active DESC, then
// entity.hydrated_at DESC, then the entity id for a stable order. A row whose
// entity is gone still appears (last), so its failure can be recorded.
func (s *Store) InterpretationsBehind(repo string, limit int) (keys []InterpretationKey, known bool, err error) {
	stamp := s.InterpStamp()
	if stamp == "" {
		return nil, false, ErrNoInterpStamp
	}
	newSchema, haveStamp, err := s.interpShape()
	if err != nil {
		return nil, false, fmt.Errorf("store: interpretations behind: %w", err)
	}
	if !newSchema || !haveStamp {
		return nil, false, nil
	}
	q := `SELECT i.repo, i.entity_type, i.entity_id
	      FROM interpretation i
	      LEFT JOIN entity e
	        ON e.repo = i.repo AND e.entity_type = i.entity_type AND e.entity_id = i.entity_id
	      WHERE i.repo = ? AND i.entity_type = 'pr'
	        AND i.interp_version IS NOT ?
	        AND i.interp_version IS NOT ?
	      ORDER BY COALESCE(e.active, 0) DESC, e.hydrated_at DESC, i.entity_id`
	args := []any{repo, stamp, stamp + FailedStampSuffix}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.sql.Query(q, args...)
	if err != nil {
		return nil, false, fmt.Errorf("store: interpretations behind: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k InterpretationKey
		if err := rows.Scan(&k.Repo, &k.EntityType, &k.EntityID); err != nil {
			return nil, false, fmt.Errorf("store: scan interpretation key: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("store: interpretations behind: %w", err)
	}
	return keys, true, nil
}
