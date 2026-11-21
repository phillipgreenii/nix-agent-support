// Package conformance is the reusable conformance checker for pg-router's
// interface message schemas (INV-INTF-2, interfaces.md). It is the executable
// reconciliation mechanism the behavior docs name: pg-router checks its own
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
	"errors"

	"github.com/phillipgreenii/pg-router/schemas"
)

// Check validates a decoded JSON message against its named schema AND the
// cross-field rules that a structural schema cannot express: schemas.Check, the
// one implementation production code also uses.
func Check(messageType string, value any) error { return schemas.Check(messageType, value) }

// CheckBytes decodes raw JSON then runs Check: the malformed/non-object path.
func CheckBytes(messageType string, data []byte) error { return schemas.CheckBytes(messageType, data) }

// MessageTypes returns every message type that has a schema artifact — the set
// the suite iterates. Delegates to the schemas registry so the two never drift.
func MessageTypes() []string { return schemas.Names() }

// IsSchemaError reports whether err is a schema-validation or unknown-version
// error from this checker (for callers that branch on the failure kind).
func IsSchemaError(err error) bool {
	return errors.Is(err, schemas.ErrValidation) || errors.Is(err, schemas.ErrUnknownSchemaVersion)
}
