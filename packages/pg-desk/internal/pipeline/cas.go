package pipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// casPlan is what one compare-and-set attempt writes, built from the row the
// attempt just read.
type casPlan struct {
	entity     store.Entity
	hydratedAt string
	active     bool
	listFP     *string
	kinds      []string
	origin, at string
	opts       store.WriteOptions
	// skip ends the loop without a write (the caller decides what that means).
	skip bool
}

// casResult is what commitCAS returns: the outcome of the winning (or last)
// attempt.
type casResult struct {
	version int64
	// retries is the number of lost-race retries made.
	retries int
	// cur and found are the row read for the last attempt.
	cur   store.Entity
	found bool
	// written is true when the write committed; skipped when the plan asked to
	// skip; exhausted when the retries ran out, with conflict the last
	// store.ErrVersionConflict error.
	written, skipped, exhausted bool
	conflict                    error
}

// commitCAS is the ONE compare-and-set write loop of the hydration paths
// (RunEntityChange, a feed row and a removed feed row): per attempt it checks
// the context, reads the entity, asks plan to build what to write from that
// row, runs seam(attempt) (a test seam, nil by default) and writes conditional
// on the version it read. A lost race re-reads and retries up to maxRetries
// times. A store error other than a version conflict, a read error and a
// cancelled context are returned as errors; an exhausted loop is reported in
// the result, because the callers treat it differently (RunEntityChange fails,
// a feed row records a failed ingest).
func (p *Pipeline) commitCAS(ctx context.Context, repo, entityType, entityID string, maxRetries int, seam func(attempt int), plan func(cur store.Entity, found bool) (casPlan, error)) (casResult, error) {
	var res casResult
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return res, fmt.Errorf("pipeline: %s %s: %w", entityType, entityID, err)
		}
		cur, found, err := p.store.GetEntity(repo, entityType, entityID)
		if err != nil {
			return res, fmt.Errorf("pipeline: read %s %s: %w", entityType, entityID, err)
		}
		res.cur, res.found = cur, found
		var expected int64
		if found {
			expected = cur.Version
		}
		cp, err := plan(cur, found)
		if err != nil {
			return res, err
		}
		if cp.skip {
			res.skipped = true
			return res, nil
		}
		if seam != nil {
			seam(attempt)
		}
		v, werr := p.store.WriteEntityStateWithLogOpts(cp.entity, expected, cp.hydratedAt, cp.active, cp.listFP, cp.kinds, cp.origin, cp.at, cp.opts)
		if werr == nil {
			res.version, res.written = v, true
			return res, nil
		}
		if !errors.Is(werr, store.ErrVersionConflict) {
			return res, fmt.Errorf("pipeline: write %s %s: %w", entityType, entityID, werr)
		}
		if res.retries >= maxRetries {
			res.exhausted, res.conflict = true, werr
			return res, nil
		}
		res.retries++
	}
}
