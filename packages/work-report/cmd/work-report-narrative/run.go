package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Generator exit codes (contract version 1).
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
	exitConfig  = 3
)

const (
	contractVersion = 1
	reportFile      = "report.md"
	// maxBaselineBytes is the largest baseline text sent to claude.
	maxBaselineBytes = 4 << 20
	// killGrace is how long claude gets between cancellation and being killed.
	killGrace = 5 * time.Second
)

// envelope is the part of the contract-version-1 envelope this generator
// reads. Unknown fields are ignored so the contract can grow. Entries are
// only counted: the baseline text is the data.
type envelope struct {
	ContractVersion int               `json:"contract_version"`
	Mode            string            `json:"mode"`
	Range           envelopeRange     `json:"range"`
	Entries         []json.RawMessage `json:"entries"`
	Baseline        *string           `json:"baseline"`
	Config          json.RawMessage   `json:"config"`
	OutputDir       string            `json:"output_dir"`
}

type envelopeRange struct {
	Spec      string  `json:"spec"`
	Since     *string `json:"since"`
	Before    string  `json:"before"`
	OpenStart bool    `json:"open_start"`
	Zone      string  `json:"zone"`
}

type manifestFile struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Title       string `json:"title,omitempty"`
}

type manifest struct {
	ContractVersion int            `json:"contract_version"`
	Files           []manifestFile `json:"files"`
	Summary         string         `json:"summary,omitempty"`
	Warnings        []string       `json:"warnings,omitempty"`
}

// run is the whole program with its streams injected; it returns the exit
// code. It writes files only in generate mode.
func run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) int {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "work-report-narrative: reading the envelope: %v\n", err)
		return exitFailure
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		fmt.Fprintf(stderr, "work-report-narrative: the envelope is not valid JSON: %v\n", err)
		return exitUsage
	}
	if env.ContractVersion != contractVersion {
		fmt.Fprintf(stderr, "work-report-narrative: unsupported contract_version %d (this generator speaks %d)\n", env.ContractVersion, contractVersion)
		return exitUsage
	}

	switch env.Mode {
	case "validate":
		return validate(env, stderr)
	case "generate":
		return generate(ctx, env, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "work-report-narrative: unknown mode %q (want generate or validate)\n", env.Mode)
		return exitUsage
	}
}

// reportProblems prints one reason per problem and returns exitConfig.
func reportProblems(stderr io.Writer, problems []string) int {
	for _, p := range problems {
		fmt.Fprintf(stderr, "work-report-narrative: invalid config: %s\n", p)
	}
	return exitConfig
}

// validate checks the config only: no files are written and no external
// service is called.
func validate(env envelope, stderr io.Writer) int {
	if _, problems := parseConfig(env.Config); len(problems) > 0 {
		return reportProblems(stderr, problems)
	}
	return exitOK
}

func generate(ctx context.Context, env envelope, stdout, stderr io.Writer) int {
	cfg, problems := parseConfig(env.Config)
	if len(problems) > 0 {
		return reportProblems(stderr, problems)
	}
	if !filepath.IsAbs(env.OutputDir) {
		fmt.Fprintf(stderr, "work-report-narrative: output_dir %q is not an absolute path\n", env.OutputDir)
		return exitUsage
	}

	var body string
	switch {
	case len(env.Entries) == 0:
		// An empty range is still a report, but there is nothing to
		// summarize: claude is not called.
		body = emptyReport(env.Range)
	case env.Baseline == nil:
		fmt.Fprintln(stderr, "work-report-narrative: the envelope has no baseline text; register this generator with inputs: [baseline]")
		return exitFailure
	case len(*env.Baseline) > maxBaselineBytes:
		fmt.Fprintf(stderr, "work-report-narrative: the baseline text is %d bytes, over the %d byte limit; narrow the range\n", len(*env.Baseline), maxBaselineBytes)
		return exitFailure
	default:
		out, err := callClaude(ctx, cfg, *env.Baseline)
		if err != nil {
			fmt.Fprintf(stderr, "work-report-narrative: %v\n", err)
			return exitFailure
		}
		body = out
	}

	if err := os.WriteFile(filepath.Join(env.OutputDir, reportFile), []byte(body), 0o600); err != nil {
		fmt.Fprintf(stderr, "work-report-narrative: writing %s: %v\n", reportFile, err)
		return exitFailure
	}
	n := len(env.Entries)
	noun := "entries"
	if n == 1 {
		noun = "entry"
	}
	m := manifest{
		ContractVersion: contractVersion,
		Files:           []manifestFile{{Path: reportFile, ContentType: "text/markdown", Title: "Work report"}},
		Summary:         fmt.Sprintf("Summarized %d %s into 1 file.", n, noun),
	}
	if err := json.NewEncoder(stdout).Encode(m); err != nil {
		fmt.Fprintf(stderr, "work-report-narrative: writing the manifest: %v\n", err)
		return exitFailure
	}
	return exitOK
}

// emptyReport is the fixed plain-text report for a range with no entries.
func emptyReport(r envelopeRange) string {
	rng := "the beginning to " + r.Before
	if !r.OpenStart && r.Since != nil {
		rng = *r.Since + " to " + r.Before
	}
	if r.Zone != "" {
		rng += " (" + r.Zone + ")"
	}
	return "Work report\n\nRange: " + rng + "\n\nNo activity was recorded in this range.\n"
}

// callClaude runs claude -p over data and returns its trimmed output plus a
// newline. Any failure (claude absent, non-zero exit, empty output) is an
// error naming the reason; no fallback text is ever produced.
func callClaude(ctx context.Context, cfg narrativeConfig, data string) (string, error) {
	path, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude is not available: %w", err)
	}
	dir, err := claudeWorkDir()
	if err != nil {
		return "", err
	}

	args := []string{"-p"}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	args = append(args, "--append-system-prompt", assemblePrompt(cfg), "--allowed-tools", "")

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.WaitDelay = killGrace
	cmd.Stdin = strings.NewReader(fence(data))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("claude was interrupted: %w", errors.Join(ctx.Err(), err))
		}
		// claude -p reports some failures (for example "Not logged in") on
		// stdout, so fall back to a bounded excerpt of it when stderr is empty.
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = excerpt(strings.TrimSpace(stdout.String()), maxReasonBytes)
		}
		if msg != "" {
			return "", fmt.Errorf("claude failed: %w: %s", err, msg)
		}
		return "", fmt.Errorf("claude failed: %w", err)
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return "", errors.New("claude produced empty output")
	}
	return out + "\n", nil
}

// maxReasonBytes bounds the claude output quoted in a failure reason.
const maxReasonBytes = 500

// excerpt returns s cut to at most n bytes (on a rune boundary).
func excerpt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// claudeWorkDirName is the throwaway working directory's name.
const claudeWorkDirName = "work-report-narrative-cwd"

// claudeWorkDir returns the empty directory claude runs in. claude writes a
// per-working-directory session transcript under ~/.claude/projects (verified
// against the real claude for this packet), so running it in the per-run
// output directory would leave one transcript directory per report and let it
// pick up any CLAUDE.md or settings found there. A fixed, empty directory
// keeps that to a single entry and a predictable, empty context. The
// directory lives under the user's cache directory, or, when that cannot be
// resolved (no HOME), under the temporary directory with the uid in its name
// so another user's directory is never adopted.
func claudeWorkDir() (string, error) {
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d", claudeWorkDirName, os.Getuid()))
	if cache, err := os.UserCacheDir(); err == nil {
		dir = filepath.Join(cache, "work-report", claudeWorkDirName)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating claude's working directory: %w", err)
	}
	return dir, nil
}
