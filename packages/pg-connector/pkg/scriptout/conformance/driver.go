// driver.go: the case-running logic conformance.Run executes against a
// Backend (see backend.go) — golden/negative-matrix static schema checks
// plus a handful of live wire round trips. Modeled on pr-pool's own
// conformance/driver package's Run/Result shape, folded directly into
// this package rather than split into a separate driver subpackage: the
// scriptout wire envelope's case set is much smaller than pr-pool's own
// message set (five schemas, not two dozen), so the extra package
// boundary pr-pool introduced for a later extraction task does not earn
// its keep here.
package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Result is the outcome of one conformance case Run executes. The zero
// value is a clean pass (nil Err, not Skipped).
type Result struct {
	// Name identifies the case, e.g. "golden/request" or
	// "invoking/unknown-op".
	Name string
	// Err is non-nil when the case failed. nil means it passed (or was
	// Skipped, in which case Err is always nil too).
	Err error
	// Skipped reports a case Run declined to attempt because backend does
	// not implement what the case needs (currently only
	// "invoking/capabilities", for a backend with no capabilities entry
	// in its dispatch table).
	Skipped bool
	// SkipReason explains a Skipped case; empty when Skipped is false.
	SkipReason string
}

// Run executes the full conformance suite against backend: a static
// schema/golden case per registered wire-envelope shape (always run,
// needs no live backend), plus the invoking cases that exercise backend
// live over the wire (or in-process, for a TableBackend). backend may be
// nil, in which case the invoking cases are Skipped rather than
// attempted — the static cases are still worth running on their own (e.g.
// as a pure regression check on the schemas/goldens themselves).
//
// A canceled ctx short-circuits Run with a single error Result rather than
// running the case set regardless.
func Run(ctx context.Context, backend Backend) []Result {
	if err := ctx.Err(); err != nil {
		return []Result{{Name: "context", Err: err}}
	}

	var results []Result
	for _, name := range MessageTypes() {
		results = append(results, goldenCase(name), negativeGenericCase(name))
	}
	results = append(results, negativeMatrixCases()...)

	if backend == nil {
		results = append(
			results,
			Result{Name: "invoking/unknown-op", Skipped: true, SkipReason: "no backend given"},
			Result{Name: "invoking/malformed-stdin", Skipped: true, SkipReason: "no backend given"},
			Result{Name: "invoking/capabilities", Skipped: true, SkipReason: "no backend given"},
		)
		return results
	}
	results = append(
		results,
		invokingUnknownOp(ctx, backend),
		invokingMalformedStdin(ctx, backend),
		invokingCapabilities(ctx, backend),
	)
	return results
}

// goldenCase proves the golden example for name validates against its
// own schema.
func goldenCase(name string) Result {
	rname := "golden/" + name
	g, err := Golden(name)
	if err != nil {
		return Result{Name: rname, Err: fmt.Errorf("read golden: %w", err)}
	}
	if err := Check(name, g); err != nil {
		return Result{Name: rname, Err: fmt.Errorf("golden failed its own schema: %w", err)}
	}
	return Result{Name: rname}
}

// negativeGenericCase proves an extra, undeclared field on the golden is
// rejected (every one of the five schemas declares
// additionalProperties:false).
func negativeGenericCase(name string) Result {
	rname := "negative-generic/" + name
	g, err := Golden(name)
	if err != nil {
		return Result{Name: rname, Err: fmt.Errorf("read golden: %w", err)}
	}
	g["totallyUnexpectedField"] = "x"
	if err := Check(name, g); err == nil {
		return Result{Name: rname, Err: fmt.Errorf("%s accepted an additional property", name)}
	}
	return Result{Name: rname}
}

// negativeCase is one row of negativeMatrix: a description and the raw
// JSON that should be rejected.
type negativeCase struct {
	desc string
	json string
}

