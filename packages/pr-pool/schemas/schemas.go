package schemas

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

//go:embed *.schema.json
var files embed.FS

// SchemaVersion is the current message-schema envelope version every request and
// reply carries (interfaces.md "common manager contract").
const SchemaVersion = "1"

// ErrValidation is returned for a message that fails its schema.
var ErrValidation = errors.New("schema validation failed")

// ErrUnknownSchemaVersion is returned when a message carries a schemaVersion the
// core cannot handle — reported, not guessed (INV-INTF-1).
var ErrUnknownSchemaVersion = errors.New("unknown schemaVersion")

// registry maps a schema name (the file stem, e.g. "source.query") to its
// compiled schema. It is built once from the embedded files.
var registry = mustLoad()

func (s *schema) UnmarshalJSON(b []byte) error {
	type alias schema
	aux := &struct {
		Const json.RawMessage `json:"const"`
		*alias
	}{alias: (*alias)(s)}
	if err := json.Unmarshal(b, aux); err != nil {
		return err
	}
	if aux.Const != nil {
		s.hasConst = true
		var c any
		if err := json.Unmarshal(aux.Const, &c); err != nil {
			return err
		}
		s.Const = c
	}
	if s.Pattern != "" {
		re, err := regexp.Compile(s.Pattern)
		if err != nil {
			return fmt.Errorf("invalid pattern %q: %w", s.Pattern, err)
		}
		s.pattern = re
	}
	return nil
}

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

// Names returns the registered schema names (sorted) — the set of message types
// with a schema artifact.
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
// schema. name is a message type such as "source.query" or "cli.ingest-event"
// (see Names). A validation failure wraps ErrValidation.
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

// ValidateBytes decodes raw JSON and validates it against the named schema. A
// non-object / malformed payload is reported (interfaces.md "malformed" case).
func ValidateBytes(name string, data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("%w: malformed JSON: %v", ErrValidation, err)
	}
	return Validate(name, v)
}

// CheckSchemaVersion enforces the envelope: the message MUST carry a
// schemaVersion the core handles, else ErrUnknownSchemaVersion (reported, not
// guessed — INV-INTF-1). A missing schemaVersion is a validation error.
func CheckSchemaVersion(value any) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: message is not an object", ErrValidation)
	}
	sv, present := obj["schemaVersion"]
	if !present {
		return fmt.Errorf("%w: missing schemaVersion", ErrValidation)
	}
	s, ok := sv.(string)
	if !ok {
		return fmt.Errorf("%w: schemaVersion must be a string", ErrValidation)
	}
	if s != SchemaVersion {
		return fmt.Errorf("%w: %q (this core handles %q)", ErrUnknownSchemaVersion, s, SchemaVersion)
	}
	return nil
}
