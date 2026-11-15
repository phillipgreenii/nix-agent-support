package session

import "testing"

func TestHostLabel(t *testing.T) {
	cases := []struct {
		name string
		h    *Host
		want string
	}{
		{"nil host", nil, "unknown"},
		{"cmux", &Host{Kind: "cmux"}, "cmux"},
		{"cmux ignores term program", &Host{Kind: "cmux", TermProgram: "ghostty"}, "cmux"},
		{"tmux", &Host{Kind: "tmux", TermProgram: "vscode"}, "tmux"},
		{"other ghostty", &Host{Kind: "other", TermProgram: "ghostty"}, "ghostty"},
		{"other vscode mixed case", &Host{Kind: "other", TermProgram: "VSCode"}, "vscode"},
		{"other unmapped program", &Host{Kind: "other", TermProgram: "example-term"}, "unknown"},
		{"other no program", &Host{Kind: "other"}, "unknown"},
		{"unrecognised kind", &Host{Kind: "weird", TermProgram: "ghostty"}, "unknown"},
	}
	for _, c := range cases {
		if got := HostLabel(c.h); got != c.want {
			t.Errorf("%s: HostLabel = %q, want %q", c.name, got, c.want)
		}
	}
}
