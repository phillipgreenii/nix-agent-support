package schema

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// perPRDef is the shape the per-PR work-item query takes in a deployment's
// config (bead pg2-eclw3j.3): E1 (metadata), E2 (focus item) and E3 (title).
const perPRDef = `{"expr":[
  "list --all --metadata-field repo={repo} --metadata-field pr_number={pr_number}",
  "list --all --metadata-field source_type=pr --metadata-field source_id={repo}#{pr_number}",
  "list --all --title-contains {repo}#{pr_number}"],
 "params":{"repo":"[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*","pr_number":"[0-9]{1,9}"}}`

func mustDef(t *testing.T, raw string) QueryDef {
	t.Helper()
	var d QueryDef
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return d
}

func TestQueryDef_Params_Decodes(t *testing.T) {
	d := mustDef(t, perPRDef)
	if len(d.Expr) != 3 || d.Watched {
		t.Fatalf("def = %#v", d)
	}
	if len(d.Params) != 2 || d.Params["pr_number"] != "[0-9]{1,9}" {
		t.Fatalf("Params = %#v", d.Params)
	}
}

func TestQueryDef_Params_RoundTrips(t *testing.T) {
	d := mustDef(t, perPRDef)
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back QueryDef
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-unmarshal %s: %v", out, err)
	}
	if !reflect.DeepEqual(back, d) {
		t.Fatalf("round trip changed the def: %#v vs %#v", back, d)
	}
}

func TestQueryDef_Unparameterized_StillMarshalsBare(t *testing.T) {
	out, err := json.Marshal(QueryDef{Expr: QueryExpr{"ready"}})
	if err != nil || string(out) != `["ready"]` {
		t.Fatalf("marshal = %s, %v; want the bare list form", out, err)
	}
}

func TestQueryDef_WatchedWithParams_IsRejected(t *testing.T) {
	var d QueryDef
	err := json.Unmarshal([]byte(`{"expr":"list --title-contains {x}","watched":true,"params":{"x":"[a-z]+"}}`), &d)
	if err == nil || !strings.Contains(err.Error(), "MUST NOT be watched") {
		t.Fatalf("err = %v, want a watched-with-params rejection", err)
	}
	// watched:false with params stays valid.
	if err := json.Unmarshal([]byte(`{"expr":"list --title-contains {x}","watched":false,"params":{"x":"[a-z]+"}}`), &d); err != nil {
		t.Fatalf("watched=false with params: %v", err)
	}
}

func TestQueryDef_Params_RejectsBadDeclarations(t *testing.T) {
	for name, raw := range map[string]string{
		"pattern does not compile": `{"expr":"list","params":{"x":"("}}`,
		"upper-case name":          `{"expr":"list","params":{"X":"a"}}`,
		"name with a brace":        `{"expr":"list","params":{"a}b":"a"}}`,
		"params is a list":         `{"expr":"list","params":["x"]}`,
		"params is null":           `{"expr":"list","params":null}`,
		"unknown sibling key":      `{"expr":"list","param":{"x":"a"}}`,
	} {
		var d QueryDef
		if err := json.Unmarshal([]byte(raw), &d); err == nil {
			t.Errorf("%s: %s decoded without error", name, raw)
		}
	}
}

// TestQueryDef_OldDecoderShape pins WHY new config + old backend answers
// query_not_recognized: the previous accepted-key set made the whole queries
// block fail to decode, which ResolveQuery reports as absent.
func TestResolveQueryDef_ParamsResolve(t *testing.T) {
	cfg := json.RawMessage(`{"queries":{"for-pr":` + perPRDef + `,"ready":"ready"}}`)
	d, ok := ResolveQueryDef(cfg, "for-pr")
	if !ok || len(d.Params) != 2 {
		t.Fatalf("ResolveQueryDef = %#v, %v", d, ok)
	}
	if _, ok := ResolveQueryDef(cfg, "ready"); !ok {
		t.Fatal("an unparameterized sibling must still resolve")
	}
}

func TestQueryDef_CheckParams(t *testing.T) {
	d := mustDef(t, perPRDef)
	good := map[string]string{"repo": "ZR-Private/ziprecruiter", "pr_number": "113446"}
	if err := d.CheckParams(good); err != nil {
		t.Fatalf("valid values: %v", err)
	}
	with := func(k, v string) map[string]string {
		m := map[string]string{"repo": good["repo"], "pr_number": good["pr_number"]}
		m[k] = v
		return m
	}
	cases := []struct {
		name   string
		values map[string]string
		want   string // substring naming the param
	}{
		{"hash in value", with("pr_number", "1#2"), "pr_number"},
		{"whitespace", with("repo", "a/b c"), "repo"},
		{"leading space", with("pr_number", " 12"), "pr_number"},
		{"trailing newline", with("pr_number", "12\n"), "pr_number"},
		{"leading dash", with("repo", "-x/y"), "repo"},
		{"unicode digits", with("pr_number", "١٢٣"), "pr_number"},
		{"empty", with("pr_number", ""), "pr_number"},
		{"too long", with("pr_number", "1234567890"), "pr_number"},
		{"missing", map[string]string{"repo": good["repo"]}, "pr_number"},
		{"unknown", map[string]string{"repo": good["repo"], "pr_number": "1", "extra": "x"}, "extra"},
		{"none at all", nil, "required"},
	}
	for _, c := range cases {
		err := d.CheckParams(c.values)
		if err == nil {
			t.Errorf("%s: accepted %v", c.name, c.values)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not name %q", c.name, err, c.want)
		}
	}
	// The pattern is part of a mismatch message.
	if err := d.CheckParams(with("pr_number", "x")); err == nil || !strings.Contains(err.Error(), "[0-9]{1,9}") {
		t.Errorf("mismatch error %v does not name the regex", err)
	}
}

