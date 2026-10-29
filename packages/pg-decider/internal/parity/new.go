package parity

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/action"
)

// hiddenValue is the value of the reserved `hidden` annotation of a hidden
// entity: {"value": true, "reason": text}.
const hiddenValue = `{"value":true,"reason":"parity fixture"}`

// NewResult is the raw plan pg-decider printed for each evaluated entity.
type NewResult struct {
	Plans map[string]action.PlanResult // entity id -> pg-decider plan --json output
	// Refused maps each legacy-seeded entity (a version-0, never-hydrated row)
	// to how the decider declined to plan it BEFORE the refresh hydrated it (see
	// classifyUnhydratedPlan). An entity that is not legacy-seeded has no entry.
	Refused map[string]string
}

// RunNew loads a scenario into a schema-2 store and reads back what the
// decider plans. In a hermetic sandbox it:
//
//  1. creates an empty store and cuts it over to schema 2: `pg-desk heartbeat`
//     (which stays after the sync stage is removed) opens the fresh store and
//     creates its schema-1 tables, then `pg-desk migrate --cutover` moves it to
//     schema 2. No `pg-desk run pr` is involved, so the seeding does not depend
//     on the sync stage;
//  2. for a legacy-seeded entity (Fixture.LegacySeeded) the store helper writes
//     the old-schema row BETWEEN those two steps, so the cutover leaves it at
//     version 0, never hydrated, exactly as it leaves every pre-existing row;
//  3. checks the decider REFUSES each legacy-seeded entity before any refresh;
//  4. hydrates every entity with `pg-desk pr refresh <id>`, hides the scenario's
//     hidden entities, then hydrates every fixture bead with
//     `pg-desk issue refresh <id>` (a bead's repo and pr_number metadata is what
//     links it to its PR);
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
	sb, cleanup, err := newSandbox(env, fx, "new")
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
	if _, err := sb.step(ctx, sb.deskExec, "heartbeat"); err != nil {
		return fail("create store", err)
	}
	if len(fx.LegacySeeded) > 0 {
		sqlite3, err := env.sqlite3Bin()
		if err != nil {
			return NewResult{}, err
		}
		for _, id := range fx.LegacySeeded {
			if err := sb.seedLegacyRow(ctx, sqlite3, id); err != nil {
				return fail("seed legacy row", err)
			}
		}
	}
	if _, err := sb.step(ctx, sb.deskExec, "migrate", "--cutover"); err != nil {
		return fail("cutover", err)
	}

	out := NewResult{Plans: map[string]action.PlanResult{}, Refused: map[string]string{}}
	for _, id := range fx.LegacySeeded {
		why, err := sb.legacyRefusal(ctx, env, id)
		if err != nil {
			return fail("legacy row", err)
		}
		out.Refused[id] = why
	}

	for _, id := range fx.Entities {
		if _, err := sb.step(ctx, sb.deskExec, "pr", "refresh", id); err != nil {
			return fail("hydrate pr", err)
		}
	}
	for _, id := range fx.Hidden {
		// The `hidden` annotation is the exact write `pg-desk hide` makes on a
		// schema-2 store (docs/behavior/pg-desk/annotate.md, reserved keys). The
		// verb itself is not used: at the pinned pg-desk it still reads the
		// old per-column annotation table and fails on a schema-2 store.
		if _, err := sb.step(ctx, sb.deskExec, "pr", "annotate", id, "--key", "hidden", "--value", hiddenValue); err != nil {
			return fail("hide", err)
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

// legacyRefusal proves the entity's row is the legacy shape (version 0, never
// hydrated) and that the decider declines to plan it (see
// classifyUnhydratedPlan), returning how it declined.
func (sb *sandbox) legacyRefusal(ctx context.Context, env Env, id string) (string, error) {
	sqlite3, err := env.sqlite3Bin()
	if err != nil {
		return "", err
	}
	version, hydratedAt, err := sb.readEntityRow(ctx, sqlite3, id)
	if err != nil {
		return "", err
	}
	if version != 0 || hydratedAt != "<null>" {
		return "", fmt.Errorf("%s is not a legacy row after the cutover: version %d, hydrated_at %s (want version 0, never hydrated)", id, version, hydratedAt)
	}
	res, err := sb.run(ctx, sb.deciderExec, "plan", "pr", id, "--json")
	if err != nil {
		return "", err
	}
	return classifyUnhydratedPlan(id, res.Code, res.Stdout, res.Stderr)
}

// classifyUnhydratedPlan says whether the decider declined to plan an entity
// whose row is unhydrated (version 0). Declining is either of
//
//   - a refusal: a non-zero exit (the fail-closed refusal of the composed
//     command is built on this), or
//   - an empty plan: exit 0 with no action at all, which is what the decider
//     itself does today (every rule reports "not matched" against facts that
//     were never observed).
//
// Any planned action is an error: it would be a write built from an
// unhydrated view, which is how an anchor gets minted twice. The returned
// string says which kind of refusal it was.
func classifyUnhydratedPlan(id string, code int, stdout, stderr string) (string, error) {
	if code != 0 {
		why, _, _ := strings.Cut(strings.TrimSpace(stderr+stdout), "\n")
		return fmt.Sprintf("refused with exit %d: %s", code, why), nil
	}
	var plan action.PlanResult
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		return "", fmt.Errorf("decode the plan of the unhydrated %s: %w", id, err)
	}
	if len(plan.Actions) != 0 {
		return "", fmt.Errorf("the decider planned %d action(s) from the never-hydrated legacy row of %s instead of declining", len(plan.Actions), id)
	}
	return "planned no action (exit 0, empty plan)", nil
}
