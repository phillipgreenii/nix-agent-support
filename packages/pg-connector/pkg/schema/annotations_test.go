package schema

import (
	"encoding/json"
	"testing"
)

// The read-answer annotations (PRSchemaVersion 11, CISchemaVersion 6) are
// optional additions the old backends never set: with them unset every
// marshaled shape MUST stay byte-identical to what it was before they existed.
// The expected strings below were recorded from the pre-change shapes.

func TestAnnotations_UnsetKeepsExistingShapesByteIdentical(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{
			"PR",
			PR{ID: "pr-1", Repo: "phillipgreenii/example", Number: 7, Title: "t", State: "open", AsOf: "2026-10-12T00:00:00Z"},
			`{"id":"pr-1","repo":"phillipgreenii/example","number":7,"title":"t","state":"open","branch":"","base":"","author":"","url":"","draft":false,"merged":false,"as_of":"2026-10-12T00:00:00Z","stale":false}`,
		},
		{
			"CIRun",
			CIRun{ID: "1", Name: "build", Status: "completed", Conclusion: "success", Repo: "phillipgreenii/example", PRID: "pr-1", AsOf: "2026-10-12T00:00:00Z"},
			`{"id":"1","name":"build","status":"completed","conclusion":"success","url":"","provider":"","repo":"phillipgreenii/example","pr_id":"pr-1","as_of":"2026-10-12T00:00:00Z","stale":false}`,
		},
		{
			"PRFilesResult",
			PRFilesResult{ID: "pr-1", Files: []PRFile{{Path: "a.go", Additions: 5, Deletions: 1}}},
			`{"id":"pr-1","files":[{"path":"a.go","additions":5,"deletions":1}]}`,
		},
		{
			"PRCommitsResult",
			PRCommitsResult{ID: "pr-1", Commits: []PRCommit{{SHA: "abc", Author: "phillipgreenii", Message: "m"}}},
			`{"id":"pr-1","commits":[{"sha":"abc","author":"phillipgreenii","message":"m"}]}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := json.Marshal(c.v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(raw) != c.want {
				t.Fatalf("got  %s\nwant %s", raw, c.want)
			}
		})
	}
}

func TestAnnotations_SetFieldsWireNames(t *testing.T) {
	age := int64(0)
	stale := false
	info := []GroupInfo{{Group: "summary", FetchedAt: "2026-10-12T00:00:00Z", FreshUntil: "2026-10-12T00:01:00Z"}}
	cases := []struct {
		name string
		v    any
		want string
	}{
		{
			"PRFilesResult",
			PRFilesResult{ID: "pr-1", Files: []PRFile{}, ServedFrom: "cache", Stale: &stale, AgeSeconds: &age, Groups: info},
			`{"id":"pr-1","files":[],"served_from":"cache","stale":false,"age_seconds":0,"groups":[{"group":"summary","fetched_at":"2026-10-12T00:00:00Z","fresh_until":"2026-10-12T00:01:00Z"}]}`,
		},
		{
			"PRCommitsResult",
			PRCommitsResult{ID: "pr-1", Commits: []PRCommit{}, ServedFrom: "origin", AgeSeconds: &age},
			`{"id":"pr-1","commits":[],"served_from":"origin","age_seconds":0}`,
		},
		{
			"GroupInfo full",
			GroupInfo{Group: "files", FetchedAt: "a", FreshUntil: "b", MaxAgeAt: "c", LastError: "boom"},
			`{"group":"files","fetched_at":"a","fresh_until":"b","max_age_at":"c","last_error":"boom"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := json.Marshal(c.v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(raw) != c.want {
				t.Fatalf("got  %s\nwant %s", raw, c.want)
			}
		})
	}
}

func TestCurrentSchemaVersions_FollowTheAnnotationBumps(t *testing.T) {
	if got := CurrentSchemaVersions["pr"]; got != 13 {
		t.Errorf("pr = %d, want 13", got)
	}
	if got := CurrentSchemaVersions["ci"]; got != 7 {
		t.Errorf("ci = %d, want 7", got)
	}
}
