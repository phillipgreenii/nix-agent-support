package deskapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/pkg/deskapi"
)

// allowedDeps lists the packages outside the standard library that this
// contract package may depend on. It is empty.
var allowedDeps = map[string]bool{}

// TestClosure fails when go list -deps of this package contains anything
// outside the standard library and the allowed list.
func TestClosure(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	const self = "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/pkg/deskapi"
	for _, p := range strings.Fields(out.String()) {
		if p == self || allowedDeps[p] {
			continue
		}
		t.Errorf("deskapi depends on %s, outside the standard library and the allowed list", p)
	}
}

func golden(t *testing.T, pattern string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "golden", pattern))
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden files for %s: %v", pattern, err)
	}
	return files
}

func compact(t *testing.T, b []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func loadSchema(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("schemas", name))
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestGoldenChanges runs the producer's encoder and the strict decoder over
// every changes golden and checks the schema.
func TestGoldenChanges(t *testing.T) {
	schema := loadSchema(t, "changes.schema.json")
	for _, f := range golden(t, "changes_*.json") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			c, err := deskapi.DecodeChanges(raw)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			enc, err := deskapi.EncodeChanges(c)
			if err != nil {
				t.Fatal(err)
			}
			if compact(t, enc) != compact(t, raw) {
				t.Errorf("round trip drifted:\n got: %s\nwant: %s", compact(t, enc), compact(t, raw))
			}
			validateJSON(t, schema, enc)
		})
	}
}

// TestGoldenChangesCarryAck pins that at least one changes golden has an ack.
func TestGoldenChangesCarryAck(t *testing.T) {
	for _, f := range golden(t, "changes_*.json") {
		raw, _ := os.ReadFile(f)
		c, err := deskapi.DecodeChanges(raw)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range c.Records {
			if r.Ack != nil && r.Ack.Lane != "" && r.Ack.Token != "" {
				return
			}
		}
	}
	t.Fatal("no changes golden carries an ack object")
}

// TestGoldenView does the same for the composite view.
func TestGoldenView(t *testing.T) {
	schema := loadSchema(t, "view.schema.json")
	for _, f := range golden(t, "view_*.json") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			v, err := deskapi.DecodeView(raw)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			enc, err := deskapi.EncodeView(v)
			if err != nil {
				t.Fatal(err)
			}
			if compact(t, enc) != compact(t, raw) {
				t.Errorf("round trip drifted:\n got: %s\nwant: %s", compact(t, enc), compact(t, raw))
			}
			validateJSON(t, schema, enc)
		})
	}
}

// TestDecodeRejectsUnknownMembers pins the strict decoders.
func TestDecodeRejectsUnknownMembers(t *testing.T) {
	if _, err := deskapi.DecodeChanges([]byte(`{"contract":"pg-desk.changes/v1","bogus":1}`)); err == nil {
		t.Error("changes: unknown member accepted")
	}
	if _, err := deskapi.DecodeView([]byte(`{"contract":"pg-desk.view/v1","bogus":1}`)); err == nil {
		t.Error("view: unknown member accepted")
	}
}

// TestSchemaRejectsBadChanges is the negative control of the validator: a
// record with an unknown member and a bad status fail the schema.
func TestSchemaRejectsBadChanges(t *testing.T) {
	schema := loadSchema(t, "changes.schema.json")
	bad := `{"contract":"pg-desk.changes/v1","type":"pr","consumer":"c","cursor":{"from":1,"to":2},
	"sources":[{"query":"q","status":"weird"}],"records":[{"seq":1,"type":"pr","id":"x","title":"t","version":1,"kinds":[],"origin":"o","at":"a","extra":1}]}`
	var v any
	if err := json.Unmarshal([]byte(bad), &v); err != nil {
		t.Fatal(err)
	}
	if errs := validate(schema, v, "$"); len(errs) < 2 {
		t.Errorf("expected at least 2 schema errors, got %v", errs)
	}
}

func validateJSON(t *testing.T, schema map[string]any, raw []byte) {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, e := range validate(schema, v, "$") {
		t.Errorf("schema: %s", e)
	}
}

// validate is a minimal test-only JSON Schema validator covering the keywords
// the hand-written schemas use: type (string or list), const, enum, required,
// properties, additionalProperties (bool or schema) and items.
func validate(s map[string]any, v any, path string) []string {
	var errs []string
	if c, ok := s["const"]; ok && fmt.Sprint(c) != fmt.Sprint(v) {
		errs = append(errs, fmt.Sprintf("%s: want const %v, got %v", path, c, v))
	}
	if e, ok := s["enum"].([]any); ok {
		found := false
		for _, x := range e {
			if fmt.Sprint(x) == fmt.Sprint(v) {
				found = true
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("%s: %v not in enum", path, v))
		}
	}
	if t, ok := s["type"]; ok {
		var types []string
		switch tt := t.(type) {
		case string:
			types = []string{tt}
		case []any:
			for _, x := range tt {
				types = append(types, x.(string))
			}
		}
		match := false
		for _, ty := range types {
			if typeMatches(ty, v) {
				match = true
			}
		}
		if !match {
			return append(errs, fmt.Sprintf("%s: %v is not of type %v", path, v, types))
		}
	}
	switch x := v.(type) {
	case map[string]any:
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if _, has := x[r.(string)]; !has {
					errs = append(errs, fmt.Sprintf("%s: missing %s", path, r))
				}
			}
		}
		props, _ := s["properties"].(map[string]any)
		for k, val := range x {
			if ps, ok := props[k].(map[string]any); ok {
				errs = append(errs, validate(ps, val, path+"."+k)...)
				continue
			}
			switch ap := s["additionalProperties"].(type) {
			case bool:
				if !ap {
					errs = append(errs, fmt.Sprintf("%s: unexpected member %s", path, k))
				}
			case map[string]any:
				errs = append(errs, validate(ap, val, path+"."+k)...)
			}
		}
	case []any:
		if is, ok := s["items"].(map[string]any); ok {
			for i, e := range x {
				errs = append(errs, validate(is, e, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	}
	return errs
}

func typeMatches(ty string, v any) bool {
	switch ty {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "integer":
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	}
	return false
}
