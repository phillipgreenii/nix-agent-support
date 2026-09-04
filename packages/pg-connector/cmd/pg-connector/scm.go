// scm.go: the "pg-connector scm" CLI verb group, built by the "generic scm
// entity/capability" packet on top of the Tier-1 core's registry/dispatcher
// and outcome-reporting helper. pg-connector remains the only user-facing
// CLI surface — scm is one of its verb groups, never a separate binary
// [design: §4, §4.7].
//
// Unlike pr/issue/ci, connector.scm is a SINGLE-VALUED registry entry
// [design: §4.1, §4.7] — dispatchScm below resolves it via registry.go's
// Single accessor, rather than reusing dispatch.go's list-oriented Dispatch
// helper (whose job is disambiguating among 0..N registered backends,
// something a single-valued entry never needs).
//
// Each of the four verbs below is a targeted op (resolves to the one
// backend registered under connector.scm) and uses the Tier-1 targeted-op
// exit-code scheme (0/4/1) via outcome.go's TargetedExitCode — this file
// calls the dispatcher and hands TargetedExitCode the raw per-call
// result/error it got back; it never decides the exit code itself
// [design: §4.5].
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

func newScmCmd() *cobra.Command {
	scmCmd := &cobra.Command{
		Use:   "scm",
		Short: "Local git SCM capability commands (worktrees, branch detection)",
	}
	scmCmd.AddCommand(newScmWorktreeCmd())
	scmCmd.AddCommand(newScmBranchCmd())
	return scmCmd
}

func newScmWorktreeCmd() *cobra.Command {
	worktreeCmd := &cobra.Command{
		Use:   "worktree",
		Short: "Local git worktree management",
	}
	worktreeCmd.AddCommand(newScmWorktreeAddCmd())
	worktreeCmd.AddCommand(newScmWorktreeRemoveCmd())
	worktreeCmd.AddCommand(newScmWorktreeListCmd())
	return worktreeCmd
}

func newScmWorktreeAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <branch-or-ref>",
		Short: "Add a local git worktree for a branch or ref (never a PR number)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			resp, dispatchErr := dispatchScm(cmd.Context(), reg, "worktree_add", map[string]string{"branch_or_ref": args[0]})
			return reportScmTargetedOutcome(cmd, resp, dispatchErr)
		},
	}
}

func newScmWorktreeRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <path>",
		Short: "Remove a local git worktree by path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			resp, dispatchErr := dispatchScm(cmd.Context(), reg, "worktree_remove", map[string]string{"path": args[0]})
			return reportScmTargetedOutcome(cmd, resp, dispatchErr)
		},
	}
}

func newScmWorktreeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List local git worktrees",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			// worktree_list is a TARGETED op, not a fan-out: connector.scm
			// is single-valued (unlike issue/ci/pr's list-type ops), so it
			// always resolves to exactly one backend [design: §4.1, §4.5,
			// §4.7].
			resp, dispatchErr := dispatchScm(cmd.Context(), reg, "worktree_list", nil)
			return reportScmTargetedOutcome(cmd, resp, dispatchErr)
		},
	}
}

func newScmBranchCmd() *cobra.Command {
	branchCmd := &cobra.Command{
		Use:   "branch",
		Short: "Local git cwd-to-branch resolution",
	}
	branchCmd.AddCommand(newScmBranchDetectCmd())
	return branchCmd
}

func newScmBranchDetectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "detect [cwd]",
		Short: "Resolve a working directory to its repo and current branch (defaults to the process's own working directory)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd := ""
			if len(args) == 1 {
				cwd = args[0]
			} else {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("pg-connector: resolve cwd: %w", err)
				}
				cwd = wd
			}
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			resp, dispatchErr := dispatchScm(cmd.Context(), reg, "branch_detect", map[string]string{"cwd": cwd})
			return reportScmTargetedOutcome(cmd, resp, dispatchErr)
		},
	}
}

// dispatchScm calls op on the one backend registered under connector.scm,
// resolved via registry.go's Single accessor (connector.scm is
// single-valued — see this file's own doc comment) — then invokes
// pkg/scriptout's caller-side Invoke against it directly. Mirrors
// dispatch.go's Dispatch helper's shape (resolve backend, then Invoke) but
// intentionally does not extend or call into Dispatch itself: Dispatch's
// job is disambiguating among 0..N backends registered under a list-valued
// connector.<type> entry, which a single-valued entry never needs.
func dispatchScm(ctx context.Context, reg *Registry, op string, args any) (*scriptout.Response, error) {
	backend, err := reg.Single("scm")
	if err != nil {
		return nil, err
	}
	if backend == "" {
		return nil, fmt.Errorf("dispatch: no backend registered for connector.scm")
	}
	return scriptout.Invoke(ctx, backend, op, args)
}

// reportScmTargetedOutcome writes resp's wire envelope (its "result" on
// success, or its "error" body per the taxonomy on failure) to stdout —
// matching the wire protocol's own "only stdout JSON is the contract"
// convention — and translates err into pg-connector's own targeted-op exit
// code via outcome.go's TargetedExitCode, never deciding the exit code
// itself [design: §4.5]. A nil resp is a CLI-level failure before any
// well-formed wire response was produced (e.g. no backend registered) —
// that case is returned as a plain error instead, so main's run() reports
// it on stderr rather than fabricating a JSON body.
func reportScmTargetedOutcome(cmd *cobra.Command, resp *scriptout.Response, err error) error {
	if resp == nil {
		return err
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetEscapeHTML(false)
	if encErr := enc.Encode(resp); encErr != nil {
		return encErr
	}
	if code := TargetedExitCode(err); code != 0 {
		return &exitError{code: code}
	}
	return nil
}
