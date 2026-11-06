package schema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// mutate decodes doc, applies f to the top-level object, and re-encodes it.
func mutate(t *testing.T, doc []byte, f func(map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatal(err)
	}
	f(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNamesListsEveryPinnedSchema(t *testing.T) {
	want := []string{"evidence-row", "evidence-rows", "findings", "query-log-entry"}
	got := Names()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestValidExamplesValidate(t *testing.T) {
	cases := map[string]string{
		"evidence-row":    "valid/evidence-row.json",
		"evidence-rows":   "valid/evidence-rows.json",
		"query-log-entry": "valid/query-log-entry.json",
		"findings":        "valid/findings.json",
	}
	for name, file := range cases {
		if err := Validate(name, read(t, file)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestEmptyFindingsAnswerValidates(t *testing.T) {
	if err := Validate("findings", read(t, "valid/findings-empty.json")); err != nil {
		t.Fatal(err)
	}
}

func TestMissingSchemaVersionFails(t *testing.T) {
	for name, file := range map[string]string{
		"evidence-row":    "valid/evidence-row.json",
		"query-log-entry": "valid/query-log-entry.json",
		"findings":        "valid/findings.json",
	} {
		doc := mutate(t, read(t, file), func(m map[string]any) { delete(m, "schema_version") })
		err := Validate(name, doc)
		if err == nil || !strings.Contains(err.Error(), "schema_version") {
			t.Errorf("%s: err = %v, want one naming schema_version", name, err)
		}
	}
}

func TestWrongSchemaVersionFails(t *testing.T) {
	doc := mutate(t, read(t, "valid/query-log-entry.json"), func(m map[string]any) { m["schema_version"] = 2 })
	err := Validate("query-log-entry", doc)
	if err == nil || !strings.Contains(err.Error(), "/schema_version") {
		t.Fatalf("err = %v, want path /schema_version", err)
	}
}

func TestFailingPathIsReported(t *testing.T) {
	doc := mutate(t, read(t, "valid/findings.json"), func(m map[string]any) {
		f := m["findings"].([]any)[0].(map[string]any)
		delete(f, "confirm_by")
	})
	err := Validate("findings", doc)
	if err == nil || !strings.Contains(err.Error(), `"/findings/0"`) || !strings.Contains(err.Error(), "confirm_by") {
		t.Fatalf("err = %v, want path /findings/0 naming confirm_by", err)
	}
}

func TestFindingTextCappedAt1200(t *testing.T) {
	long := func(n int) []byte {
		return mutate(t, read(t, "valid/findings.json"), func(m map[string]any) {
			f := m["findings"].([]any)[0].(map[string]any)
			f["problem"] = strings.Repeat("x", n)
		})
	}
	if err := Validate("findings", long(1200)); err != nil {
		t.Errorf("1200 characters: %v", err)
	}
	err := Validate("findings", long(1201))
	if err == nil || !strings.Contains(err.Error(), "/findings/0/problem") {
		t.Errorf("1201 characters: err = %v, want path /findings/0/problem", err)
	}
}

func TestEndMustBeTrue(t *testing.T) {
	doc := mutate(t, read(t, "valid/findings-empty.json"), func(m map[string]any) { m["end"] = false })
	if err := Validate("findings", doc); err == nil {
		t.Fatal("end=false validated")
	}
}

func TestIdPatterns(t *testing.T) {
	bad := mutate(t, read(t, "valid/evidence-row.json"), func(m map[string]any) { m["id"] = "E-1" })
	if err := Validate("evidence-row", bad); err == nil {
		t.Error("E-1 validated")
	}
	bad = mutate(t, read(t, "valid/query-log-entry.json"), func(m map[string]any) { m["id"] = "X-001" })
	if err := Validate("query-log-entry", bad); err == nil {
		t.Error("X-001 validated")
	}
	ok := mutate(t, read(t, "valid/query-log-entry.json"), func(m map[string]any) { m["id"] = "Q-1000" })
	if err := Validate("query-log-entry", ok); err != nil {
		t.Errorf("Q-1000: %v", err)
	}
}

func TestEvidenceRowsRejectsBareRowAndBadElement(t *testing.T) {
	if err := Validate("evidence-rows", read(t, "valid/evidence-row.json")); err == nil {
		t.Error("a bare row validated as an array")
	}
	arr := []byte("[" + string(mutate(t, read(t, "valid/evidence-row.json"), func(m map[string]any) { delete(m, "schema_version") })) + "]")
	err := Validate("evidence-rows", arr)
	if err == nil || !strings.Contains(err.Error(), `"/0"`) {
		t.Errorf("err = %v, want path /0", err)
	}
	if err := Validate("evidence-rows", []byte("[]")); err != nil {
		t.Errorf("empty array: %v", err)
	}
}

func TestDateTimeFormatIsAsserted(t *testing.T) {
	doc := mutate(t, read(t, "valid/query-log-entry.json"), func(m map[string]any) { m["at"] = "yesterday" })
	if err := Validate("query-log-entry", doc); err == nil {
		t.Fatal("a non-RFC3339 timestamp validated")
	}
}

func TestOffsetBearingTimestampValidates(t *testing.T) {
	doc := mutate(t, read(t, "valid/query-log-entry.json"), func(m map[string]any) { m["at"] = "2026-01-08T00:05:00-05:00" })
	if err := Validate("query-log-entry", doc); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidJSONAndUnknownSchema(t *testing.T) {
	if err := Validate("findings", []byte("{")); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("err = %v", err)
	}
	if err := Validate("nope", []byte("{}")); err == nil {
		t.Error("unknown schema validated")
	}
}
