// Package conformance is the reusable conformance checker for
// pkg/scriptout's wire envelope (bead pg2-7vgn5, design doc Appendix A
// "Wire protocol and testing"). It layers the cross-field rule a
// structural JSON Schema cannot express (Response's "exactly one of
// result/error, never neither, never both" contract — pkg/scriptout's own
// exec.go documents this as the exact shape of a real historical bug,
// [bug A7]) on top of the raw schema validation in the sibling schemas
// package, plus a Backend abstraction and a driver (see driver.go) so a
// real compiled Tier-2 backend binary, or an in-process DispatchTable
// double, can be run against the whole suite the same way.
//
// Modeled on pr-pool's own conformance package
// (packages/pr-pool/conformance) — same Golden-loader/Check/CheckBytes
// shape — reimplemented rather than imported, since pr-pool is a separate
// Go module.
package conformance

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/schemas"
)

//go:embed testdata/golden/*.json
var goldenFS embed.FS

// Golden loads and decodes the golden fixture for schema name (
// testdata/golden/<name>.json) as a JSON object.
func Golden(name string) (map[string]any, error) {
	b, err := goldenFS.ReadFile("testdata/golden/" + name + ".json")
	if err != nil {
		return nil, err
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// Check validates a decoded JSON value against its named schema. name is
// one of schemas.Names() ("request", "response-success",
// "response-error", "error", "capabilities-response"). Prefer
// CheckResponse for a live Response envelope, whose success/error branch
// is not known ahead of time.
func Check(name string, value any) error {
	return schemas.Validate(name, value)
}

// CheckBytes decodes raw JSON then runs Check — the malformed/non-object
// path.
func CheckBytes(name string, data []byte) error {
	return schemas.ValidateBytes(name, data)
}

// CheckResponse validates value as one wire Response envelope: exactly one
// of its top-level "result"/"error" keys must be present (never neither,
// never both — the exact protocol violation pkg/scriptout/exec.go's own
// Invoke guards against as [bug A7]), and the present branch must satisfy
// its own schema ("response-success" or "response-error"). This is the
// entry point a driver.Backend round trip's reply is checked against,
// since which branch a given reply takes is not known ahead of time the
// way it is for a golden fixture.
func CheckResponse(value any) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: response is not an object", schemas.ErrValidation)
	}
	_, hasResult := obj["result"]
	_, hasError := obj["error"]
	switch {
	case hasResult && hasError:
		return fmt.Errorf("%w: response carries both result and error (protocol violation)", schemas.ErrValidation)
	case hasResult:
		return Check("response-success", value)
	case hasError:
		return Check("response-error", value)
	default:
		return fmt.Errorf("%w: response carries neither result nor error (protocol violation, bug A7)", schemas.ErrValidation)
	}
}

// CheckResponseBytes decodes raw JSON then runs CheckResponse.
func CheckResponseBytes(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("%w: malformed JSON: %v", schemas.ErrValidation, err)
	}
	return CheckResponse(v)
}

// MessageTypes returns every wire-envelope shape that has a schema
// artifact — the set the driver's static golden/negative cases iterate.
// Delegates to the schemas registry so the two never drift.
func MessageTypes() []string { return schemas.Names() }

// IsSchemaError reports whether err is a schema-validation error from this
// checker, for a caller that branches on the failure kind.
func IsSchemaError(err error) bool {
	return errors.Is(err, schemas.ErrValidation)
}
