// Package contract checks the daemon and the CLI against api/openapi.yaml. It
// is a test helper: only _test files import it, so it is not linked into the
// binary. It reads the OpenAPI 3.1 document, whose schemas are JSON Schema
// 2020-12, compiles the response and request schemas on demand and validates a
// body against them.
package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/schemas"
)

const (
	specURL  = "mem://pg-task-focus/api/openapi.json"
	eventURL = "mem://pg-task-focus/schemas/event.schema.json"
)

// Spec is the loaded document.
type Spec struct {
	doc      map[string]any
	compiler *jsonschema.Compiler
}

// Load reads api/openapi.yaml and registers it, and the event schema it
// refers to, with a compiler that reads nothing else.
func Load(tb testing.TB) *Spec {
	tb.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(api.OpenAPI(), &doc); err != nil {
		tb.Fatalf("api/openapi.yaml is not valid YAML: %v", err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		tb.Fatalf("api/openapi.yaml does not convert to JSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(refuse{})
	for url, b := range map[string][]byte{specURL: raw, eventURL: schemas.Event()} {
		v, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			tb.Fatalf("%s: %v", url, err)
		}
		if err := c.AddResource(url, v); err != nil {
			tb.Fatalf("%s: %v", url, err)
		}
	}
	return &Spec{doc: doc, compiler: c}
}

type refuse struct{}

func (refuse) Load(url string) (any, error) {
	return nil, fmt.Errorf("resource %s is not available", url)
}

// Operations lists "METHOD /path" for every operation the document defines,
// sorted.
func (s *Spec) Operations() []string {
	var out []string
	paths, _ := s.doc["paths"].(map[string]any)
	for p, item := range paths {
		for m := range item.(map[string]any) {
			switch m {
			case "get", "post", "put", "patch", "delete":
				out = append(out, strings.ToUpper(m)+" "+p)
			}
		}
	}
	sort.Strings(out)
	return out
}

func esc(s string) string { return strings.NewReplacer("~", "~0", "/", "~1").Replace(s) }

func (s *Spec) node(pointer string) any {
	var cur any = s.doc
	for _, tok := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		tok = strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[tok]
	}
	return cur
}

// resolve follows a "$ref": "#/..." node to its pointer.
func (s *Spec) resolve(pointer string) string {
	for i := 0; i < 5; i++ {
		m, ok := s.node(pointer).(map[string]any)
		if !ok {
			return pointer
		}
		ref, ok := m["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/") {
			return pointer
		}
		pointer = strings.TrimPrefix(ref, "#")
	}
	return pointer
}

func (s *Spec) validate(tb testing.TB, pointer string, body []byte, what string) {
	tb.Helper()
	sch, err := s.compiler.Compile(specURL + "#" + pointer)
	if err != nil {
		tb.Fatalf("%s: the schema at %s does not compile: %v", what, pointer, err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		tb.Errorf("%s: the body is not JSON: %v\n%s", what, err, body)
		return
	}
	if err := sch.Validate(v); err != nil {
		tb.Errorf("%s does not validate against the OpenAPI schema at %s:\n%v\nbody: %s", what, pointer, err, body)
	}
}

// ValidateResponse checks body, the response of operation (method, template)
// with the given status and media type, against the document: the schema of
// the response for that status, else of `default`. It fails the test when the
// operation, the status or the media type is not defined.
func (s *Spec) ValidateResponse(tb testing.TB, method, template string, status int, mediaType string, body []byte) {
	tb.Helper()
	base := "/paths/" + esc(template) + "/" + strings.ToLower(method) + "/responses"
	resp := base + "/" + strconv.Itoa(status)
	if s.node(resp) == nil {
		resp = base + "/default"
	}
	if s.node(resp) == nil {
		tb.Errorf("%s %s: the document defines no response for status %d", method, template, status)
		return
	}
	resp = s.resolve(resp)
	mt, _, _ := strings.Cut(mediaType, ";")
	content := resp + "/content/" + esc(strings.TrimSpace(mt)) + "/schema"
	if s.node(content) == nil {
		tb.Errorf("%s %s: status %d has no %q response in the document", method, template, status, mt)
		return
	}
	s.validate(tb, content, body, fmt.Sprintf("the %d response of %s %s", status, method, template))
}

// ValidateRequest checks a request body against the operation's request schema.
func (s *Spec) ValidateRequest(tb testing.TB, method, template string, body []byte) {
	tb.Helper()
	rb := s.resolve("/paths/" + esc(template) + "/" + strings.ToLower(method) + "/requestBody")
	content := rb + "/content/application~1json/schema"
	if s.node(content) == nil {
		tb.Errorf("%s %s: the document defines no JSON request body", method, template)
		return
	}
	s.validate(tb, content, body, "the request body of "+method+" "+template)
}

// ValidateSchema checks body against components.schemas.<name>.
func (s *Spec) ValidateSchema(tb testing.TB, name string, body []byte) {
	tb.Helper()
	p := "/components/schemas/" + esc(name)
	if s.node(p) == nil {
		tb.Fatalf("the document defines no schema %s", name)
	}
	s.validate(tb, p, body, "the "+name+" document")
}

// Enum returns the string members of components.schemas.<name>.enum.
func (s *Spec) Enum(tb testing.TB, name string) []string {
	tb.Helper()
	m, _ := s.node("/components/schemas/" + esc(name)).(map[string]any)
	list, _ := m["enum"].([]any)
	var out []string
	for _, v := range list {
		if str, ok := v.(string); ok {
			out = append(out, str)
		}
	}
	return out
}

// TemplateOf maps a concrete path onto the template of an operation: the
// first path template of the document that matches it (a {name} segment
// matches any one non-empty segment).
func (s *Spec) TemplateOf(path string) (string, bool) {
	paths, _ := s.doc["paths"].(map[string]any)
	segs := strings.Split(path, "/")
	var keys []string
	for k := range paths {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ts := strings.Split(k, "/")
		if len(ts) != len(segs) {
			continue
		}
		ok := true
		for i := range ts {
			if strings.HasPrefix(ts[i], "{") {
				ok = ok && segs[i] != ""
			} else {
				ok = ok && ts[i] == segs[i]
			}
		}
		if ok {
			return k, true
		}
	}
	return "", false
}
