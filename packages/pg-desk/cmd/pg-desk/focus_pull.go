package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// `pg-desk focus pull` (spec section 7.3): strictly additive and outside the
// draft/lock cycle. It computes the rank in its own run, ignores and leaves
// alone the stored draft (its marks survive), and performs no removal, no
// re-cap and no re-sort. It IS a lock of what it adds: the rows take
// rank_position by the lock-sequence rule (maximum plus one, in rank order)
// with the tier of the run, the period row is get-or-created (not under
// --dry-run) and each pulled entity gets the focus_selected annotation, the
// same writes and the same per-entity lock as select (focus.ApplyAnnotations).
//
// A positional argument is ALWAYS a key; a count is only --top K; --top
// together with a key is a usage error; a bare invocation previews (the plan
// and what is left to pull, nothing written). Pull is uncapped.
//
// Check order (the exit code a run takes is the first failing check): usage
// and flag errors (exit 1), period closed (6), a pull into a future period or
// into a period that has no rows while another period holds rows (1; the same
// checks run again inside the pull's transaction), the request (3 when no
// named key has an entity row at all), then the writes (3, 2 partial, 0).
//
// Output: the CHANGES block of select (focus.RenderChanges) over the rows
// selected; each routine skip is one stderr line `skip <key>: <reason>`;
// --json prints {contract, verb, run_id, period, selected[], skipped[],
// annotations[]}. Nothing is minted: the annotation is what the decider reads.
//
// Telemetry (D24): the verb emits no OpenTelemetry and no Prometheus signal.
// It writes one focus_run row per non-dry run and prints the same record as
// one structured stderr line (focus_run.go); it logs nothing else.

// Flag name of pull's count.
const focusFlagTop = "top"

// focusPullBeforeTx is called between pull's resolution and its transaction,
// so a test can commit a lock in the gap (the in-transaction checks).
var focusPullBeforeTx = func(*store.Store) {}

func init() {
	focusGroup().AddCommand(newFocusPullCmd())
}

func newFocusPullCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "pull [--date YYYY-MM-DD] [--top K | key...] [--dry-run] [--json]",
		Short: "Add candidates to the plan without replacing it: --top K or named keys",
		Long: `Add candidates to the plan of the addressed period. Strictly additive: pull
removes nothing, re-caps nothing and re-sorts nothing, it ignores the stored
draft and leaves it (and its marks) alone, and it is uncapped.

A positional argument is ALWAYS a key (a PR number, a PR URL, OWNER/REPO#N or
a tracker key), so "pull 3" means the PR 3, never "three more". A count is only
--top K, which takes the K best-ranked candidates not yet selected, by the
CURRENT rank. --top together with a key is a usage error. A bare invocation
previews: it recomputes, prints the plan and what is left to pull, and writes
nothing. --dry-run reports exactly what would be selected and annotated and
writes nothing, not even the period row.

A named key that is already selected, or is no longer (or never was) a
candidate, is a routine skip reported on stderr and the run still exits 0.
Each added row is a plan row like any other and persists across later select
runs unless struck. Nothing is minted here: pull writes selection state and
the focus_selected annotation the decider reacts to.

A pull never replaces a plan: into a period that has no rows while another
period holds rows it is exit 1 (lock today's plan first: run show, then
select --apply), and so is a pull into a future period.

Exit codes: 0 done (including nothing left to pull and routine skips), 1 usage
or the no-plan and future-period cases, 2 rows committed but an annotation
write failed (run select --repair), 3 a store write failed or no named key has
an entity row, 6 period closed.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runFocusPull(cmd, args) },
	}
	addFocusFlags(c)
	c.Flags().Int(focusFlagTop, 0, "Pull the top K candidates not yet selected, by the current rank")
	c.Flags().Bool(focusFlagDryRun, false, "Report what would be selected and annotated; write nothing")
	return c
}

// pullRun is one pull run.
type pullRun struct {
	cmd     *cobra.Command
	env     *focusEnv
	run     *focusRun
	writing bool
	done    bool
}

// finish ends the run with err's exit code when no row-writing path did: a
// writing run still writes its focus_run row (exit 1, 3 and 6 are counted by
// the run statistics), then prints the stderr line.
func (p *pullRun) finish(err error) error {
	if p.done {
		return err
	}
	p.done = true
	if p.writing && !p.run.Inserted() {
		_ = p.run.Start(p.env.Store)
	}
	_ = p.run.Finish(focusExitCode(err))
	return err
}

func runFocusPull(cmd *cobra.Command, args []string) error {
	topGiven := cmd.Flags().Changed(focusFlagTop)
	top, _ := cmd.Flags().GetInt(focusFlagTop)
	dry, _ := cmd.Flags().GetBool(focusFlagDryRun)
	if topGiven && len(args) > 0 {
		return focusUsageError("--top cannot be used together with a key (a count is only --top K, a positional argument is always a key)",
			"name keys, or use --top K alone")
	}
	if topGiven && top <= 0 {
		return focusUsageError(fmt.Sprintf("--top %d must be positive", top), "pass --top K with K of 1 or more")
	}
	preview := !topGiven && len(args) == 0
	writing := !preview && !dry

	env, err := openFocusEnv(cmd, writing)
	if err != nil {
		return err
	}
	defer env.Close()
	p := &pullRun{cmd: cmd, env: env, writing: writing}
	p.run = newFocusRun(cmd.ErrOrStderr(), focusVerbPull, env.Period, env.Actor, !writing, env.Now)
	return p.finish(p.once(args, top, preview))
}

// once is the whole run after the environment is open; its error is the
// run's exit.
func (p *pullRun) once(args []string, top int, preview bool) error {
	cmd, env := p.cmd, p.env
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	var keys []focus.Key
	for _, raw := range args {
		k, err := focus.ParseKey(raw, env.Cfg)
		if err != nil {
			return mapFocusErr(err, env.Period.Key)
		}
		keys = append(keys, k)
	}
	g, err := gatherFocus(env)
	if err != nil {
		return err
	}
	if g.closed {
		return focusPeriodClosedError(env.Period.Key)
	}
	deps := focusLockDeps(cmd, env)
	deps.Hydrate = nil // pull never hydrates: a named key must already have an entity row
	period := focus.Period{Type: env.Period.Type, Key: env.Period.Key}
	if err := focus.PullPreflight(deps, period); err != nil {
		return mapFocusErr(err, env.Period.Key)
	}
	if err := p.printNotices(g); err != nil {
		return err
	}

	cap := g.periodCap()
	rk := g.rank(cap)
	if preview {
		return p.previewScreen(g, rk, cap)
	}

	seen := map[focus.Key]bool{}
	var liveKeys []focus.Key
	add := func(k focus.Key) {
		if !seen[k] {
			seen[k] = true
			liveKeys = append(liveKeys, k)
		}
	}
	for _, r := range rk.Rows {
		add(r.Key)
	}
	for _, pr := range g.plan {
		add(pr.Key)
	}
	for _, k := range keys {
		add(k)
	}
	live := focus.BuildLive(g.in, liveKeys, rk, focus.LiveOptions{Watched: g.watched, SelectedAt: g.selectedAt()})
	plan := focus.ResolvePull(rk, g.set, live.Facts, g.plan, g.in.Entities, focus.PullRequest{Top: top, Keys: keys})
	if plan.AllMissing {
		return focusTotalError(
			"focus pull: none of the named keys has an entity row in the store ("+strings.Join(keyTexts(keys), ", ")+")",
			"check the keys, or hydrate them with pg-desk pr refresh or pg-desk issue refresh, then re-run",
		)
	}

	// The effects are resolved BEFORE the write, as select does.
	fs := focus.FinalSet{Insert: plan.Select}
	pv, perr := focus.BuildPreview(ctx, deps, focus.LockInput{Period: period, Actor: env.Actor, Final: fs}, fs)
	if perr != nil {
		return mapFocusErr(perr, env.Period.Key)
	}
	counts := pullCounts(g, rk, cap, len(plan.Select))

	if !p.writing {
		p.run.SetCounts(counts)
		if err := p.print(pv, plan.Select, plan.Skipped, nil); err != nil {
			return err
		}
		p.done = true
		return p.run.Finish(0)
	}

	focusPullBeforeTx(env.Store)
	res, lerr := focus.Pull(ctx, deps, focus.PullInput{Period: period, Actor: env.Actor, Select: plan.Select, RunID: p.run.RunID()})
	if lerr != nil {
		return mapFocusErr(lerr, env.Period.Key)
	}
	p.run.Adopt(env.Store)
	for _, a := range res.Annotations {
		p.run.Annotation(keyText(a.Key), a.Outcome, a.Seq)
	}
	counts.Selected = len(res.Inserted)
	p.run.SetCounts(counts)
	skipped := append([]focus.Skip(nil), plan.Skipped...)
	for _, k := range res.Raced {
		skipped = append(skipped, focus.Skip{Key: k, Reason: focus.SkipAlreadySelected})
	}
	inserted := make([]focus.Insertion, len(res.Inserted))
	copy(inserted, res.Inserted)
	if err := p.print(pv, inserted, skipped, res.Annotations); err != nil {
		return err
	}
	p.done = true
	var runErr error
	if res.ExitCode == focus.ExitPartial {
		failed := 0
		for _, a := range res.Annotations {
			if a.Outcome != focus.AnnotationOK {
				failed++
			}
		}
		runErr = focusPartialError(
			fmt.Sprintf("focus pull: the rows are added but %d focus_selected annotation write(s) failed", failed),
			"run pg-desk focus select --repair",
		)
	}
	if ferr := p.run.Finish(focusExitCode(runErr)); ferr != nil && runErr == nil {
		return focusTotalError("focus pull: finalize the run record: "+ferr.Error(), "run pg-desk focus select --repair")
	}
	return runErr
}

// printNotices prints the candidate-source notices on stderr, so a degraded
// or failed listing cannot hide behind a pull that looks complete.
func (p *pullRun) printNotices(g *focusGather) error {
	for _, n := range g.listingNotices() {
		if _, err := fmt.Fprintln(p.cmd.ErrOrStderr(), n); err != nil {
			return err
		}
	}
	return nil
}

// pullCounts are the run's counting members: the cap the rank was computed
// under, how many rows the rank placed and by tier, the rank-input counters
// and the coverage state.
func pullCounts(g *focusGather, rk focus.Ranking, cap, selected int) focusCounts {
	c := focusCounts{
		Cap: cap, Ranked: len(rk.Rows), Selected: selected,
		RankInputs: focusRankInputs(rk.Inputs),
		Coverage:   focusCoverageSummary{Incomplete: g.cov.State.Incomplete, Reasons: g.cov.State.Reasons},
	}
	for _, r := range rk.Rows {
		switch r.Tier {
		case focus.TierOverdue:
			c.Tiers.Overdue++
		case focus.TierStarted:
			c.Tiers.Started++
		case focus.TierNotStarted:
			c.Tiers.NotStarted++
		}
	}
	return c
}

// previewScreen is a bare invocation: the plan and what is left to pull,
// computed and printed, nothing written.
func (p *pullRun) previewScreen(g *focusGather, rk focus.Ranking, cap int) error {
	s := g.buildScreen(screenInput{
		verb: "pull", mode: focusModePlan, draftLine: planDraftLine(g), fresh: rk, cap: cap,
	})
	p.run.SetCounts(pullCounts(g, rk, cap, 0))
	w := p.cmd.OutOrStdout()
	if p.env.JSON {
		if err := printFocusJSON(w, s.Verb, s.payload(), ""); err != nil {
			return err
		}
	} else {
		s.writeText(w)
	}
	p.done = true
	return p.run.Finish(0)
}

// pullSelectedJSON is one member of the --json `selected` array.
type pullSelectedJSON struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Effect  string `json:"effect"`
	Because string `json:"because"`
}

// print writes what the run did (or would do): the CHANGES block over the
// rows selected in text, the skips on stderr, and in --json the one document.
func (p *pullRun) print(pv focus.PreviewResult, selected []focus.Insertion, skipped []focus.Skip, anns []focus.AnnotationOutcome) error {
	want := map[focus.Key]bool{}
	for _, s := range selected {
		want[s.Key] = true
	}
	var rows []focus.ChangeRow
	for _, c := range pv.Changes {
		if c.Direction == focus.DirectionSelect && want[c.Key] {
			rows = append(rows, c)
		}
	}
	pv.Changes = rows
	pv.NeverClosed = nil

	if err := writePullSkips(p.cmd.ErrOrStderr(), skipped); err != nil {
		return err
	}
	w := p.cmd.OutOrStdout()
	if p.env.JSON {
		sel := make([]pullSelectedJSON, 0, len(rows))
		for _, c := range rows {
			sel = append(sel, pullSelectedJSON{Key: keyText(c.Key), Title: c.Title, Effect: c.Effect, Because: c.Because})
		}
		skips := make([]map[string]any, 0, len(skipped))
		for _, s := range skipped {
			skips = append(skips, map[string]any{"key": keyText(s.Key), "reason": s.Reason})
		}
		outAnns := make([]map[string]any, 0, len(anns))
		for _, a := range anns {
			outAnns = append(outAnns, map[string]any{"key": keyText(a.Key), "outcome": a.Outcome, "seq": a.Seq})
		}
		runID := ""
		if p.writing {
			runID = p.run.RunID()
		}
		return printFocusJSON(w, "pull", map[string]any{
			"run_id": runID, "period": p.env.Period.Key,
			"selected": sel, "skipped": skips, "annotations": outAnns,
		}, runID)
	}
	_, err := io.WriteString(w, focus.RenderChanges(pv))
	return err
}

// writePullSkips prints each routine skip as one line, `skip <key>: <reason>`.
func writePullSkips(w io.Writer, skipped []focus.Skip) error {
	for _, s := range skipped {
		if _, err := fmt.Fprintf(w, "skip %s: %s\n", keyText(s.Key), s.Reason); err != nil {
			return err
		}
	}
	return nil
}
