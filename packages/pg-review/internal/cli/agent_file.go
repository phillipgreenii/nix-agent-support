package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/runner"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

// RunEnv is the environment variable the runner sets to name the run of a
// ccpool analyst, so `agent file` and `agent context` find it without --run.
const RunEnv = "PG_REVIEW_RUN"

func init() { registerCommand(groupAgent, newAgentFileCmd) }

func newAgentFileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "file <findings.json>",
		Short: "Validate and file a findings document (- reads stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dry, _ := cmd.Flags().GetBool("dry-run")
			runID, err := resolveRun(cmd)
			if err != nil {
				return err
			}
			var raw []byte
			if args[0] == "-" {
				raw, err = io.ReadAll(cmd.InOrStdin())
			} else {
				raw, err = os.ReadFile(args[0])
			}
			if err != nil {
				return usageErrorf("read findings: %v", err)
			}
			cfg, err := config.Load(config.EnvFromOS(), assetsFS())
			if err != nil {
				return err
			}
			l, err := ledger.Open(cmd.Context())
			if err != nil {
				return err
			}
			defer l.Close()
			res, err := ledger.FileFindings(cmd.Context(), l, cfg, runID, raw, dry)
			if err != nil {
				return mapFileError(err)
			}
			if !dry {
				if err := runner.FinishInteractive(cmd.Context(), l, runID, cfg.Env.CacheHome); err != nil {
					return mapFileError(err)
				}
			}
			return printJSONOrText(cmd, res, fmt.Sprintf("filed %d, deduplicated %d, skipped %d, waiting %d",
				res.Filed, res.Deduplicated, res.Skipped, res.Waiting))
		},
	}
	cmd.Flags().Bool("dry-run", false, "validate and report without filing")
	cmd.Flags().String("run", "", "run id (required for an interactive run; taken from agent context for a ccpool run)")
	return cmd
}

// resolveRun reads --run, else the PG_REVIEW_RUN environment variable.
func resolveRun(cmd *cobra.Command) (int64, error) {
	s, _ := cmd.Flags().GetString("run")
	if s == "" {
		s = os.Getenv(RunEnv)
	}
	if s == "" {
		return 0, usageErrorf("no run: pass --run <id> (or run under a ccpool analyst, which sets %s)", RunEnv)
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, usageErrorf("--run %q is not a run id", s)
	}
	return id, nil
}

// mapFileError maps ledger and validation errors to the exit table.
func mapFileError(err error) error {
	var ve *validate.Error
	switch {
	case errors.As(err, &ve):
		return ExitErrorf(exitcode.ValidationRejected, "%s", ve.Error())
	case errors.Is(err, ledger.ErrNotFound):
		return ExitErrorf(exitcode.ValidationRejected, "run not found")
	case errors.Is(err, ledger.ErrBatchConflict):
		return ExitErrorf(exitcode.ValidationRejected, "%v", err)
	case errors.Is(err, ledger.ErrIntegrity), errors.Is(err, ledger.ErrRunState),
		errors.Is(err, ledger.ErrDuplicate), errors.Is(err, ledger.ErrInvalid),
		errors.Is(err, ledger.ErrSchemaTooNew):
		return ExitErrorf(exitcode.Ledger, "%v", err)
	}
	// A concurrent filer holding the write lock past the busy timeout.
	if s := err.Error(); strings.Contains(s, "database is locked") || strings.Contains(s, "SQLITE_BUSY") {
		return ExitErrorf(exitcode.Ledger, "ledger busy: %v", err)
	}
	return err
}

func printJSONOrText(cmd *cobra.Command, v any, text string) error {
	f, err := OutputFormat(cmd)
	if err != nil {
		return err
	}
	if f == outputJSON {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(b))
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), text)
	return nil
}
