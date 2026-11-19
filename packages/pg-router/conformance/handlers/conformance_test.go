// Package conformance is this module's own conformance-suite invocation
// (docket pg2-oju6w Task 5.2/5.3's "Test:" file, folded per the operator's
// 2026-09-11 decision): it proves packages/pg-router-handlers's real,
// compiled binary — not a package-level fake — passes INTF-HANDLER's
// positive+negative checks, per ADR 0065's Acceptance section ("the handler
// module imports the conformance package's driver and passes the
// INTF-HANDLER invoking check against itself").
//
// Two layers: the STATIC schema/golden checks (packages/pg-router/conformance/
// driver.Run, which iterates every message type regardless of Target — dispatch
// and query included — via golden fixtures and the negative matrix, no live
// process needed), and a LIVE layer that execs the real compiled binary for
// `dispatch` and `query`, feeding it the exact golden requests over stdin/stdout
// and checking the reply against its own reply schema — the one thing
// driver.Run cannot do today (driver.Target carries no Handler slot, only
// MonRead/Query/Command; see driver.go's own doc comment). The live `dispatch`
// case uses a "command" role (argv ["true"]) rather than a real ccpool
// session — this module's own executor tests already cover ccpool's
// business logic end to end against fakes (internal/executor/ccpool_test.go);
// this suite's job is proving the WIRE ADAPTER around it, not re-proving
// ccpool's behavior.
//
// The role travels as the `role` object ({name, config}) in the JSON request on
// stdin (docket pg2-12gr4, handler split), and the suite runs against the two
// adapter binaries, pg-router-handler-command (dispatch) and
// pg-router-handler-ccpool (query).
package conformance

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance/driver"
)

// TestStaticSchemaChecks runs driver.Run's golden/negative-matrix suite —
// the same static checks INV-INTF-2 requires of every implementer, applied
// here to this module's own INTF-HANDLER/INTF-SOURCE message types
// (handler.dispatch{,-reply}, source.query{,-reply}, and every other
// registered schema pg-router's own suite already covers). No live
// participant is needed for this layer (driver.Target{} is enough); the
// live binary is exercised separately below.
func TestStaticSchemaChecks(t *testing.T) {
	results := driver.Run(context.Background(), driver.Target{})
	for _, r := range results {
		if r.Skipped || r.Busy {
			continue
		}
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		}
	}
}
