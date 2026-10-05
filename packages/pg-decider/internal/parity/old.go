package parity

import (
	"context"
	"encoding/json"
	"fmt"
)

// PlannedRow is one write the old sync stage planned. Old kinds are "anchor",
// "feedback-cycle" and "review-request".
type PlannedRow struct{ Kind, ContentHash string }

// OldResult is what the old sync stage planned for each evaluated entity.
type OldResult struct{ Rows map[string][]PlannedRow } // entity id -> rows

// RunOld loads the scenario through the OLD code path and reads back what sync
// planned. In a hermetic sandbox (fresh state directory, config, PATH with only
// the fake pg-connector) it runs, per entity,
//
//	pg-desk run pr <id> --change added      gather, interpret, store, sync
//
// under sync.mode = "plan", which makes no pg-connector issue write: each
// planned write becomes a ledger row with no bead id. Entities in the
// scenario's hidden list are then hidden with `pg-desk hide`, the same
// pre-cutover step the new side takes. The rows are read back with
// `pg-desk show <id> --json` (member planned_sync_rows), which lists only the
// ledger rows that still lack a bead id, so a write that merely adopts an
// existing bead is not a planned row.
//
// A pg-desk refusal wraps ErrUnsupported.
func RunOld(ctx context.Context, env Env, sc Scenario) (OldResult, error) {
	if err := env.validate(); err != nil {
		return OldResult{}, err
	}
	fx, err := LoadFixture(sc)
	if err != nil {
		return OldResult{}, err
	}
	sb, cleanup, err := newSandbox(env, fx, "old", "plan")
	if err != nil {
		return OldResult{}, err
	}
	defer cleanup()
	if err := sb.verifyStore(ctx); err != nil {
		return OldResult{}, err
	}

	for _, id := range fx.Entities {
		if _, err := sb.step(ctx, sb.deskExec, "run", "pr", id, "--change", "added"); err != nil {
			return OldResult{}, fmt.Errorf("scenario %s: old run: %w", fx.Name, err)
		}
	}
	if err := sb.assertConnectorUsed(fx.Entities); err != nil {
		return OldResult{}, err
	}
	for _, id := range fx.Hidden {
		if _, err := sb.step(ctx, sb.deskExec, "hide", id, "parity fixture"); err != nil {
			return OldResult{}, fmt.Errorf("scenario %s: old hide: %w", fx.Name, err)
		}
	}

	out := OldResult{Rows: map[string][]PlannedRow{}}
	for _, id := range fx.Entities {
		res, err := sb.step(ctx, sb.deskExec, "show", id, "--json")
		if err != nil {
			return OldResult{}, fmt.Errorf("scenario %s: old show: %w", fx.Name, err)
		}
		rows, err := decodePlannedRows(res.Stdout)
		if err != nil {
			return OldResult{}, fmt.Errorf("scenario %s: old show %s: %w", fx.Name, id, err)
		}
		out.Rows[id] = rows
	}
	return out, nil
}

// decodePlannedRows reads planned_sync_rows from `pg-desk show --json`; an
// absent member is no rows, never nil.
func decodePlannedRows(stdout string) ([]PlannedRow, error) {
	var payload struct {
		Planned []struct {
			Kind        string `json:"kind"`
			ContentHash string `json:"content_hash"`
		} `json:"planned_sync_rows"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		return nil, fmt.Errorf("decode show payload: %w", err)
	}
	rows := make([]PlannedRow, 0, len(payload.Planned))
	for _, r := range payload.Planned {
		rows = append(rows, PlannedRow{Kind: r.Kind, ContentHash: r.ContentHash})
	}
	return rows, nil
}
