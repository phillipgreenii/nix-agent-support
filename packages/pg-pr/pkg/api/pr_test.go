package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPRJSONIncludesDiffStatsAndTitle(t *testing.T) {
	pr := PR{
		Repo: "owner/repo", Number: 1, State: "open", Title: "Fix bar",
		Additions: 10, Deletions: 3, ChangedFiles: 2,
	}
	b, err := json.Marshal(pr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{`"title":"Fix bar"`, `"additions":10`, `"deletions":3`, `"changed_files":2`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}
