package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// InteractiveReader is the one summary-service method the confirmer uses: an
// origin read of each entity's summary that goes through the governor as class 1
// and is single-flight. *summary.Service satisfies it.
type InteractiveReader interface {
	InteractiveFresh(ctx context.Context, ids []string) []summary.InteractiveResult
}

// StoreReader reads what the confirming read stored: an entity row and a group.
// *store.Store satisfies it.
type StoreReader interface {
	GetEntity(ctx context.Context, kind, id string) (store.EntityRow, bool, error)
	GetGroup(ctx context.Context, kind, id, group string) (store.GroupRow, bool, error)
}

var (
	_ InteractiveReader = (*summary.Service)(nil)
	_ StoreReader       = (*store.Store)(nil)
)

type confirmer struct {
	svc InteractiveReader
	st  StoreReader
}

// NewConfirmer returns the Confirmer over the summary service: ONE interactive
// read of the entity's summary, answered with its stored State (OPEN, MERGED or
// CLOSED, GitHub's spelling), or Gone when the read found the PR gone (the
// entity is tombstoned). Any other failure of the read, a governor refusal
// included, is returned as an error (wrapped, so errors.As still finds the
// refusal).
func NewConfirmer(svc InteractiveReader, st StoreReader) Confirmer {
	return confirmer{svc: svc, st: st}
}

// Confirm implements Confirmer. ref is only for the error text: the read is by
// node id.
func (c confirmer) Confirm(ctx context.Context, node, ref string) (Confirmation, error) {
	res := c.svc.InteractiveFresh(ctx, []string{node})
	if len(res) != 1 {
		return Confirmation{}, fmt.Errorf("query: confirm %s: the summary read answered %d results, want 1", ref, len(res))
	}
	switch r := res[0]; {
	case r.Err != nil:
		return Confirmation{}, fmt.Errorf("query: confirm %s: %w", ref, r.Err)
	case r.Pending:
		return Confirmation{}, fmt.Errorf("query: confirm %s: the summary read was still running at the deadline", ref)
	}
	ent, ok, err := c.st.GetEntity(ctx, entity.KindPR, node)
	if err != nil {
		return Confirmation{}, err
	}
	if ok && !ent.TombstonedAt.IsZero() {
		return Confirmation{Gone: true}, nil
	}
	g, ok, err := c.st.GetGroup(ctx, entity.KindPR, node, summary.GroupName)
	if err != nil {
		return Confirmation{}, err
	}
	if !ok || len(g.Content) == 0 {
		return Confirmation{}, errors.New("query: confirm " + ref + ": the read stored no summary")
	}
	var s summary.Summary
	if err := json.Unmarshal(g.Content, &s); err != nil {
		return Confirmation{}, fmt.Errorf("query: confirm %s: decode the stored summary: %w", ref, err)
	}
	return Confirmation{State: s.State}, nil
}
