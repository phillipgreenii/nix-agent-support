package schemas

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// decode is a test helper: unmarshal a schema document (exercises UnmarshalJSON
// + const-presence detection).
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
		{"boolean ok", `{"type":"boolean"}`, `true`, false},
		{"boolean bad", `{"type":"boolean"}`, `"t"`, true},
		{"integer ok", `{"type":"integer"}`, `5`, false},
		{"integer fractional", `{"type":"integer"}`, `5.5`, true},
		{"integer bad type", `{"type":"integer"}`, `"5"`, true},
		{"number ok", `{"type":"number"}`, `5.5`, false},
		{"number bad", `{"type":"number"}`, `"x"`, true},
		{"minimum violated", `{"type":"number","minimum":0}`, `-1`, true},
		{"maximum violated", `{"type":"number","maximum":1}`, `2`, true},
		{"in range", `{"type":"number","minimum":0,"maximum":1}`, `0.5`, false},
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

// TestValidator_TypelessObjectKeywords proves that object keywords
// (required / properties / additionalProperties) bind by keyword PRESENCE,
// independent of a declared "type". A schema written with NO "type" (e.g. a
// oneOf sub-branch, or a future/edited schema) must still ENFORCE these
// constraints — otherwise it silently accepts anything, a hole in the
// INV-INTF-2 conformance contract.
func TestValidator_TypelessObjectKeywords(t *testing.T) {
	doc := `{"additionalProperties":false,"required":["a"],"properties":{"a":{"type":"string"},"b":{"type":"integer"}}}`
	if err := val(t, doc, `{"a":"x"}`); err != nil {
		t.Fatalf("valid typeless object rejected: %v", err)
	}
	if err := val(t, doc, `{"b":1}`); err == nil {
		t.Fatalf("typeless schema: missing required not caught")
	}
	if err := val(t, doc, `{"a":"x","z":1}`); err == nil {
		t.Fatalf("typeless schema: additionalProperties violation not caught")
	}
	if err := val(t, doc, `{"a":1}`); err == nil {
		t.Fatalf("typeless schema: wrong-type property not caught")
	}
}

// TestValidator_TypelessArrayKeywords proves that array keywords (items /
// minItems) bind by keyword PRESENCE, independent of a declared "type".
func TestValidator_TypelessArrayKeywords(t *testing.T) {
	doc := `{"minItems":1,"items":{"type":"string"}}`
	if err := val(t, doc, `[]`); err == nil {
		t.Fatalf("typeless schema: minItems not caught")
	}
	if err := val(t, doc, `["a",2]`); err == nil {
		t.Fatalf("typeless schema: array item wrong-type not caught")
	}
	if err := val(t, doc, `["a"]`); err != nil {
		t.Fatalf("valid typeless array rejected: %v", err)
	}
}

