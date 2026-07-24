// Package schemas holds pr-pool's message schemas — the versioned JSON Schema
// artifacts that back every interface's conformance suite (INV-INTF-1 /
// INV-INTF-2, interfaces.md). Each interface message type (INTF-SOURCE,
// INTF-HANDLER, INTF-MON, INTF-STORE, INTF-CLI) has a schema here; the embedded
// files ARE the authoritative artifacts and the reusable conformance checker
// (package conformance) validates messages against them.
//
// This is bead pg2-hvlyj.13 (plan item 5.1). The validator is a compact,
// self-contained interpreter of the JSON Schema SUBSET these message shapes
// use — deliberately no external dependency, keeping the core minimal
// (GOAL-MIN-1) and avoiding a gomod2nix/vendoring change. Supported keywords:
// type, required, properties, additionalProperties(false), enum, const, items,
// minItems, minimum, maximum, oneOf, and "$ref" resolved BY NAME against the
// registry (e.g. {"$ref": "event"}).
package schemas

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// schema is the decoded form of one JSON Schema document (the subset we support).
type schema struct {
	Ref                  string             `json:"$ref"`
	Type                 string             `json:"type"`
	Required             []string           `json:"required"`
	Properties           map[string]*schema `json:"properties"`
	AdditionalProperties *bool              `json:"additionalProperties"`
	Enum                 []any              `json:"enum"`
	Const                any                `json:"const"`
	Items                *schema            `json:"items"`
	MinItems             *int               `json:"minItems"`
	Minimum              *float64           `json:"minimum"`
	Maximum              *float64           `json:"maximum"`
	OneOf                []*schema          `json:"oneOf"`

	// hasConst records whether "const" was present (nil is a legal const value).
	hasConst bool
}

// resolver looks a named schema up in the registry so "$ref" can point at a
// shared definition (e.g. the reusable "event" schema).
type resolver func(name string) (*schema, bool)

// validate checks value against s, returning a path-qualified error on the first
// violation (deterministic: properties are visited in sorted order).
func (s *schema) validate(value any, path string, resolve resolver) error {
	if s.Ref != "" {
		target, ok := resolve(s.Ref)
		if !ok {
			return fmt.Errorf("%s: unresolved $ref %q", path, s.Ref)
		}
		return target.validate(value, path, resolve)
	}

	if s.hasConst {
		if !jsonEqual(value, s.Const) {
			return fmt.Errorf("%s: const mismatch: got %v, want %v", path, value, s.Const)
		}
	}
	if len(s.Enum) > 0 {
		matched := false
		for _, e := range s.Enum {
			if jsonEqual(value, e) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: value %v not in enum", path, value)
		}
	}
	if len(s.OneOf) > 0 {
		n := 0
		for _, sub := range s.OneOf {
			if sub.validate(value, path, resolve) == nil {
				n++
			}
		}
		if n != 1 {
			return fmt.Errorf("%s: oneOf matched %d schemas, want exactly 1", path, n)
		}
	}

	switch s.Type {
	case "":
		// no type constraint
	case "object":
		return s.validateObject(value, path, resolve)
	case "array":
		return s.validateArray(value, path, resolve)
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s: expected string, got %s", path, kindOf(value))
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s: expected boolean, got %s", path, kindOf(value))
		}
	case "integer":
		f, ok := asNumber(value)
		if !ok {
			return fmt.Errorf("%s: expected integer, got %s", path, kindOf(value))
		}
		if f != math.Trunc(f) {
			return fmt.Errorf("%s: expected integer, got fractional %v", path, f)
		}
		return s.checkRange(f, path)
	case "number":
		f, ok := asNumber(value)
		if !ok {
			return fmt.Errorf("%s: expected number, got %s", path, kindOf(value))
		}
		return s.checkRange(f, path)
	default:
		return fmt.Errorf("%s: unsupported schema type %q", path, s.Type)
	}
	return nil
}

func (s *schema) checkRange(f float64, path string) error {
	if s.Minimum != nil && f < *s.Minimum {
		return fmt.Errorf("%s: %v < minimum %v", path, f, *s.Minimum)
	}
	if s.Maximum != nil && f > *s.Maximum {
		return fmt.Errorf("%s: %v > maximum %v", path, f, *s.Maximum)
	}
	return nil
}

func (s *schema) validateObject(value any, path string, resolve resolver) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: expected object, got %s", path, kindOf(value))
	}
	for _, req := range s.Required {
		if _, present := obj[req]; !present {
			return fmt.Errorf("%s: missing required field %q", path, req)
		}
	}
	if s.AdditionalProperties != nil && !*s.AdditionalProperties {
		var extra []string
		for k := range obj {
			if _, declared := s.Properties[k]; !declared {
				extra = append(extra, k)
			}
		}
		if len(extra) > 0 {
			sort.Strings(extra)
			return fmt.Errorf("%s: additional properties not allowed: %s", path, strings.Join(extra, ", "))
		}
	}
	// Deterministic property order for stable error messages.
	names := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if v, present := obj[k]; present {
			if err := s.Properties[k].validate(v, joinPath(path, k), resolve); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *schema) validateArray(value any, path string, resolve resolver) error {
	arr, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s: expected array, got %s", path, kindOf(value))
	}
	if s.MinItems != nil && len(arr) < *s.MinItems {
		return fmt.Errorf("%s: array has %d items, want >= %d", path, len(arr), *s.MinItems)
	}
	if s.Items != nil {
		for i, v := range arr {
			if err := s.Items.validate(v, fmt.Sprintf("%s[%d]", path, i), resolve); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- helpers --------------------------------------------------------------

func asNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64, int, int64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// jsonEqual compares two decoded-JSON scalars for schema const/enum matching.
// Numbers compare by value; everything else by Go equality (scalars only —
// enum/const are scalars in these schemas).
func jsonEqual(a, b any) bool {
	if an, aok := asNumber(a); aok {
		if bn, bok := asNumber(b); bok {
			return an == bn
		}
		return false
	}
	return a == b
}
