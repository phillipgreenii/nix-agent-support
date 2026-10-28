package main

import "testing"

func TestRetiredRootOf(t *testing.T) {
	roots := []string{"/Volumes/ziprecruiter", "/Users/phillipg/phillipg_mbp"}
	tests := []struct {
		name, tool, input, want string
	}{
		{"grep path under retired root", "Grep", `{"pattern":"x","path":"/Volumes/ziprecruiter/monorepo/a"}`, "/Volumes/ziprecruiter"},
		{"root itself", "Grep", `{"pattern":"x","path":"/Volumes/ziprecruiter"}`, "/Volumes/ziprecruiter"},
		{"trailing slash and dot-dot are cleaned", "Grep", `{"path":"/Volumes/ziprecruiter/a/../b/"}`, "/Volumes/ziprecruiter"},
		{"dot-dot escaping the root is not under it", "Grep", `{"path":"/Volumes/ziprecruiter/../gitrepos/ziprecruiter"}`, ""},
		{"read file_path", "Read", `{"file_path":"/Users/phillipg/phillipg_mbp/x/y.go"}`, "/Users/phillipg/phillipg_mbp"},
		{"notebook_path", "NotebookEdit", `{"notebook_path":"/Volumes/ziprecruiter/n.ipynb"}`, "/Volumes/ziprecruiter"},
		{"sibling with shared prefix is not under the root", "Grep", `{"path":"/Volumes/ziprecruiter-new/a"}`, ""},
		{"live root", "Grep", `{"path":"/Volumes/gitrepos/ziprecruiter/a"}`, ""},
		{"glob pattern under retired root", "Glob", `{"pattern":"/Volumes/ziprecruiter/**/*.go"}`, "/Volumes/ziprecruiter"},
		{"pattern is only a path for Glob", "Grep", `{"pattern":"/Volumes/ziprecruiter/x"}`, ""},
		{"relative path never matches", "Grep", `{"path":"Volumes/ziprecruiter/a"}`, ""},
		{"bash command text is not a path", "Bash", `{"command":"ls /Volumes/ziprecruiter/a"}`, ""},
		{"agent prompt text is not a path", "Agent", `{"prompt":"look in /Volumes/ziprecruiter/a"}`, ""},
		{"malformed json", "Grep", `{"path":`, ""},
		{"non-string path", "Grep", `{"path":7}`, ""},
		{"empty input", "Grep", ``, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retiredRootOf(tt.tool, tt.input, roots); got != tt.want {
				t.Errorf("retiredRootOf(%q, %s) = %q, want %q", tt.tool, tt.input, got, tt.want)
			}
		})
	}
	if got := retiredRootOf("Grep", `{"path":"/Volumes/ziprecruiter/a"}`, nil); got != "" {
		t.Errorf("no roots must match nothing, got %q", got)
	}
}

// TestDefaultRetiredRootsAreAbsolute guards the built-in list: a relative or
// empty entry would silently match every relative-looking prefix or nothing.
func TestDefaultRetiredRootsAreAbsolute(t *testing.T) {
	if len(defaultRetiredRoots) == 0 {
		t.Fatal("defaultRetiredRoots is empty")
	}
	for _, r := range defaultRetiredRoots {
		if len(r) < 2 || r[0] != '/' || r[len(r)-1] == '/' {
			t.Errorf("default retired root %q must be an absolute path without a trailing slash", r)
		}
	}
}
