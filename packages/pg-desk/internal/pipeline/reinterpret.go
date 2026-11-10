package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// ReinterpretOptions parameterizes ReinterpretStored.
type ReinterpretOptions struct {
	// Origin labels the interpretation_changed record a changed decision
	// appends (the entry point: "run", "reinterpret", ...).
	Origin string

	// DryRun derives the row and returns it without writing anything: no
	// interpretation write, no stamp, no change record. The result's Write is
	// the zero value.
	DryRun bool

	// beforeWrite is a TEST SEAM, nil in production: it runs between the
	// facts read and the interpretation write, so a test can commit competing
	// facts in that gap. A returned error is the write's error.
	beforeWrite func() error
}

// ReinterpretResult says what ReinterpretStored did.
type ReinterpretResult struct {
	// Found is false when the entity row does not exist (nothing was
	// interpreted and nothing was written; not an error).
	Found bool
	// Row is the interpretation row that was derived (and, unless the write
	// was skipped, stored). Interp is the interpreter's own result.
	Row    store.Interpretation
	Interp interpret.Interpretation
	// Write is what the store did: written, skipped (different facts were
	// committed since the read), whether the decision pair changed and whether
	// a record was appended.
	Write store.InterpretationWriteResult
}

// ReinterpretStored re-derives the interpretation of one entity from the facts
// already stored for it and writes it. It never gathers, never touches the
// entity row (its facts, as-of time, version and hydration time stay as they
// were) and makes no network call. It loads the entity row, runs
// interpretEntity over `GatherResult{Payload: facts, AsOf: entity.AsOf}` and
// hands the row to Store.WriteInterpretation with the content hash it read and
// the stamp required, asking for an interpretation_changed record (the store
// honors it for a `pr` only).
//
// It is a free function, not a Pipeline method: pipeline.New builds a
// gatherer, and the re-interpretation path must stay free of any connector
// dependency. It requires schema 2. A write skipped because a hydration
// committed different facts in the meantime is reported in the result, not as
// an error: the newer hydration writes its own row.
//
// Errors carry a stage tag (load_facts, interpret, store).
func ReinterpretStored(ctx context.Context, st *store.Store, cfg *config.Config, clock interpret.Clock, entityType, entityID string, opts ReinterpretOptions) (ReinterpretResult, error) {
	repo := repoOf(cfg)
	if err := ctx.Err(); err != nil {
		return ReinterpretResult{}, TagStage(StageLoadFacts, fmt.Errorf("pipeline: reinterpret %s %s: %w", entityType, entityID, err))
	}
	entity, found, err := st.GetEntity(repo, entityType, entityID)
	if err != nil {
		return ReinterpretResult{}, TagStage(StageLoadFacts, fmt.Errorf("pipeline: reinterpret get entity %s %s: %w", entityType, entityID, err))
	}
	if !found {
		return ReinterpretResult{}, nil
	}

	var facts gather.Facts
	if err := json.Unmarshal([]byte(entity.Facts), &facts); err != nil {
		return ReinterpretResult{Found: true}, TagStage(StageLoadFacts, fmt.Errorf("pipeline: reinterpret decode stored facts %s %s: %w", entityType, entityID, err))
	}

	row, interp, err := interpretEntity(repo, cfg, clock, entityType, entityID, gather.GatherResult{Payload: json.RawMessage(entity.Facts), AsOf: entity.AsOf})
	if err != nil {
		return ReinterpretResult{Found: true}, TagStage(StageInterpret, fmt.Errorf("pipeline: reinterpret interpret %s %s: %w", entityType, entityID, err))
	}
	res := ReinterpretResult{Found: true, Row: row, Interp: interp}

	if opts.DryRun {
		return res, nil
	}
	if h := opts.beforeWrite; h != nil {
		if err := h(); err != nil {
			return res, TagStage(StageStore, fmt.Errorf("pipeline: reinterpret store %s %s: %w", entityType, entityID, err))
		}
	}
	res.Write, err = st.WriteInterpretation(row, store.InterpretationWriteOptions{
		ExpectedHash: entity.ContentHash,
		RecordChange: true,
		RequireStamp: true,
		At:           clock.Now().UTC().Format(time.RFC3339),
		Origin:       opts.Origin,
	})
	if err != nil {
		return res, TagStage(StageStore, fmt.Errorf("pipeline: reinterpret store %s %s: %w", entityType, entityID, err))
	}
	return res, nil
}
