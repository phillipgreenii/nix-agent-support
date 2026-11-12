package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// The interpretation write path (docs/behavior/pg-desk/reinterpret.md,
// "One interpretation path", INV-REINTERPRET-6 to 8 and 14): the only code
// that writes an interpretation row in production is WriteInterpretation, with
// StampInterpretation (stamp only) and MarkInterpretationFailed (failed marker)
// as its two narrow siblings. All three require schema 2.

// ChangeKindInterpretationChanged is the change_log kind a re-interpretation
// of a `pr` appends when its decision pair (approvals.waiting_on, panel)
// changed. It is history only: nothing decides on it.
const ChangeKindInterpretationChanged = "interpretation_changed"

// entityTypePR is the one entity type that gets an interpretation_changed
// record (an `issue` or `thread` consumer reads its change log, where an
// unknown kind could be misread).
const entityTypePR = "pr"

// ErrNoInterpretation is returned (matchable with errors.Is) when a stamp or
// failed-marker update names an interpretation row that does not exist.
var ErrNoInterpretation = errors.New("interpretation not found")

// ErrStampRequired is returned by a write that asked for a stamp
// (RequireStamp, and StampInterpretation always) on a handle that has none:
// without it the row would stay behind forever and every pass would repeat.
var ErrStampRequired = errors.New("store: interpretation write requires a stamp and the handle has none")

// InterpretationWriteStep names a step of the write transaction, for the
// fault-injection hook (SetInterpretationWriteHook).
type InterpretationWriteStep string

// The steps, in the order the transaction takes them.
const (
	StepBegin   InterpretationWriteStep = "begin"
	StepLock    InterpretationWriteStep = "lock"
	StepReadOld InterpretationWriteStep = "read-old"
	StepUpsert  InterpretationWriteStep = "upsert"
	StepAppend  InterpretationWriteStep = "append"
	StepCommit  InterpretationWriteStep = "commit"
)

// SetInterpretationWriteHook installs fn as a fault-injection seam run inside
// WriteInterpretation, StampInterpretation and MarkInterpretationFailed just
// before each step of their transaction; a non-nil return aborts and rolls
// back the write. Passing nil clears it. Test use only.
func (s *Store) SetInterpretationWriteHook(fn func(step InterpretationWriteStep) error) {
	s.interpWriteHook = fn
}

func (s *Store) interpStep(step InterpretationWriteStep) error {
	if h := s.interpWriteHook; h != nil {
		return h(step)
	}
	return nil
}

// InterpretationWriteOptions parameterizes WriteInterpretation.
type InterpretationWriteOptions struct {
	// ExpectedHash is the entity.content_hash the row was derived from. The
	// write is skipped when the stored hash differs (different facts were
	// committed since). It is NOT an entity-version check: a version bump with
	// the same facts still writes.
	ExpectedHash string
	// RecordChange asks for one interpretation_changed record when the
	// decision pair changed. Honored for a `pr` only.
	RecordChange bool
	// RequireStamp makes the write fail with ErrStampRequired on a handle with
	// no stamp (the re-interpretation paths). Without it a handle with no
	// stamp writes NULL (a hydration through a bare handle).
	RequireStamp bool
	// At and Origin stamp the change record (RFC3339 time, origin label).
	At     string
	Origin string
}

// InterpretationWriteResult says what WriteInterpretation did.
type InterpretationWriteResult struct {
	// Written is true when the row was upserted.
	Written bool
	// Skipped is true when the stored facts differ from the expected hash;
	// nothing was written.
	Skipped bool
	// DecisionChanged is true when the pair (approvals.waiting_on, panel) of
	// the row that existed differs from the new row's. False when no row
	// existed before.
	DecisionChanged bool
	// Recorded is true when an interpretation_changed record was appended.
	Recorded bool
}

// decisionPair reads the pair a change record is about from a row's columns.
func decisionPair(approvals, panel string) (waitingOn, p string) {
	var a struct {
		WaitingOn string `json:"waiting_on"`
	}
	_ = json.Unmarshal([]byte(approvals), &a) // a malformed blob reads as no decision
	return a.WaitingOn, panel
}

