package schema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestQueryExpr_UnmarshalJSON_SingleString(t *testing.T) {
	var q QueryExpr
	if err := json.Unmarshal([]byte(`"is:open author:@me"`), &q); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(q, QueryExpr{"is:open author:@me"}) {
		t.Fatalf("q = %#v", q)
	}
}

func TestQueryExpr_UnmarshalJSON_ListOfStrings(t *testing.T) {
	var q QueryExpr
	if err := json.Unmarshal([]byte(`["is:open author:@me","is:open review-requested:@me"]`), &q); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := QueryExpr{"is:open author:@me", "is:open review-requested:@me"}
	if !reflect.DeepEqual(q, want) {
		t.Fatalf("q = %#v, want %#v", q, want)
	}
}

func TestQueryExpr_UnmarshalJSON_MalformedType(t *testing.T) {
	var q QueryExpr
	if err := json.Unmarshal([]byte(`42`), &q); err == nil {
		t.Fatal("expected an error decoding a bare number as a QueryExpr")
	}
}

func TestResolveQuery_Found(t *testing.T) {
	config := json.RawMessage(`{"queries":{"team":"is:open author:@me"}}`)
	expr, ok := ResolveQuery(config, "team")
	if !ok {
		t.Fatal("expected team to resolve")
	}
	if !reflect.DeepEqual(expr, QueryExpr{"is:open author:@me"}) {
		t.Fatalf("expr = %#v", expr)
	}
}

func TestResolveQuery_ListValued(t *testing.T) {
	config := json.RawMessage(`{"queries":{"team":["a","b"]}}`)
	expr, ok := ResolveQuery(config, "team")
	if !ok {
		t.Fatal("expected team to resolve")
	}
	if !reflect.DeepEqual(expr, QueryExpr{"a", "b"}) {
		t.Fatalf("expr = %#v", expr)
	}
}

func TestResolveQuery_NotRecognized(t *testing.T) {
	config := json.RawMessage(`{"queries":{"team":"is:open"}}`)
	if _, ok := ResolveQuery(config, "nonexistent-name"); ok {
		t.Fatal("expected nonexistent-name to not resolve")
	}
}

func TestResolveQuery_NilConfig(t *testing.T) {
	if _, ok := ResolveQuery(nil, "team"); ok {
		t.Fatal("expected a nil config to never resolve any name")
	}
}

func TestResolveQuery_EmptyConfig(t *testing.T) {
	if _, ok := ResolveQuery(json.RawMessage(""), "team"); ok {
		t.Fatal("expected an empty config to never resolve any name")
	}
}

func TestResolveQuery_ConfigWithNoQueriesBlock(t *testing.T) {
	config := json.RawMessage(`{"rate_reserve_points":500}`)
	if _, ok := ResolveQuery(config, "team"); ok {
		t.Fatal("expected a config with no queries block to never resolve any name")
	}
}

func TestResolveQuery_MalformedConfig(t *testing.T) {
	if _, ok := ResolveQuery(json.RawMessage(`not json`), "team"); ok {
		t.Fatal("expected malformed config to resolve to (nil, false), not a decode panic")
	}
}