// negativeMatrix pins at least one independently-violable-constraint
// negative case per schema (mirrors pr-pool's own negativeMatrix
// completeness rule — see TestNegativeMatrixCompleteness in
// conformance_test.go).
var negativeMatrix = map[string][]negativeCase{
	"request": {
		{"missing op", `{"args":{}}`},
		{"op wrong type", `{"op":5}`},
	},
	"response-success": {
		{"missing protocolVersion", `{"result":{}}`},
		{"missing result", `{"protocolVersion":1}`},
		{"protocolVersion wrong type", `{"protocolVersion":"1","result":{}}`},
	},
	"response-error": {
		{"missing protocolVersion", `{"error":{"code":"not_found","message":"x"}}`},
		{"missing error", `{"protocolVersion":1}`},
		{"error.code out of taxonomy", `{"protocolVersion":1,"error":{"code":"bogus","message":"x"}}`},
		{"error missing message", `{"protocolVersion":1,"error":{"code":"not_found"}}`},
	},
	"error": {
		{"missing code", `{"message":"x"}`},
		{"missing message", `{"code":"not_found"}`},
		{"code out of enum", `{"code":"bogus","message":"x"}`},
	},
	"capabilities-response": {
		{"missing protocolVersion", `{"schemaVersions":{},"ops":[]}`},
		{"missing schemaVersions", `{"protocolVersion":1,"ops":[]}`},
		{"missing ops", `{"protocolVersion":1,"schemaVersions":{}}`},
		{"ops wrong item type", `{"protocolVersion":1,"schemaVersions":{},"ops":[5]}`},
	},
}

func negativeMatrixCases() []Result {
	var results []Result
	// Sorted schema-name iteration so Run's output order is deterministic.
	names := make([]string, 0, len(negativeMatrix))
	for name := range negativeMatrix {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, c := range negativeMatrix[name] {
			results = append(results, negativeMatrixCase(name, c))
		}
	}
	return results
}

func negativeMatrixCase(name string, c negativeCase) Result {
	rname := "negative-matrix/" + name + "/" + c.desc
	var v any
	if err := json.Unmarshal([]byte(c.json), &v); err != nil {
		return Result{Name: rname, Err: fmt.Errorf("bad test json: %w", err)}
	}
	if err := Check(name, v); err == nil {
		return Result{Name: rname, Err: fmt.Errorf("%s: %q was ACCEPTED but should be rejected", name, c.desc)}
	}
	return Result{Name: rname}
}

// invokingUnknownOp sends a request for an op no conformant backend
// implements and proves the reply is a well-formed error-branch Response
// whose error.code is exactly "unknown_op" (guaranteed by
// pkg/scriptout/serve.go's own unconditional table-miss branch — true
// for ANY DispatchTable, even an empty one) with the matching exit code
// (ExitCodeForCode("unknown_op"), bead pg2-7vgn5).
func invokingUnknownOp(ctx context.Context, backend Backend) Result {
	name := "invoking/unknown-op"
	out, code, err := backend.Invoke(ctx, []byte(`{"op":"__scriptout_conformance_unknown_op_probe__"}`))
	if err != nil {
		return Result{Name: name, Err: fmt.Errorf("invoke: %w", err)}
	}
	var v any
	if jsonErr := json.Unmarshal(out, &v); jsonErr != nil {
		return Result{Name: name, Err: fmt.Errorf("reply not JSON: %w (stdout=%q)", jsonErr, out)}
	}
	if err := CheckResponse(v); err != nil {
		return Result{Name: name, Err: fmt.Errorf("reply failed response schema: %w", err)}
	}
	obj, _ := v.(map[string]any)
	errObj, ok := obj["error"].(map[string]any)
	if !ok {
		return Result{Name: name, Err: fmt.Errorf("expected an error branch for an unknown op, got %v", v)}
	}
	gotCode, _ := errObj["code"].(string)
	if gotCode != "unknown_op" {
		return Result{Name: name, Err: fmt.Errorf("error.code = %q, want unknown_op", gotCode)}
	}
	if want := scriptout.ExitCodeForCode("unknown_op"); code != want {
		return Result{Name: name, Err: fmt.Errorf("exit code = %d, want %d (unknown_op)", code, want)}
	}
	return Result{Name: name}
}

