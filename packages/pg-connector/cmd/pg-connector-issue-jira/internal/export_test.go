package internal

import (
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestExport_DecodeAndMapMatchTheBackend(t *testing.T) {
	raw := `{"key":"ABC-1","summary":"s","status":"Open","project":"ABC"}`
	iss, err := DecodeIssue(raw)
	if err != nil {
		t.Fatalf("DecodeIssue: %v", err)
	}
	want, _ := decodePJIRAIssue(raw)
	if iss.Key != want.Key || iss.Key != "ABC-1" {
		t.Fatalf("key = %q, want ABC-1", iss.Key)
	}
	asOf := time.Date(2026, 10, 24, 1, 2, 3, 0, time.UTC)
	got := ToSchemaIssue(iss, asOf)
	exp := toSchemaIssue(want, asOf)
	if got.ID != exp.ID || got.Title != exp.Title || got.AsOf != exp.AsOf {
		t.Fatalf("ToSchemaIssue = %+v, want %+v", got, exp)
	}
	if got.AsOf != asOf.Format(time.RFC3339) {
		t.Fatalf("AsOf = %q, want it passed through unchanged (%s)", got.AsOf, asOf.Format(time.RFC3339))
	}
	if _, err := DecodeIssue(`{"summary":"no key"}`); err == nil {
		t.Fatal("DecodeIssue accepted an empty key")
	}
}

func TestExport_DecodeSearchResult(t *testing.T) {
	res, err := DecodeSearchResult(`{"items":[{"key":"ABC-1"},{"key":"ABC-2"}],"truncated":true}`)
	if err != nil {
		t.Fatalf("DecodeSearchResult: %v", err)
	}
	if len(res.Items) != 2 || !res.Truncated {
		t.Fatalf("result = %+v, want 2 items and Truncated", res)
	}
	if _, err := DecodeSearchResult(`not json`); err == nil {
		t.Fatal("DecodeSearchResult accepted garbage")
	}
}

func TestExport_ClassifyPjiraError(t *testing.T) {
	cases := map[string]error{
		"pjira: issue ABC-1 not found":                scriptout.ErrNotFound,
		"pjira: issue ABC-1: status 401 Unauthorized": scriptout.ErrUnauthenticated,
		"pjira: boom": scriptout.ErrUnavailable,
	}
	for msg, want := range cases {
		if got := ClassifyPjiraError(msg); !errors.Is(got, want) {
			t.Errorf("ClassifyPjiraError(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestExport_IsJiraKey(t *testing.T) {
	for _, ok := range []string{"ABC-1", "a1_b-22", "X-9"} {
		if !IsJiraKey(ok) {
			t.Errorf("IsJiraKey(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "ABC", "1-2", "ABC-", "ABC-1 ", "--help", "ABC-1/2"} {
		if IsJiraKey(bad) {
			t.Errorf("IsJiraKey(%q) = true", bad)
		}
	}
}

func TestExport_WithGetenvAnswersFromTheLookupAndLeavesTheOriginal(t *testing.T) {
	b := New(&fakeRunner{})
	t.Setenv(EnvProject, "FROMPROC")
	w := b.WithGetenv(fakeEnv(map[string]string{EnvProject: "FROMCFG"}))
	if p, err := ResolveProject(w.getenvFunc()); err != nil || p != "FROMCFG" {
		t.Fatalf("project via WithGetenv = %q, %v; want FROMCFG", p, err)
	}
	if p, err := ResolveProject(b.getenvFunc()); err != nil || p != "FROMPROC" {
		t.Fatalf("original backend project = %q, %v; want the process env's FROMPROC", p, err)
	}
	if w.runner == nil {
		t.Fatal("WithGetenv dropped the runner")
	}
}
