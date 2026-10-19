package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// readBodyFile reads the child's description from path. An empty file is an
// error rather than a silent fall back to the derived text: the caller named a
// file and meant its content.
func readBodyFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("--body-file: %w", err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", fmt.Errorf("--body-file %q is empty", path)
	}
	return string(raw), nil
}

// attachVerifiedChildLong is the command's long help. The label list is read
// from gate.WorkflowStateLabels so the help cannot drift from the behaviour.
func attachVerifiedChildLong() string {
	return fmt.Sprintf(`Runs the deferred-first post-deploy gate sequence for a landed bead:
create the verification child deferred, prove it is absent from bd ready,
attach one pn:applied gate per --gate <repo-key>=<sha>, un-defer, re-prove
absence, and comment the link on the implementation bead.

The child is created complete, so no hand edit is needed before a drain worker
can claim it once its gates resolve (it stays out of bd ready until then):
  labels       the implementation bead's labels, which carry its repo and project
               labels, except its workflow-state labels (%s);
               has-acceptance-criteria is always set
  description  --body-file's content; default: derived from the implementation
               bead (its id and title) and the gated commits
  acceptance   --acceptance; default: derived from --title

--title is capped at 500 characters (bd's limit); a longer one is rejected with
exit 1 before anything is created. Keep it a short summary and put the long
check list in --body-file (or a bd comment on the child). An unreadable or empty
--body-file is rejected with exit 1, also before anything is created.

Exit codes: 0 fully gated; 1 generic failure; 3 gating incomplete and the
child was LEFT DEFERRED (safe — route the impl bead to STUCK); 4 the child
could not be proven un-workable (do NOT close the impl bead).`, strings.Join(gate.WorkflowStateLabels, ", "))
}

// writeAttachResult prints the result of an attach run to w. Nothing is
// printed when no child exists: a failure before the child was created has
// nothing to report, and under --json a {"child":"","gates":null} object would
// look like a success shape to a caller that parses stdout first (pg2-0jt3s).
// Every partial failure (exit 3 or 4) has a child id, so its result, the child
// and the gates created so far, still prints.
func writeAttachResult(w io.Writer, out gate.AttachResult, asJSON bool) {
	if out.ChildID == "" {
		return
	}
	if asJSON {
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	fmt.Fprintf(w, "child=%s gates=%d\n", out.ChildID, len(out.Gates))
}

// attachExitCode maps an Attach error to the documented exit code: 4 the child
// could not be proven un-workable, 3 gating incomplete, 1 anything else.
func attachExitCode(err error) int {
	switch {
	case errors.Is(err, gate.ErrChildMayBeWorkable):
		return 4
	case errors.Is(err, gate.ErrGatingIncomplete):
		return 3
	}
	return 1
}

func newGateAttachVerifiedChildCmd() *cobra.Command {
	var (
		impl, title, actor, reason, bodyFile, acceptance string
		gates                                            []string
		asJSON                                           bool
	)
	cmd := &cobra.Command{
		Use:   "attach-verified-child",
		Short: "Create a DEFERRED verification child of --impl, gate it on the landed commits, then un-defer",
		Long:  attachVerifiedChildLong(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			specs, err := parseGateSpecs(gates)
			if err != nil {
				return err
			}
			var description string
			if bodyFile != "" {
				if description, err = readBodyFile(bodyFile); err != nil {
					return err
				}
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
				Description: description, Acceptance: acceptance,
			})
			// Emit the result BEFORE exiting: on partial failure the child id and
			// the gates already created are what the operator needs.
			writeAttachResult(cmd.OutOrStdout(), out, asJSON)
			// The warning goes to stderr under BOTH output modes: a --json caller
			// still gets "comment_failed":true, but stderr is what a human sees.
			if out.CommentFailed {
				fmt.Fprintln(cmd.ErrOrStderr(), "pb: warning: gating complete but the impl-bead comment failed; record the link manually")
			}
			if aerr != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "pb:", aerr)
				os.Exit(attachExitCode(aerr))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&impl, "impl", "", "implementation bead id (required)")
	cmd.Flags().StringVar(&title, "title", "", "verification child title (required)")
	cmd.Flags().StringArrayVar(&gates, "gate", nil, "<repo-key>=<sha> to gate on; repeatable, one per changed repo (required)")
	cmd.Flags().StringVar(&actor, "actor", "", "bd actor id (required)")
	cmd.Flags().StringVar(&reason, "reason", "", `gate reason (default "post-deploy verify for <impl>")`)
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "file holding the child's description, e.g. the concrete post-apply checks (default: derived from --impl)")
	cmd.Flags().StringVar(&acceptance, "acceptance", "", "the child's acceptance criteria (default: derived from --title)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	_ = cmd.MarkFlagRequired("impl")
	_ = cmd.MarkFlagRequired("title")
	_ = cmd.MarkFlagRequired("gate")
	_ = cmd.MarkFlagRequired("actor")
	return cmd
}
