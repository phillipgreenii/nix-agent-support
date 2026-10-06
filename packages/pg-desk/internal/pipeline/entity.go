package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// RunGenericEntity gathers and interprets one entity through the generic
// per-type registries and persists the payload and interpretation. It is
// the type-agnostic counterpart of Run and does not touch the PR path.
//
// A failed detail read leaves the previous snapshot in place and logs
// nothing: every gather or interpret error returns BEFORE any store write,
// and this function never calls logRun or writes to the pipeline log writer.
//
// D-G8: a "not_found" result is an error unless the caller asked for an
// explicit removal (change == gather.ChangeRemoved). Treating an entity that
// drops out of every watched query as removed/inactive is the caller's
// policy, not this function's.
func (p *Pipeline) RunGenericEntity(ctx context.Context, entityType, entityID string, change gather.ChangeKind) error {
	ge, ok := p.entityGatherers[entityType]
	if !ok {
		return fmt.Errorf("pipeline: no gather adapter registered for entity type %q", entityType)
	}
	result, err := ge.GatherEntity(ctx, entityID, change)
	if err != nil {
		return fmt.Errorf("pipeline: gather %s %s: %w", entityType, entityID, err)
	}
	if result.RemovedState == "not_found" && change != gather.ChangeRemoved {
		return fmt.Errorf("pipeline: %s %s: not found", entityType, entityID)
	}
	ie, ok := interpret.EntityInterpreters()[entityType]
	if !ok {
		return fmt.Errorf("pipeline: no interpret adapter registered for entity type %q", entityType)
	}
	interp, err := ie(result, p.clock, p.cfg)
	if err != nil {
		return fmt.Errorf("pipeline: interpret %s %s: %w", entityType, entityID, err)
	}
	if _, err := p.persistRaw(entityType, entityID, result.Payload, result.AsOf, interp); err != nil {
		return fmt.Errorf("pipeline: persist %s %s: %w", entityType, entityID, err)
	}
	// Derived links live only on the migrated schema, so a store that has not
	// been cut over keeps the entity and interpretation rows written above and
	// skips the link rebuild (bead pg2-5l0x4.14: `run issue` hydrates issue
	// entities on the version 1 store).
	if err := p.store.RequireNewSchema(); errors.Is(err, store.ErrOldSchema) {
		return nil
	} else if err != nil {
		return fmt.Errorf("pipeline: links %s %s: %w", entityType, entityID, err)
	}
	// Rebuild the entity's derived links (runs whether or not any decider
	// subscribes). A removed or empty payload clears them.
	removed := result.RemovedState != "" || len(result.Payload) == 0
	if err := p.extractAndReplace(entityType, entityID, result.Payload, removed, p.clock.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("pipeline: links %s %s: %w", entityType, entityID, err)
	}
	return nil
}

// persistRaw is the payload-and-interpretation half of the write: it upserts
// the entity row (facts = payload; as_of = the payload's as-of, falling back
// to interp.AsOf; content hash computed as the PR path does) and the
// interpretation row. Version checks, hydrated_at/active bookkeeping and
// change-log append belong to the governing design's write entry point.
//
// HeadSHA reconciliation decision: the PR path stores store.Entity.HeadSHA
// from gather.Facts.HeadSHA (JSON key head_sha). The pr payload is that same
// Facts value marshaled, so persistRaw decodes the payload's top-level
// "head_sha" key and stores it, for any entity type (types without the key
// get ""). This keeps a pr row written here identical to the PR path's.
func (p *Pipeline) persistRaw(entityType, entityID string, payload json.RawMessage, asOf string, interp interpret.Interpretation) (store.Interpretation, error) {
	factsJSON, headSHA := factsAndHead(payload)

	if asOf == "" {
		asOf = interp.AsOf
	}
	if err := p.store.UpsertEntity(store.Entity{
		Repo:        p.repo(),
		EntityType:  entityType,
		EntityID:    entityID,
		Facts:       string(factsJSON),
		AsOf:        asOf,
		ContentHash: contentHash(factsJSON),
		HeadSHA:     headSHA,
	}); err != nil {
		return store.Interpretation{}, fmt.Errorf("upsert entity: %w", err)
	}

	row, err := p.interpretationRow(entityType, entityID, interp)
	if err != nil {
		return store.Interpretation{}, err
	}
	if err := p.store.UpsertInterpretation(row); err != nil {
		return store.Interpretation{}, fmt.Errorf("upsert interpretation: %w", err)
	}
	return row, nil
}

// factsAndHead normalizes a gather payload into the entity.facts JSON (an
// empty payload is "{}") and decodes its top-level head_sha key (non-object
// or malformed payloads carry none).
func factsAndHead(payload json.RawMessage) (factsJSON []byte, headSHA string) {
	factsJSON = []byte(payload)
	if len(factsJSON) == 0 {
		factsJSON = []byte("{}")
	}
	var head struct {
		HeadSHA string `json:"head_sha"`
	}
	_ = json.Unmarshal(factsJSON, &head)
	return factsJSON, head.HeadSHA
}

// interpretationRow builds the store row for one interpretation (the JSON
// columns marshaled), without writing it.
func (p *Pipeline) interpretationRow(entityType, entityID string, interp interpret.Interpretation) (store.Interpretation, error) {
	repo := p.repo()
	marshal := func(name string, v any) (string, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("marshal %s: %w", name, err)
		}
		return string(b), nil
	}
	enrichment, err := marshal("enrichment", interp.Enrichment)
	if err != nil {
		return store.Interpretation{}, err
	}
	urgency, err := marshal("urgency", interp.Urgency)
	if err != nil {
		return store.Interpretation{}, err
	}
	dispositions, err := marshal("dispositions", interp.Dispositions)
	if err != nil {
		return store.Interpretation{}, err
	}
	approvals, err := marshal("approvals", interp.Approvals)
	if err != nil {
		return store.Interpretation{}, err
	}
	matchReasons, err := marshal("match reasons", interp.MatchReasons)
	if err != nil {
		return store.Interpretation{}, err
	}

	row := store.Interpretation{
		Repo:           repo,
		EntityType:     entityType,
		EntityID:       entityID,
		Ownership:      interp.Ownership,
		Enrichment:     enrichment,
		Urgency:        urgency,
		Category:       interp.Category,
		Dispositions:   dispositions,
		Approvals:      approvals,
		GateState:      interp.GateState,
		MatchReasons:   matchReasons,
		Panel:          interp.Panel,
		ReadyToPromote: interp.ReadyToPromote,
		Degraded:       interp.Degraded != "",
		SyncError:      "",
		AsOf:           interp.AsOf,
	}
	return row, nil
}
