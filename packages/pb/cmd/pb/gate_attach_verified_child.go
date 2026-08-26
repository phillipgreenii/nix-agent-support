package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/gate"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/spf13/cobra"
)

func parseGateSpecs(raw []string) ([]gate.GateSpec, error) {
	specs := make([]gate.GateSpec, 0, len(raw))
	for _, s := range raw {
		repo, sha, ok := strings.Cut(s, "=")
		if !ok || repo == "" || sha == "" {
			return nil, fmt.Errorf("--gate %q: want <repo-key>=<sha>", s)
		}
		specs = append(specs, gate.GateSpec{Repo: repo, Commit: sha})
	}
	return specs, nil
}

func newGateAttachVerifiedChildCmd() *cobra.Command {
	var (
		impl, title, actor, reason string
		gates                      []string
		asJSON                     bool
	)
	cmd := &cobra.Command{
		Use:   "attach-verified-child",
		Short: "Create a DEFERRED verification child of --impl, gate it on the landed commits, then un-defer",
		Long: `Runs the deferred-first post-deploy gate sequence for a landed bead:
create the verification child deferred, prove it is absent from bd ready,
attach one pn:applied gate per --gate <repo-key>=<sha>, un-defer, re-prove
absence, and comment the link on the implementation bead.

Exit codes: 0 fully gated; 1 generic failure; 3 gating incomplete and the
child was LEFT DEFERRED (safe — route the impl bead to STUCK); 4 the child
could not be proven un-workable (do NOT close the impl bead).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			specs, err := parseGateSpecs(gates)
			if err != nil {
				return err
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			if reason == "" {
				reason = "post-deploy verify for " + impl
			}
			r := run.CLIRunner{}
			d := gate.CreateDeps{PN: pn.Client{R: r}, BD: bd.Client{R: r}, PatchID: patchid.Client{R: r}, R: r}
			out, aerr := gate.Attach(context.Background(), d, gate.AttachParams{
				WorkspaceDir: wd, ImplID: impl, Title: title,
				Gates: specs, Actor: actor, Reason: reason,
			})
			// Emit the result BEFORE exiting: on partial failure the child id and
			// the gates already created are what the operator needs.
			if asJSON {
				_ = json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			} else if out.ChildID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "child=%s gates=%d\n", out.ChildID, len(out.Gates))
			}
			// The warning goes to stderr under BOTH output modes: a --json caller
			// still gets "comment_failed":true, but stderr is what a human sees.
			if out.CommentFailed {
				fmt.Fprintln(cmd.ErrOrStderr(), "pb: warning: gating complete but the impl-bead comment failed; record the link manually")
			}
			if aerr != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "pb:", aerr)
				switch {
				case errors.Is(aerr, gate.ErrChildMayBeWorkable):
					os.Exit(4)
				case errors.Is(aerr, gate.ErrGatingIncomplete):
					os.Exit(3)
				}
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&impl, "impl", "", "implementation bead id (required)")
	cmd.Flags().StringVar(&title, "title", "", "verification child title (required)")
	cmd.Flags().StringArrayVar(&gates, "gate", nil, "<repo-key>=<sha> to gate on; repeatable, one per changed repo (required)")
	cmd.Flags().StringVar(&actor, "actor", "", "bd actor id (required)")
	cmd.Flags().StringVar(&reason, "reason", "", `gate reason (default "post-deploy verify for <impl>")`)
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	_ = cmd.MarkFlagRequired("impl")
	_ = cmd.MarkFlagRequired("title")
	_ = cmd.MarkFlagRequired("gate")
	_ = cmd.MarkFlagRequired("actor")
	return cmd
}
