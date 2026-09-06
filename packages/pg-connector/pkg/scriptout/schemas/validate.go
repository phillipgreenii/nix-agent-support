// validate.go: the compact JSON Schema SUBSET interpreter this package's
// own embedded schemas actually use. Modeled on pr-pool's own
// schemas/validate.go (packages/pr-pool/schemas/validate.go) — same
// approach (a small self-contained interpreter, no external dependency),
// deliberately a SMALLER keyword set: type, required, properties,
// additionalProperties(false), enum, items, and "$ref" resolved BY NAME
// against the registry. pr-pool's own richer message set additionally
// needed const/oneOf/pattern/minItems/minimum/maximum; none of the five
// scriptout wire-envelope shapes here (request, response-success,
// response-error, error, capabilities-response) do, so those keywords are
// simply not implemented — add them here, following pr-pool's own
// validate.go as the reference, if a future schema in this package needs
// one.
package schemas

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// schema is the decoded form of one JSON Schema document (the subset we
// support).
type schema struct {
	Ref                  string             `json:"$ref"`
	Type                 string             `json:"type"`
	Required             []string           `json:"required"`
	Properties           map[string]*schema `json:"properties"`
	AdditionalProperties *bool              `json:"additionalProperties"`
	Enum                 []any              `json:"enum"`
	Items                *schema            `json:"items"`
}

// resolver looks a named schema up in the registry so "$ref" can point at
// a shared definition (e.g. this package's "error" schema, referenced from
// "response-error").
type resolver func(name string) (*schema, bool)

// validate checks value against s, returning a path-qualified error on the
// first violation (deterministic: properties are visited in sorted order).
func (s *schema) validate(value any, path string, resolve resolver) error {
	if s.Ref != "" {
		target, ok := resolve(s.Ref)
		if !ok {
			return fmt.Errorf("%s: unresolved $ref %q", path, s.Ref)
		}
		return target.validate(value, path, resolve)
	}

	if len(s.Enum) > 0 && !slices.Contains(s.Enum, value) {
		return fmt.Errorf("%s: value %v not in enum %v", path, value, s.Enum)
	}

	// "type" asserts the value's kind. The object/array keywords are
	// applied afterwards by keyword presence, below.
	switch s.Type {
	case "":
		// no declared type — object/array keywords below still apply when present.
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("%s: expected object, got %s", path, kindOf(value))
		}
	case "array":
		if _, ok := value.([]any); !ok {
			return fmt.Errorf("%s: expected array, got %s", path, kindOf(value))
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s: expected string, got %s", path, kindOf(value))
		}
	case "integer":
		f, ok := asNumber(value)
		if !ok {
			return fmt.Errorf("%s: expected integer, got %s", path, kindOf(value))
		}
		if f != float64(int64(f)) {
			return fmt.Errorf("%s: expected integer, got fractional %v", path, f)
		}
	default:
		return fmt.Errorf("%s: unsupported schema type %q", path, s.Type)
	}

	// Object and array keywords bind by keyword PRESENCE, independent of
	// the declared "type" (matching pr-pool's own validator convention) —
	// a typeless schema still enforces them.
	if obj, ok := value.(map[string]any); ok {
		if err := s.validateObjectConstraints(obj, path, resolve); err != nil {
			return err
		}
	}
	if arr, ok := value.([]any); ok {
		if err := s.validateArrayConstraints(arr, path, resolve); err != nil {
			return err
		}
	}
	return nil
}

// validateObjectConstraints applies the object keywords (required,
// additionalProperties, properties) to an already-typed object.
func (s *schema) validateObjectConstraints(obj map[string]any, path string, resolve resolver) error {
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

// validateArrayConstraints applies the "items" keyword to an already-typed
// array.
func (s *schema) validateArrayConstraints(arr []any, path string, resolve resolver) error {
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
