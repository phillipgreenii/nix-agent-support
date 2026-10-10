package main

import (
	"fmt"
	"os"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/unstick"
	"github.com/spf13/cobra"
)

func newUnstickReportCmd(env unstickEnv) *cobra.Command {
	var (
		workdir, root string
		asJSON        bool
		now           nowFlag
	)
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Re-export the bead database and print the sweep's before/after report",
		Long: `Stage 8 of /pb:unstick-beads. Re-exports to <workdir>/export.post.jsonl,
fetches the post-sweep ready list (<workdir>/ready.post.json), reads
prepare.json, batches/, results/*.md and followups.txt, and prints the report
with its arithmetic: before/after counters, who left the open-not-ready set
(sweep vs peers), markers by outcome, closed/undeferred/de-labelled/retargeted
beads, released claims, new beads and the OPERATOR:/FOLLOWUP: lines.

Attribution is a HEURISTIC (bd records no closer): a bead is the sweep's iff it
is in a dispatched batch and carries a non-unchanged marker at or after the
sweep start, or was closed and is listed as "closed <id>" in results/*.md.

A failed post-sweep ready fetch is not fatal: the report is produced without
the ready-based figures and a warning goes to stderr.

Exit codes: 0 ok; 1 usage, IO or internal error; 2 the bd export failed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w, err := requireWorkdir(workdir)
			if err != nil {
				return err
			}
			t, err := now.resolve(env)
			if err != nil {
				return err
			}
			if _, err := unstick.ReadPrepare(w.Join(unstick.PrepareFile)); err != nil {
				return fmt.Errorf("%w (run `pb unstick prepare` first)", err)
			}
			r, err := resolveRoot(env, root)
			if err != nil {
				return err
			}
			ctx := cmdCtx(cmd)
			client := bd.Client{R: env.Runner}
			if err := client.Export(ctx, r, w.Join(unstick.ExportPostFile)); err != nil {
				return bdFailure(err)
			}
			post, err := unstick.ReadExportFile(w.Join(unstick.ExportPostFile))
			if err != nil {
				return err
			}
			var readyIDs []string
			haveReady := false
			if raw, rerr := client.Ready(ctx, r); rerr != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "pb: warning: post-sweep ready list unavailable:", rerr)
			} else if rows, perr := unstick.ParseReady(raw); perr != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "pb: warning: post-sweep ready list unusable:", perr)
			} else {
				if werr := os.WriteFile(w.Join("ready.post.json"), raw, 0o600); werr != nil {
					return werr
				}
				haveReady = true
				for _, rr := range rows {
					readyIDs = append(readyIDs, rr.ID)
				}
			}
			in, err := unstick.LoadReportInput(w, post, readyIDs, haveReady, t)
			if err != nil {
				return err
			}
			rep := unstick.BuildReport(in)
			if asJSON {
				b, err := rep.RenderJSON()
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(b)
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), rep.RenderHuman())
			return err
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "absolute path of the sweep work directory (required)")
	cmd.Flags().StringVar(&root, "root", "", "pn workspace root holding the beads DB (default: $PN_WORKSPACE_ROOT, else the nearest pn-workspace.toml)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	now.register(cmd)
	return cmd
}
