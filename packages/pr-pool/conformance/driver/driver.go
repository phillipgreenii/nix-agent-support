// Package driver is the extracted, importable conformance case runner (Task
// 3.13). It carries the case-running logic package conformance's own tests
// used to run directly against message types (golden fixtures validate, the
// generic additionalProperties negative, the per-message-type negative
// matrix) — moved here so a later phase's own test suite can call
// driver.Run(ctx, target Target) []Result directly instead of only through
// `go test ./conformance/...`. Golden/schema loading itself stays in package
// conformance (Task 3.13 Binding decisions); this package imports conformance
// for that access rather than carrying a second copy.
package driver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/pr-pool/conformance"
)

// Result is the outcome of one conformance case Run executes: a static
// schema/golden check, or an invoking check against a live Target
// participant. The zero value is a clean pass (nil Err, not Skipped).
type Result struct {
	// Name identifies the case, e.g. "golden/event" or "invoking/mon.read".
	Name string
	// Err is non-nil when the case failed. nil means it passed (or was
	// Skipped, in which case Err is always nil too).
	Err error
	// Skipped reports a case Run declined to attempt — either because Target
	// carries no participant for it (mon.read, query) or because the
	// interface it exercises is not realized yet (store, until Task 6.0).
	Skipped bool
	// SkipReason explains a Skipped case; empty when Skipped is false.
	SkipReason string
}

// Target is the set of live participants Run may invoke checks against,
// beyond the static golden/schema checks it always runs. Each field is
// optional — a nil field's invoking check is reported Skipped rather than
// attempted, so a caller that has, say, a live mon.read handler and no query
// source still gets a complete, non-panicking result set (Task 3.13
// Contract).
type Target struct {
	// MonRead, when set, is invoked over the "mon.read" subcommand (INTF-MON
	// pull direction, Task 3.6) to prove the request/reply wire shape holds
	// live against this target, not just against a golden fixture.
	MonRead conformance.Participant
	// Query, when set, is invoked over the "query" subcommand (INTF-SOURCE
	// pull direction) the same way.
	Query conformance.Participant
}

// Run executes every conformance case against target, returning one Result
// per case. It reproduces the schema/golden checks package conformance's own
// tests ran directly before this extraction (golden fixtures validate; the
// generic additionalProperties negative; the per-message-type negative
// matrix) — the identical pass/fail set, through one importable call — plus
// the invoking checks for query and mon.read that target's participants
// enable, and the store check, which ships pre-skipped: INTF-STORE is not
// realized until Phase 6 (Task 3.13 Objective / Binding decisions).
//
// A canceled ctx short-circuits Run with a single error Result rather than
// running the (side-effect-free, but potentially live-network-backed once a
// real Target is wired) case set regardless.
func Run(ctx context.Context, target Target) []Result {
	if err := ctx.Err(); err != nil {
		return []Result{{Name: "context", Err: err}}
	}

	var results []Result
	for _, mt := range conformance.MessageTypes() {
		results = append(results, goldenCase(mt), negativeGenericCase(mt))
	}
	results = append(results, negativeMatrixCases()...)
	results = append(results, invokeMonRead(target.MonRead), invokeQuery(target.Query), storeCase())
	return results
}

// goldenCase reproduces TestGoldenFixturesValidate's case for mt: the golden
// example must validate against its own schema (GOAL-7).
func goldenCase(mt string) Result {
	name := "golden/" + mt
	g, err := conformance.Golden(mt)
	if err != nil {
		return Result{Name: name, Err: fmt.Errorf("read golden: %w", err)}
	}
	if err := conformance.Check(mt, g); err != nil {
		return Result{Name: name, Err: fmt.Errorf("golden failed its own schema: %w", err)}
	}
	return Result{Name: name}
}

// negativeGenericCase reproduces TestNegative_Generic's case for mt: an extra,
// undeclared field on the golden must be rejected (additionalProperties).
func negativeGenericCase(mt string) Result {
	name := "negative-generic/" + mt
	g, err := conformance.Golden(mt)
	if err != nil {
		return Result{Name: name, Err: fmt.Errorf("read golden: %w", err)}
	}
	g["totallyUnexpectedField"] = "x"
	if err := conformance.Check(mt, g); err == nil {
		return Result{Name: name, Err: fmt.Errorf("%s accepted an additional property", mt)}
	}
	return Result{Name: name}
}

// negativeMatrixCases reproduces TestNegative_Matrix: each independently
// violable constraint (required-field-missing, wrong-type,
// enum-out-of-range, …) has a rejecting case.
func negativeMatrixCases() []Result {
	var results []Result
	for mt, cases := range negativeMatrix {
		for _, c := range cases {
			results = append(results, negativeMatrixCase(mt, c))
		}
	}
	return results
}

