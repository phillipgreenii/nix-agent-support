package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// OriginalsConfirmedAt returns when the recorder last confirmed the original
// transcript at transcriptPath present for externalID (unix seconds). ok is false
// when nothing is stored or the stored answer is for a different path.
func (s *Store) OriginalsConfirmedAt(ctx context.Context, externalID, transcriptPath string) (int64, bool, error) {
	var path string
	var at int64
	err := s.db.QueryRowContext(ctx,
		`SELECT transcript_path, confirmed_at FROM originals_probe WHERE external_id = ?`, externalID).Scan(&path, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("originals probe %q: %w", externalID, err)
	}
	if path != transcriptPath {
		return 0, false, nil
	}
	return at, true, nil
}

// SetOriginalsConfirmed records that the original transcript at transcriptPath
// was confirmed present at unix time at.
func (s *Store) SetOriginalsConfirmed(ctx context.Context, externalID, transcriptPath string, at int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO originals_probe (external_id, transcript_path, confirmed_at) VALUES (?,?,?)
		 ON CONFLICT(external_id) DO UPDATE SET transcript_path = excluded.transcript_path, confirmed_at = excluded.confirmed_at`,
		externalID, transcriptPath, at)
	if err != nil {
		return fmt.Errorf("set originals probe %q: %w", externalID, err)
	}
	return nil
}
