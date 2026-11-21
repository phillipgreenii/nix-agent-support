// settle.go: the settle subcommand the router runs once the items of a
// --deferred-ack poll are accepted: it acknowledges the consumer's cursor
// through a token with "pg-desk <type> changes-ack".
package main

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

// errUsage marks a malformed settle invocation; run maps it to exit 2.
var errUsage = errors.New("pg-router-source-pg-desk: usage error")

func newSettleCmd() *cobra.Command {
	var consumer, lane, through string
	c := &cobra.Command{
		Use:   "settle <type> --consumer <name> --lane <lane> --through <token>",
		Short: "Acknowledge a --deferred-ack poll through a token (pg-desk <type> changes-ack)",
		Long: `Runs "pg-desk <type> changes-ack --consumer <name> --next-seq <token>". The lane
of every item is its entity type, so --lane must equal <type>; --through must
be a non-negative integer. Exit 0 on success, 2 for a usage error, 1 when the
changes-ack call fails.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			stderr := cmd.ErrOrStderr()
			usage := func(format string, a ...any) error {
				fmt.Fprintf(stderr, "pg-router-source-pg-desk settle: "+format+"\n", a...)
				return errUsage
			}
			if consumer == "" {
				return usage("--consumer is required")
			}
			if lane != args[0] {
				return usage("--lane %q is not the entity type %q", lane, args[0])
			}
			if n, err := strconv.ParseInt(through, 10, 64); err != nil || n < 0 {
				return usage("--through %q is not a non-negative integer", through)
			}
			res, err := runPgDesk(cmd.Context(), []string{args[0], "changes-ack", "--consumer", consumer, "--next-seq", through})
			if err != nil {
				fmt.Fprintln(stderr, err)
				return errFailed
			}
			if res.exitCode != 0 {
				if len(res.stderr) > 0 {
					_, _ = stderr.Write(res.stderr)
				}
				fmt.Fprintf(stderr, "pg-router-source-pg-desk: pg-desk changes-ack exited %d\n", res.exitCode)
				return errFailed
			}
			return nil
		},
	}
	c.Flags().StringVar(&consumer, "consumer", "", "pg-desk consumer name whose cursor is acknowledged (required)")
	c.Flags().StringVar(&lane, "lane", "", "the lane of the settled items: the entity type (required)")
	c.Flags().StringVar(&through, "through", "", "the token to acknowledge through, a non-negative integer (required)")
	return c
}
