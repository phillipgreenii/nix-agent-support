package cmdparse

import (
	"reflect"
	"testing"
)

func TestGitInvocation(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantChdirs []string
		wantSub    string
		wantRest   []string
	}{
		{"plain commit", []string{"commit", "-m", "x"}, nil, "commit", []string{"-m", "x"}},
		{"dash-C", []string{"-C", "/repo", "commit"}, []string{"/repo"}, "commit", []string{}},
		{"chained dash-C", []string{"-C", "a", "-C", "b", "status"}, []string{"a", "b"}, "status", []string{}},
		{"config-injection then commit", []string{"-c", "k=v", "commit"}, nil, "commit", []string{}},
		{"commit with -c flag after subcmd", []string{"commit", "-c", "HEAD~1"}, nil, "commit", []string{"-c", "HEAD~1"}},
		{"no subcommand", []string{"-C", "/repo"}, []string{"/repo"}, "", nil},
		{"commit-tree not commit", []string{"commit-tree", "abc"}, nil, "commit-tree", []string{"abc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch, sub, rest := GitInvocation(tt.args)
			if !reflect.DeepEqual(ch, tt.wantChdirs) || sub != tt.wantSub || !reflect.DeepEqual(rest, tt.wantRest) {
				t.Errorf("GitInvocation(%v) = (%v,%q,%v), want (%v,%q,%v)", tt.args, ch, sub, rest, tt.wantChdirs, tt.wantSub, tt.wantRest)
			}
		})
	}
}
