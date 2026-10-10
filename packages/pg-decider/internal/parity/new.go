package parity

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/pg-decider/internal/action"
)

// NewResult is the raw plan pg-decider printed for each evaluated entity.
type NewResult struct{ Plans map[string]action.PlanResult } // entity id -> pg-decider plan --json output

// RunNew loads the SAME scenario through the NEW code path and reads back what
// the decider plans. In a hermetic sandbox it:
//
//  1. builds an old-schema store the way the real cutover finds one, by running
//     `pg-desk run pr <id> --change added` per entity under sync.mode = "off"
//     (the new-schema verbs refuse an old-schema store, which is why the new
//     store is built by migration and not opened fresh);
//  2. hides the scenario's hidden entities, as the old side does;
//  3. migrates the store with `pg-desk migrate --cutover`;
//  4. hydrates every entity with `pg-desk pr refresh <id>`, then every fixture
//     bead with `pg-desk issue refresh <id>` (a bead's repo and pr_number
//     metadata is what links it to its PR);
//  5. writes the scenario's focus_selected annotations, if any;
//  6. runs `pg-decider plan pr <id> --json` and keeps the raw PlanResult.
//
// Nothing is applied. A pg-desk or pg-decider refusal wraps ErrUnsupported.
func RunNew(ctx context.Context, env Env, sc Scenario) (NewResult, error) {
	if err := env.validate(); err != nil {
		return NewResult{}, err
	}
	fx, err := LoadFixture(sc)
	if err != nil {
		return NewResult{}, err
	}
	sb, cleanup, err := newSandbox(env, fx, "new", "off")
	if err != nil {
		return NewResult{}, err
	}
	defer cleanup()
	if err := sb.verifyStore(ctx); err != nil {
		return NewResult{}, err
	}

	fail := func(step string, err error) (NewResult, error) {
		return NewResult{}, fmt.Errorf("scenario %s: new %s: %w", fx.Name, step, err)
	}
	for _, id := range fx.Entities {
		if _, err := sb.step(ctx, sb.deskExec, "run", "pr", id, "--change", "added"); err != nil {
			return fail("seed old-schema store", err)
		}
	}
	for _, id := range fx.Hidden {
		if _, err := sb.step(ctx, sb.deskExec, "hide", id, "parity fixture"); err != nil {
			return fail("hide", err)
		}
	}
	if _, err := sb.step(ctx, sb.deskExec, "migrate", "--cutover"); err != nil {
		return fail("cutover", err)
	}
	for _, id := range fx.Entities {
		if _, err := sb.step(ctx, sb.deskExec, "pr", "refresh", id); err != nil {
			return fail("hydrate pr", err)
		}
	}
	for _, b := range fx.Beads {
		if _, err := sb.step(ctx, sb.deskExec, "issue", "refresh", b.ID); err != nil {
			return fail("hydrate issue", err)
		}
	}
	for _, id := range fx.Entities {
		period, ok := fx.FocusSelected[id]
		if !ok {
			continue
		}
		if _, err := sb.step(ctx, sb.deskExec, "pr", "annotate", id, "--key", "focus_selected", "--value", period); err != nil {
			return fail("select for focus", err)
		}
	}
	if err := sb.assertConnectorUsed(fx.Entities); err != nil {
		return NewResult{}, err
	}

	out := NewResult{Plans: map[string]action.PlanResult{}}
	for _, id := range fx.Entities {
		res, err := sb.step(ctx, sb.deciderExec, "plan", "pr", id, "--json")
		if err != nil {
			return fail("plan", err)
		}
		var plan action.PlanResult
		if err := json.Unmarshal([]byte(res.Stdout), &plan); err != nil {
			return fail("decode plan of "+id, err)
		}
		out.Plans[id] = plan
	}
	return out, nil
}
