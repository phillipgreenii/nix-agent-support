package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// `pg-desk focus show` and `pg-desk focus replan` (spec sections 7.1 and
// 7.6). show has three modes: an applicable stored draft is re-rendered (the
// draft's order, tiers, deciding keys and cap line; every fact live); with no
// applicable draft and no plan it computes ONE draft and stores it only when
// every store condition holds; with a locked plan and no applicable draft it
// prints the PLAN and stores nothing. replan prints the same layout over the
// existing plan and replaces the stored draft only for an open today period.
//
// Telemetry (D24): the focus verbs emit no OpenTelemetry and no Prometheus
// signal and write no focus_run row; show writes the single focus_draft row in
// the one case above and replan replaces it, and neither logs. The rank is a
// compute-only function of stored facts: no LLM, no network and no tracker
// call at show time.

// Flag names of show and replan.
const (
	focusFlagCap     = "cap"
	focusFlagAll     = "all"
	focusFlagNoStore = "no-store"
)

// focusShowBeforeStore is called between show's compute and its store, so a
// test can commit a lock in the gap (the lock race of spec section 10.1).
var focusShowBeforeStore = func(*store.Store) {}

// focusReplanBeforeStore is the same seam for replan.
var focusReplanBeforeStore = func(*store.Store) {}

func init() {
	focusGroup().AddCommand(newFocusShowCmd(), newFocusReplanCmd())
}

func newFocusShowCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "show [--date YYYY-MM-DD] [--cap N] [--all] [--no-store] [--json]",
		Short: "Show the plan or draft of a period, with live facts and the coverage header",
		Long: `Show the addressed period (--date, else today in focus.time_zone).

An applicable stored draft is re-rendered: its order, tiers, deciding keys and
cap line are the draft's, every fact is read live, and a drift notice and a
"new since draft" block say what changed. With no applicable draft and no plan
it computes ONE draft and stores it (only for today, only for an open period
without a plan, and never with --no-store); reply to lock it in with
"select". With a locked plan and no applicable draft it prints the plan and
stores nothing. --no-store computes and renders but never stores and never
re-renders a stored draft.

Exit codes: 0 coverage complete, 2 coverage incomplete (a listing degraded or
failed, or a hydration backlog above focus.coverage_backlog_max), 3 no
candidates computable, 1 usage error or an unmigrated store.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runFocusShow(cmd) },
	}
	addFocusFlags(c)
	c.Flags().Int(focusFlagCap, 0, "Cap of a newly computed draft (a usage error with an applicable stored draft)")
	c.Flags().Bool(focusFlagAll, false, "Print every candidate (default: the plan plus the next ten)")
	c.Flags().Bool(focusFlagNoStore, false, "Compute and render; never store and never re-render a stored draft")
	return c
}

// focusCapFlag reads --cap: (0, false) when absent, a usage error when not
// positive.
func focusCapFlag(cmd *cobra.Command) (int, bool, error) {
	if !cmd.Flags().Changed(focusFlagCap) {
		return 0, false, nil
	}
	n, _ := cmd.Flags().GetInt(focusFlagCap)
	if n <= 0 {
		return 0, false, focusUsageError(fmt.Sprintf("--cap %d must be positive", n), "pass --cap N with N of 1 or more")
	}
	return n, true, nil
}

func draftToStore(period focusPeriod, doc focus.DraftDoc, cap int, madeAt time.Time) (store.FocusDraft, error) {
	body, err := focus.EncodeDraft(doc)
	if err != nil {
		return store.FocusDraft{}, err
	}
	return store.FocusDraft{
		PeriodType: period.Type, PeriodKey: period.Key, Cap: cap,
		MadeAt: madeAt.UTC().Format(time.RFC3339), BodyJSON: string(body),
	}, nil
}

func runFocusShow(cmd *cobra.Command) error {
	capFlag, capGiven, err := focusCapFlag(cmd)
	if err != nil {
		return err
	}
	all, _ := cmd.Flags().GetBool(focusFlagAll)
	noStoreFlag, _ := cmd.Flags().GetBool(focusFlagNoStore)
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
	noStore := noStoreFlag
	if !env.Today {
		noStore = true
		notices = append(notices, fmt.Sprintf("NOTICE period %s is not today; no draft stored", env.Period.Key))
	}

	// Classify the stored draft against the addressed period.
	var doc focus.DraftDoc
	applicable := g.hasDraft && isApplicableDraft(g.draft, env.Period)
	readable := false
	if applicable {
		d, derr := focus.DecodeDraft([]byte(g.draft.BodyJSON))
		readable = derr == nil
		doc = d
	}
	later := g.hasDraft && !applicable && g.draft.PeriodType == env.Period.Type && g.draft.PeriodKey > env.Period.Key
	if later {
		notices = append(notices, fmt.Sprintf("NOTICE stored draft is for %s; pass --date %s or run replan", g.draft.PeriodKey, g.draft.PeriodKey))
	}
	if noStoreFlag && applicable {
		notices = append(notices, "NOTICE stored draft ignored (--no-store)")
	}

	// Mode 1: re-render the applicable stored draft.
	if readable && !noStoreFlag {
		if capGiven {
			return focusUsageError("--cap cannot be used with an applicable stored draft (its cap is the draft's)",
				"drop --cap, or run replan --cap N to recompute")
		}
		madeAt, _ := time.Parse(time.RFC3339, g.draft.MadeAt)
		fresh := g.rank(g.draft.Cap)
		line := fmt.Sprintf("DRAFT (stored; reply to lock it in), draft made %s ago", ageText(maxDuration(env.Now.Sub(madeAt), 0)))
		s := g.buildScreen(screenInput{
			verb: "show", mode: focusModeDraft, stored: true, draftLine: line, fresh: fresh,
			cap: g.draft.Cap, doc: &doc, madeAt: madeAt, reRender: true, showAll: all, notices: notices,
		})
		return finishFocusScreen(cmd, env, g, s)
	}
	if applicable && !readable && !noStoreFlag {
		// An unreadable draft is treated as absent, not as an error.
		notices = append(notices, "NOTICE stored draft unreadable; ignored")
	}

	// Mode 3: a plan exists and no applicable draft is used.
	if g.hasPlan() {
		cap := g.periodCap()
		if capGiven {
			cap = capFlag
		}
		fresh := g.rank(cap)
		s := g.buildScreen(screenInput{
			verb: "show", mode: focusModePlan, draftLine: planDraftLine(g), fresh: fresh,
			cap: cap, showAll: all, notices: notices,
		})
		return finishFocusScreen(cmd, env, g, s)
	}

	// Mode 2: compute ONE draft; store it only when every condition holds.
	cap := focus.DefaultCap
	if capGiven {
		cap = capFlag
	}
	fresh := g.rank(cap)
	newDoc := focus.NewInitialDraft(fresh, cap, g.cov.State)
	stale := g.hasDraft && isStaleDraft(g.draft, env.Period.Key, env.PeriodClosedFunc())
	free := !g.hasDraft || stale || (applicable && !readable)
	canStore := !noStore && !g.closed && free && !g.totalFailure()
	stored := false
	if canStore {
		focusShowBeforeStore(env.Store)
		fd, err := draftToStore(env.Period, newDoc, cap, env.Now)
		if err != nil {
			return focusTotalError("focus: encode draft: "+err.Error(), "report this as a pg-desk bug")
		}
		if applicable && !readable {
			planNow, err := env.Store.FocusDraftReplace(fd)
			if err != nil {
				return focusTotalError("focus: store draft: "+err.Error(), "check the pg-desk store, then re-run")
			}
			stored = !planNow
			if stored {
				notices = replaceNotice(notices, "NOTICE stored draft unreadable; ignored", "NOTICE stored draft unreadable; replaced")
			}
		} else {
			stored, err = env.Store.FocusDraftStore(fd, env.Period.Key)
			if err != nil {
				return focusTotalError("focus: store draft: "+err.Error(), "check the pg-desk store, then re-run")
			}
		}
		if !stored {
			// A plan may have appeared between the compute and the insert.
			if err := g.reloadPlan(); err != nil {
				return err
			}
			if g.hasPlan() {
				pcap := g.periodCap()
				pfresh := g.rank(pcap)
				s := g.buildScreen(screenInput{
					verb: "show", mode: focusModePlan, draftLine: planDraftLine(g), fresh: pfresh,
					cap: pcap, showAll: all, notices: notices,
				})
				return finishFocusScreen(cmd, env, g, s)
			}
		}
	}
	line := "DRAFT (stored; reply to lock it in)"
	if !stored {
		line = "DRAFT (computed, not stored)"
	}
	s := g.buildScreen(screenInput{
		verb: "show", mode: focusModeDraft, stored: stored, draftLine: line, fresh: fresh,
		cap: cap, doc: &newDoc, madeAt: env.Now, showAll: all, notices: notices,
	})
	return finishFocusScreen(cmd, env, g, s)
}

func replaceNotice(ns []string, old, repl string) []string {
	for i, n := range ns {
		if n == old {
			ns[i] = repl
		}
	}
	return ns
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// planDraftLine is the header of plan mode: the plan and when it was locked.
func planDraftLine(g *focusGather) string {
	latest := ""
	for _, p := range g.plan {
		if p.SelectedAt > latest {
			latest = p.SelectedAt
		}
	}
	return fmt.Sprintf("PLAN (locked %s)", latest)
}

// finishFocusScreen prints the screen (text or the --json document) and
// returns the coverage exit: 0, 2 or 3.
func finishFocusScreen(cmd *cobra.Command, env *focusEnv, g *focusGather, s *focusScreen) error {
	if env.JSON {
		if err := printFocusJSON(cmd.OutOrStdout(), s.Verb, s.payload(), ""); err != nil {
			return err
		}
	} else {
		s.writeText(cmd.OutOrStdout())
	}
	return g.coverageResult()
}
