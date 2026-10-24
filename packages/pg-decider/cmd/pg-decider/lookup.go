// lookup.go: the bounded work-item lookup of a pr run, shared by plan and apply
// (docs/behavior/pg-decider/plan-and-apply.md, "The work-item lookup").
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/apply"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/metrics"
	"github.com/phillipgreenii/pg-decider/internal/plan"
)

// lookupEntityType is the one entity type whose runs make the lookup: the PR
// lane's. An issue run keeps its own dedup lookups.
const lookupEntityType = "pr"

// lookupRead reads and bounds the all-status listing (apply.ReadLookup); tests
// swap it. A nil Lookup with a nil error means "no lookup" and never happens in
// production for a pr run.
var lookupRead = func(ctx context.Context, env apply.Env) (*apply.Lookup, error) {
	return apply.ReadLookup(ctx, env)
}

// readRunLookup reads the lookup of a pr run, once, before the rules run. It
// returns (nil, true) for any other entity type. On a lookup that cannot be
// trusted it prints the diagnostic and the failed-closed counters line (with
// the figures known, so the composed command can persist them), and returns
// ok=false; the caller exits with exitcode.LookupUntrusted and writes nothing.
func readRunLookup(ctx context.Context, errOut io.Writer, cfg *config.Config, verb, typ, id string) (*apply.Lookup, bool) {
	if typ != lookupEntityType {
		return nil, true
	}
	lk, err := lookupRead(ctx, apply.Env{Command: applyCommand, Config: cfg, Clock: time.Now, Stderr: errOut})
	if err == nil {
		return lk, true
	}
	reason := "lookup-unavailable"
	var items, ceiling *int
	var le *apply.LookupError
	if errors.As(err, &le) {
		reason, items, ceiling = le.Reason, le.Items, le.Ceiling
	}
	fmt.Fprintf(errOut, "pg-decider: cannot %s %s %s: %v\n", verb, typ, id, err)
	_ = metrics.EmitLookupFailureLine(errOut, typ, id, reason, items, ceiling)
	return nil, false
}

// lookupFigures are the figures the plan prints; nil without a lookup.
func lookupFigures(lk *apply.Lookup) *plan.Lookup {
	if lk == nil {
		return nil
	}
	return &plan.Lookup{Items: lk.Items(), Ceiling: lk.Ceiling()}
}
