package schema

import (
	"encoding/json"
	"testing"
)

func TestWorktreeInfo_JSONRoundTrip(t *testing.T) {
	in := WorktreeInfo{Path: "/home/u/w/feature", Branch: "feature", Ref: "feature"}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out WorktreeInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", out, in)
	}
}

func TestWorktreeInfo_JSONShape(t *testing.T) {
	raw, err := json.Marshal(WorktreeInfo{Path: "/w/feature", Branch: "feature", Ref: "feature"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"path":"/w/feature","branch":"feature","ref":"feature"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestBranchInfo_JSONRoundTrip(t *testing.T) {
	in := BranchInfo{Repo: "owner/repo", Branch: "feature"}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out BranchInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", out, in)
	}
}

func TestBranchInfo_JSONShape(t *testing.T) {
	raw, err := json.Marshal(BranchInfo{Repo: "owner/repo", Branch: "feature"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"repo":"owner/repo","branch":"feature"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}
