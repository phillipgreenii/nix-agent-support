// Package schemas holds pg-connector's scriptout wire-envelope schemas
// (bead pg2-7vgn5) — the JSON Schema artifacts backing
// pkg/scriptout/conformance's conformance suite for the generic,
// capability-agnostic wire protocol pkg/scriptout/envelope.go defines
// (Request, Response's success/error branches, Error, and the bespoke
// CapabilitiesResponse shape). It is modeled on pr-pool's own schemas
// package (packages/pr-pool/schemas): same embed-and-interpret approach,
// deliberately reimplemented here (see validate.go) rather than imported —
// pr-pool is a separate Go module (github.com/phillipgreenii/pr-pool) this
// module has no dependency on, and pr-pool itself is left unmodified,
// used only as the reference pattern.
//
// This package is deliberately scoped to the WIRE ENVELOPE only — the
// {op, args} request shell, the {protocolVersion, schemaVersion,
// result|error} response shell, the six-value error taxonomy, and the
// bespoke capabilities shape — not to any one capability's own args/result
// payload shape (pr/issue/ci/scm, already typed in pkg/schema). The
// envelope is what EVERY backend must speak regardless of which capability
// it implements, and is exactly the layer the design doc's Appendix A
// "Wire protocol and testing" flagged as having no independent conformance
// check: a backend's own unit tests only prove its own encode logic
// against its own decode logic, never against a schema no test in that
// backend wrote.
package schemas

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed *.schema.json
var files embed.FS

// ErrValidation is returned for a message that fails its schema.
var ErrValidation = errors.New("schemas: validation failed")

// registry maps a schema name (the file stem, e.g. "request") to its
// compiled schema. Built once from the embedded files.
var registry = mustLoad()

func mustLoad() map[string]*schema {
	reg, err := load()
	if err != nil {
		panic("schemas: " + err.Error())
	}
	return reg
}

func load() (map[string]*schema, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	reg := map[string]*schema{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".schema.json") {
			continue
		}
		b, err := files.ReadFile(name)
		if err != nil {
			return nil, err
		}
		var s schema
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		reg[strings.TrimSuffix(name, ".schema.json")] = &s
	}
	return reg, nil
}

func resolve(name string) (*schema, bool) {
	s, ok := registry[name]
	return s, ok
}

// Names returns the registered schema names (sorted) — the set of message
// types with a schema artifact.
func Names() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Has reports whether a schema named name is registered.
func Has(name string) bool {
	_, ok := registry[name]
	return ok
}

// Validate checks a decoded JSON message value against the named message
// schema. name is one of Names() (e.g. "request", "response-success"). A
// validation failure wraps ErrValidation.
func Validate(name string, value any) error {
	s, ok := registry[name]
	if !ok {
		return fmt.Errorf("%w: no schema named %q", ErrValidation, name)
	}
	if err := s.validate(value, name, resolve); err != nil {
		return fmt.Errorf("%w: %v", ErrValidation, err)
	}
	return nil
}

// ValidateBytes decodes raw JSON and validates it against the named
// schema. A non-object/malformed payload is reported, not crashed.
func ValidateBytes(name string, data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("%w: malformed JSON: %v", ErrValidation, err)
	}
	return Validate(name, v)
}