// TestValidator_Pattern proves the general-purpose "pattern" keyword: a regex
// match against a string value, applying by keyword presence (independent of
// a declared "type", like the object/array keywords above) and a no-op on a
// non-string value (JSON Schema semantics — the keyword only constrains
// strings).
func TestValidator_Pattern(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		val     string
		wantErr bool
	}{
		{"matches", `{"pattern":"^[a-z]+$"}`, `"abc"`, false},
		{"does not match", `{"pattern":"^[a-z]+$"}`, `"ABC"`, true},
		{"typed string, matches", `{"type":"string","pattern":"^\\d+$"}`, `"123"`, false},
		{"typed string, does not match", `{"type":"string","pattern":"^\\d+$"}`, `"12a"`, true},
		{"unanchored: substring match is enough", `{"pattern":"abc"}`, `"xxabcxx"`, false},
		{"unanchored: absent substring fails", `{"pattern":"abc"}`, `"xyz"`, true},
		{"no-op on non-string value", `{"pattern":"^[a-z]+$"}`, `5`, false},
		{"no pattern keyword: no constraint", `{}`, `"anything at all"`, false},
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

// TestValidator_PatternInvalidRegexFailsToLoad proves a malformed regex in a
// schema document fails fast at decode time (UnmarshalJSON), the same moment
// the embedded schemas are decoded at package init (mustLoad) — so a broken
// "pattern" in a real schema file panics loudly there rather than surfacing as
// a confusing failure on first validate call.
func TestValidator_PatternInvalidRegexFailsToLoad(t *testing.T) {
	var s schema
	err := json.Unmarshal([]byte(`{"pattern":"(unterminated"}`), &s)
	if err == nil {
		t.Fatal("invalid regex pattern was accepted at decode time")
	}
}

// TestEventSchema_InstantPattern proves event.schema.json's `pattern` on `at`
// and `expiresAt` (bead pg2-kgydy, plan item 2) rejects malformed-shape values
// while still accepting every RFC3339 form eventqueue.parseInstant's
// time.Parse(time.RFC3339, ...) call accepts — including a numeric offset, a
// bare "Z", and (Go's own parsing quirk: RFC3339's layout omits fractional
// seconds, but time.Parse accepts them anyway) a fractional-second component.
func TestEventSchema_InstantPattern(t *testing.T) {
	valid := []string{
		"2026-07-16T12:00:00Z",
		"2026-07-16T12:00:00+00:00",
		"2026-07-16T12:00:00-05:00",
		"2026-07-16T12:00:00.123Z",
		"2026-07-16T12:00:00.123456789Z",
	}
	for _, at := range valid {
		t.Run("accepts "+at, func(t *testing.T) {
			ev := map[string]any{"id": "e", "type": "t", "at": at, "expiresAt": at}
			if err := Validate("event", ev); err != nil {
				t.Fatalf("well-formed RFC3339 instant %q rejected: %v", at, err)
			}
		})
	}

	invalid := []string{
		"not-a-date",
		"2026-13-45",               // no time component at all — not RFC3339 shape
		"2026-07-16 12:00:00",      // space instead of "T"
		"2026-07-16t12:00:00z",     // lowercase T/Z (Go's parser is case-sensitive here)
		"15m",                      // the duration shape the ttl field used to carry
		"2026-07-16T12:00:00+0000", // offset missing the ":"
	}
	for _, at := range invalid {
		t.Run("rejects "+at, func(t *testing.T) {
			ev := map[string]any{"id": "e", "type": "t", "at": at}
			if err := Validate("event", ev); err == nil {
				t.Fatalf("malformed instant %q was accepted", at)
			}
		})
	}

	// A value that is well-formed RFC3339 SYNTAX (matches the pattern) but
	// calendar-invalid (month 13) is NOT something a regex can catch — that is
	// left to eventqueue.DecodeEvent's time.Parse, by design (see
	// event.schema.json's description and DecodeEvent's doc comment).
	t.Run("pattern accepts calendar-invalid-but-shape-valid month", func(t *testing.T) {
		ev := map[string]any{"id": "e", "type": "t", "at": "2026-13-45T12:00:00Z"}
		if err := Validate("event", ev); err != nil {
			t.Fatalf("shape-valid-but-calendar-invalid instant unexpectedly failed the schema: %v", err)
		}
	})

	// The same pattern applies to expiresAt, not just at.
	t.Run("expiresAt is also constrained", func(t *testing.T) {
		ev := map[string]any{"id": "e", "type": "t", "expiresAt": "not-a-date"}
		if err := Validate("event", ev); err == nil {
			t.Fatal("malformed expiresAt was accepted")
		}
	})
}

func TestValidator_EnumConstOneOfArray(t *testing.T) {
	if err := val(t, `{"enum":["a","b"]}`, `"c"`); err == nil {
		t.Fatalf("enum out-of-range not caught")
	}
	if err := val(t, `{"enum":["a","b"]}`, `"a"`); err != nil {
		t.Fatalf("enum member rejected: %v", err)
	}
	if err := val(t, `{"const":true}`, `false`); err == nil {
		t.Fatalf("const mismatch not caught")
	}
	if err := val(t, `{"const":"1"}`, `"1"`); err != nil {
		t.Fatalf("const match rejected: %v", err)
	}
	// oneOf: exactly one branch must match.
	oneof := `{"oneOf":[{"type":"string"},{"type":"integer"}]}`
	if err := val(t, oneof, `"x"`); err != nil {
		t.Fatalf("oneOf single-match rejected: %v", err)
	}
	if err := val(t, oneof, `true`); err == nil {
		t.Fatalf("oneOf zero-match not caught")
	}
	// array minItems + items type.
	arr := `{"type":"array","minItems":1,"items":{"type":"string"}}`
	if err := val(t, arr, `[]`); err == nil {
		t.Fatalf("minItems not caught")
	}
	if err := val(t, arr, `["a",2]`); err == nil {
		t.Fatalf("array item wrong-type not caught")
	}
	if err := val(t, arr, `["a"]`); err != nil {
		t.Fatalf("valid array rejected: %v", err)
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
	// Unresolved ref is an error.
	bad := decode(t, `{"$ref":"missing"}`)
	if err := bad.validate("x", "$", res); err == nil {
		t.Fatalf("unresolved ref not caught")
	}
}

func TestRegistryLoadsAllInterfaces(t *testing.T) {
	want := []string{
		"event", "source.query", "source.query-reply",
		"handler.dispatch", "handler.dispatch-reply",
		"mon.read", "mon.read-reply", "mon.update", "mon.update-reply",
		"store.request", "store.reply",
		"cli.ingest-event", "cli.ingest-event-reply", "cli.push-inject", "cli.status", "cli.status-reply",
		"cli.self-status", "cli.self-status-reply", "cli.error", "cli.register", "cli.register-reply",
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
	err := ValidateBytes("event", []byte(`{not json`))
	if err == nil || !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation for malformed JSON, got %v", err)
	}
}

func TestCheckSchemaVersion(t *testing.T) {
	if err := CheckSchemaVersion(map[string]any{"schemaVersion": "1"}); err != nil {
		t.Fatalf("current version rejected: %v", err)
	}
	if err := CheckSchemaVersion(map[string]any{}); err == nil {
		t.Fatalf("missing schemaVersion not caught")
	}
	if err := CheckSchemaVersion(map[string]any{"schemaVersion": 1.0}); err == nil {
		t.Fatalf("non-string schemaVersion not caught")
	}
	if err := CheckSchemaVersion("not-an-object"); err == nil {
		t.Fatalf("non-object not caught")
	}
	err := CheckSchemaVersion(map[string]any{"schemaVersion": "99"})
	if !errors.Is(err, ErrUnknownSchemaVersion) {
		t.Fatalf("unknown version not reported as ErrUnknownSchemaVersion: %v", err)
	}
}

// The push-inject schema is a bare $ref to event — validating an event-shaped
// value through it must resolve and pass.
func TestPushInjectRefResolves(t *testing.T) {
	// The DEFAULT event carries neither instant — both are optional (INV-EVT-1) —
	// so this minimal shape MUST validate through the $ref.
	ev := map[string]any{"schemaVersion": "1", "id": "e", "type": "t"}
	if err := Validate("cli.push-inject", ev); err != nil {
		t.Fatalf("push-inject (ref to event) rejected the default event: %v", err)
	}
	withExpiry := map[string]any{"schemaVersion": "1", "id": "e", "type": "t", "expiresAt": "2026-07-16T12:15:00Z"}
	if err := Validate("cli.push-inject", withExpiry); err != nil {
		t.Fatalf("push-inject (ref to event) rejected an event with expiresAt: %v", err)
	}
	if err := Validate("cli.push-inject", map[string]any{"id": "e"}); err == nil || !strings.Contains(err.Error(), "type") {
		t.Fatalf("push-inject should require event field type, got %v", err)
	}
	// The duration-valued field is gone, and the closed object REJECTS it — so a
	// caller still sending the old shape is told, not silently mis-served
	// (DEC-EVENT-1, bead pg2-85dv2).
	legacy := map[string]any{"schemaVersion": "1", "id": "e", "type": "t", "ttl": "5m"}
	if err := Validate("cli.push-inject", legacy); err == nil {
		t.Fatal("push-inject accepted the legacy duration-valued ttl field")
	}
}