func negativeMatrixCase(mt string, c negativeCase) Result {
	name := "negative-matrix/" + mt + "/" + c.desc
	var v any
	if err := json.Unmarshal([]byte(c.json), &v); err != nil {
		return Result{Name: name, Err: fmt.Errorf("bad test json: %w", err)}
	}
	if err := conformance.Check(mt, v); err == nil {
		return Result{Name: name, Err: fmt.Errorf("%s: %s was ACCEPTED but should be rejected", mt, c.desc)}
	}
	return Result{Name: name}
}

// negativeCase is one row of negativeMatrix: a description and the raw JSON
// that should be rejected.
type negativeCase struct {
	desc string
	json string
}

// negativeMatrix is TestNegative_Matrix's case table, moved here verbatim
// (Task 3.13's case-running-logic extraction).
var negativeMatrix = map[string][]negativeCase{
	"event": {
		{"missing id", `{"schemaVersion":"1","type":"t"}`},
		{"missing type", `{"schemaVersion":"1","id":"e"}`},
		{"wrong-type payload", `{"id":"e","type":"t","payload":"notobj"}`},
		{"wrong-type expiresAt", `{"id":"e","type":"t","expiresAt":900}`},
		// The duration-valued field is GONE from the event (DEC-EVENT-1), and
		// additionalProperties:false is what makes that a REJECTION rather than a
		// silently-ignored leftover — the one check that would have caught the
		// doc-side deletion never reaching the code (bead pg2-85dv2).
		{"legacy duration field", `{"id":"e","type":"t","ttl":"5m"}`},
	},
	"source.query": {
		{"missing callback", `{"schemaVersion":"1","id":"q"}`},
		{"schemaVersion const mismatch", `{"schemaVersion":"9","id":"q","callback":"c"}`},
		{"wrong-type id", `{"schemaVersion":"1","id":5,"callback":"c"}`},
	},
	"source.query-reply": {
		{"neither branch", `{"schemaVersion":"1","id":"q"}`},
		{"deferred wrong const", `{"schemaVersion":"1","id":"q","deferred":false}`},
	},
	"handler.dispatch": {
		{"missing event", `{"schemaVersion":"1","id":"h"}`},
		{"event missing type", `{"schemaVersion":"1","id":"h","event":{"id":"e"}}`},
		{"event carries the legacy duration field", `{"schemaVersion":"1","id":"h","event":{"id":"e","type":"t","ttl":"5m"}}`},
	},
	"handler.dispatch-reply": {
		{"neither branch", `{"schemaVersion":"1","id":"h"}`},
	},
	"mon.read": {
		{"missing metrics", `{"schemaVersion":"1","id":"m"}`},
		{"metrics wrong item type", `{"schemaVersion":"1","id":"m","metrics":[1,2]}`},
	},
	"mon.read-reply": {
		{"value wrong type", `{"schemaVersion":"1","id":"m","values":[{"name":"x","value":"y"}]}`},
	},
	"mon.update": {
		{"missing value", `{"schemaVersion":"1","id":"m","name":"x"}`},
	},
	"mon.update-reply": {
		{"missing accepted", `{"schemaVersion":"1","id":"m"}`},
	},
	"store.request": {
		{"enum op out of range", `{"schemaVersion":"1","id":"s","op":"purge","key":"k"}`},
		{"missing key", `{"schemaVersion":"1","id":"s","op":"get"}`},
	},
	"store.reply": {
		{"wrong-type ok", `{"schemaVersion":"1","id":"s","ok":"yes"}`},
	},
	"cli.ingest-event": {
		{"missing events", `{"schemaVersion":"1","id":"t"}`},
		{"empty events (minItems)", `{"schemaVersion":"1","id":"t","events":[]}`},
		{"event missing id", `{"schemaVersion":"1","id":"t","events":[{"type":"t"}]}`},
	},
	"cli.ingest-event-reply": {
		{"accepted wrong type", `{"schemaVersion":"1","id":"t","accepted":"1","rejected":[]}`},
		{"rejected item missing reason", `{"schemaVersion":"1","id":"t","accepted":0,"rejected":[{"id":"e"}]}`},
	},
	"cli.push-inject": {
		{"missing type (event ref)", `{"schemaVersion":"1","id":"e"}`},
		{"legacy duration field (event ref)", `{"schemaVersion":"1","id":"e","type":"t","ttl":"5m"}`},
	},
	"cli.status-reply": {
		{"delivery missing handler", `{"schemaVersion":"1","deliveries":[{"id":"h","event":"e"}],"queues":[],"config":{"sources":0,"handlers":0}}`},
		{"delivery carries the removed state field", `{"schemaVersion":"1","deliveries":[{"id":"h","handler":"r","event":"e","state":"running"}],"queues":[],"config":{"sources":0,"handlers":0}}`},
		{"queue depth wrong type", `{"schemaVersion":"1","deliveries":[],"queues":[{"type":"t","depth":"3"}],"config":{"sources":0,"handlers":0}}`},
		{"core carries an unknown field", `{"schemaVersion":"1","deliveries":[],"queues":[],"config":{"sources":0,"handlers":0},"core":{"state":"started","pid":1,"extra":"x"}}`},
		{"gate missing set", `{"schemaVersion":"1","deliveries":[],"queues":[],"config":{"sources":0,"handlers":0},"gates":[{"name":"quota_paused"}]}`},
		{"activity entry missing outcome", `{"schemaVersion":"1","deliveries":[],"queues":[],"config":{"sources":0,"handlers":0},"activity":[{"seq":1,"startedAt":"2026-09-01T00:00:00Z","type":"t"}]}`},
		{"activityDropped wrong type", `{"schemaVersion":"1","deliveries":[],"queues":[],"config":{"sources":0,"handlers":0},"activityDropped":"yes"}`},
	},
	"cli.status": {
		{"schemaVersion const mismatch", `{"schemaVersion":"9"}`},
		{"since wrong type", `{"schemaVersion":"1","since":"5"}`},
		{"since negative-shaped (not an integer)", `{"schemaVersion":"1","since":-1.5}`},
	},
	"cli.error": {
		{"missing error", `{"schemaVersion":"1"}`},
		{"error wrong type", `{"schemaVersion":"1","error":5}`},
	},
	"cli.self-status": {
		{"missing participantId", `{"schemaVersion":"1","id":"t","self":"healthy"}`},
		{"missing self", `{"schemaVersion":"1","id":"t","participantId":"p"}`},
		{"self out of enum", `{"schemaVersion":"1","id":"t","participantId":"p","self":"mostly-fine"}`},
	},
	"cli.self-status-reply": {
		{"missing accepted", `{"schemaVersion":"1","id":"t"}`},
		{"accepted wrong type", `{"schemaVersion":"1","id":"t","accepted":"yes"}`},
	},
	"cli.register": {
		{"missing id", `{"schemaVersion":"1","kind":"handler"}`},
		{"missing kind", `{"schemaVersion":"1","id":"role-triage"}`},
		{"kind out of enum", `{"schemaVersion":"1","id":"role-triage","kind":"orchestrator"}`},
		{"self out of enum", `{"schemaVersion":"1","id":"role-triage","kind":"handler","self":"mostly-fine"}`},
	},
	"cli.register-reply": {
		{"missing accepted", `{"schemaVersion":"1","callback":"","selfStatusCallback":"c"}`},
		{"accepted wrong type", `{"schemaVersion":"1","accepted":"yes","callback":"","selfStatusCallback":"c"}`},
		{"missing selfStatusCallback", `{"schemaVersion":"1","accepted":true,"callback":""}`},
	},
}

