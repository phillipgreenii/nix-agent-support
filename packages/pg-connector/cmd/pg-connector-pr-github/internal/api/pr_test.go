package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPR_HasConflict(t *testing.T) {
	tests := []struct {
		name string
		pr   PR
		want bool
	}{
		{"conflicting mergeable", PR{Mergeable: "CONFLICTING"}, true},
		{"dirty merge state", PR{MergeStateStatus: "DIRTY"}, true},
		{"mergeable clean", PR{Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"}, false},
		{"unknown is not a conflict", PR{Mergeable: "UNKNOWN"}, false},
		{"zero value", PR{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.pr.HasConflict(); got != tt.want {
				t.Errorf("HasConflict() = %v, want %v", got, tt.want)
			}
		})
	}
}

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
