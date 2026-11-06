package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

func init() { registerCommand(groupJob, newJobResumeCmd) }

func newJobResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <lens>",
		Short: "Resume a paused lens",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			lens := args[0]
			l, err := ledger.Open(ctx)
			if err != nil {
				return ExitErrorf(exitcode.Ledger, "%v", err)
			}
			defer l.Close()
			lenses, err := l.Lenses(ctx)
			if err != nil {
				return mapHumanError(err)
			}
			known := false
			for _, x := range lenses {
				known = known || x == lens
			}
			if !known {
				cfg, cerr := config.Load(config.EnvFromOS(), assetsFS())
				if cerr != nil {
					return cerr
				}
				for _, cl := range cfg.Lenses {
					known = known || cl.ID == lens
				}
			}
			if !known {
				return ExitErrorf(exitcode.ValidationRejected, "unknown lens %q", lens)
			}
			if err := l.WithTx(ctx, func(t *ledger.Tx) error { return t.RecordResume(ctx, lens) }); err != nil {
				if errors.Is(err, ledger.ErrNotFound) {
					return ExitErrorf(exitcode.ValidationRejected, "unknown lens %q", lens)
				}
				return mapHumanError(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "lens %s resumed\n", lens)
			return nil
		},
	}
}
