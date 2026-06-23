package feedbackclassify

import "testing"

func TestClassifyAuthor(t *testing.T) {
	reg := NewRegistry(map[string]BotPolicy{
		"coderabbitai[bot]": {AgentName: "coderabbit", DefaultSeverity: "warning"},
	})
	cases := []struct {
		name      string
		login     string
		typename  string
		body      string
		self      string
		wantKind  string
		wantAgent string
		wantOurs  bool
	}{
		{"third-party bot", "coderabbitai[bot]", "Bot", "x", "phillipg", "agent", "coderabbit", false},
		{"self manual note", "phillipg", "User", "just a note", "phillipg", "human", "", false},
		{"our reply as self", "phillipg", "User", "<!-- pg-pr --> fixed", "phillipg", "agent", "pg-pr", true},
		{"teammate", "alice", "User", "lgtm?", "phillipg", "human", "", false},
		{"unknown bot fallback", "newbot[bot]", "Bot", "y", "phillipg", "agent", "other", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reg.Classify(c.login, c.typename, c.body, c.self)
			if got.Kind != c.wantKind || got.AgentName != c.wantAgent || got.IsOurs != c.wantOurs {
				t.Fatalf("Classify(%s) = %+v, want kind=%s agent=%s ours=%v",
					c.login, got, c.wantKind, c.wantAgent, c.wantOurs)
			}
		})
	}
}