func TestQueryDef_CheckParams_IsFullMatchNotSearch(t *testing.T) {
	d := QueryDef{Params: map[string]string{"x": "a|b"}}
	// An unanchored alternation would let "ab" through a search; the whole
	// value must match one alternative.
	if err := d.CheckParams(map[string]string{"x": "ab"}); err == nil {
		t.Fatal(`"ab" matched a|b: the pattern is not applied as a full match`)
	}
	if err := d.CheckParams(map[string]string{"x": "b"}); err != nil {
		t.Fatalf(`"b": %v`, err)
	}
}

func TestQueryDef_CheckParams_UnparameterizedAcceptsNoValues(t *testing.T) {
	d := QueryDef{Expr: QueryExpr{"ready"}}
	if err := d.CheckParams(nil); err != nil {
		t.Fatalf("no values: %v", err)
	}
	if err := d.CheckParams(map[string]string{"x": "1"}); err == nil {
		t.Fatal("a value for an undeclared param must be rejected")
	}
}

func TestQueryDef_SubstituteParams_ValueIsOneToken(t *testing.T) {
	d := QueryDef{Params: map[string]string{"t": ".*"}}
	argv := []string{"list", "--title-contains", "{t}"}
	// A value with whitespace and a flag-looking tail stays inside ONE element.
	got, err := d.SubstituteParams(argv, map[string]string{"t": "a b --limit 5"})
	if err != nil {
		t.Fatalf("SubstituteParams: %v", err)
	}
	want := []string{"list", "--title-contains", "a b --limit 5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if argv[2] != "{t}" {
		t.Fatal("the template argv was modified")
	}
}

func TestQueryDef_SubstituteParams_EmbeddedAndSinglePass(t *testing.T) {
	d := QueryDef{Params: map[string]string{"repo": ".*", "n": ".*"}}
	got, err := d.SubstituteParams([]string{"list", "--title-contains", "{repo}#{n}"}, map[string]string{"repo": "{n}", "n": "7"})
	if err != nil {
		t.Fatalf("SubstituteParams: %v", err)
	}
	if got[2] != "{n}#7" {
		t.Fatalf("token = %q, want %q (a value is never expanded again)", got[2], "{n}#7")
	}
}

func TestQueryDef_SubstituteParams_RefusesUnreplacedPlaceholder(t *testing.T) {
	d := QueryDef{Params: map[string]string{"x": ".*"}}
	if _, err := d.SubstituteParams([]string{"list", "--title-contains", "{y}"}, map[string]string{"x": "1"}); err == nil || !strings.Contains(err.Error(), "{y}") {
		t.Fatalf("err = %v, want a refusal naming {y}", err)
	}
	// An unparameterized query whose expression carries a brace is refused too.
	if _, err := (QueryDef{}).SubstituteParams([]string{"list", "--title-contains", "{x}"}, nil); err == nil {
		t.Fatal("a placeholder with no declared params must be refused")
	}
	// A declared param with no value is refused rather than left empty.
	if _, err := d.SubstituteParams([]string{"list", "{x}"}, nil); err == nil {
		t.Fatal("a declared param with no value must be refused")
	}
	// No braces: untouched.
	got, err := (QueryDef{}).SubstituteParams([]string{"ready"}, nil)
	if err != nil || !reflect.DeepEqual(got, []string{"ready"}) {
		t.Fatalf("plain argv = %v, %v", got, err)
	}
}

func TestQueryDef_SubstituteParams_ValueCannotBecomeAFlag(t *testing.T) {
	d := QueryDef{Params: map[string]string{"x": ".*"}}
	if _, err := d.SubstituteParams([]string{"list", "{x}"}, map[string]string{"x": "--all"}); err == nil {
		t.Fatal("a bare-placeholder token whose value starts with - must be refused")
	}
	// A token that started with - is the author's own and is allowed.
	if _, err := d.SubstituteParams([]string{"list", "--status={x}"}, map[string]string{"x": "open"}); err != nil {
		t.Fatalf("--status={x}: %v", err)
	}
}

func TestQueryParams_Context(t *testing.T) {
	if got := QueryParamsFromContext(context.Background()); len(got.Values) != 0 || len(got.Def.Params) != 0 {
		t.Fatalf("bare context = %#v, want zero", got)
	}
	want := QueryParams{Def: QueryDef{Params: map[string]string{"a": "b"}}, Values: map[string]string{"a": "b"}}
	if got := QueryParamsFromContext(WithQueryParams(context.Background(), want)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
}
