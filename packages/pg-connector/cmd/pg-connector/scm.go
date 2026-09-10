// scm.go: the "pg-connector scm" CLI verb group, built by the "generic scm
// entity/capability" packet on top of the Tier-1 core's registry/dispatcher
// and outcome-reporting helper. pg-connector remains the only user-facing
// CLI surface — scm is one of its verb groups, never a separate binary
// (interfaces.md's INTF-CLI).
//
// Unlike pr/issue/ci, connector.scm is a SINGLE-VALUED registry entry
// (INV-REG-1) — dispatchScm below resolves it via registry.go's
// Single accessor, rather than reusing dispatch.go's list-oriented Dispatch
// helper (whose job is disambiguating among 0..N registered backends,
// something a single-valued entry never needs).
//
// Each of the four verbs below is a targeted op (resolves to the one
// backend registered under connector.scm) and uses the Tier-1 targeted-op
// exit-code scheme (0/4/1) via outcome.go's TargetedExitCode — this file
// calls the dispatcher and hands TargetedExitCode the raw per-call
// result/error it got back; it never decides the exit code itself
// (INV-EXIT-1).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
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
	cmd := &cobra.Command{
		Use:   "add <branch-or-ref>",
		Short: "Add a local git worktree for a branch or ref (never a PR number)",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend (connector.scm is single-valued today, so this only guards against a stale/mistyped name)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportScmTargetedOutcome(cmd, nil, err, humanizeWorktreeInfo)
		}
		resp, dispatchErr := dispatchScm(cmd.Context(), reg, "worktree_add", map[string]string{"branch_or_ref": args[0]}, *backendFlag)
		return reportScmTargetedOutcome(cmd, resp, dispatchErr, humanizeWorktreeInfo)
	}
	return cmd
}

func newScmWorktreeRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <path>",
		Short: "Remove a local git worktree by path",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend (connector.scm is single-valued today, so this only guards against a stale/mistyped name)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		humanize := func(json.RawMessage) (string, error) {
			return fmt.Sprintf("Worktree removed: %s", args[0]), nil
		}
		reg, err := LoadRegistry()
		if err != nil {
			return reportScmTargetedOutcome(cmd, nil, err, humanize)
		}
		resp, dispatchErr := dispatchScm(cmd.Context(), reg, "worktree_remove", map[string]string{"path": args[0]}, *backendFlag)
		return reportScmTargetedOutcome(cmd, resp, dispatchErr, humanize)
	}
	return cmd
}

func newScmWorktreeListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List local git worktrees",
		Args:  cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend (connector.scm is single-valued today, so this only guards against a stale/mistyped name)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportScmTargetedOutcome(cmd, nil, err, humanizeWorktreeList)
		}
		// worktree_list is a TARGETED op, not a fan-out: connector.scm
		// is single-valued (unlike issue/ci/pr's list-type ops), so it
		// always resolves to exactly one backend (INV-REG-1; INV-EXIT-1).
		resp, dispatchErr := dispatchScm(cmd.Context(), reg, "worktree_list", nil, *backendFlag)
		return reportScmTargetedOutcome(cmd, resp, dispatchErr, humanizeWorktreeList)
	}
	return cmd
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
	cmd := &cobra.Command{
		Use:   "detect [cwd]",
		Short: "Resolve a working directory to its repo and current branch (defaults to the process's own working directory)",
		Args:  cobra.MaximumNArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend (connector.scm is single-valued today, so this only guards against a stale/mistyped name)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
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
			return reportScmTargetedOutcome(cmd, nil, err, humanizeBranchInfo)
		}
		resp, dispatchErr := dispatchScm(cmd.Context(), reg, "branch_detect", map[string]string{"cwd": cwd}, *backendFlag)
		return reportScmTargetedOutcome(cmd, resp, dispatchErr, humanizeBranchInfo)
	}
	return cmd
}

