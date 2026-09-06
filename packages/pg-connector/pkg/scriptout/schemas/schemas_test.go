package schemas

import (
	"encoding/json"
	"errors"
	"testing"
)

// decode is a test helper: unmarshal a schema document (exercises the
// decode path validate.go relies on).
func decode(t *testing.T, doc string) *schema {
	t.Helper()
	var s schema
	if err := json.Unmarshal([]byte(doc), &s); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	return &s
}

func val(t *testing.T, doc, jsonVal string) error {
	t.Helper()
	s := decode(t, doc)
	var v any
	if err := json.Unmarshal([]byte(jsonVal), &v); err != nil {
		t.Fatalf("decode value: %v", err)
	}
	return s.validate(v, "$", func(string) (*schema, bool) { return nil, false })
}

func TestValidator_Types(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		val     string
		wantErr bool
	}{
		{"string ok", `{"type":"string"}`, `"x"`, false},
		{"string bad", `{"type":"string"}`, `3`, true},
		{"integer ok", `{"type":"integer"}`, `5`, false},
		{"integer fractional", `{"type":"integer"}`, `5.5`, true},
		{"integer bad type", `{"type":"integer"}`, `"5"`, true},
		{"object bad", `{"type":"object"}`, `3`, true},
		{"array bad", `{"type":"array"}`, `3`, true},
		{"unsupported type", `{"type":"weird"}`, `1`, true},
		{"no type", `{}`, `1`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := val(t, tc.doc, tc.val)
			if tc.wantErr != (err != nil) {
				t.Fatalf("got err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestValidator_ObjectConstraints(t *testing.T) {
	doc := `{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"string"},"b":{"type":"integer"}}}`
	if err := val(t, doc, `{"a":"x"}`); err != nil {
		t.Fatalf("valid object rejected: %v", err)
	}
	if err := val(t, doc, `{"b":1}`); err == nil {
		t.Fatalf("missing required not caught")
	}
	if err := val(t, doc, `{"a":"x","z":1}`); err == nil {
		t.Fatalf("additionalProperties violation not caught")
	}
	if err := val(t, doc, `{"a":1}`); err == nil {
		t.Fatalf("wrong-type property not caught")
	}
}

// TestValidator_TypelessObjectKeywords proves object keywords bind by
// keyword PRESENCE, independent of a declared "type" — e.g. a $ref target
// with no "type":"object" of its own still enforces required/
// additionalProperties (mirrors pr-pool's own validator convention).
func TestValidator_TypelessObjectKeywords(t *testing.T) {
	doc := `{"additionalProperties":false,"required":["a"],"properties":{"a":{"type":"string"}}}`
	if err := val(t, doc, `{"a":"x"}`); err != nil {
		t.Fatalf("valid typeless object rejected: %v", err)
	}
	if err := val(t, doc, `{}`); err == nil {
		t.Fatalf("typeless schema: missing required not caught")
	}
	if err := val(t, doc, `{"a":"x","z":1}`); err == nil {
		t.Fatalf("typeless schema: additionalProperties violation not caught")
	}
}

func TestValidator_Items(t *testing.T) {
	doc := `{"type":"array","items":{"type":"string"}}`
	if err := val(t, doc, `["a","b"]`); err != nil {
		t.Fatalf("valid array rejected: %v", err)
	}
	if err := val(t, doc, `["a",2]`); err == nil {
		t.Fatalf("array item wrong-type not caught")
	}
	if err := val(t, doc, `[]`); err != nil {
		t.Fatalf("empty array (no minItems constraint) rejected: %v", err)
	}
}

func TestValidator_Enum(t *testing.T) {
	doc := `{"enum":["a","b"]}`
	if err := val(t, doc, `"a"`); err != nil {
		t.Fatalf("enum member rejected: %v", err)
	}
	if err := val(t, doc, `"c"`); err == nil {
		t.Fatalf("enum out-of-range not caught")
	}
}

func TestValidator_Ref(t *testing.T) {
	inner := decode(t, `{"type":"string"}`)
	outer := decode(t, `{"$ref":"thing"}`)
	res := func(name string) (*schema, bool) {
		if name == "thing" {
			return inner, true
		}
		return nil, false
	}
	if err := outer.validate("ok", "$", res); err != nil {
		t.Fatalf("resolved ref rejected valid value: %v", err)
	}
	if err := outer.validate(3, "$", res); err == nil {
		t.Fatalf("resolved ref accepted invalid value")
	}
	bad := decode(t, `{"$ref":"missing"}`)
	if err := bad.validate("x", "$", res); err == nil {
		t.Fatalf("unresolved ref not caught")
	}
}

func TestRegistryLoadsAllEnvelopeSchemas(t *testing.T) {
	want := []string{
		"request", "response-success", "response-error", "error", "capabilities-response",
	}
	for _, name := range want {
		if !Has(name) {
			t.Errorf("schema %q not registered", name)
		}
	}
	if len(Names()) != len(want) {
		t.Errorf("registry has %d schemas, want %d: %v", len(Names()), len(want), Names())
	}
}

func TestValidate_UnknownName(t *testing.T) {
	err := Validate("nope", map[string]any{})
	if err == nil || !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation for unknown schema, got %v", err)
	}
}

func TestValidateBytes_Malformed(t *testing.T) {
	err := ValidateBytes("request", []byte(`{not json`))
	if err == nil || !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation for malformed JSON, got %v", err)
	}
}

// TestResponseErrorSchema_RefResolvesToErrorSchema proves
// "response-error"'s $ref to "error" actually resolves against the real
// embedded registry (not just the synthetic resolver TestValidator_Ref
// exercises above).
func TestResponseErrorSchema_RefResolvesToErrorSchema(t *testing.T) {
	good := map[string]any{
		"protocolVersion": float64(1),
		"error":           map[string]any{"code": "not_found", "message": "x"},
	}
	if err := Validate("response-error", good); err != nil {
		t.Fatalf("well-formed response-error rejected: %v", err)
	}
	bad := map[string]any{
		"protocolVersion": float64(1),
		"error":           map[string]any{"code": "not_a_real_code", "message": "x"},
	}
	if err := Validate("response-error", bad); err == nil {
		t.Fatalf("response-error accepted an out-of-taxonomy error.code")
	}
}
