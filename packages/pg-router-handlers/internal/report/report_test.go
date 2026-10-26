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
