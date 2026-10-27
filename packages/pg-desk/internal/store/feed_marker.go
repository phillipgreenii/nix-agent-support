package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// The feed marker of INV-ICD-19 (redelivery is idempotent).
//
// pg-desk keys idempotence on the feed's own sequence AND on the daemon's
// store identity, because the sequence alone would skip new rows after a
// moved-aside daemon store restarts it at 1. For each backend instance it
// keeps, in the meta table, the highest feed seq it has committed together
// with the store identity that seq belongs to. The marker is written by the
// same transaction as the row's entity write and change row, so a crash
// either keeps all of them or none; a redelivered row at or below the marker
// is then skipped without touching the store.

// metaKeyFeedSeqPrefix is the meta key prefix of the marker; the full key is
// change_flow.feed_seq.<instance>.
const metaKeyFeedSeqPrefix = "change_flow.feed_seq."

// afterAppendHook is a fault-injection seam for tests: when non-nil it runs
// inside writeEntity's transaction after the append and before the marker
// write, and a non-nil return aborts and rolls back the whole write. It is
// nil in production.
var afterAppendHook func() error

// feedMarkerValue is the JSON stored under the marker's meta key.
type feedMarkerValue struct {
	StoreIdentity string `json:"store_identity"`
	Seq           int64  `json:"seq"`
}

func feedMarkerKey(instance string) string { return metaKeyFeedSeqPrefix + instance }

// FeedMarker returns the stored marker of instance, or found=false when none
// is stored.
func (s *Store) FeedMarker(instance string) (m FeedMarker, found bool, err error) {
	raw, found, err := s.GetMeta(feedMarkerKey(instance))
	if err != nil || !found {
		return FeedMarker{}, false, err
	}
	m, err = decodeFeedMarker(instance, raw)
	if err != nil {
		return FeedMarker{}, false, err
	}
	return m, true, nil
}

func decodeFeedMarker(instance, raw string) (FeedMarker, error) {
	var v feedMarkerValue
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return FeedMarker{}, fmt.Errorf("store: decode feed marker %q: %w", instance, err)
	}
	return FeedMarker{Instance: instance, StoreIdentity: v.StoreIdentity, Seq: v.Seq}, nil
}

// ShouldSkipFeedRow reports whether the feed row (storeIdentity, seq,
// entityID) of instance must be skipped as a redelivery: the stored marker
// has the same store identity and seq is at or below its seq, or a
// failed-ingest entry already exists for (instance, entityID, seq) under the
// same store identity (an entry of another identity names another sequence).
func (s *Store) ShouldSkipFeedRow(instance, storeIdentity string, seq int64, entityID string) (bool, error) {
	m, found, err := s.FeedMarker(instance)
	if err != nil {
		return false, err
	}
	if found && m.StoreIdentity == storeIdentity && seq <= m.Seq {
		return true, nil
	}
	ok, err := s.ingestFailedExists()
	if err != nil || !ok {
		return false, err
	}
	var identity string
	err = s.sql.QueryRow(
		`SELECT store_identity FROM ingest_failed WHERE instance = ? AND entity_id = ? AND seq = ?`,
		instance, entityID, seq,
	).Scan(&identity)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: check ingest failure (%s,%s,%d): %w", instance, entityID, seq, err)
	}
	return identity == storeIdentity, nil
}

// ResetFeedMarker deletes the marker of instance. It is called on a
// different store identity or a cursor_expired: answer, before any row is
// processed. Deleting an absent marker is not an error.
func (s *Store) ResetFeedMarker(instance string) error {
	return s.DeleteMeta(feedMarkerKey(instance))
}

// AdvanceFeedMarker writes m as the marker of m.Instance without any entity
// or change_log write (a row that needs no entity change). It follows the
// write path's rule: within one store identity the marker only moves
// forward, and a different identity replaces it.
func (s *Store) AdvanceFeedMarker(m FeedMarker) error {
	tx, err := s.sql.Begin()
	if err != nil {
		return fmt.Errorf("store: advance feed marker %q: begin: %w", m.Instance, err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	if err := writeFeedMarkerTx(tx, m); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: advance feed marker %q: commit: %w", m.Instance, err)
	}
	return nil
}

// writeFeedMarkerTx upserts m inside tx: written when none is stored, when
// the stored identity differs, or when m.Seq is greater than the stored seq;
// an equal or lower seq under the same identity leaves it unchanged.
func writeFeedMarkerTx(tx *sql.Tx, m FeedMarker) error {
	key := feedMarkerKey(m.Instance)
	var raw string
	err := tx.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No marker yet: write below.
	case err != nil:
		return fmt.Errorf("store: read feed marker %q: %w", m.Instance, err)
	default:
		cur, err := decodeFeedMarker(m.Instance, raw)
		if err != nil {
			return err
		}
		if cur.StoreIdentity == m.StoreIdentity && m.Seq <= cur.Seq {
			return nil
		}
	}
	val, err := json.Marshal(feedMarkerValue{StoreIdentity: m.StoreIdentity, Seq: m.Seq})
	if err != nil {
		return fmt.Errorf("store: encode feed marker %q: %w", m.Instance, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		key, string(val),
	); err != nil {
		return fmt.Errorf("store: write feed marker %q: %w", m.Instance, err)
	}
	return nil
}
