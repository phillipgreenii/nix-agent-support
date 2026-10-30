package claudecontrolend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestMatchTakesOnlyTopLevelEndFiles(t *testing.T) {
	s := New()
	if c, ok := s.Match("ends/example-1.1793375050000.json"); !ok || c["file"] != "example-1.1793375050000" {
		t.Fatalf("%v %v", c, ok)
	}
	for _, p := range []string{"ends/.example.tmp", "ends/sub/x.json", "sessions/1.json", "ends/x.txt", "x.json"} {
		if _, ok := s.Match(p); ok {
			t.Errorf("%q matched", p)
		}
	}
}

func TestParseReadsTheSessionReasonAndTime(t *testing.T) {
	recs, err := New().Parse(nil, []byte(`{"sessionId":"example-1","reason":"prompt_input_exit","timeMs":1793375050000}`))
	if err != nil || len(recs) != 1 {
		t.Fatalf("%v %v", recs, err)
	}
	r := recs[0]
	if r.Type != "control_end" || r.Session != "example-1" || r.Time.UnixMilli() != 1793375050000 {
		t.Fatalf("record %+v", r)
	}
	got := map[string]string{}
	for _, f := range r.Fields {
		got[f.Name] = f.S
	}
	if got["reason"] != "prompt_input_exit" {
		t.Fatalf("fields %+v", r.Fields)
	}
	if _, err := New().Parse(nil, []byte(`{`)); err == nil {
		t.Fatal("a torn file parsed")
	}
}

func TestValidAppliesTheSchemasRequiredKeysAndEnum(t *testing.T) {
	ms := int64(5)
	for _, reason := range Reasons {
		if !(End{SessionID: "s", Reason: reason, TimeMs: &ms}).Valid() {
			t.Errorf("reason %q is invalid", reason)
		}
	}
	neg := int64(-1)
	for name, e := range map[string]End{
		"no session": {Reason: "other", TimeMs: &ms}, "no time": {SessionID: "s", Reason: "other"},
		"bad reason": {SessionID: "s", Reason: "surprise", TimeMs: &ms}, "negative time": {SessionID: "s", Reason: "other", TimeMs: &neg},
	} {
		if e.Valid() {
			t.Errorf("%s is valid", name)
		}
	}
}

// The mod owns the format (schema/session-end.schema.json in the marketplace
// plugin); when the schema is reachable from this checkout the source must
// agree with it. A build that holds only this package (the nix sandbox) skips.
func TestReasonEnumMatchesTheModsSchema(t *testing.T) {
	p := filepath.Join("..", "..", "..", "..", "..", "claude-marketplace", "pg-claude-control", "schema", "session-end.schema.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("the mod's schema is not reachable from this build: %v", err)
	}
	var sch struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(b, &sch); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sch.Properties["reason"].Enum, Reasons) {
		t.Fatalf("schema enum %v, source %v", sch.Properties["reason"].Enum, Reasons)
	}
	for _, k := range []string{"sessionId", "reason", "timeMs"} {
		if !slices.Contains(sch.Required, k) {
			t.Errorf("the schema does not require %s", k)
		}
	}
}