// invokingMalformedStdin sends bytes that are not valid JSON at all and
// proves the reply is still a well-formed error-branch Response, whose
// exit code matches whatever error.code the body reports — the general
// self-consistency invariant ExitCodeForError/ExitCodeForCode exist to
// guarantee (bead pg2-7vgn5), checked here end to end against a live
// backend rather than only against pkg/scriptout's own unit tests. This
// deliberately does NOT pin which of the six taxonomy codes a malformed
// request must map to — only that whichever one it reports, the exit code
// agrees with it.
func invokingMalformedStdin(ctx context.Context, backend Backend) Result {
	name := "invoking/malformed-stdin"
	out, code, err := backend.Invoke(ctx, []byte("this is not json"))
	if err != nil {
		return Result{Name: name, Err: fmt.Errorf("invoke: %w", err)}
	}
	var v any
	if jsonErr := json.Unmarshal(out, &v); jsonErr != nil {
		return Result{Name: name, Err: fmt.Errorf("reply not JSON: %w (stdout=%q)", jsonErr, out)}
	}
	if err := CheckResponse(v); err != nil {
		return Result{Name: name, Err: fmt.Errorf("reply failed response schema: %w", err)}
	}
	obj, _ := v.(map[string]any)
	errObj, ok := obj["error"].(map[string]any)
	if !ok {
		return Result{Name: name, Err: fmt.Errorf("expected an error branch for malformed stdin, got %v", v)}
	}
	gotCode, _ := errObj["code"].(string)
	if want := scriptout.ExitCodeForCode(gotCode); code != want {
		return Result{Name: name, Err: fmt.Errorf(
			"exit code = %d, but error.code=%q implies exit code %d (exit code and wire body must never disagree, bead pg2-7vgn5)",
			code, gotCode, want,
		)}
	}
	return Result{Name: name}
}

// invokingCapabilities sends the capabilities op and, if backend
// implements it, proves the bespoke (non-enveloped) reply satisfies the
// "capabilities-response" schema with exit 0 [design's bug A6: an error
// envelope must never silently decode as a zero-value capabilities
// success]. A backend that answers unknown_op for capabilities (design
// [design: §4.2] makes this op optional) reports the case Skipped rather
// than a synthetic failure.
func invokingCapabilities(ctx context.Context, backend Backend) Result {
	name := "invoking/capabilities"
	out, exitCode, err := backend.Invoke(ctx, []byte(`{"op":"capabilities"}`))
	if err != nil {
		return Result{Name: name, Err: fmt.Errorf("invoke: %w", err)}
	}
	var v any
	if jsonErr := json.Unmarshal(out, &v); jsonErr != nil {
		return Result{Name: name, Err: fmt.Errorf("reply not JSON: %w (stdout=%q)", jsonErr, out)}
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return Result{Name: name, Err: fmt.Errorf("capabilities reply is not an object: %v", v)}
	}
	if errObj, ok := obj["error"].(map[string]any); ok {
		if errCode, _ := errObj["code"].(string); errCode == "unknown_op" {
			return Result{Name: name, Skipped: true, SkipReason: "backend does not implement the capabilities op"}
		}
		return Result{Name: name, Err: fmt.Errorf("capabilities call failed: %v", errObj)}
	}
	if err := Check("capabilities-response", v); err != nil {
		return Result{Name: name, Err: fmt.Errorf("reply failed capabilities-response schema: %w", err)}
	}
	if exitCode != 0 {
		return Result{Name: name, Err: fmt.Errorf("exit code = %d, want 0", exitCode)}
	}
	return Result{Name: name}
}
