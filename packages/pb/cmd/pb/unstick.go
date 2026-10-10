package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/discover"
	"github.com/phillipgreenii/pb/internal/gate"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/phillipgreenii/pb/internal/unstick"
	"github.com/spf13/cobra"
)

// GateCheckFunc runs the pn:applied gate check (dry-run) for a workspace root.
type GateCheckFunc func(ctx context.Context, root string, now time.Time) (gate.CheckResult, error)

// unstickEnv holds everything the unstick commands take from the outside world,
// so tests can script bd (FakeRunner), pin the clock and redirect /tmp.
type unstickEnv struct {
	Runner    run.Runner
	Now       func() time.Time
	TmpBase   string // parent of auto-allocated work directories
	Getenv    func(string) string
	Getwd     func() (string, error)
	GateCheck GateCheckFunc // nil = in-process gate.Check over Runner
}

func defaultUnstickEnv() unstickEnv {
	return unstickEnv{Runner: run.CLIRunner{}, Now: nowUTC, TmpBase: os.TempDir(), Getenv: os.Getenv, Getwd: os.Getwd}
}

func (e unstickEnv) gateCheck(ctx context.Context, root string, now time.Time) (gate.CheckResult, error) {
	if e.GateCheck != nil {
		return e.GateCheck(ctx, root, now)
	}
	d := gate.CheckDeps{PN: pn.Client{R: e.Runner}, BD: bd.Client{R: e.Runner}, PatchID: patchid.Client{R: e.Runner}}
	// StaleAfter 0 disables stale handling; DryRun makes the check read-only.
	return gate.Check(ctx, d, gate.CheckParams{
		WorkspaceDir: root, DryRun: true, LastN: 100, StaleHandler: "convert-to-human", Now: now,
	})
}

func newUnstickCmd() *cobra.Command {
	return newUnstickCmdWith(defaultUnstickEnv())
}

func newUnstickCmdWith(env unstickEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "unstick",
		Short:        "Deterministic stages of the /pb:unstick-beads sweep",
		SilenceUsage: true,
		Long: `Deterministic stages of the /pb:unstick-beads sweep: inventory and triage
(prepare), follow-up batches (batch), sweep markers (marker) and the closing
report (report). Per-bead judgement stays with the batch workers.

Exit codes: 0 ok; 1 usage, IO or internal error; 2 a bd call failed.`,
	}
	cmd.AddCommand(newUnstickPrepareCmd(env))
	cmd.AddCommand(newUnstickBatchCmd(env))
	cmd.AddCommand(newUnstickMarkerCmd(env))
	cmd.AddCommand(newUnstickReportCmd(env))
	return cmd
}

// nowFlag is the hidden --now override (RFC3339) used by tests and goldens.
type nowFlag struct{ raw string }

func (n *nowFlag) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&n.raw, "now", "", "")
	_ = cmd.Flags().MarkHidden("now")
}

func (n *nowFlag) resolve(env unstickEnv) (time.Time, error) {
	if n.raw == "" {
		return env.Now().UTC().Truncate(time.Second), nil
	}
	t, err := unstick.ParseTime(n.raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("--now: %w", err)
	}
	return t.Truncate(time.Second), nil
}

// resolveRoot returns flag when set, else the workspace root via internal/discover.
func resolveRoot(env unstickEnv, flag string) (string, error) {
	if flag != "" {
		if !filepath.IsAbs(flag) {
			return "", fmt.Errorf("--root must be an absolute path, got %q", flag)
		}
		return flag, nil
	}
	wd, err := env.Getwd()
	if err != nil {
		return "", err
	}
	return discover.WorkspaceRoot(env.Getenv, wd)
}

// requireWorkdir checks that --workdir names an existing sweep directory.
func requireWorkdir(path string) (unstick.Workdir, error) {
	if path == "" {
		return unstick.Workdir{}, fmt.Errorf("--workdir is required")
	}
	if !filepath.IsAbs(path) {
		return unstick.Workdir{}, fmt.Errorf("--workdir must be an absolute path, got %q", path)
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return unstick.Workdir{}, fmt.Errorf("--workdir %q is not an existing directory", path)
	}
	return unstick.Workdir{Path: path}, nil
}

func cmdCtx(cmd *cobra.Command) context.Context {
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
}

// bdFailure marks err as a failed bd call (exit 2).
func bdFailure(err error) error { return newExitError(exitBD, err) }

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
