// list.go: shared helpers behind the pr/issue-only "list" verb (bead
// pg2-2j5ac.28.1) — pr.go's newPrListCmd and
// issue.go's newIssueListCmd each call into these rather than duplicating
// the query_not_recognized/--backend-pinning logic twice. ci and scm never
// get a "list" verb: ci's own EXISTING "ci list <pr-id>" verb (ci.go) is a
// different, PR-keyed op predating this packet, and this packet's own
// Contract explicitly excludes both ci and scm from the new named-query
// list op ("two exemptions: scm has no remote entity, and ci ... keeps
// its existing PR-keyed ci list <pr-id> verb unchanged").
package main

import (
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// queryNotRecognizedReason is the Reason string a "list" fan-out's own
// sources[] row carries when that specific backend answered
// query_not_recognized — deliberately distinct from the generic "not
// applicable" reason an ordinary unknown_op-based disabled row carries
// elsewhere (auth.go/ci.go/attention.go/search.go), so
// allQueryNotRecognized below can tell the two apart precisely: design
// treats "some backends don't recognize this query name" as
// ordinary (excluded from degraded accounting, same as any other
// disabled row) but "EVERY registered backend of the type answered
// query_not_recognized" as a distinct umbrella-level failure
// (invalid_argument), never conflated with "every backend doesn't
// implement list at all."
const queryNotRecognizedReason = "not applicable: query not recognized"

// resolveListBackends returns the backend set a "list" fan-out actually
// queries: every backend registered for entityType (the ordinary
// fan-out case), or exactly the one named by pinned — validated against
// that registration — when pinned is non-empty (design's "id-less
// op rule": "MUST fan out when it can (list)" unless --backend pins one).
func resolveListBackends(reg *Registry, entityType, pinned string) ([]string, error) {
	backends, err := reg.List(entityType)
	if err != nil {
		return nil, err
	}
	if pinned == "" {
		return backends, nil
	}
	for _, b := range backends {
		if b == pinned {
			return []string{b}, nil
		}
	}
	return nil, fmt.Errorf("dispatch: --backend %q is not registered for connector.%s (registered: %v)", pinned, entityType, backends)
}

// classifyListSource turns one backend's raw "list" op error into its
// sources[] row: query_not_recognized becomes disabled with
// queryNotRecognizedReason (excluded from degraded accounting, exactly
// like every other disabled row — INV-EXIT-2's own rule, applied here);
// any other unknown_op is the ordinary "this backend doesn't implement
// list at all" case (the generic "not applicable" reason every other
// fan-out in this package already uses); anything else is degraded.
func classifyListSource(backend string, err error) SourceResult {
	if errors.Is(err, scriptout.ErrQueryNotRecognized) {
		return SourceResult{Source: backend, Status: SourceDisabled, Reason: queryNotRecognizedReason}
	}
	if errors.Is(err, scriptout.ErrUnknownOp) {
		return SourceResult{Source: backend, Status: SourceDisabled, Reason: "not applicable"}
	}
	return SourceResult{Source: backend, Status: SourceDegraded, Reason: err.Error()}
}

// allQueryNotRecognized reports whether sources is non-empty and EVERY
// row is specifically a query_not_recognized disabled row — the one case
// design has the umbrella deviate from the ordinary fan-out
// exit-code scheme (0/2/3, INV-EXIT-1) to fail the whole call as
// invalid_argument instead ("If EVERY registered backend of that type
// answers query_not_recognized, the umbrella fails the whole call with
// invalid_argument"). A zero-backend result (nothing registered at all)
// is deliberately NOT this case — that stays the ordinary "zero sources"
// fan-out failure (exit 3), matching every other fan-out's existing
// zero-backend convention.
func allQueryNotRecognized(sources []SourceResult) bool {
	if len(sources) == 0 {
		return false
	}
	for _, s := range sources {
		if s.Status != SourceDisabled || s.Reason != queryNotRecognizedReason {
			return false
		}
	}
	return true
}

// listQueryNotRecognizedErr builds the umbrella-level error design
// names for the allQueryNotRecognized case: invalid_argument, reported
// through the same synthetic wire envelope every other CLI-level
// pre-dispatch failure uses (scriptout.ErrorResponse, via
// writeTargetedResult's nil-resp path — see reportPrTargetedOutcome/
// reportIssueTargetedOutcome).
func listQueryNotRecognizedErr(entityType, query string) error {
	return scriptout.WrapError(scriptout.ErrInvalidArgument,
		fmt.Sprintf("query %q is not recognized by any registered %s backend", query, entityType))
}

// listExitCode computes the "list" fan-out's own CLI exit code via the
// ordinary fan-out scheme (INV-EXIT-1, FanOutOutcome.ExitCode) — the
// caller (newPrListCmd/newIssueListCmd) is responsible for checking
// allQueryNotRecognized FIRST and taking the distinct invalid_argument
// path (via listQueryNotRecognizedErr + reportXTargetedOutcome, whose own
// TargetedExitCode(err) resolves invalid_argument to 1) before ever
// reaching this function — so this never needs to special-case that
// outcome itself.
func listExitCode(sources []SourceResult) int {
	return FanOutOutcome{Sources: sources}.ExitCode()
}
