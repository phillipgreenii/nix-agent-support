// ci_changes.go: the "pg-connector ci changes" verb, the CI view of a PR
// query's change log (spec "CI changes"). It is the generic forward-only
// "changes" command built for entity type "ci" (newChangesCmd): a ci backend
// that declares owns_changes is forwarded, flushed and acknowledged exactly as
// "pr changes" is (changes_forward.go), its entities (the PR's CI state
// {pr_id, rollup, runs, version, head_sha}) passing through as raw JSON.
//
// "ci" has no list op, so there is no ledger diff to fall back to. A ci backend
// that does NOT declare owns_changes is therefore never forwarded and never
// sent to the ledger path: the umbrella answers for it itself.
//
//   - Its sources[] row is "disabled" (so it stays out of the degraded count and
//     the exit code, like every other not-applicable row) when its capabilities
//     answered without owns_changes, with the reason
//     `ci instance "<name>" does not own changes (its capabilities do not
//     declare owns_changes): changes is not forwarded`.
//   - Its row is "degraded" when its capabilities call itself failed, with the
//     reason `ci instance "<name>" does not own changes (its capabilities call
//     failed: <error>): changes is not forwarded`; an outage must stay visible.
//     Both kinds count as non-owning for the whole-call rule below.
//   - When EVERY queried ci backend (after any --backend pin) is non-owning, the
//     call as a whole fails as invalid_argument through the synthetic-envelope
//     route listQueryNotRecognizedErr uses, its message carrying every
//     instance's reason. No ledger file and no cache entry is written, and the
//     backend receives nothing but its capabilities request.
//   - A registry that mixes owning and non-owning ci backends answers the
//     owning ones normally and carries the others' reasons on their rows.
//
// The daemon's own refusals (the key conflict between (consumer, pr, query) and
// (consumer, ci, query), cursor_expired:) are relayed as the source's failure
// with the message intact: forwardChangesOne does that for every failed call.
package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// newCiChangesCmd is the "ci changes" verb: newChangesCmd's flags exactly.
func newCiChangesCmd() *cobra.Command { return newChangesCmd("ci") }

// ciNotOwningResult is the umbrella's own answer for a ci backend that does not
// own changes. capsErr is the failure of its capabilities call, or nil when the
// call answered without owns_changes.
func ciNotOwningResult(entityType, backend, query string, capsErr error) changesBackendResult {
	res := changesBackendResult{
		backend:     backend,
		key:         LedgerKey{Type: entityType, Backend: backend, Query: query, Instance: ledgerInstanceDiscriminator(entityType)},
		skipAdvance: true,
		notOwning:   true,
	}
	if capsErr != nil {
		res.status = SourceDegraded
		res.reason = fmt.Sprintf("%s instance %q does not own changes (its capabilities call failed: %v): changes is not forwarded", entityType, backend, capsErr)
		return res
	}
	res.status = SourceDisabled
	res.reason = fmt.Sprintf("%s instance %q does not own changes (its capabilities do not declare owns_changes): changes is not forwarded", entityType, backend)
	return res
}

// allNotOwningChanges reports whether results is non-empty and every row is a
// non-owning ci backend's own answer.
func allNotOwningChanges(results []changesBackendResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, r := range results {
		if !r.notOwning {
			return false
		}
	}
	return true
}

// ciNotOwningErr is the whole-call invalid_argument for the all-non-owning case.
func ciNotOwningErr(results []changesBackendResult) error {
	reasons := make([]string, 0, len(results))
	for _, r := range results {
		reasons = append(reasons, r.reason)
	}
	return scriptout.WrapError(scriptout.ErrInvalidArgument, strings.Join(reasons, "; "))
}