func TestQueryDef_UnmarshalJSON_Table(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    QueryDef
		errPart string
	}{
		{"bare string", `"is:open"`, QueryDef{Expr: QueryExpr{"is:open"}}, ""},
		{"bare list", `["a","b"]`, QueryDef{Expr: QueryExpr{"a", "b"}}, ""},
		{"one-element list", `["a"]`, QueryDef{Expr: QueryExpr{"a"}}, ""},
		{"empty list", `[]`, QueryDef{Expr: QueryExpr{}}, ""},
		{"null is one empty expression", `null`, QueryDef{Expr: QueryExpr{""}}, ""},
		{"object string expr watched", `{"expr":"a","watched":true}`, QueryDef{Expr: QueryExpr{"a"}, Watched: true}, ""},
		{"object list expr watched", `{"expr":["a","b"],"watched":true}`, QueryDef{Expr: QueryExpr{"a", "b"}, Watched: true}, ""},
		{"object watched false", `{"expr":["a"],"watched":false}`, QueryDef{Expr: QueryExpr{"a"}}, ""},
		{"object watched defaults false", `{"expr":"a"}`, QueryDef{Expr: QueryExpr{"a"}}, ""},
		{"typo key", `{"expr":"a","watch":true}`, QueryDef{}, `"watch"`},
		{"watched is a string", `{"expr":"a","watched":"true"}`, QueryDef{}, "watched"},
		{"watched is null", `{"expr":"a","watched":null}`, QueryDef{}, "watched"},
		{"missing expr", `{"watched":true}`, QueryDef{}, "expr"},
		{"expr is a number", `{"expr":5,"watched":true}`, QueryDef{}, "expr"},
		{"bare number", `5`, QueryDef{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got QueryDef
			err := json.Unmarshal([]byte(c.in), &got)
			wantErr := c.errPart != "" || c.name == "bare number"
			if wantErr {
				if err == nil {
					t.Fatalf("want an error, got %#v", got)
				}
				if c.errPart != "" && !strings.Contains(err.Error(), c.errPart) {
					t.Fatalf("error %q does not name %q", err, c.errPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestQueryDef_MarshalJSON(t *testing.T) {
	cases := []struct {
		name string
		in   QueryDef
		want string
	}{
		{"unwatched list", QueryDef{Expr: QueryExpr{"a", "b"}}, `["a","b"]`},
		{"unwatched one-element stays a list", QueryDef{Expr: QueryExpr{"a"}}, `["a"]`},
		{"unwatched nil is null", QueryDef{}, `null`},
		{"watched is the object form", QueryDef{Expr: QueryExpr{"a"}, Watched: true}, `{"expr":["a"],"watched":true}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(c.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != c.want {
				t.Fatalf("got %s, want %s", b, c.want)
			}
			// An unwatched definition is exactly the marshalled expression.
			if !c.in.Watched {
				e, _ := json.Marshal(c.in.Expr)
				if string(b) != string(e) {
					t.Fatalf("unwatched %s != json.Marshal(Expr) %s", b, e)
				}
			}
		})
	}
}

func TestQueryDef_RoundTrip(t *testing.T) {
	for _, in := range []string{`["a","b"]`, `["a"]`, `{"expr":["a","b"],"watched":true}`} {
		var q QueryDef
		if err := json.Unmarshal([]byte(in), &q); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(q)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != in {
			t.Fatalf("round trip %s -> %s", in, out)
		}
	}
	// A bare string normalizes to the list form (the hash input is the same
	// for a string and a one-element list).
	var q QueryDef
	if err := json.Unmarshal([]byte(`"a"`), &q); err != nil {
		t.Fatal(err)
	}
	if out, _ := json.Marshal(q); string(out) != `["a"]` {
		t.Fatalf("bare string marshals as %s", out)
	}
}

func TestResolveQueryDef(t *testing.T) {
	config := json.RawMessage(`{"queries":{"a":"x","b":{"expr":["y","z"],"watched":true}}}`)
	if d, ok := ResolveQueryDef(config, "a"); !ok || d.Watched || !reflect.DeepEqual(d.Expr, QueryExpr{"x"}) {
		t.Fatalf("a = %#v ok=%v", d, ok)
	}
	if d, ok := ResolveQueryDef(config, "b"); !ok || !d.Watched || !reflect.DeepEqual(d.Expr, QueryExpr{"y", "z"}) {
		t.Fatalf("b = %#v ok=%v", d, ok)
	}
	if e, ok := ResolveQuery(config, "b"); !ok || !reflect.DeepEqual(e, QueryExpr{"y", "z"}) {
		t.Fatalf("ResolveQuery over an object form = %#v ok=%v", e, ok)
	}
	if _, ok := ResolveQueryDef(config, "missing"); ok {
		t.Fatal("missing name resolved")
	}
	if _, ok := ResolveQueryDef(nil, "a"); ok {
		t.Fatal("nil config resolved")
	}
	if _, ok := ResolveQueryDef(json.RawMessage(`{"queries":{"a":{"watch":true,"expr":"x"}}}`), "a"); ok {
		t.Fatal("a typo block is treated as absent, as a malformed block is")
	}
}

func TestResolveQuery_NullEntryIsOneEmptyExpression(t *testing.T) {
	expr, ok := ResolveQuery(json.RawMessage(`{"queries":{"a":null}}`), "a")
	if !ok || !reflect.DeepEqual(expr, QueryExpr{""}) {
		t.Fatalf("null entry = %#v ok=%v, want one empty expression", expr, ok)
	}
}
