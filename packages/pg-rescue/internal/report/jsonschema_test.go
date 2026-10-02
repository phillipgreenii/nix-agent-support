package report

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// A deliberately small JSON Schema validator, enough for schemas/report.schema.json
// and no more. It exists so the package needs no third-party dependency (a new
// module would mean new gomod2nix hashes for a test-only convenience). It FAILS
// on any keyword it does not implement, so a schema edit can never silently
// become unchecked.
//
// Supported: $ref (local "#/..." only), type (name or list), enum, const,
// required, properties, additionalProperties (false or a schema), items,
// minItems, minLength, minimum, pattern, oneOf, allOf, if/then/else.
// Annotation keywords ($schema, $id, title, description, $defs) are ignored.

type schemaValidator struct{ root map[string]any }

func newSchemaValidator(t testing.TB, schemaJSON []byte) *schemaValidator {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(schemaJSON, &root); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	return &schemaValidator{root: root}
}

// Validate returns every violation found in the instance (a JSON document), or
// nil when it conforms.
func (v *schemaValidator) Validate(instanceJSON []byte) []string {
	var inst any
	dec := json.NewDecoder(strings.NewReader(string(instanceJSON)))
	dec.UseNumber()
	if err := dec.Decode(&inst); err != nil {
		return []string{"instance is not JSON: " + err.Error()}
	}
	return v.check(v.root, inst, "$")
}

var annotationKeys = []string{"$schema", "$id", "title", "description", "$defs"}

func (v *schemaValidator) check(schema map[string]any, inst any, path string) []string {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, path+": "+fmt.Sprintf(format, a...)) }

	keys := make([]string, 0, len(schema))
	for k := range schema {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sub := schema[k]
		switch k {
		case "$ref":
			target, err := v.resolve(sub.(string))
			if err != nil {
				add("%v", err)
				continue
			}
			errs = append(errs, v.check(target, inst, path)...)
		case "type":
			var names []string
			switch tv := sub.(type) {
			case string:
				names = []string{tv}
			case []any:
				for _, n := range tv {
					names = append(names, n.(string))
				}
			}
			if !slices.ContainsFunc(names, func(n string) bool { return typeMatches(n, inst) }) {
				add("type %s, want %s", typeName(inst), strings.Join(names, "|"))
			}
		case "enum":
			ok := false
			for _, e := range sub.([]any) {
				if jsonEqual(e, inst) {
					ok = true
				}
			}
			if !ok {
				add("%v is not one of %v", inst, sub)
			}
		case "const":
			if !jsonEqual(sub, inst) {
				add("%v != const %v", inst, sub)
			}
		case "required":
			if obj, ok := inst.(map[string]any); ok {
				for _, r := range sub.([]any) {
					if _, present := obj[r.(string)]; !present {
						add("missing required property %q", r)
					}
				}
			}
		case "properties":
			if obj, ok := inst.(map[string]any); ok {
				for name, ps := range sub.(map[string]any) {
					if val, present := obj[name]; present {
						errs = append(errs, v.check(ps.(map[string]any), val, path+"."+name)...)
					}
				}
			}
		case "additionalProperties":
			obj, ok := inst.(map[string]any)
			if !ok {
				continue
			}
			declared, _ := schema["properties"].(map[string]any)
			for name, val := range obj {
				if _, isDeclared := declared[name]; isDeclared {
					continue
				}
				switch ap := sub.(type) {
				case bool:
					if !ap {
						add("unexpected property %q", name)
					}
				case map[string]any:
					errs = append(errs, v.check(ap, val, path+"."+name)...)
				}
			}
		case "items":
			if arr, ok := inst.([]any); ok {
				for i, el := range arr {
					errs = append(errs, v.check(sub.(map[string]any), el, fmt.Sprintf("%s[%d]", path, i))...)
				}
			}
		case "minItems":
			if arr, ok := inst.([]any); ok && float64(len(arr)) < sub.(float64) {
				add("has %d items, want at least %v", len(arr), sub)
			}
		case "minLength":
			if s, ok := inst.(string); ok && float64(len([]rune(s))) < sub.(float64) {
				add("string shorter than %v", sub)
			}
		case "minimum":
			if n, ok := inst.(json.Number); ok {
				f, _ := n.Float64()
				if f < sub.(float64) {
					add("%v < minimum %v", n, sub)
				}
			}
		case "pattern":
			if s, ok := inst.(string); ok && !regexp.MustCompile(sub.(string)).MatchString(s) {
				add("%q does not match %s", s, sub)
			}
		case "oneOf":
			matches := 0
			for _, alt := range sub.([]any) {
				if len(v.check(alt.(map[string]any), inst, path)) == 0 {
					matches++
				}
			}
			if matches != 1 {
				add("matches %d of the oneOf alternatives, want exactly 1", matches)
			}
		case "allOf":
			for _, alt := range sub.([]any) {
				errs = append(errs, v.check(alt.(map[string]any), inst, path)...)
			}
		case "if", "then", "else":
			// handled together below
		default:
			if slices.Contains(annotationKeys, k) {
				continue
			}
			add("validator does not implement schema keyword %q; extend jsonschema_test.go", k)
		}
	}
	if cond, ok := schema["if"].(map[string]any); ok {
		branch := "else"
		if len(v.check(cond, inst, path)) == 0 {
			branch = "then"
		}
		if bs, ok := schema[branch].(map[string]any); ok {
			errs = append(errs, v.check(bs, inst, path)...)
		}
	}
	return errs
}

func (v *schemaValidator) resolve(ref string) (map[string]any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("only local $ref is supported, got %q", ref)
	}
	var cur any = v.root
	for _, part := range strings.Split(ref[2:], "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("$ref %q does not resolve", ref)
		}
		cur = m[part]
	}
	m, ok := cur.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("$ref %q does not resolve to a schema", ref)
	}
	return m, nil
}

