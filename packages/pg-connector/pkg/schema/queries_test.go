package schema

import (
	"encoding/json"
	"reflect"
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
