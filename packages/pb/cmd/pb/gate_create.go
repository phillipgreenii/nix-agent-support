package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/gate"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/spf13/cobra"
)

func newGateCreateCmd() *cobra.Command {
	var (
		blocks  string
		repo    string
		commit  string
		commits string
		reason  string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create pn:applied gate(s) blocking a bead until a change is applied",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if blocks == "" {
				return fmt.Errorf("--blocks <beadid> is required")
			}
			if repo == "" {
				return fmt.Errorf("--repo <repo> is required")
			}
			if reason == "" {
				reason = "pn:applied gate"
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			r := run.CLIRunner{}
			d := gate.CreateDeps{PN: pn.Client{R: r}, BD: bd.Client{R: r}, PatchID: patchid.Client{R: r}, R: r}
			out, err := gate.Create(context.Background(), d, gate.CreateParams{
				WorkspaceDir: wd, BeadID: blocks, Repo: repo, Commit: commit, Commits: commits, Reason: reason,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			for _, g := range out.Gates {
				fmt.Fprintf(cmd.OutOrStdout(), "created gate %s (await_id=%s baseline=%q)\n", g.GateID, g.AwaitID, g.AppliedBaseline)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&blocks, "blocks", "", "bead id to block (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "workspace repo key (required)")
	cmd.Flags().StringVar(&commit, "commit", "", "commit-ish to gate (default HEAD)")
	cmd.Flags().StringVar(&commits, "commits", "", "commit range → one gate per commit (advanced)")
	cmd.Flags().StringVar(&reason, "reason", "", "gate reason")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}