func typeMatches(name string, inst any) bool {
	switch name {
	case "null":
		return inst == nil
	case "string":
		_, ok := inst.(string)
		return ok
	case "boolean":
		_, ok := inst.(bool)
		return ok
	case "array":
		_, ok := inst.([]any)
		return ok
	case "object":
		_, ok := inst.(map[string]any)
		return ok
	case "number":
		_, ok := inst.(json.Number)
		return ok
	case "integer":
		n, ok := inst.(json.Number)
		if !ok {
			return false
		}
		f, err := n.Float64()
		return err == nil && f == math.Trunc(f)
	}
	return false
}

func typeName(inst any) string {
	switch inst.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case json.Number:
		return "number"
	}
	return fmt.Sprintf("%T", inst)
}

// jsonEqual compares a schema literal (decoded with float64 numbers) with an
// instance value (decoded with json.Number).
func jsonEqual(schemaVal, inst any) bool {
	if f, ok := schemaVal.(float64); ok {
		n, ok := inst.(json.Number)
		if !ok {
			return false
		}
		g, err := n.Float64()
		return err == nil && f == g
	}
	return fmt.Sprint(schemaVal) == fmt.Sprint(inst) && typeName(schemaVal) == typeName(inst)
}

// The validator is only as good as its own tests: each keyword is shown to
// accept and to reject, and unsupported keywords are shown to fail loudly.
func TestSchemaValidatorKeywords(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		ok     []string
		bad    []string
	}{
		{"type", `{"type":"string"}`, []string{`"a"`}, []string{`1`, `null`, `{}`}},
		{"type list", `{"type":["string","null"]}`, []string{`"a"`, `null`}, []string{`1`, `[]`}},
		{"integer", `{"type":"integer"}`, []string{`3`, `3.0`}, []string{`3.5`, `"3"`}},
		{"enum", `{"enum":["a","b"]}`, []string{`"a"`}, []string{`"c"`, `1`}},
		{"const", `{"const":1}`, []string{`1`}, []string{`2`, `"1"`}},
		{"required", `{"required":["a"]}`, []string{`{"a":1}`}, []string{`{}`, `{"b":1}`}},
		{"properties", `{"properties":{"a":{"type":"string"}}}`, []string{`{}`, `{"a":"x"}`, `{"z":1}`}, []string{`{"a":1}`}},
		{"additionalProperties false", `{"properties":{"a":{}},"additionalProperties":false}`, []string{`{"a":1}`}, []string{`{"b":1}`}},
		{"additionalProperties schema", `{"additionalProperties":{"type":"string"}}`, []string{`{"b":"x"}`}, []string{`{"b":1}`}},
		{"items", `{"items":{"type":"string"}}`, []string{`[]`, `["a"]`}, []string{`["a",1]`}},
		{"minItems", `{"minItems":2}`, []string{`[1,2]`}, []string{`[1]`}},
		{"minLength", `{"minLength":2}`, []string{`"ab"`}, []string{`"a"`}},
		{"minimum", `{"minimum":1}`, []string{`1`, `2`}, []string{`0`, `-3`}},
		{"pattern", `{"pattern":"^a+$"}`, []string{`"aaa"`}, []string{`"ab"`}},
		{"oneOf", `{"oneOf":[{"type":"null"},{"type":"string"}]}`, []string{`null`, `"a"`}, []string{`1`}},
		{"oneOf rejects two matches", `{"oneOf":[{"type":"string"},{"minLength":0}]}`, []string{`1`}, []string{`"a"`}},
		{"allOf", `{"allOf":[{"type":"object"},{"required":["a"]}]}`, []string{`{"a":1}`}, []string{`{}`, `1`}},
		{
			"if then else", `{"if":{"properties":{"k":{"const":"x"}}},"then":{"required":["a"]},"else":{"required":["b"]}}`,
			[]string{`{"k":"x","a":1}`, `{"k":"y","b":1}`},
			[]string{`{"k":"x"}`, `{"k":"y","a":1}`},
		},
		{"ref", `{"$defs":{"s":{"type":"string"}},"properties":{"a":{"$ref":"#/$defs/s"}}}`, []string{`{"a":"x"}`}, []string{`{"a":1}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newSchemaValidator(t, []byte(tc.schema))
			for _, in := range tc.ok {
				if errs := v.Validate([]byte(in)); len(errs) != 0 {
					t.Errorf("%s should be valid: %v", in, errs)
				}
			}
			for _, in := range tc.bad {
				if errs := v.Validate([]byte(in)); len(errs) == 0 {
					t.Errorf("%s should be invalid", in)
				}
			}
		})
	}

	v := newSchemaValidator(t, []byte(`{"format":"date-time"}`))
	if errs := v.Validate([]byte(`"x"`)); len(errs) == 0 || !strings.Contains(errs[0], "does not implement") {
		t.Errorf("an unsupported keyword must fail loudly, got %v", errs)
	}
	v = newSchemaValidator(t, []byte(`{"$ref":"https://example.com/x.json"}`))
	if errs := v.Validate([]byte(`1`)); len(errs) == 0 {
		t.Error("a remote $ref must fail loudly")
	}
	v = newSchemaValidator(t, []byte(`{"$ref":"#/$defs/missing"}`))
	if errs := v.Validate([]byte(`1`)); len(errs) == 0 {
		t.Error("a dangling $ref must fail loudly")
	}
	if errs := v.Validate([]byte(`{`)); len(errs) == 0 {
		t.Error("a non-JSON instance must be reported")
	}
}
