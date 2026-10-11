package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
)

func newFocusReplanCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "replan [--date YYYY-MM-DD] [--cap N] [--all] [--json]",
		Short: "Open a draft again over the locked plan: the plan as PLAN, every other candidate re-ranked below",
		Long: `Open a DRAFT again. The existing plan is shown as the PLAN block (its order and
tiers frozen, facts live) and every other candidate, new ones included, is
ranked afresh below it. A replan proposes nothing: a candidate joins the plan
only when the reply names it (+key) and the operator locks.

It writes only the single stored draft, replacing any stored draft, and only
when the addressed period is today and not closed; for any other --date it
prints and stores nothing. On a closed period it prints the plan and stores
nothing. On a period with no plan it prints what show prints (the first plan is
the degenerate replan). It never prompts and writes no run record.

Exit codes are show's: 0, 2 (coverage incomplete), 3, 1.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runFocusReplan(cmd) },
	}
	addFocusFlags(c)
	c.Flags().Int(focusFlagCap, 0, "Cap line of the new draft (moves the displayed line only; nothing is persisted)")
	c.Flags().Bool(focusFlagAll, false, "Print every candidate (default: the plan plus the next ten)")
	return c
}

// replanAttempts bounds the recompute loop when a lock keeps changing the
// plan state between compute and store.
const replanAttempts = 3

func runFocusReplan(cmd *cobra.Command) error {
	capFlag, capGiven, err := focusCapFlag(cmd)
	if err != nil {
		return err
	}
	all, _ := cmd.Flags().GetBool(focusFlagAll)
	env, err := openFocusEnv(cmd, false)
	if err != nil {
		return err
	}
	defer env.Close()
	g, err := gatherFocus(env)
	if err != nil {
		return err
	}

	var notices []string
	if !env.Today {
		notices = append(notices, fmt.Sprintf("NOTICE period %s is not today; no draft stored", env.Period.Key))
	}
	if g.hasDraft && isApplicableDraft(g.draft, env.Period) {
		if _, derr := focus.DecodeDraft([]byte(g.draft.BodyJSON)); derr != nil {
			notices = append(notices, "NOTICE stored draft unreadable; replaced")
		}
	}

	// A closed period has no draft to make: print the plan, store nothing.
	if g.closed {
		cap := g.periodCap()
		if capGiven {
			cap = capFlag
		}
		fresh := g.rank(cap)
		s := g.buildScreen(screenInput{
			verb: "replan", mode: focusModePlan, draftLine: planDraftLine(g), fresh: fresh,
			cap: cap, showAll: all, notices: notices,
		})
		return finishFocusScreen(cmd, env, g, s)
	}

	canStore := env.Today && !g.totalFailure()
	for attempt := 0; ; attempt++ {
		cap := focus.DefaultCap
		if g.hasPlan() {
			cap = g.periodCap()
		}
		if capGiven {
			cap = capFlag
		}
		fresh := g.rank(cap)
		doc := focus.NewReplanDraft(g.plan, fresh, g.cov.State, cap, g.firstSeen)
		stored := false
		if canStore {
			focusReplanBeforeStore(env.Store)
			fd, err := draftToStore(env.Period, doc, cap, env.Now)
			if err != nil {
				return focusTotalError("focus: encode draft: "+err.Error(), "report this as a pg-desk bug")
			}
			changed, err := env.Store.FocusDraftReplan(fd, g.hasPlan())
			if err != nil {
				return focusTotalError("focus: store draft: "+err.Error(), "check the pg-desk store, then re-run")
			}
			if changed && attempt+1 < replanAttempts {
				// A lock changed the plan state between the compute and the
				// store: recompute for the state the store now holds.
				if err := g.reloadPlan(); err != nil {
					return err
				}
				continue
			}
			stored = !changed
		}
		line := "DRAFT (stored; reply to lock it in)"
		if !stored {
			line = "DRAFT (computed, not stored)"
		}
		s := g.buildScreen(screenInput{
			verb: "replan", mode: focusModeDraft, stored: stored, draftLine: line, fresh: fresh,
			cap: cap, doc: &doc, madeAt: env.Now, showAll: all, notices: notices,
		})
		return finishFocusScreen(cmd, env, g, s)
	}
}
