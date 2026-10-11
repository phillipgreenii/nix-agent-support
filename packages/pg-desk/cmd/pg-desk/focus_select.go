package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// `pg-desk focus select` (spec section 7.2): the LOCK. It obtains the draft
// (the applicable stored one, else one computed in this process with the
// fresh_order warning), parses the reply on stdin, computes the final set
// (focus.ComputeFinalSet), runs hand-add hydration as a separate reported
// pre-step, and for --apply reconciles focus_selection in ONE store
// transaction (focus.Lock) before it writes the focus_selected annotations,
// completes the run record's per-entity {outcome, seq} and finalizes
// exit_code last. Without --apply, or with --dry-run, it previews and writes
// nothing. --repair re-writes missing or mismatched annotations from the
// table and reads no reply.
//
// Check order (the exit code a run takes is the first failing check): usage
// and flag errors (exit 1), period closed (6), reply (1), the first-lock rule
// (1), hand-add hydration (1), then the writes (3, 2 partial, 0).
//
// Output (spec section 7.2 step 7): a preview prints the draft and then the
// CHANGES block (focus.RenderChanges) and nothing else; --dry-run and --apply
// add the echo table (focus.RenderEcho), which names the resolved entity and
// title of every applied token and the effect and because clause of its
// downstream consequence, read from the one bead-state table `focus show`
// reads. --json carries changes[] and echo[] with effect, because and cause
// as separate fields. The routine skips go to stderr.
//
// Telemetry (D24): the verb emits no OpenTelemetry and no Prometheus signal.
// It writes one focus_run row per non-dry run and prints the same record as
// one structured stderr line (focus_run.go); it logs nothing else.

// Flag names of select.
const (
	focusFlagApply  = "apply"
	focusFlagDryRun = "dry-run"
	focusFlagMerge  = "merge"
	focusFlagRepair = "repair"
)

// focusSelectBeforeLock is called between select's compute and its lock, so
// a test can commit a lock in the gap (a concurrent lock).
var focusSelectBeforeLock = func(*store.Store) {}

// newFocusAnnotator builds the production Annotator over a store; tests
// replace it to point the per-entity lock at a temp dir or to inject a
// failure.
var newFocusAnnotator = func(st *store.Store) focus.Annotator {
	return focus.NewAnnotator(st, store.NewLocker(store.LockerOptions{}))
}

// selectAttempts bounds the recompute loop when a concurrent lock, pull or
// replan changes the plan or the stored draft between compute and lock.
const selectAttempts = 3

func init() {
	focusGroup().AddCommand(newFocusSelectCmd())
}

func newFocusSelectCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "select [--date YYYY-MM-DD] [--cap N] [--apply | --dry-run] [--merge KEEP=ABSORBED,...] [--repair] [--json]",
		Short: "Lock the draft into the plan: the reply on stdin (ok, -key, +key, cap=N)",
		Long: `Lock the draft of the addressed period into its plan. The reply is read from
stdin: ok, -<key>, +<key> and cap=N, separated by whitespace or commas.

Without --apply (or with --dry-run, alone or together with --apply) select
PREVIEWS: it computes everything and writes nothing, and leaves the stored
draft. With --apply it is the LOCK: it reconciles the plan in one transaction
with the stored-draft deletion, writes the focus_selected annotations, and
records the run. With no stored draft the order is computed now and may
differ from what you saw (a WARNING says so).

--merge KEEP=ABSORBED (repeatable, comma-separated) records an external link
that folds one candidate into another; +<absorbedKey> reverses it. --repair
re-writes any missing or mismatched focus_selected annotation from the table
(every annotated entity and every entity with a plan row), reads no reply and
no --merge, and works on a closed period.

Nothing is minted here: select writes selection state only.

Exit codes: 0 applied (or previewed), 1 usage (including a first lock for an
earlier or a future period), 2 rows committed but an annotation write failed
(run select --repair), 3 a write failed outright, 6 period closed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runFocusSelect(cmd) },
	}
	addFocusFlags(c)
	c.Flags().Int(focusFlagCap, 0, "Cap of the in-process draft (a usage error with an applicable stored draft)")
	c.Flags().Bool(focusFlagApply, false, "Write the lock (without it select previews)")
	c.Flags().Bool(focusFlagDryRun, false, "Preview and write nothing (also with --apply)")
	c.Flags().StringArray(focusFlagMerge, nil, "Fold ABSORBED into KEEP: KEEP=ABSORBED[,KEEP=ABSORBED...] (repeatable)")
	c.Flags().Bool(focusFlagRepair, false, "Re-write missing or mismatched focus_selected annotations from the table")
	return c
}

// selectFlags are the parsed flags of one select run.
type selectFlags struct {
	capN     int
	capGiven bool
	apply    bool // write the lock
	dry      bool
	merges   []string
	repair   bool
}

func runFocusSelect(cmd *cobra.Command) error {
	var f selectFlags
	var err error
	if f.capN, f.capGiven, err = focusCapFlag(cmd); err != nil {
		return err
	}
	applyFlag, _ := cmd.Flags().GetBool(focusFlagApply)
	f.dry, _ = cmd.Flags().GetBool(focusFlagDryRun)
	f.merges, _ = cmd.Flags().GetStringArray(focusFlagMerge)
	f.repair, _ = cmd.Flags().GetBool(focusFlagRepair)
	f.apply = applyFlag && !f.dry
	if f.repair {
		if len(f.merges) > 0 {
			return focusUsageError("--repair takes no --merge", "drop --merge, or run select without --repair")
		}
		if f.capGiven {
			return focusUsageError("--repair takes no --cap", "drop --cap, or run select without --repair")
		}
		// --apply is accepted and means nothing here: a repair always writes
		// unless --dry-run is given.
		return runFocusRepair(cmd, f)
	}

	env, err := openFocusEnv(cmd, f.apply)
	if err != nil {
		return err
	}
	defer env.Close()
	raw, rerr := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 1<<20+2))
	s := &selectRun{cmd: cmd, env: env, f: f, raw: raw, readErr: rerr}
	s.run = newFocusRun(cmd.ErrOrStderr(), focusVerbSelect, env.Period, env.Actor, !f.apply, env.Now)
	for attempt := 0; ; attempt++ {
		err := s.once()
		if errors.Is(err, focus.ErrPlanChanged) {
			if attempt+1 < selectAttempts {
				continue
			}
			err = focusTotalError("focus select: the plan kept changing under the lock", "re-run select")
		}
		return s.finishEarly(err)
	}
}

// selectRun is one select run, possibly recomputed after a lock race.
type selectRun struct {
	cmd     *cobra.Command
	env     *focusEnv
	f       selectFlags
	raw     []byte
	readErr error
	run     *focusRun
	done    bool // the run record is finished
}

// finishEarly ends the run with err's exit code when no row-writing path did:
// a non-dry run still writes its focus_run row (exit 1, 3 and 6 are counted by
// the run statistics), then prints the stderr line.
func (s *selectRun) finishEarly(err error) error {
	if s.done {
		return err
	}
	s.done = true
	if s.f.apply && !s.run.Inserted() {
		_ = s.run.Start(s.env.Store)
	}
	_ = s.run.Finish(focusExitCode(err))
	return err
}

// planned is everything the compute stage decided.
type planned struct {
	g        *focusGather
	doc      focus.DraftDoc
	stored   *store.FocusDraft
	fresh    bool
	madeAt   time.Time
	cap      int
	rk       focus.Ranking
	live     focus.Live
	reply    focus.Reply
	merges   []focus.Merge
	fs       focus.FinalSet
	deps     focus.LockDeps
	lockIn   focus.LockInput
	hydrated []focus.Key
}

func (s *selectRun) deps() focus.LockDeps {
	env := s.env
	return focus.LockDeps{
		Store: env.Store, Annotator: newFocusAnnotator(env.Store), Config: env.Cfg,
		Now: func() time.Time { return env.Now },
		Hydrate: func(ctx context.Context, k focus.Key) error {
			res, err := refreshEntity(ctx, env.Cfg, env.Store, k.Type, k.ID)
			if err != nil {
				return err
			}
			if res.Degraded != "" {
				return fmt.Errorf("hydration degraded: %s", res.Degraded)
			}
			return nil
		},
		Stderr: s.cmd.ErrOrStderr(), Actor: env.Actor,
	}
}

// mapFocusErr turns an error of internal/focus into the verb's focus error.
func mapFocusErr(err error, period string) error {
	if err == nil {
		return nil
	}
	var fe *focusError
	if errors.As(err, &fe) {
		return err
	}
	var ue *focus.UsageError
	switch {
	case errors.As(err, &ue):
		return focusUsageError(ue.Msg, ue.Remedy)
	case errors.Is(err, focus.ErrPeriodClosed):
		return focusPeriodClosedError(period)
	}
	return focusTotalError(err.Error(), "check the pg-desk store, then re-run")
}

// plan runs the compute stage: gather, closed check, draft, reply, merges and
// the final set. It writes nothing.
func (s *selectRun) plan() (*planned, error) {
	env, f := s.env, s.f
	g, err := gatherFocus(env)
	if err != nil {
		return nil, err
	}
	p := &planned{g: g}

	applicable := g.hasDraft && isApplicableDraft(g.draft, env.Period)
	readable := false
	if applicable {
		if d, derr := focus.DecodeDraft([]byte(g.draft.BodyJSON)); derr == nil {
			readable, p.doc = true, d
		}
	}
	// Flag usage errors come before the closed check.
	if applicable && readable && f.capGiven {
		return nil, focusUsageError("--cap cannot be used with an applicable stored draft (its cap is the draft's)",
			"drop --cap, and put cap=N in the reply")
	}
	merges, merr := focus.ParseMerges(f.merges, env.Cfg)
	if merr != nil {
		return nil, mapFocusErr(merr, env.Period.Key)
	}
	p.merges = merges

	if g.closed {
		return nil, focusPeriodClosedError(env.Period.Key)
	}
	if s.readErr != nil {
		return nil, focusUsageError("focus select: cannot read the reply: "+s.readErr.Error(), "pipe the reply on stdin")
	}
	reply, err := focus.ParseReply(bytes.NewReader(s.raw))
	if err != nil {
		return nil, mapFocusErr(err, env.Period.Key)
	}
	p.reply = reply

	if applicable && readable {
		sd := g.draft
		p.stored = &sd
		p.cap = g.draft.Cap
		p.madeAt, _ = time.Parse(time.RFC3339, g.draft.MadeAt)
		p.rk = g.rank(p.cap)
	} else {
		if g.totalFailure() {
			return nil, g.coverageResult()
		}
		p.fresh = true
		p.cap = focus.DefaultCap
		if g.hasPlan() {
			p.cap = g.periodCap()
		}
		if f.capGiven {
			p.cap = f.capN
		}
		p.rk = g.rank(p.cap)
		p.doc = focus.NewReplanDraft(g.plan, p.rk, g.cov.State, p.cap, g.firstSeen)
		p.madeAt = env.Now
	}

	// Live facts of every key the draft, the plan and the reply name.
	seen := map[focus.Key]bool{}
	var keys []focus.Key
	add := func(k focus.Key) {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for _, r := range p.doc.Rows {
		add(r.Key)
	}
	for _, pr := range g.plan {
		add(pr.Key)
	}
	for _, raw := range append(append([]string(nil), reply.Plus...), reply.Minus...) {
		if k, kerr := focus.ParseKey(raw, env.Cfg); kerr == nil {
			add(k)
		}
	}
	for _, m := range p.merges {
		add(m.Kept)
		add(m.Absorbed)
	}
	p.live = focus.BuildLive(g.in, keys, p.rk, focus.LiveOptions{Watched: g.watched, SelectedAt: g.selectedAt()})

	var periodCap *int
	if g.found && g.period.Cap != nil {
		v := *g.period.Cap
		periodCap = &v
	}
	draftCap := p.cap
	fs, err := focus.ComputeFinalSet(focus.FinalSetInput{
		Draft: &p.doc, Plan: g.plan, Reply: reply, Candidates: g.set, Facts: p.live.Facts,
		Merges: p.merges, PersistedMerges: focus.PersistedMerges(g.in),
		PeriodHasRows: g.hasPlan(), PeriodCap: periodCap, DefaultCap: focus.DefaultCap, DraftCap: &draftCap,
		OpenChild: func(k focus.Key) (focus.Key, bool) {
			if kids := focus.OpenChildren(g.in, k); len(kids) > 0 {
				return kids[0], true
			}
			return focus.Key{}, false
		},
		AnyPeriodRows: g.in.PlanKeys, Config: env.Cfg,
	})
	if err != nil {
		return nil, mapFocusErr(err, env.Period.Key)
	}
	p.fs = fs
	p.deps = s.deps()
	p.lockIn = focus.LockInput{
		Period: focus.Period{Type: env.Period.Type, Key: env.Period.Key}, Actor: env.Actor,
		Apply: f.apply, DryRun: f.dry, Final: fs, Draft: &p.doc, FreshOrder: p.fresh,
		Merges: p.merges, ReplyCap: reply.Cap, RunID: s.run.RunID(),
		Seen: focus.Seen{Plan: g.plan, Draft: p.stored},
	}
	if p.fresh {
		p.lockIn.Draft = &p.doc
	}
	return p, nil
}

// once computes and then previews or applies the lock.
func (s *selectRun) once() error {
	ctx := s.cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	env := s.env
	p, err := s.plan()
	if err != nil {
		return err
	}
	// Doomed runs never hydrate: the first-lock rule is checked first.
	if err := focus.PreflightLock(ctx, p.deps, p.lockIn); err != nil {
		return mapFocusErr(err, env.Period.Key)
	}
	var need []focus.Key
	for _, k := range p.fs.HandAdded {
		if _, stored := p.g.in.Entities[k]; !stored {
			need = append(need, k)
		}
	}
	if len(need) > 0 && s.f.apply {
		hydrated, herr := focus.HydrateHandAdds(ctx, p.deps, need, p.g.in)
		if herr != nil {
			return mapFocusErr(herr, env.Period.Key)
		}
		// The store changed: recompute over it (a hand-add that turns out
		// closed or hidden is a usage error now).
		if p, err = s.plan(); err != nil {
			return err
		}
		p.hydrated = hydrated
		p.lockIn.Hydrated = hydrated
	}

	if p.fresh && s.f.apply {
		_, _ = fmt.Fprintln(s.cmd.ErrOrStderr(), "WARNING: no stored draft; the order was computed now and may differ from what you saw")
	}
	counts := s.counts(p)
	if !s.f.apply {
		res, lerr := focus.Lock(ctx, p.deps, p.lockIn)
		if lerr != nil {
			return mapFocusErr(lerr, env.Period.Key)
		}
		s.run.SetCounts(counts)
		if perr := s.print(p, res.Preview, res); perr != nil {
			return perr
		}
		s.done = true
		return s.run.Finish(0)
	}

	// The effects are resolved BEFORE the lock: afterwards the rows are gone.
	pv, perr := focus.BuildPreview(ctx, p.deps, p.lockIn, p.fs)
	if perr != nil {
		return mapFocusErr(perr, env.Period.Key)
	}
	focusSelectBeforeLock(env.Store)
	res, lerr := focus.Lock(ctx, p.deps, p.lockIn)
	if lerr != nil {
		if errors.Is(lerr, focus.ErrPlanChanged) {
			return lerr
		}
		return mapFocusErr(lerr, env.Period.Key)
	}
	s.run.Adopt(env.Store)
	for _, a := range res.Annotations {
		s.run.Annotation(keyText(a.Key), a.Outcome, a.Seq)
	}
	counts.Selected = len(res.Inserted)
	counts.Removed = removedCounts(res.Deleted)
	s.run.SetCounts(counts)
	if perr := s.print(p, pv, res); perr != nil {
		return perr
	}
	s.done = true
	var runErr error
	if res.ExitCode == focus.ExitPartial {
		failed := 0
		for _, a := range res.Annotations {
			if a.Outcome != focus.AnnotationOK {
				failed++
			}
		}
		runErr = focusPartialError(
			fmt.Sprintf("focus select: the plan is locked but %d focus_selected annotation write(s) failed", failed),
			"run pg-desk focus select --repair",
		)
	}
	if ferr := s.run.Finish(focusExitCode(runErr)); ferr != nil && runErr == nil {
		return focusTotalError("focus select: finalize the run record: "+ferr.Error(), "run pg-desk focus select --repair")
	}
	return runErr
}

func removedCounts(ds []focus.Deletion) focusRemoved {
	var r focusRemoved
	for _, d := range ds {
		switch d.Cause {
		case focus.CauseOperator:
			r.Operator++
		case focus.CauseCap:
			r.Cap++
		case focus.CauseDropped:
			r.Dropped++
		case focus.CauseNewPeriod:
			r.NewPeriod++
		}
	}
	return r
}

// counts builds the run's counting members from the compute stage. It also
// computes a fresh rank ONLY to count drift_rows and record draft_age_seconds.
func (s *selectRun) counts(p *planned) focusCounts {
	env, g := s.env, p.g
	c := focusCounts{
		Cap: p.fs.EffectiveCap, Ranked: len(p.rk.Rows), Selected: len(p.fs.Insert),
		Removed: removedCounts(p.fs.Delete), Forced: keyTexts(p.fs.Forced),
		Handadded: keyTexts(p.fs.HandAdded), Hydrated: keyTexts(p.hydrated),
		RankInputs: focusRankInputs(p.rk.Inputs), FreshOrder: p.fresh,
		Coverage: focusCoverageSummary{Incomplete: g.cov.State.Incomplete, Reasons: g.cov.State.Reasons},
	}
	for _, m := range p.fs.Absorbed {
		c.Absorbed = append(c.Absorbed, keyText(m.Absorbed))
	}
	for _, r := range p.rk.Rows {
		switch r.Tier {
		case focus.TierOverdue:
			c.Tiers.Overdue++
		case focus.TierStarted:
			c.Tiers.Started++
		case focus.TierNotStarted:
			c.Tiers.NotStarted++
		}
	}
	gone := map[focus.Key]bool{}
	for _, d := range p.fs.Delete {
		gone[d.Key] = true
	}
	for _, pr := range g.plan {
		if gone[pr.Key] {
			continue
		}
		f := p.live.Facts[pr.Key]
		if f.Claimant != "" {
			c.Kept.Claimed++
		}
		if f.Deferred {
			c.Kept.Deferred++
		}
	}
	if !p.fresh {
		view := focus.ReRender(p.doc, p.madeAt, env.Now, p.live.Facts, p.rk, g.plan)
		c.DriftRows = view.Drift.RowsPlacedDifferently
		c.DraftAgeSeconds = int64(maxDuration(env.Now.Sub(p.madeAt), 0) / time.Second)
	}
	return c
}

// selectChangeJSON is one member of the --json `changes` array: a row the
// run newly selects or removes. effect is the closed vocabulary (mint, hold,
// release, none), because the "Shown as" clause of the bead-state table,
// cause the stored cause of a removal ("" for a selection) and cause_text its
// display words; consequence is the phrase the text prints.
type selectChangeJSON struct {
	Direction   string `json:"direction"`
	Key         string `json:"key"`
	Title       string `json:"title"`
	Effect      string `json:"effect"`
	Because     string `json:"because"`
	Cause       string `json:"cause"`
	CauseText   string `json:"cause_text"`
	Consequence string `json:"consequence"`
	State       string `json:"state"`
	Bead        string `json:"bead"`
	Source      string `json:"source,omitempty"`
}

// selectEchoJSON is one member of the --json `echo` array.
type selectEchoJSON struct {
	Kind        string `json:"kind"`
	Token       string `json:"token"`
	Key         string `json:"key"`
	Other       string `json:"other,omitempty"`
	Title       string `json:"title"`
	Result      string `json:"result"`
	Effect      string `json:"effect"`
	Because     string `json:"because"`
	Cause       string `json:"cause"`
	Consequence string `json:"consequence"`
	Bead        string `json:"bead"`
}

func changesJSON(pv focus.PreviewResult) []selectChangeJSON {
	out := make([]selectChangeJSON, 0, len(pv.Changes))
	for _, c := range pv.Changes {
		out = append(out, selectChangeJSON{
			Direction: c.Direction, Key: keyText(c.Key), Title: c.Title, Effect: c.Effect, Because: c.Because,
			Cause: c.Cause, CauseText: c.CauseText, Consequence: c.Echo, State: string(c.State), Bead: c.BeadID, Source: c.Source,
		})
	}
	return out
}

func echoJSON(pv focus.PreviewResult) []selectEchoJSON {
	out := make([]selectEchoJSON, 0, len(pv.Echo))
	for _, e := range pv.Echo {
		row := selectEchoJSON{
			Kind: e.Kind, Token: e.Token, Title: e.Title, Result: e.Detail, Effect: e.Effect, Because: e.Because,
			Cause: e.Cause, Consequence: e.Consequence, Bead: e.BeadID,
		}
		if e.Key != (focus.Key{}) {
			row.Key = keyText(e.Key)
		}
		if e.Other != nil {
			row.Other = keyText(*e.Other)
		}
		out = append(out, row)
	}
	return out
}

// print writes what the run did (or would do): in text the draft (a preview
// only), the hydration report, the CHANGES block and the echo table, in --json
// the same facts as members. pv is the preview resolved before any write; res
// the lock's own result. The routine skips go to stderr.
func (s *selectRun) print(p *planned, pv focus.PreviewResult, res focus.LockResult) error {
	var draft *focusScreen
	if !s.f.apply {
		draft = s.draftScreen(p)
	}
	hydrated := keyTexts(p.hydrated)
	wouldHydrate := keyTexts(pv.WouldHydrate)
	if s.f.apply {
		wouldHydrate = []string{}
	}
	if _, err := io.WriteString(s.cmd.ErrOrStderr(), focus.RenderSkips(pv)); err != nil {
		return err
	}
	w := s.cmd.OutOrStdout()
	if s.env.JSON {
		anns := make([]map[string]any, 0, len(res.Annotations))
		for _, a := range res.Annotations {
			anns = append(anns, map[string]any{"key": keyText(a.Key), "outcome": a.Outcome, "seq": a.Seq})
		}
		payload := map[string]any{
			"period": s.env.Period.Key, "applied": s.f.apply, "dry_run": !s.f.apply,
			"changes": changesJSON(pv), "echo": echoJSON(pv), "annotations": anns,
			"hydrated": nonNilStrings(hydrated), "would_hydrate": nonNilStrings(wouldHydrate),
			"fresh_order": p.fresh, "first_lock": pv.FirstLock, "replaces_plan": pv.ReplacesPlan,
			"never_closed": nonNilStrings(pv.NeverClosed),
		}
		if draft != nil {
			payload["draft"] = draft.payload()
		}
		runID := ""
		if s.f.apply {
			runID = s.run.RunID()
		}
		return printFocusJSON(w, "select", payload, runID)
	}
	if draft != nil {
		draft.writeText(w)
	}
	for _, k := range wouldHydrate {
		if _, err := fmt.Fprintf(w, "would hydrate %s\n", k); err != nil {
			return err
		}
	}
	for _, k := range hydrated {
		if _, err := fmt.Fprintf(w, "hydrated %s\n", k); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w, focus.RenderChanges(pv)); err != nil {
		return err
	}
	// A bare preview prints the CHANGES block alone; --dry-run and --apply
	// add the echo table (a dry run's echo equals the apply echo).
	if s.f.apply || s.f.dry {
		if _, err := io.WriteString(w, focus.RenderEcho(pv)); err != nil {
			return err
		}
	}
	return nil
}

// draftScreen builds the draft a preview prints before the CHANGES block: the
// stored draft re-rendered, or the one computed in this process.
func (s *selectRun) draftScreen(p *planned) *focusScreen {
	line := "DRAFT (computed in this run, not stored)"
	in := screenInput{
		verb: "select", mode: focusModeDraft, draftLine: line, fresh: p.rk, cap: p.cap, doc: &p.doc, madeAt: p.madeAt,
	}
	if !p.fresh {
		in.stored, in.reRender = true, true
		in.draftLine = fmt.Sprintf("DRAFT (stored), draft made %s ago", ageText(maxDuration(s.env.Now.Sub(p.madeAt), 0)))
	}
	return p.g.buildScreen(in)
}

func nonNilStrings(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// runFocusRepair is select --repair: re-write missing or mismatched
// focus_selected annotations from the table. It reads no reply, is exempt
// from the period-closed check, and (without --dry-run) writes a focus_run
// row of verb repair.
func runFocusRepair(cmd *cobra.Command, f selectFlags) error {
	writing := !f.dry
	env, err := openFocusEnv(cmd, writing)
	if err != nil {
		return err
	}
	defer env.Close()
	run := newFocusRun(cmd.ErrOrStderr(), focusVerbRepair, env.Period, env.Actor, f.dry, env.Now)
	if writing {
		_ = run.Start(env.Store)
	}
	s := &selectRun{cmd: cmd, env: env, f: f}
	deps := s.deps()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	res, rerr := focus.Repair(ctx, deps, f.dry)
	if rerr != nil {
		err := focusTotalError(rerr.Error(), "check the pg-desk store, then re-run")
		_ = run.Finish(focusExitCode(err))
		return err
	}
	failed := 0
	for _, a := range res.Written {
		if !f.dry {
			run.Annotation(keyText(a.Key), a.Outcome, a.Seq)
		}
		if a.Outcome == focus.AnnotationFailed {
			failed++
		}
	}
	run.SetCounts(focusCounts{})
	w := cmd.OutOrStdout()
	if env.JSON {
		anns := make([]map[string]any, 0, len(res.Written))
		for _, a := range res.Written {
			anns = append(anns, map[string]any{"key": keyText(a.Key), "outcome": a.Outcome, "seq": a.Seq})
		}
		if err := printFocusJSON(w, "repair", map[string]any{"dry_run": f.dry, "annotations": anns}, run.RunID()); err != nil {
			return err
		}
	} else {
		ws := append([]focus.AnnotationOutcome(nil), res.Written...)
		sort.SliceStable(ws, func(i, j int) bool { return keyText(ws[i].Key) < keyText(ws[j].Key) })
		for _, a := range ws {
			word := a.Outcome
			if word == focus.AnnotationOK {
				word = "wrote"
			}
			if _, err := fmt.Fprintf(w, "repair %s %s -\n", keyText(a.Key), word); err != nil {
				return err
			}
		}
	}
	var runErr error
	if failed > 0 {
		runErr = focusPartialError(
			fmt.Sprintf("focus select --repair: %d annotation write(s) failed", failed),
			"re-run pg-desk focus select --repair",
		)
	}
	if ferr := run.Finish(focusExitCode(runErr)); ferr != nil && runErr == nil {
		return focusTotalError("focus select --repair: finalize the run record: "+ferr.Error(), "re-run pg-desk focus select --repair")
	}
	return runErr
}
