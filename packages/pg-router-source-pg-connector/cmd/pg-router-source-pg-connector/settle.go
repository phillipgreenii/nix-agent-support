// settle.go: the "settle" verb and the deferred-ack helpers of "changes".
//
// settle is the acknowledgement half of a --deferred-ack query: pg-router's
// core runs it with a lane and the frontier token it has settled through, and
// it sends pg-connector's changes-ack for that backend. Nothing else
// acknowledges, except the empty-poll case of changes --deferred-ack.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/phillipgreenii/x/exitcode"
	"github.com/spf13/cobra"
)

// laneSet is the succeeded, next_seq-carrying source rows of one
// --deferred-ack changes answer, keyed by backend name (the lane).
type laneSet struct {
	nextSeq map[string]string
	order   []string
}

// newLaneSet validates the source rows of a --deferred-ack answer. A row that
// is disabled is a backend that does not own its changes (the umbrella marks
// it so under --no-ack); a call that mixes owning and non-owning backends is
// refused here, naming the backend, because only an owning backend can be
// acknowledged by position. A row that is not succeeded carries no usable
// position and is never acknowledged.
func newLaneSet(stderr io.Writer, sources []changesSource) (*laneSet, error) {
	l := &laneSet{nextSeq: make(map[string]string)}
	for _, s := range sources {
		if s.Status == "disabled" {
			fmt.Fprintf(stderr, "pg-router-source-pg-connector changes: --deferred-ack refused: backend %q does not own its changes (%s)\n", s.Backend, s.Reason)
			return nil, errUsage
		}
		if s.Status != "succeeded" || isAbsentRaw(s.NextSeq) {
			continue
		}
		var n int64
		if err := json.Unmarshal(s.NextSeq, &n); err != nil || n < 0 {
			fmt.Fprintf(stderr, "pg-router-source-pg-connector changes: backend %q: next_seq is not a non-negative integer\n", s.Backend)
			return nil, errFailed
		}
		l.nextSeq[s.Backend] = strconv.FormatInt(n, 10)
		l.order = append(l.order, s.Backend)
	}
	return l, nil
}

// changesAckArgs builds the pg-connector argv of one acknowledgement.
func changesAckArgs(entityType, query, consumer, nextSeq, backend string) []string {
	args := []string{entityType, "changes-ack", "--consumer", consumer}
	if query != "" {
		args = append(args, "--query", query)
	}
	return append(args, "--next-seq", nextSeq, "--backend", backend, "--output", "json")
}

// ackEmptyPoll acknowledges each succeeded source's next_seq, in answer order.
// Any failed acknowledgement stops the loop and returns its error.
func (l *laneSet) ackEmptyPoll(ctx context.Context, stderr io.Writer, entityType, query, consumer string) error {
	for _, lane := range l.order {
		if err := runAck(ctx, stderr, changesAckArgs(entityType, query, consumer, l.nextSeq[lane], lane)); err != nil {
			return err
		}
	}
	return nil
}

// runAck runs one changes-ack. A skipped (budget-full) call did not
// acknowledge, so it is a failure here, not an empty success.
func runAck(ctx context.Context, stderr io.Writer, args []string) error {
	_, err := invokeOrFail(ctx, stderr, args, nil)
	if errors.Is(err, errBudgetSkipped) {
		return failWith(exitcode.Unavailable)
	}
	return err
}

func newSettleCmd() *cobra.Command {
	var consumer, lane, through string
	cmd := &cobra.Command{
		Use:   "settle <type> [query]",
		Short: "Acknowledge a --deferred-ack changes poll through a settled token (pg-connector changes-ack for one backend)",
		Args:  cobra.RangeArgs(1, 2),
	}
	cmd.Flags().StringVar(&consumer, "consumer", "", "consumer id whose position is acknowledged (required)")
	cmd.Flags().StringVar(&lane, "lane", "", "the lane (backend name) of the settled token (required)")
	cmd.Flags().StringVar(&through, "through", "", "the frontier token, a non-negative decimal integer, to acknowledge through (required)")
	_ = cmd.MarkFlagRequired("consumer")
	_ = cmd.MarkFlagRequired("lane")
	_ = cmd.MarkFlagRequired("through")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		var query string
		if len(args) == 2 {
			query = args[1]
		}
		n, err := strconv.ParseUint(through, 10, 63)
		if err != nil || lane == "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "pg-router-source-pg-connector settle: --through %q must be a non-negative decimal integer and --lane must not be empty\n", through)
			return errUsage
		}
		return runAck(cmd.Context(), cmd.ErrOrStderr(), changesAckArgs(args[0], query, consumer, strconv.FormatUint(n, 10), lane))
	}
	return cmd
}

// changeSeqKey is the row's own seq as the digest key of --deferred-ack, or ""
// when the row carries none.
func changeSeqKey(c changesEntry) string {
	return string(compactRaw(c.Seq))
}
