package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// reinterpretOrigin labels the interpretation_changed record the command's
// writes append.
const reinterpretOrigin = "reinterpret"

func init() {
	typeGroup(entityTypePR).AddCommand(newReinterpretCmd())
}

// newReinterpretCmd builds `pg-desk pr reinterpret <pr> | --open`: a loop over
// pipeline.ReinterpretStored and Store.StampInterpretation. It has no
// interpretation logic of its own, starts no hydration and makes no network
// call (docs/behavior/pg-desk/reinterpret.md, "The command").
func newReinterpretCmd() *cobra.Command {
	var open, dryRun, verbose bool
	c := &cobra.Command{
		Use:   "reinterpret <pr> | --open",
		Short: "Re-interpret PRs from the facts already stored",
		Long: `Recompute the interpretation (who owes the next step, the panel, the reason)
of PRs from the facts pg-desk already holds. Nothing is read from GitHub or
pg-connector, and no entity row is written.

<pr> (OWNER/REPO#N, a PR URL or a bare number) re-interprets that one PR
whatever its stamp, including a closed one, and retries a row marked failed.

--open selects the PR interpretations whose stamp differs from the running
stamp (the build version plus a digest of the configuration interpret reads)
and are not marked failed under it, re-interprets the open ones and gives the
rest the current stamp without touching their content. It has no row cap and
no time budget.

A changed waiting_on or panel appends one interpretation_changed record (read
by pr history only). --dry-run prints what would change and writes nothing,
not even a stamp. --verbose prints one line per PR.

Exit codes: 0 when nothing failed; 1 otherwise (bad arguments, an unresolvable
<pr>, a schema-1 store, a failed row).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			if (ref == "") == !open {
				return errors.New("reinterpret: give exactly one of <pr> and --open")
			}
			return runReinterpret(cmd, ref, open, dryRun, verbose)
		},
	}
	c.Flags().BoolVar(&open, "open", false, "Re-interpret every behind open PR and stamp the rest")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would change; write nothing, not even a stamp")
	c.Flags().BoolVar(&verbose, "verbose", false, "Print one line per PR: old and new waiting_on, panel and reason")
	return c
}

// reinterpretCounts is what one pass did.
type reinterpretCounts struct {
	Selected, Changed, Unchanged, StampedOnly, Skipped, Failed int
}

func runReinterpret(cmd *cobra.Command, ref string, open, dryRun, verbose bool) error {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("reinterpret: load config: %w", err)
	}
	if len(cfg.Repos) == 0 {
		return errors.New("reinterpret: no repository configured")
	}
	repo := cfg.Repos[0].Remote
	var id string
	if !open {
		if _, id, err = resolveTypedRef(cfg, entityTypePR, ref); err != nil {
			return fmt.Errorf("reinterpret: %w", err)
		}
	}
	st, err := openNewSchemaStore("reinterpret")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	cmd.SilenceUsage = true
	r := newReinterpreter(cmd, cfg, st, repo, dryRun, verbose)

	var keys []store.InterpretationKey
	if open {
		var known bool
		keys, known, err = st.InterpretationsBehind(repo, 0)
		if err != nil {
			return fmt.Errorf("reinterpret: %w", err)
		}
		if !known {
			return errors.New("reinterpret: the store cannot answer which interpretations are behind (not on schema 2, or no stamp column)")
		}
	} else {
		keys = []store.InterpretationKey{{Repo: repo, EntityType: entityTypePR, EntityID: id}}
	}

	for _, k := range keys {
		if err := cmd.Context().Err(); err != nil {
			return fmt.Errorf("reinterpret: %w", err)
		}
		r.one(k, !open)
	}
	if dryRun {
		if _, err := fmt.Fprintln(r.out, "dry run: nothing written, nothing stamped"); err != nil {
			return err
		}
	}
	c := r.counts
	if _, err := fmt.Fprintf(r.out, "reinterpreted %d open PRs: %d changed, %d unchanged; %d not open (stamped only), %d skipped, %d failed\n",
		c.Changed+c.Unchanged, c.Changed, c.Unchanged, c.StampedOnly, c.Skipped, c.Failed); err != nil {
		return err
	}
	if c.Failed > 0 {
		return fmt.Errorf("reinterpret: %d PR(s) failed", c.Failed)
	}
	return nil
}

// newReinterpreter builds the pass over st and gives st the running stamp, so
// every row the pass writes carries it (INV-REINTERPRET-4).
func newReinterpreter(cmd *cobra.Command, cfg *config.Config, st *store.Store, repo string, dryRun, verbose bool) *reinterpreter {
	st.SetInterpStamp(runningStamp(cfg))
	return &reinterpreter{
		ctx: cmd.Context(), st: st, cfg: cfg, clock: interpret.SystemClock{}, repo: repo,
		out: cmd.OutOrStdout(), errOut: cmd.ErrOrStderr(), dryRun: dryRun, verbose: verbose,
	}
}

// reinterpreter holds one pass's collaborators and counts.
type reinterpreter struct {
	ctx     context.Context
	st      *store.Store
	cfg     *config.Config
	clock   interpret.Clock
	repo    string
	out     io.Writer
	errOut  io.Writer
	dryRun  bool
	verbose bool
	counts  reinterpretCounts
}

// one processes one interpretation row. force re-interprets it whatever its
// state (the named-PR form); otherwise a row whose PR is not open is only
// stamped. A failure is reported, counted and, when it is the facts or the
// entity that is at fault, recorded on the row; it never stops the pass.
func (r *reinterpreter) one(k store.InterpretationKey, force bool) {
	e, found, err := r.st.GetEntity(k.Repo, k.EntityType, k.EntityID)
	if err != nil {
		r.fail(k, fmt.Errorf("get entity: %w", err), false)
		return
	}
	if !found {
		r.fail(k, errors.New("entity is not stored"), true)
		return
	}
	state, decodable := classify.PRStateOfFacts([]byte(e.Facts))
	if !force && decodable && state != "open" {
		r.stampOnly(k, e.ContentHash, state)
		return
	}
	// An undecodable row goes through the re-interpretation so that it fails
	// the same way and is marked failed there.
	old, hadOld, err := r.st.GetInterpretation(k.Repo, k.EntityType, k.EntityID)
	if err != nil {
		r.fail(k, fmt.Errorf("read interpretation: %w", err), false)
		return
	}
	res, err := pipeline.ReinterpretStored(r.ctx, r.st, r.cfg, r.clock, k.EntityType, k.EntityID, pipeline.ReinterpretOptions{Origin: reinterpretOrigin, DryRun: r.dryRun})
	if err != nil {
		stage, _ := pipeline.StageOf(err)
		r.fail(k, err, stage == pipeline.StageLoadFacts || stage == pipeline.StageInterpret)
		return
	}
	if !res.Found {
		r.fail(k, errors.New("entity is not stored"), true)
		return
	}
	if res.Write.Skipped {
		r.counts.Skipped++
		r.line(k, "skipped: different facts were committed since they were read")
		return
	}
	changed := hadOld && store.DecisionPairChanged(old, res.Row)
	if changed {
		r.counts.Changed++
	} else {
		r.counts.Unchanged++
	}
	newD := res.Row.Decision()
	oldD := old.Decision()
	if !hadOld {
		oldD = store.Decision{}
	}
	slog.Debug("pg-desk reinterpret", "id", k.EntityID, "old_waiting_on", oldD.WaitingOn, "new_waiting_on", newD.WaitingOn,
		"panel", newD.Panel, "reason", newD.Reason, "changed", changed, "dry_run", r.dryRun)
	verdict := "unchanged"
	if changed {
		verdict = "changed"
	}
	r.line(k, fmt.Sprintf("%s: waiting_on %q -> %q, panel %q -> %q, reason %q -> %q",
		verdict, oldD.WaitingOn, newD.WaitingOn, oldD.Panel, newD.Panel, oldD.Reason, newD.Reason))
}

// stampOnly gives a PR that is not open the current stamp, keeping its content.
func (r *reinterpreter) stampOnly(k store.InterpretationKey, hash, state string) {
	if state == "" {
		state = "no recognisable state"
	}
	if r.dryRun {
		r.counts.StampedOnly++
		r.line(k, fmt.Sprintf("not open (%s): would stamp only", state))
		return
	}
	stamped, err := r.st.StampInterpretation(k, hash)
	switch {
	case err != nil:
		r.fail(k, fmt.Errorf("stamp: %w", err), false)
	case !stamped:
		r.counts.Skipped++
		r.line(k, "skipped: different facts were committed since they were read")
	default:
		r.counts.StampedOnly++
		r.line(k, fmt.Sprintf("not open (%s): stamped only", state))
	}
}

// fail reports and counts a failed row, and records it on the row (under the
// running stamp, so the next catch-up skips it) when mark is set and the pass
// writes.
func (r *reinterpreter) fail(k store.InterpretationKey, cause error, mark bool) {
	r.counts.Failed++
	_, _ = fmt.Fprintf(r.errOut, "reinterpret: %s: %v\n", k.EntityID, cause)
	if !mark || r.dryRun {
		return
	}
	if err := r.st.MarkInterpretationFailed(k, r.st.InterpStamp()); err != nil && !errors.Is(err, store.ErrNoInterpretation) {
		_, _ = fmt.Fprintf(r.errOut, "reinterpret: %s: mark failed: %v\n", k.EntityID, err)
	}
}

// line prints one --verbose line.
func (r *reinterpreter) line(k store.InterpretationKey, msg string) {
	if !r.verbose {
		return
	}
	_, _ = fmt.Fprintf(r.out, "%s: %s\n", k.EntityID, msg)
}
