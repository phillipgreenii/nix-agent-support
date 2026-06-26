package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Revision is one observed (head_sha, base_sha) of a PR in time order.
type Revision struct {
	ID            int64
	PRID          int64
	Seq           int
	HeadSHA       string
	BaseSHA       string // "" when NULL
	ObservedAt    string
	LastSeenAt    string
	CIState       string
	CIPassed      int
	CIFailed      int
	CIPending     int
	CICapturedAt  string // "" when NULL
	ReviewedAt    string // "" when NULL
	MyReviewState string // "" when NULL
}

const revisionColumns = `id, pr_id, seq, head_sha, COALESCE(base_sha,''),
	observed_at, last_seen_at, ci_state, ci_passed, ci_failed, ci_pending,
	COALESCE(ci_captured_at,''), COALESCE(reviewed_at,''), COALESCE(my_review_state,'')`

func scanRevision(s rowScanner) (Revision, error) {
	var r Revision
	err := s.Scan(&r.ID, &r.PRID, &r.Seq, &r.HeadSHA, &r.BaseSHA,
		&r.ObservedAt, &r.LastSeenAt, &r.CIState, &r.CIPassed, &r.CIFailed,
		&r.CIPending, &r.CICapturedAt, &r.ReviewedAt, &r.MyReviewState)
	return r, err
}

// samePair reports whether two (head, base) pairs are the same revision. A NULL
// (empty) base on either side degrades to a head-only comparison.
func samePair(aHead, aBase, bHead, bBase string) bool {
	if aHead != bHead {
		return false
	}
	if aBase == "" || bBase == "" {
		return true
	}
	return aBase == bBase
}

// RecordRevision appends a new revision when (headSHA, baseSHA) differs from the
// PR's latest revision, otherwise bumps the latest revision's last_seen_at. It
// returns the resulting latest revision and whether a new row was appended.
func (db *DB) RecordRevision(ctx context.Context, prID int64, headSHA, baseSHA string) (Revision, bool, error) {
	var result Revision
	var appended bool
	err := db.InTx(ctx, func(tx *Tx) error {
		now := nowRFC3339()
		latest, err := tx.latestRevision(prID)
		if err != nil {
			return err
		}
		if latest != nil && samePair(latest.HeadSHA, latest.BaseSHA, headSHA, baseSHA) {
			if _, err := tx.Exec(`UPDATE pr_revision SET last_seen_at=? WHERE id=?`, now, latest.ID); err != nil {
				return fmt.Errorf("store: touch revision: %w", err)
			}
			latest.LastSeenAt = now
			result = *latest
			return nil
		}
		seq := 1
		if latest != nil {
			seq = latest.Seq + 1
		}
		var base any
		if baseSHA != "" {
			base = baseSHA
		}
		res, err := tx.Exec(`INSERT INTO pr_revision
			(pr_id, seq, head_sha, base_sha, observed_at, last_seen_at)
			VALUES (?,?,?,?,?,?)`, prID, seq, headSHA, base, now, now)
		if err != nil {
			return fmt.Errorf("store: append revision: %w", err)
		}
		id, _ := res.LastInsertId()
		appended = true
		result = Revision{ID: id, PRID: prID, Seq: seq, HeadSHA: headSHA,
			BaseSHA: baseSHA, ObservedAt: now, LastSeenAt: now, CIState: "none"}
		return nil
	})
	return result, appended, err
}

// latestRevision returns the highest-seq revision for prID, or nil if none.
func (t *Tx) latestRevision(prID int64) (*Revision, error) {
	row := t.QueryRow(`SELECT `+revisionColumns+`
		FROM pr_revision WHERE pr_id=? ORDER BY seq DESC LIMIT 1`, prID)
	r, err := scanRevision(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: latest revision: %w", err)
	}
	return &r, nil
}