// dispatchScm calls op on the one backend registered under connector.scm,
// resolved via registry.go's Single accessor (connector.scm is
// single-valued — see this file's own doc comment) — then invokes
// pkg/scriptout's caller-side Invoke against it directly, attaching that
// backend's own registered config block (registry.go's BackendConfig) the
// same way dispatch.go's invokeOne does for the list-valued capabilities.
// Mirrors dispatch.go's Dispatch helper's shape (resolve backend, then
// Invoke) but intentionally does not extend or call into Dispatch itself:
// Dispatch's job is disambiguating among 0..N backends registered under a
// list-valued connector.<type> entry, which a single-valued entry never
// needs.
//
// pinned ("" for no pin) is threaded onto every scm verb for CLI-surface
// consistency with pr/issue/ci (design's "id-less op rule" bullet:
// "ci.go/scm.go need the flag even though neither gets list") — since
// connector.scm can register at most one backend today, a non-empty
// pinned only ever VALIDATES that name against the single registered
// backend (or the absence of one); it never changes which backend is
// actually dispatched to.
func dispatchScm(ctx context.Context, reg *Registry, op string, args any, pinned string) (*scriptout.Response, error) {
	backend, err := reg.Single("scm")
	if err != nil {
		return nil, err
	}
	if backend == "" {
		return nil, fmt.Errorf("dispatch: no backend registered for connector.scm")
	}
	if pinned != "" && pinned != backend {
		return nil, fmt.Errorf("dispatch: --backend %q is not registered for connector.scm (registered: %s)", pinned, backend)
	}
	return invokeOne(ctx, reg, backend, op, args)
}

// reportScmTargetedOutcome writes resp's outcome to stdout — in the
// default OutputJSON mode, its wire envelope ("result" on success, or
// "error" per the taxonomy on failure) verbatim, matching the wire
// protocol's own "only stdout JSON is the contract" convention; in
// OutputHuman mode, humanize's formatted rendering instead
// [bead pg2-ox1k6] — see output.go's writeTargetedResult, which this
// delegates to. It translates err into pg-connector's own targeted-op
// exit code via outcome.go's TargetedExitCode, never deciding the exit
// code itself (INV-EXIT-1). A nil resp is a Tier-1 CLI-level failure
// before any well-formed wire response was produced (e.g. no backend
// registered, or an ambiguous multi-backend registration) — rather than
// returning a plain error, writeTargetedResult now builds a synthetic
// error envelope for it via scriptout.ErrorResponse and reports it
// through stdout exactly like a backend-reported failure
// [bug pg2-njx27].
func reportScmTargetedOutcome(cmd *cobra.Command, resp *scriptout.Response, err error, humanize humanizeResult) error {
	return writeTargetedResult(cmd, resp, err, humanize)
}

// formatWorktreeInfo renders one local git worktree's path/branch/ref as
// human-readable text.
func formatWorktreeInfo(w schema.WorktreeInfo) string {
	return fmt.Sprintf("worktree: %s\n  branch: %s\n  ref: %s", w.Path, w.Branch, w.Ref)
}

// humanizeWorktreeInfo formats a `scm worktree add` result
// (schema.WorktreeInfo) for human display.
func humanizeWorktreeInfo(raw json.RawMessage) (string, error) {
	var w schema.WorktreeInfo
	if err := scriptout.Decode(raw, &w); err != nil {
		return "", err
	}
	return formatWorktreeInfo(w), nil
}

// humanizeWorktreeList formats a `scm worktree list` result
// ([]schema.WorktreeInfo) for human display.
func humanizeWorktreeList(raw json.RawMessage) (string, error) {
	var list []schema.WorktreeInfo
	if err := scriptout.Decode(raw, &list); err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "worktrees: (none)", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "worktrees (%d):\n", len(list))
	for i, w := range list {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "  %s (branch=%s, ref=%s)", w.Path, w.Branch, w.Ref)
	}
	return b.String(), nil
}

// humanizeBranchInfo formats a `scm branch detect` result
// (schema.BranchInfo) for human display.
func humanizeBranchInfo(raw json.RawMessage) (string, error) {
	var info schema.BranchInfo
	if err := scriptout.Decode(raw, &info); err != nil {
		return "", err
	}
	return fmt.Sprintf("repo: %s\nbranch: %s", info.Repo, info.Branch), nil
}
