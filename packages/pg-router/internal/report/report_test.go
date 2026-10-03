package report

import "testing"

func TestResult_actionsCarryVerbAndRefs(t *testing.T) {
	r := Result{Actions: []Action{
		{Verb: Created, Refs: []Ref{{Type: "bead", ID: "Y"}, {Type: "bead", ID: "Z"}}},
		{Verb: Closed, Refs: []Ref{{Type: "bead", ID: "X"}}},
	}}
	if r.Actions[0].Verb != Created || len(r.Actions[0].Refs) != 2 {
		t.Fatalf("created action wrong: %+v", r.Actions[0])
	}
	if r.Actions[1].Verb != Closed || r.Actions[1].Refs[0].ID != "X" {
		t.Fatalf("closed action wrong: %+v", r.Actions[1])
	}
}

func TestVerb_vocabulary(t *testing.T) {
	for _, v := range []Verb{Created, Closed, HandedBack, Unclaimed, Escalated, Indeterminate} {
		if v == "" {
			t.Fatal("verb constant is empty")
		}
	}
}

func TestResult_fieldsForEventlog(t *testing.T) {
	r := Result{Actions: []Action{{Verb: Closed, Refs: []Ref{{Type: "bead", ID: "X"}}}}}
	f := r.Fields()
	acts, ok := f["actions"].([]map[string]any)
	if !ok || len(acts) != 1 || acts[0]["verb"] != "closed" {
		t.Fatalf("Fields() shape wrong: %#v", f)
	}
}

func TestFromOutcome(t *testing.T) {
	dflt := []Ref{{Type: "bead", ID: "D"}}
	tests := []struct {
		name    string
		outcome string
		want    []Action
	}{
		{"empty structured result has no actions", `{"actions":[]}`, []Action{}},
		{
			"structured verb is relayed with its refs", `{"actions":[{"verb":"closed","refs":[{"type":"bead","id":"X"}]}]}`,
			[]Action{{Verb: "closed", Refs: []Ref{{Type: "bead", ID: "X"}}}},
		},
		{
			"structured verb without refs falls back to default refs", `{"actions":[{"verb":"X"}]}`,
			[]Action{{Verb: "X", Refs: dflt}},
		},
		{
			"several structured actions keep order", `{"actions":[{"verb":"created","refs":[{"type":"bead","id":"Y"}]},{"verb":"closed","refs":[{"type":"bead","id":"Z"}]}]}`,
			[]Action{{Verb: "created", Refs: []Ref{{Type: "bead", ID: "Y"}}}, {Verb: "closed", Refs: []Ref{{Type: "bead", ID: "Z"}}}},
		},
		{"plain token is the verbatim verb", `delivered`, []Action{{Verb: "delivered", Refs: dflt}}},
		{"json without actions key is the verbatim verb", `{"other":1}`, []Action{{Verb: `{"other":1}`, Refs: dflt}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FromOutcome(tc.outcome, dflt).Actions
			if len(got) != len(tc.want) {
				t.Fatalf("got %d actions %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i].Verb != tc.want[i].Verb || len(got[i].Refs) != len(tc.want[i].Refs) {
					t.Fatalf("action %d = %+v, want %+v", i, got[i], tc.want[i])
				}
				for j := range got[i].Refs {
					if got[i].Refs[j] != tc.want[i].Refs[j] {
						t.Fatalf("action %d ref %d = %+v, want %+v", i, j, got[i].Refs[j], tc.want[i].Refs[j])
					}
				}
			}
		})
	}
}
