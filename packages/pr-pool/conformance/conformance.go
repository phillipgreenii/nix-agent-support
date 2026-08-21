// Package conformance is the reusable conformance checker for pr-pool's
// interface message schemas (INV-INTF-2, interfaces.md). It is the executable
// reconciliation mechanism the behavior docs name: pr-pool checks its own
// participants against it, and a downstream implementer (e.g. the ZR set)
// imports and runs the SAME suite against each INTF-ZR-* implementation rather
// than a verbatim peer cross-check (method INV-18, implementer form).
//
// It layers on the raw JSON Schema validation in package schemas with the few
// cross-field rules a structural schema cannot express, plus a reference
// participant and the CLI transport (JSON on stdin -> JSON on stdout, coarse
// exit codes) so the boundary is proven live.
//
// This is bead pg2-hvlyj.13 (plan item 5.1).
package conformance

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/phillipgreenii/pr-pool/schemas"
)

// Check validates a decoded JSON message against its named schema AND the
// cross-field rules that a structural schema cannot express. It is the single
// entry point both pr-pool and a downstream implementer call.
func Check(messageType string, value any) error {
	if err := schemas.Validate(messageType, value); err != nil {
		return err
	}
	if rule := crossFieldRules[messageType]; rule != nil {
		return rule(value)
	}
	return nil
}

// CheckBytes decodes raw JSON then runs Check — the malformed/non-object path.
func CheckBytes(messageType string, data []byte) error {
	if err := schemas.ValidateBytes(messageType, data); err != nil {
		return err
	}
	// Re-decode for the cross-field pass (schemas.ValidateBytes already proved it
	// decodes and matches the structural schema).
	if rule := crossFieldRules[messageType]; rule != nil {
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		return rule(v)
	}
	return nil
}

// MessageTypes returns every message type that has a schema artifact — the set
// the suite iterates. Delegates to the schemas registry so the two never drift.
func MessageTypes() []string { return schemas.Names() }

// crossFieldRules holds the per-message rules the JSON Schema subset cannot
// state (conditional presence, dependent fields).
var crossFieldRules = map[string]func(any) error{
	"store.request": storeRequestRule,
}

// storeRequestRule enforces "value required IFF op==put".
func storeRequestRule(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: store.request is not an object", schemas.ErrValidation)
	}
	op, _ := obj["op"].(string)
	_, hasValue := obj["value"]
	if op == "put" && !hasValue {
		return fmt.Errorf("%w: op=put requires a value", schemas.ErrValidation)
	}
	if op != "put" && hasValue {
		return fmt.Errorf("%w: value present but op=%q (value is only for put)", schemas.ErrValidation, op)
	}
	return nil
}

// IsSchemaError reports whether err is a schema-validation or unknown-version
// error from this checker (for callers that branch on the failure kind).
func IsSchemaError(err error) bool {
	return errors.Is(err, schemas.ErrValidation) || errors.Is(err, schemas.ErrUnknownSchemaVersion)
}
