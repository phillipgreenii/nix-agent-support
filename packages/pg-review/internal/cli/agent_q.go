package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/appcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/broker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

func init() { registerCommand(groupAgent, newAgentQCmd) }

// newAgentQCmd is `agent q <provider> <query-name> [--arg value ...]`. Flag
// parsing is off because the extra flags belong to the provider; the command
// takes only --run and --output itself.
func newAgentQCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "q <provider> <query-name> [--arg value ...] [--run <id>]",
		Short: "Run a read-only evidence query through the broker",
		Long: `Runs one query that the provider lists for the run's lens, with the run's scope
and window appended, and prints a JSON array of rows, each carrying the id
Q-nnn in query_id. --scope, --from and --to are refused. The run is --run <id>
or the PG_REVIEW_RUN environment variable. Errors are JSON {"error": "..."}
with exit 4 (refused or over budget) or 7 (provider failure).`,
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var rest []string
			run := ""
			for i := 0; i < len(args); i++ {
				a := args[i]
				switch {
				case a == "-h" || a == "--help":
					return cmd.Help()
				case a == "--run" && i+1 < len(args):
					run = args[i+1]
					i++
				case strings.HasPrefix(a, "--run="):
					run = strings.TrimPrefix(a, "--run=")
				case a == "--output" && i+1 < len(args):
					if args[i+1] != outputJSON {
						return usageErrorf("agent q prints JSON only")
					}
					i++
				case a == "--output=json":
				default:
					rest = append(rest, a)
				}
			}
			if len(rest) == 0 {
				return usageErrorf("usage: agent q <provider> <query-name> [--arg value ...]")
			}
			if run != "" {
				if err := cmd.Flags().Set("run", run); err != nil {
					return err
				}
			}
			runID, err := resolveRun(cmd)
			if err != nil {
				return err
			}
			cfg, err := config.Load(config.EnvFromOS(), assetsFS())
			if err != nil {
				return err
			}
			l, err := ledger.Open(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = l.Close() }()
			res, err := broker.Query(cmd.Context(), broker.Deps{Cfg: cfg, Ledger: l}, runID, rest[0], rest[1:])
			if err != nil {
				return brokerError(cmd, err)
			}
			b, err := json.Marshal(res.Rows)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(b))
			return nil
		},
	}
	// Declared so help lists it and resolveRun finds it; the command parses
	// it by hand because flag parsing is off.
	cmd.Flags().String("run", "", "run id (default: the PG_REVIEW_RUN environment variable)")
	return cmd
}

// brokerError prints the JSON error and maps it to its exit code.
func brokerError(cmd *cobra.Command, err error) error {
	var code int
	var re *broker.RefusedError
	var pe *broker.ProviderError
	switch {
	case errors.As(err, &re):
		code = appcode.Validation
	case errors.As(err, &pe):
		code = appcode.Provider
	default:
		return err
	}
	b, _ := json.Marshal(map[string]string{"error": err.Error()})
	fmt.Fprintln(cmd.OutOrStdout(), string(b))
	return ExitErrorf(code, "")
}
