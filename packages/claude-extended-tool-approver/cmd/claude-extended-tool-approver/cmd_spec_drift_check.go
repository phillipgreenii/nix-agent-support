package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/embeddedspecs"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/specdrift"
	"github.com/spf13/cobra"
)

func newSpecDriftCheckCmd() *cobra.Command {
	var embedded, record bool
	var dir string
	cmd := &cobra.Command{
		Use:   "spec-drift-check --embedded",
		Short: "Diff each embedded command spec's flag set against the pinned binary's captured --help (P13/P14)",
		Long: `spec-drift-check is the "--help drift check" (Phase 3 item 2; P13/P14):
it captures each embedded built-in command's LIVE --help output, hashes it
(SHA-256), and either records or compares that hash against
internal/embeddedspecs/data/help-hashes.json.

Default (--check) mode re-captures each command's live --help, hashes it,
and compares against the already-committed, embedded help-hashes.json,
failing (nonzero exit) on any drift -- this is what "nix flake check" runs.

--record mode captures+hashes and WRITES help-hashes.json into --dir
(default assumes running from the claude-extended-tool-approver module
root) -- the committed, generated-data path (mirroring cmd/genspecs's own
"re-runnable generator, not something nix flake check invokes" convention).
Run this locally/in CI where the real tools are on PATH, then run this
repo's formatter/prek over the written file before committing.

cd and export are pure shell builtins with no separate on-PATH binary and
are exempt from capture/compare (recorded/expected as null).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !embedded {
				return fmt.Errorf("spec-drift-check: specify --embedded")
			}
			if record {
				return runSpecDriftRecord(cmd, dir)
			}
			return runSpecDriftCheck(cmd)
		},
	}
	cmd.Flags().BoolVar(&embedded, "embedded", false, "Operate over packet 1.2's embedded built-in specs (internal/embeddedspecs.FS)")
	cmd.Flags().BoolVar(&record, "record", false, "Capture live --help output and WRITE help-hashes.json (default: --check mode, compare against the committed file)")
	cmd.Flags().StringVar(&dir, "dir", "internal/embeddedspecs/data", "Directory to (re)write help-hashes.json into in --record mode (default assumes running from the claude-extended-tool-approver module root)")
	return cmd
}

func runSpecDriftRecord(cmd *cobra.Command, dir string) error {
	hashes, err := specdrift.Record(embeddedspecs.FS, "data", specdrift.CaptureHelp)
	if err != nil {
		return fmt.Errorf("spec-drift-check: %w", err)
	}
	path := filepath.Join(dir, specdrift.HelpHashesFile)
	if err := specdrift.WriteHashes(path, hashes); err != nil {
		return fmt.Errorf("spec-drift-check: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "spec-drift-check: recorded %d hashes to %s\n", len(hashes), path)
	return nil
}

func runSpecDriftCheck(cmd *cobra.Command) error {
	recorded, err := specdrift.LoadHashes(embeddedspecs.FS, "data/"+specdrift.HelpHashesFile)
	if err != nil {
		return fmt.Errorf("spec-drift-check: %w", err)
	}
	drifts, err := specdrift.Check(embeddedspecs.FS, "data", recorded, specdrift.CaptureHelp)
	if err != nil {
		return fmt.Errorf("spec-drift-check: %w", err)
	}
	if len(drifts) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "spec-drift-check: no drift")
		return nil
	}
	for _, d := range drifts {
		fmt.Fprintf(cmd.OutOrStdout(), "spec-drift-check: %s: %s\n", d.Name, d.Reason)
	}
	os.Exit(1)
	return nil
}