// invokeMonRead runs the mon.read (INTF-MON pull direction, Task 3.6)
// request/reply pair through participant over the wire — proving live
// conformance against a real handler, not just a golden fixture. A nil
// participant means target carries no mon.read handler to check, so this
// reports Skipped rather than a synthetic failure.
func invokeMonRead(participant conformance.Participant) Result {
	return invokeCheck("invoking/mon.read", "mon.read", "mon.read-reply", participant)
}

// invokeQuery runs the query (INTF-SOURCE pull direction) request/reply pair
// through participant the same way invokeMonRead does for mon.read.
func invokeQuery(participant conformance.Participant) Result {
	return invokeCheck("invoking/query", "source.query", "source.query-reply", participant)
}

// invokeCheck is the shared invoking-check mechanism: send requestType's
// golden fixture to participant over the subcommand, then Check the reply
// against replyType. subcommand and requestType are always equal for the two
// callers above (the wire verb IS the message type name here), but are kept
// distinct parameters since that is not a general rule the driver should
// assume.
func invokeCheck(name, subcommand, replyType string, participant conformance.Participant) Result {
	if participant == nil {
		return Result{Name: name, Skipped: true, SkipReason: "target carries no " + subcommand + " participant"}
	}
	req, err := conformance.Golden(subcommand)
	if err != nil {
		return Result{Name: name, Err: fmt.Errorf("read %s golden: %w", subcommand, err)}
	}
	reply, code, err := conformance.RoundTrip(participant, subcommand, req)
	if err != nil {
		return Result{Name: name, Err: fmt.Errorf("%s round trip: %w", subcommand, err)}
	}
	if code != conformance.ExitOK {
		return Result{Name: name, Err: fmt.Errorf("%s exit=%d, want %d", subcommand, code, conformance.ExitOK)}
	}
	var v any
	if err := json.Unmarshal(reply, &v); err != nil {
		return Result{Name: name, Err: fmt.Errorf("%s reply not JSON: %w", subcommand, err)}
	}
	if err := conformance.Check(replyType, v); err != nil {
		return Result{Name: name, Err: fmt.Errorf("%s reply failed %s schema: %w", subcommand, replyType, err)}
	}
	return Result{Name: name}
}

// storeCase always reports the store check Skipped: INTF-STORE is not
// realized until Phase 6, so there is nothing yet for any Target to carry
// (Task 3.13 Objective / Binding decisions).
func storeCase() Result {
	return Result{Name: "invoking/store", Skipped: true, SkipReason: "enabled by Task 6.0"}
}