// WriteInterpretation is the only code that writes an interpretation row.
//
// It runs ONE transaction that takes the write lock FIRST (a no-op UPDATE of
// the entity row, the technique of annotationTx: a deferred read-then-write
// transaction otherwise fails at once with SQLITE_BUSY), returns ErrNoEntity
// when the entity row is missing, skips when the stored content_hash differs
// from opts.ExpectedHash, reads the old interpretation row INSIDE the
// transaction (never GetInterpretation: it uses s.sql and the pool holds one
// connection, so it would deadlock), upserts the new row with the handle's
// stamp and, when asked and the decision pair changed on a `pr`, appends one
// interpretation_changed record at the entity's current version without
// bumping it.
//
// The schema check runs BEFORE Begin, so a schema-1 store is refused with the
// cutover message having written nothing.
func (s *Store) WriteInterpretation(row Interpretation, opts InterpretationWriteOptions) (InterpretationWriteResult, error) {
	wrap := func(err error) error {
		return fmt.Errorf("store: write interpretation (%s,%s,%s): %w", row.Repo, row.EntityType, row.EntityID, err)
	}
	if err := s.RequireNewSchema(); err != nil {
		return InterpretationWriteResult{}, err
	}
	stamp := s.InterpStamp()
	if opts.RequireStamp && stamp == "" {
		return InterpretationWriteResult{}, wrap(ErrStampRequired)
	}
	haveStamp, err := s.hasInterpStampColumn()
	if err != nil {
		return InterpretationWriteResult{}, wrap(err)
	}

	tx, err := s.beginLocked(StepBegin)
	if err != nil {
		return InterpretationWriteResult{}, wrap(err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	version, hash, err := s.lockEntity(tx, row.Repo, row.EntityType, row.EntityID)
	if err != nil {
		return InterpretationWriteResult{}, wrap(err)
	}
	if hash != opts.ExpectedHash {
		return InterpretationWriteResult{Skipped: true}, nil
	}

	if err := s.interpStep(StepReadOld); err != nil {
		return InterpretationWriteResult{}, wrap(err)
	}
	var oldApprovals, oldPanel string
	oldErr := tx.QueryRowContext(
		context.Background(),
		`SELECT approvals, panel FROM interpretation WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		row.Repo, row.EntityType, row.EntityID,
	).Scan(&oldApprovals, &oldPanel)
	hadOld := !errors.Is(oldErr, sql.ErrNoRows)
	if oldErr != nil && hadOld {
		return InterpretationWriteResult{}, wrap(oldErr)
	}

	if err := s.interpStep(StepUpsert); err != nil {
		return InterpretationWriteResult{}, wrap(err)
	}
	if err := upsertInterpretationNew(tx, row, haveStamp, stamp); err != nil {
		return InterpretationWriteResult{}, wrap(err)
	}

	res := InterpretationWriteResult{Written: true}
	if hadOld {
		ow, op := decisionPair(oldApprovals, oldPanel)
		nw, np := decisionPair(row.Approvals, row.Panel)
		res.DecisionChanged = ow != nw || op != np
	}
	if opts.RecordChange && res.DecisionChanged && row.EntityType == entityTypePR {
		if err := s.interpStep(StepAppend); err != nil {
			return InterpretationWriteResult{}, wrap(err)
		}
		if _, err := s.AppendChangeLogTx(tx, ChangeRecord{
			Repo: row.Repo, EntityType: row.EntityType, EntityID: row.EntityID,
			Version: version, Kinds: []string{ChangeKindInterpretationChanged},
			Origin: opts.Origin, At: opts.At,
		}); err != nil {
			return InterpretationWriteResult{}, wrap(err)
		}
		res.Recorded = true
	}
	if err := s.interpStep(StepCommit); err != nil {
		return InterpretationWriteResult{}, wrap(err)
	}
	if err := tx.Commit(); err != nil {
		return InterpretationWriteResult{}, wrap(err)
	}
	return res, nil
}

// StampInterpretation updates only the stamp of the interpretation row of key
// to the handle's stamp, under the same lock-and-check as WriteInterpretation
// (a row whose facts changed since expectedHash was read is skipped). It is
// for a row that is not open: its content stays, no record is appended. It
// returns whether the stamp was written. The handle MUST carry a stamp
// (ErrStampRequired), and the row MUST exist (ErrNoInterpretation).
func (s *Store) StampInterpretation(key InterpretationKey, expectedHash string) (stamped bool, err error) {
	wrap := func(err error) error {
		return fmt.Errorf("store: stamp interpretation (%s,%s,%s): %w", key.Repo, key.EntityType, key.EntityID, err)
	}
	if err := s.RequireNewSchema(); err != nil {
		return false, err
	}
	stamp := s.InterpStamp()
	if stamp == "" {
		return false, wrap(ErrStampRequired)
	}
	tx, err := s.beginLocked(StepBegin)
	if err != nil {
		return false, wrap(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, hash, err := s.lockEntity(tx, key.Repo, key.EntityType, key.EntityID)
	if err != nil {
		return false, wrap(err)
	}
	if hash != expectedHash {
		return false, nil
	}
	if err := s.setStampTx(tx, key, stamp); err != nil {
		return false, wrap(err)
	}
	if err := s.interpStep(StepCommit); err != nil {
		return false, wrap(err)
	}
	if err := tx.Commit(); err != nil {
		return false, wrap(err)
	}
	return true, nil
}

// MarkInterpretationFailed gives the row of key the stamp `<stamp>!failed`
// (FailedStampSuffix), keeping its content, WITHOUT looking at the entity: it
// must work when the entity row is the thing that is gone. stamp is the
// running stamp (the handle's, passed by the caller so the marker names the
// stamp it was computed under). The row MUST exist (ErrNoInterpretation).
func (s *Store) MarkInterpretationFailed(key InterpretationKey, stamp string) error {
	wrap := func(err error) error {
		return fmt.Errorf("store: mark interpretation failed (%s,%s,%s): %w", key.Repo, key.EntityType, key.EntityID, err)
	}
	if err := s.RequireNewSchema(); err != nil {
		return err
	}
	if stamp == "" {
		return wrap(ErrStampRequired)
	}
	tx, err := s.sql.BeginTx(context.Background(), nil)
	if err != nil {
		return wrap(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.interpStep(StepBegin); err != nil {
		return wrap(err)
	}
	if err := s.setStampTx(tx, key, stamp+FailedStampSuffix); err != nil {
		return wrap(err)
	}
	if err := s.interpStep(StepCommit); err != nil {
		return wrap(err)
	}
	if err := tx.Commit(); err != nil {
		return wrap(err)
	}
	return nil
}

// setStampTx updates interp_version of key's row; ErrNoInterpretation when
// there is no such row or the table has no stamp column.
func (s *Store) setStampTx(tx *sql.Tx, key InterpretationKey, stamp string) error {
	if err := s.interpStep(StepUpsert); err != nil {
		return err
	}
	res, err := tx.ExecContext(
		context.Background(),
		`UPDATE interpretation SET `+interpStampColumn+` = ? WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		stamp, key.Repo, key.EntityType, key.EntityID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNoInterpretation
	}
	return nil
}

// beginLocked begins a transaction, running the begin-step hook.
func (s *Store) beginLocked(step InterpretationWriteStep) (*sql.Tx, error) {
	tx, err := s.sql.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	if err := s.interpStep(step); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// lockEntity takes the write lock with a no-op UPDATE of the entity row and
// returns its version and content_hash; ErrNoEntity when the row is missing.
func (s *Store) lockEntity(tx *sql.Tx, repo, entityType, entityID string) (version int64, hash string, err error) {
	if err := s.interpStep(StepLock); err != nil {
		return 0, "", err
	}
	res, err := tx.ExecContext(
		context.Background(),
		`UPDATE entity SET version = version WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		repo, entityType, entityID,
	)
	if err != nil {
		return 0, "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, "", err
	}
	if n == 0 {
		return 0, "", ErrNoEntity
	}
	err = tx.QueryRowContext(
		context.Background(),
		`SELECT version, content_hash FROM entity WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		repo, entityType, entityID,
	).Scan(&version, &hash)
	if err != nil {
		return 0, "", err
	}
	return version, hash, nil
}
