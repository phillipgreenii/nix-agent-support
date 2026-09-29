package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"sort"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/embeddedspecs"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/specfmt"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/speclint"
	"github.com/spf13/cobra"
)

// lintFindingJSON is the --format=json wire shape for one speclint.Finding.
type lintFindingJSON struct {
	Severity string `json:"severity"`
	Check    string `json:"check"`
	Spec     string `json:"spec"`
	Field    string `json:"field,omitempty"`
	Detail   string `json:"detail"`
}

func newLintCmd() *cobra.Command {
	var embedded bool
	var userDir, repoDir, format string
	cmd := &cobra.Command{
		Use:   "lint [PATH...]",
		Short: "Lint command specs (packet 1.1's versioned format) for structural and citation rules",
		Long: `lint runs the checks internal/speclint implements (P13/P14) over one or
more command specs: citation presence (thin-citation staging for the
embedded built-ins), the danger-shaped-flag role check, the
UnknownFlagInert justification check, and overrides-conflict reporting.

--embedded lints packet 1.2's 45 generated embedded built-in specs
(internal/embeddedspecs.FS) — this is what "nix flake check" runs.
--user-dir/--repo-dir additionally load specfmt's user-level/repo-level
layers (specfmt.Repository), enabling overrides-conflict detection against
--embedded. Bare PATH arguments each lint one on-disk spec file directly,
outside the Repository/layer machinery (no overrides-conflict detection
across bare files).

Exit status is 1 if any HARD finding is reported, 0 otherwise (WARN
findings, e.g. the interim thin citations on the embedded built-ins, never
fail the exit code).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLint(cmd, embedded, userDir, repoDir, format, args)
		},
	}
	cmd.Flags().BoolVar(&embedded, "embedded", false, "Lint packet 1.2's embedded built-in specs")
	cmd.Flags().StringVar(&userDir, "user-dir", "", "Additionally lint the specfmt user-level layer at DIR")
	cmd.Flags().StringVar(&repoDir, "repo-dir", "", "Additionally lint the specfmt repo-level layer at DIR")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text|json")
	return cmd
}

func runLint(cmd *cobra.Command, embedded bool, userDir, repoDir, format string, paths []string) error {
	if !embedded && userDir == "" && repoDir == "" && len(paths) == 0 {
		return fmt.Errorf("lint: specify --embedded, --user-dir, --repo-dir, or one or more spec file paths")
	}

	var findings []speclint.Finding

	if embedded || userDir != "" || repoDir != "" {
		var embFS fs.FS
		if embedded {
			embFS = embeddedspecs.FS
		}
		// builtin scoping (internal/speclint/doc.go): true only when this
		// invocation lints NOTHING but the embedded layer — the primary,
		// "nix flake check" case. Mixing in --user-dir/--repo-dir is a
		// deliberate override-conflict test scenario, not the staged
		// citation-presence case, so it is treated as fully hand-written for
		// checks 1-3.
		builtin := embedded && userDir == "" && repoDir == ""
		repo := specfmt.NewRepository(embFS, userDir, repoDir)
		report, err := speclint.LintRepository(repo, builtin)
		if err != nil {
			return fmt.Errorf("lint: %w", err)
		}
		findings = append(findings, report.Findings...)
	}

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("lint: reading %s: %w", p, err)
		}
		var s specfmt.Spec
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("lint: decoding %s: %w", p, err)
		}
		if err := specfmt.Validate(s); err != nil {
			findings = append(findings, speclint.Finding{
				Severity: speclint.SeverityHard,
				Check:    speclint.CheckInvalidSpec,
				Spec:     p,
				Detail:   err.Error(),
			})
			continue
		}
		findings = append(findings, speclint.LintSpec(p, s)...)
	}

	printLintFindings(cmd, format, findings)

	for _, f := range findings {
		if f.Severity == speclint.SeverityHard {
			os.Exit(1)
		}
	}
	return nil
}

func printLintFindings(cmd *cobra.Command, format string, findings []speclint.Finding) {
	out := cmd.OutOrStdout()
	switch format {
	case "json":
		wire := make([]lintFindingJSON, len(findings))
		for i, f := range findings {
			wire[i] = lintFindingJSON{
				Severity: string(f.Severity),
				Check:    string(f.Check),
				Spec:     f.Spec,
				Field:    f.Field,
				Detail:   f.Detail,
			}
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(wire)
	default:
		if len(findings) == 0 {
			fmt.Fprintln(out, "lint: no findings")
			return
		}
		sorted := make([]speclint.Finding, len(findings))
		copy(sorted, findings)
		sort.SliceStable(sorted, func(i, j int) bool {
			if sorted[i].Severity != sorted[j].Severity {
				// HARD before WARN.
				return sorted[i].Severity == speclint.SeverityHard
			}
			return sorted[i].Spec < sorted[j].Spec
		})
		for _, f := range sorted {
			fmt.Fprintln(out, f.String())
		}
	}
}
