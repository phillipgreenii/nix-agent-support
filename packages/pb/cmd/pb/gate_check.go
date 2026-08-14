package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/duration"
	"github.com/phillipgreenii/pb/internal/gate"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/spf13/cobra"
)

func newGateCheckCmd() *cobra.Command {
	var (
		dryRun       bool
		strict       bool
		lastN        int
		staleHandler string
		staleAfter   string
		asJSON       bool
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Resolve pn:applied gates whose change has been applied (run inside a workspace)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dur, err := duration.ParseDuration(staleAfter)
			if err != nil {
				return fmt.Errorf("--stale-after: %w", err)
			}
			if staleHandler != "convert-to-human" && staleHandler != "close" {
				return fmt.Errorf("--stale-handler must be convert-to-human or close")
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			r := run.CLIRunner{}
			d := gate.CheckDeps{PN: pn.Client{R: r}, BD: bd.Client{R: r}, PatchID: patchid.Client{R: r}}
			out, err := gate.Check(context.Background(), d, gate.CheckParams{
				WorkspaceDir: wd, DryRun: dryRun, Strict: strict, LastN: lastN,
				StaleHandler: staleHandler, StaleAfter: dur, Now: nowUTC(),
			})
			if err != nil {
				return err
			}
			if asJSON {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(out); err != nil {
					return err
				}
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "resolved=%d would_resolve=%d blocked=%d skipped=%d stale=%d\n",
					len(out.Resolved), len(out.WouldResolve), len(out.Blocked), len(out.Skipped), len(out.StaleActions))
				// Blocked gates are correctly still closed, so they do NOT affect the
				// exit code — but their reason is the actionable one ("push, relock,
				// re-apply"), and a bare count leaves the operator with no way to tell
				// a stuck gate from a waiting one. Print it.
				for _, b := range out.Blocked {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  blocked %s (%s): %s\n", b.GateID, b.Repo, b.Reason)
				}
			}
			if len(out.Skipped) > 0 {
				os.Exit(1) // best-effort: non-zero iff something was undeterminable
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would resolve/convert; change nothing")
	cmd.Flags().BoolVar(&strict, "strict", false, "skip dirty repos")
	cmd.Flags().IntVar(&lastN, "last-n", 100, "commits to scan when baseline is absent/diverged")
	cmd.Flags().StringVar(&staleHandler, "stale-handler", "convert-to-human", "convert-to-human|close")
	cmd.Flags().StringVar(&staleAfter, "stale-after", "3d", "gate age before stale-handling (ms..d, >=1ms)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}
