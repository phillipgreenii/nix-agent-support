package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChangeSource_JSONNames(t *testing.T) {
	raw, err := json.Marshal(ChangeSource{Backend: "b", Status: "succeeded", Version: 3, NextSeq: 9})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"backend":"b","status":"succeeded","version":3,"truncated":false,"next_seq":9}`
	if string(raw) != want {
		t.Errorf("without a reason = %s, want %s", raw, want)
	}
	raw, _ = json.Marshal(ChangeSource{Reason: "why"})
	if !strings.Contains(string(raw), `"reason":"why"`) {
		t.Errorf("with a reason = %s", raw)
	}
}

func TestPRChange_JSONNames(t *testing.T) {
	raw, err := json.Marshal(PRChange{
		Change: "added", Source: "s", Entity: PR{ID: "o/r#1", Version: 4, HeadSHA: "h"},
		Seq: 7, Kinds: []string{"title_changed"}, Fields: json.RawMessage(`[{"name":"title"}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"change", "source", "entity", "seq", "kinds", "fields"} {
		if _, ok := m[k]; !ok {
			t.Errorf("key %q missing from %s", k, raw)
		}
	}
	var e struct {
		Version int64  `json:"version"`
		HeadSHA string `json:"head_sha"`
	}
	if err := json.Unmarshal(m["entity"], &e); err != nil || e.Version != 4 || e.HeadSHA != "h" {
		t.Errorf("entity = %s (%v)", m["entity"], err)
	}
}

func TestSourceFreshness_OmitsEmptyTimesAndError(t *testing.T) {
	raw, _ := json.Marshal(SourceFreshness{Query: "mine"})
	if string(raw) != `{"query":"mine"}` {
		t.Errorf("empty = %s", raw)
	}
	raw, _ = json.Marshal(SourceFreshness{Query: "mine", LastSuccessAt: "2026-10-12T09:00:00Z", LastError: "boom"})
	if string(raw) != `{"query":"mine","last_success_at":"2026-10-12T09:00:00Z","last_error":"boom"}` {
		t.Errorf("full = %s", raw)
	}
}

func TestPRChangesResult_NamesAndAckResult(t *testing.T) {
	raw, _ := json.Marshal(PRChangesResult{Sources: []ChangeSource{}, Changes: []PRChange{}, SourcesFreshness: []SourceFreshness{}})
	if string(raw) != `{"sources":[],"changes":[],"sources_freshness":[]}` {
		t.Errorf("result = %s", raw)
	}
	raw, _ = json.Marshal(ChangesAckResult{Consumer: "c", Query: "q", AckedSeq: 5})
	if string(raw) != `{"consumer":"c","query":"q","acked_seq":5}` {
		t.Errorf("ack result = %s", raw)
	}
}

func TestPR_VersionIsOmittedWhenZero(t *testing.T) {
	raw, _ := json.Marshal(PR{ID: "pr-1"})
	if strings.Contains(string(raw), `"version"`) {
		t.Errorf("zero version emitted: %s", raw)
	}
	raw, _ = json.Marshal(PR{ID: "pr-1", Version: 2})
	if !strings.Contains(string(raw), `"version":2`) {
		t.Errorf("version missing: %s", raw)
	}
}

func TestCIChange_JSONNames(t *testing.T) {
	raw, err := json.Marshal(CIChange{
		Change: "changed", Source: "s",
		Entity: CIChangeEntity{PRID: "o/r#1", Rollup: "failure", Runs: []CIRun{}, Version: 4, HeadSHA: "h"},
		Seq:    7, Kinds: []string{"ci_changed"}, Fields: json.RawMessage(`[]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"change":"changed","source":"s","entity":{"pr_id":"o/r#1","rollup":"failure","runs":[],"version":4,"head_sha":"h"},"seq":7,"kinds":["ci_changed"],"fields":[]}`
	if string(raw) != want {
		t.Errorf("ci change = %s, want %s", raw, want)
	}
}

func TestCIChangesResult_JSONNames(t *testing.T) {
	raw, err := json.Marshal(CIChangesResult{Sources: []ChangeSource{}, Changes: []CIChange{}, SourcesFreshness: []SourceFreshness{}})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"sources":[],"changes":[],"sources_freshness":[]}`
	if string(raw) != want {
		t.Errorf("result = %s, want %s", raw, want)
	}
}
