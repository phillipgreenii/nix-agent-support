package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// entity_meta (bead pg2-0o693) is the backend's own metadata beside an entity:
// {"watched": true|false} states the flag, and an answer with no entity_meta, or
// one without watched, states nothing. The umbrella passes it through issue
// show and issue list unchanged: it is never added, dropped, defaulted to false
// or rewritten, on a live answer and on one served from the umbrella cache.

// entityMetaCases are the entity_meta shapes a backend may answer with. want is
// the exact entity_meta member the umbrella's answer must carry; nil means the
// key is absent.
var entityMetaCases = []struct {
	name string
	meta string // the entity_meta member, "" for none
	want map[string]any
}{
	{"watched true", `"entity_meta":{"watched":true}`, map[string]any{"watched": true}},
	{"watched false", `"entity_meta":{"watched":false}`, map[string]any{"watched": false}},
	{"absent", ``, nil},
}

func entityMetaIssue(id, meta string) string {
	s := `{"id":"` + id + `","title":"t","state":"open","url":"u"`
	if meta != "" {
		s += "," + meta
	}
	return s + "}"
}

// entityMetaOf decodes obj (a JSON object) and returns its entity_meta member
// and whether the key was present at all.
func entityMetaOf(t *testing.T, obj json.RawMessage) (map[string]any, bool) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(obj, &m); err != nil {
		t.Fatalf("decode %s: %v", obj, err)
	}
	raw, ok := m["entity_meta"]
	if !ok {
		return nil, false
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("decode entity_meta %s: %v", raw, err)
	}
	return meta, true
}

func requireEntityMeta(t *testing.T, label string, obj json.RawMessage, want map[string]any) {
	t.Helper()
	got, present := entityMetaOf(t, obj)
	if want == nil {
		if present {
			t.Fatalf("%s: entity_meta = %v, want the key absent (absent stays absent): %s", label, got, obj)
		}
		return
	}
	if !present || !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: entity_meta = %v (present=%t), want exactly %v: %s", label, got, present, want, obj)
	}
}

func TestEntityMeta_IssueShowPassesTheBackendsEntityMetaThroughUnchanged(t *testing.T) {
	for _, tc := range entityMetaCases {
		t.Run(tc.name, func(t *testing.T) {
			writeOpAwareFakeBackend(t, "backend-meta-show", map[string]string{
				"show": `{"protocolVersion":1,"schemaVersion":1,"result":` + entityMetaIssue("iss-1", tc.meta) + `}`,
			}, `{}`)
			writeIssueConfigFor(t, "backend-meta-show")

			// The first show is live; the second is served from the umbrella cache,
			// and both carry the backend's entity_meta exactly.
			for _, label := range []string{"live show", "cached show"} {
				stdout, _, code := executePr(t, []string{"issue", "show", "iss-1"})
				if code != 0 {
					t.Fatalf("%s: exit code = %d, want 0; stdout=%s", label, code, stdout)
				}
				var resp struct {
					Result json.RawMessage `json:"result"`
				}
				if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
					t.Fatalf("%s: decode response: %v (stdout=%s)", label, err, stdout)
				}
				requireEntityMeta(t, label, resp.Result, tc.want)
			}
		})
	}
}

func TestEntityMeta_IssueShowWatchAndFreshPassTheEntityMetaThrough(t *testing.T) {
	for _, tc := range entityMetaCases {
		t.Run(tc.name, func(t *testing.T) {
			writeOpAwareFakeBackend(t, "backend-meta-watch", map[string]string{
				"show": `{"protocolVersion":1,"schemaVersion":1,"result":` + entityMetaIssue("iss-1", tc.meta) + `}`,
			}, `{}`)
			writeIssueConfigFor(t, "backend-meta-watch")

			stdout, _, code := executePr(t, []string{"issue", "show", "iss-1", "--watch", "--fresh"})
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
			}
			var resp struct {
				Result json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
				t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
			}
			requireEntityMeta(t, "show --watch --fresh", resp.Result, tc.want)
		})
	}
}

func TestEntityMeta_IssueListPassesEachEntitysEntityMetaThroughUnchanged(t *testing.T) {
	// One listing holds all three shapes at once, so each entity is judged by its
	// own entity_meta and none borrows another's.
	ents := ""
	for i, tc := range entityMetaCases {
		if i > 0 {
			ents += ","
		}
		ents += entityMetaIssue("iss-"+tc.name[:1]+string(rune('0'+i)), tc.meta)
	}
	list := `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[` + ents + `],"present_ids":["iss-w0","iss-w1","iss-a2"],"cursor":null,"truncated":false}}`
	writeOpAwareFakeBackend(t, "backend-meta-list", map[string]string{"list": list}, `{}`)
	writeIssueConfigFor(t, "backend-meta-list")

	stdout, _, code := executePr(t, []string{"issue", "list", "--query", "mine"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var out struct {
		Entities []json.RawMessage `json:"entities"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode list answer: %v (stdout=%s)", err, stdout)
	}
	if len(out.Entities) != len(entityMetaCases) {
		t.Fatalf("entities = %d, want %d: %s", len(out.Entities), len(entityMetaCases), stdout)
	}
	for i, tc := range entityMetaCases {
		requireEntityMeta(t, "list entity "+tc.name, out.Entities[i], tc.want)
	}
}
