package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/x/exitcode"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// changesAckResult is the one JSON object changes-ack prints.
type changesAckResult struct {
	Consumer string `json:"consumer"`
	Type     string `json:"type"`
	AckedSeq int64  `json:"acked_seq"`
}

// newChangesAckCmd builds `pg-desk <type> changes-ack --consumer NAME
// --next-seq N`: the deferred acknowledgement of a `changes --no-ack` poll,
// mirroring pg-connector's changes-ack. Exit codes: 0 ok (a value at or below
// the cursor is a no-op), 2 a missing or malformed --next-seq or one past the
// highest delivered sequence, 4 an unregistered consumer (checked before the
// upper bound), 1 any other failure. A failure is reported, never retried.
func newChangesAckCmd(entityType string) *cobra.Command {
	var consumer, nextSeqText, output string
	c := &cobra.Command{
		Use:   "changes-ack",
		Short: fmt.Sprintf("Acknowledge a %s changes poll made with --no-ack, advancing the consumer cursor to --next-seq", entityType),
		Long: fmt.Sprintf(`Advance NAME's cursor to --next-seq, the cursor.to of a "pg-desk %s changes
--no-ack" envelope. The cursor never moves backward (a value at or below it is
a harmless no-op) and never passes the highest sequence delivered to NAME
(exit 2). An unregistered consumer exits 4. Prints {"consumer","type","acked_seq"},
where acked_seq is the cursor after the call.`, entityType),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChangesAck(cmd, entityType, consumer, nextSeqText, output)
		},
	}
	c.Flags().StringVar(&consumer, "consumer", "", "Consumer name whose cursor is acknowledged (required)")
	c.Flags().StringVar(&nextSeqText, "next-seq", "", "The cursor.to of the poll to acknowledge, a non-negative integer (required)")
	c.Flags().StringVar(&output, "output", "", "Output format: json (default) or human")
	return c
}

func runChangesAck(cmd *cobra.Command, entityType, consumer, nextSeqText, output string) error {
	usage := func(format string, a ...any) error {
		return newExitError(exitcode.Usage, fmt.Errorf("changes-ack: "+format, a...))
	}
	if strings.TrimSpace(consumer) == "" {
		return usage("--consumer is required")
	}
	if nextSeqText == "" {
		return usage("--next-seq is required")
	}
	nextSeq, err := strconv.ParseInt(nextSeqText, 10, 64)
	if err != nil || nextSeq < 0 {
		return usage("--next-seq must be a non-negative integer, got %q", nextSeqText)
	}
	if output == "" {
		output = os.Getenv(outputEnvVar)
	}
	human := strings.EqualFold(output, "human")
	ctx := cmd.Context()
	st, err := openNewSchemaStore(ctx, "changes-ack")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	cmd.SilenceUsage = true

	unlock, err := st.LockConsumer(entityType, consumer)
	if err != nil {
		return fmt.Errorf("changes-ack: lock consumer %s/%s: %w", entityType, consumer, err)
	}
	acked, err := st.AckCursor(ctx, consumer, entityType, nextSeq)
	unlock()
	switch {
	case errors.Is(err, store.ErrConsumerNotFound):
		return newExitError(exitcode.NotFound, fmt.Errorf("changes-ack: consumer %q is not registered for %s", consumer, entityType))
	case errors.Is(err, store.ErrAckBeyondDelivered):
		return newExitError(exitcode.Usage, fmt.Errorf("changes-ack: --next-seq %d is past the highest delivered sequence of consumer %q", nextSeq, consumer))
	case err != nil:
		return fmt.Errorf("changes-ack: %w", err)
	}
	if human {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "acknowledged consumer %q at position %d\n", consumer, acked)
		return err
	}
	b, err := json.Marshal(changesAckResult{Consumer: consumer, Type: entityType, AckedSeq: acked})
	if err != nil {
		return fmt.Errorf("changes-ack: marshal json: %w", err)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(b))
	return err
}
