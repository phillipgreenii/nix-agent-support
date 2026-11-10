package pipeline

import (
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

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

// interpretEntity runs the registry interpreter for entityType over result and
// builds the store row (the JSON columns marshaled), without writing it. It is
// the only caller of the registry interpreters and the only builder of an
// interpretation row; every flow that writes an interpretation goes through it.
// It returns the row and the interpreter's own result (callers read Degraded
// and AsOf from it).
func interpretEntity(repo string, cfg *config.Config, clock interpret.Clock, entityType, entityID string, result gather.GatherResult) (store.Interpretation, interpret.Interpretation, error) {
	ie, ok := interpret.EntityInterpreters()[entityType]
	if !ok {
		return store.Interpretation{}, interpret.Interpretation{}, fmt.Errorf("pipeline: no interpret adapter registered for entity type %q", entityType)
	}
	interp, err := ie(result, clock, cfg)
	if err != nil {
		return store.Interpretation{}, interpret.Interpretation{}, err
	}
	row, err := interpretationRow(repo, entityType, entityID, interp)
	if err != nil {
		return store.Interpretation{}, interpret.Interpretation{}, err
	}
	return row, interp, nil
}

// interpretationRow is interpretEntity's row builder.
func interpretationRow(repo, entityType, entityID string, interp interpret.Interpretation) (store.Interpretation, error) {
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
