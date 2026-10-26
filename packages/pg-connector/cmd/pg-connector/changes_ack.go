// changes_ack.go: the "pg-connector <type> changes-ack" verb (INTF-WIRE-ACK-2),
// the caller's own acknowledgement of a forwarded changes poll that was made
// with --no-ack. It sends changes_ack {consumer, query?, next_seq} to ONE
// backend and prints the backend's own answer unchanged: the answer is the
// caller's confirmation, so a failure is reported (never swallowed, never
// retried) and the exit code follows a targeted call's scheme (0 ok, 4 not
// found, 1 anything else, invalid_argument being a 1).
//
// It is an id-less write on one backend, so with more than one backend
// registered for the type and no --backend it fails invalid_argument before any
// wire call, and a backend that does not declare owns_changes is refused
// (invalid_argument naming the instance) without touching any ledger. --query is
// optional exactly as on changes for an owning backend: omitted, no query
// argument is sent.
package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// newChangesAckCmd builds the "changes-ack" verb for entityType; every type
// that has a changes verb registers it next to that verb.
func newChangesAckCmd(entityType string) *cobra.Command {
	var query, consumer, nextSeqText string
	cmd := &cobra.Command{
		Use:   "changes-ack",
		Short: fmt.Sprintf("Acknowledge a %s changes poll made with --no-ack, at the next_seq its source row carried", entityType),
		Args:  cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "the backend to acknowledge on (required when more than one is registered under connector."+entityType+", since the acknowledgement is an id-less write on one backend)")
	cmd.Flags().StringVar(&query, "query", "", "named query the poll ran (optional, exactly as on changes for a backend that owns its changes: omitted, no query is sent)")
	cmd.Flags().StringVar(&consumer, "consumer", "", "consumer id whose position is acknowledged (required)")
	cmd.Flags().StringVar(&nextSeqText, "next-seq", "", "the sources[].next_seq of the poll to acknowledge, a non-negative integer (required)")
	_ = cmd.MarkFlagRequired("consumer")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		report := func(resp *scriptout.Response, err error) error {
			return writeTargetedResult(cmd, resp, err, humanizeChangesAck)
		}
		nextSeq, err := strconv.ParseInt(nextSeqText, 10, 64)
		if err != nil || nextSeq < 0 {
			return report(nil, scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("changes-ack: --next-seq must be a non-negative integer, got %q", nextSeqText)))
		}
		reg, err := LoadRegistry()
		if err != nil {
			return report(nil, err)
		}
		backends, err := resolveBackends(reg, entityType)
		if err != nil {
			return report(nil, err)
		}
		var backend string
		switch {
		case *backendFlag != "":
			b, pinErr := pinBackend(entityType, backends, *backendFlag)
			if pinErr != nil {
				return report(nil, scriptout.WrapError(scriptout.ErrInvalidArgument, pinErr.Error()))
			}
			backend = b
		case len(backends) > 1:
			return report(nil, scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("changes-ack: %d backends registered for connector.%s; --backend is required to name the one to acknowledge on (registered: %v)", len(backends), entityType, backends)))
		default:
			backend = backends[0]
		}
		owns, ownsErr := backendOwnsChanges(cmd.Context(), reg, backend)
		if ownsErr != nil {
			return report(nil, fmt.Errorf("changes-ack: capabilities of %s instance %q: %w", entityType, backend, ownsErr))
		}
		if !owns {
			return report(nil, scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("%s instance %q does not own changes (its capabilities do not declare owns_changes): changes-ack exists only for a backend that owns its changes", entityType, backend)))
		}
		resp, callErr := invokeOne(cmd.Context(), reg, backend, "changes_ack", ackCallArgs(consumer, query, nextSeq))
		return report(resp, callErr)
	}
	return cmd
}

// humanizeChangesAck formats a changes-ack answer for --output human.
func humanizeChangesAck(result json.RawMessage) (string, error) {
	var ack schema.ChangesAckResult
	if err := scriptout.Decode(result, &ack); err != nil {
		return "", err
	}
	return fmt.Sprintf("acknowledged consumer %q at position %d", ack.Consumer, ack.AckedSeq), nil
}
