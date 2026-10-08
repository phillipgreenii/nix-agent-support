// Package schemacheck compiles a JSON Schema (2020-12) and validates JSON
// documents against it. It wraps the third-party validator so the rest of the
// module sees one small surface: every violation of a document is reported at
// once, each with the JSON pointer of the value at fault, and the properties
// a schema annotates as free text can be listed.
package schemacheck

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// freeTextKeyword is the annotation a schema puts on a property that holds
// text the operator typed, as opposed to an identifier, a name or a snapshot
// of the configuration. A validator ignores an unknown keyword, so it changes
// nothing about what validates; it exists so the set of such properties is
// written down and a new one is a deliberate choice.
const freeTextKeyword = "x-free-text"

// Schema is a compiled JSON Schema. It is safe for concurrent use.
type Schema struct {
	name     string
	compiled *jsonschema.Schema
	doc      map[string]any
}

// Violation is one place where a document breaks a schema.
type Violation struct {
	// Pointer is the JSON pointer (RFC 6901) of the offending value, empty for
	// the document itself. For a missing or an unexpected property it is the
	// object that lacks or holds it, and Message names the property.
	Pointer string
	// Message says what is wrong in one sentence.
	Message string
}

// ValidationError lists every violation of a document. It is what Validate
// returns for a document that parses but does not satisfy the schema.
type ValidationError struct {
	Name       string // the schema's name, as given to Compile
	Violations []Violation
}

func (e *ValidationError) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "does not satisfy %s", e.Name)
	for _, v := range e.Violations {
		fmt.Fprintf(&sb, "\n- at %q: %s", v.Pointer, v.Message)
	}
	return sb.String()
}

// Compile compiles raw, a JSON Schema document, under name, which is used in
// messages and as the resource name the schema's own references resolve
// against. The schema MUST be draft 2020-12 and MUST NOT refer to any other
// resource: nothing is read from the file system or the network.
func Compile(name string, raw []byte) (*Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("schema %s is not valid JSON: %w", name, err)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema %s is not a JSON object", name)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(noLoader{})
	url := "mem://pg-task-focus/" + name
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("schema %s: %w", name, err)
	}
	compiled, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("schema %s does not compile: %w", name, err)
	}
	return &Schema{name: name, compiled: compiled, doc: obj}, nil
}

// noLoader refuses every resource the schema has not been given, so a
// reference to a file or a URL fails to compile instead of reading it.
type noLoader struct{}

func (noLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("resource %s is not available: a schema MUST NOT refer to another resource", url)
}

// Validate checks doc, one JSON document, against the schema. A document that
// is not JSON, or has data after the value, is an ordinary error; one that
// breaks the schema is a *ValidationError listing every violation.
func (s *Schema) Validate(doc []byte) error {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return fmt.Errorf("document is not valid JSON: %w", err)
	}
	err = s.compiled.Validate(v)
	if err == nil {
		return nil
	}
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return err
	}
	out := &ValidationError{Name: s.name}
	printer := message.NewPrinter(language.English)
	collect(verr, printer, &out.Violations)
	sort.SliceStable(out.Violations, func(i, j int) bool { return out.Violations[i].Pointer < out.Violations[j].Pointer })
	return out
}

// collect appends the leaves of the error tree: the inner nodes only say which
// keyword or reference the leaves fell under.
func collect(e *jsonschema.ValidationError, p *message.Printer, out *[]Violation) {
	if len(e.Causes) == 0 {
		v := Violation{Pointer: pointer(e.InstanceLocation), Message: e.ErrorKind.LocalizedString(p)}
		if !slices.Contains(*out, v) {
			*out = append(*out, v)
		}
		return
	}
	for _, c := range e.Causes {
		collect(c, p, out)
	}
}

func pointer(tokens []string) string {
	var sb strings.Builder
	for _, t := range tokens {
		sb.WriteByte('/')
		sb.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(t))
	}
	return sb.String()
}

// FreeTextFields lists the paths, within the definition def, of every
// property annotated x-free-text: true. def names an entry of the schema's
// $defs (an event type, for the event schema) or is empty for the schema
// itself. A path joins property names with a dot, writes an array's items as
// [], and an object's variable keys as *: label, kv[].value, fields.reason.
// The result is sorted; an unknown def yields nothing.
func FreeTextFields(s *Schema, def string) []string {
	return s.fields(def, func(prop map[string]any) bool { return prop[freeTextKeyword] == true })
}

// StringFields lists, in the same notation as FreeTextFields, the path of
// every property of the definition whose value is a string, free text or not.
// A test pins the set so a new string property is a deliberate choice.
func StringFields(s *Schema, def string) []string {
	return s.fields(def, func(prop map[string]any) bool { return prop["type"] == "string" })
}

func (s *Schema) fields(def string, pick func(map[string]any) bool) []string {
	root := s.doc
	if def != "" {
		defs, _ := s.doc["$defs"].(map[string]any)
		d, ok := defs[def].(map[string]any)
		if !ok {
			return nil
		}
		root = d
	}
	var out []string
	s.walk(root, "", func(path string, prop map[string]any) {
		if pick(prop) {
			out = append(out, path)
		}
	})
	sort.Strings(out)
	return out
}

// walk visits every property reachable from node through properties, items
// and additionalProperties, following local references.
func (s *Schema) walk(node map[string]any, prefix string, visit func(path string, prop map[string]any)) {
	node = s.resolve(node)
	if props, ok := node["properties"].(map[string]any); ok {
		for name, raw := range props {
			prop, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			prop = s.resolve(prop)
			visit(path, prop)
			s.walk(prop, path, visit)
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		path := prefix + "[]"
		items = s.resolve(items)
		visit(path, items)
		s.walk(items, path, visit)
	}
	if extra, ok := node["additionalProperties"].(map[string]any); ok {
		path := "*"
		if prefix != "" {
			path = prefix + ".*"
		}
		extra = s.resolve(extra)
		visit(path, extra)
		s.walk(extra, path, visit)
	}
}

// resolve follows a local "#/$defs/<name>" reference, keeping the keywords
// written next to the reference. Any other reference is returned unresolved.
func (s *Schema) resolve(node map[string]any) map[string]any {
	for range 8 {
		ref, ok := node["$ref"].(string)
		if !ok {
			return node
		}
		name, ok := strings.CutPrefix(ref, "#/$defs/")
		if !ok {
			return node
		}
		defs, _ := s.doc["$defs"].(map[string]any)
		target, ok := defs[name].(map[string]any)
		if !ok {
			return node
		}
		merged := make(map[string]any, len(node)+len(target))
		for k, v := range target {
			merged[k] = v
		}
		for k, v := range node {
			if k != "$ref" {
				merged[k] = v
			}
		}
		node = merged
	}
	return node
}
