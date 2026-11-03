package client_test

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

func TestResumeLineScopesToTheProjectDirectoryAndQuotesThePath(t *testing.T) {
	for name, tc := range map[string]struct {
		c    client.ConversationInfo
		want string
	}{
		"originals present": {client.ConversationInfo{SessionID: "s1", OriginalsExist: true, Evidence: client.Evidence{Cwd: "/example/work"}}, "cd /example/work && claude --resume s1"},
		"removed":           {client.ConversationInfo{SessionID: "s1", Evidence: client.Evidence{Cwd: "/example/work"}}, "pg-claude-code-recorder extract s1 && cd /example/work && claude --resume s1"},
		"path with a space": {client.ConversationInfo{SessionID: "s1", OriginalsExist: true, Evidence: client.Evidence{Cwd: "/example/my work"}}, "cd '/example/my work' && claude --resume s1"},
		"path with a quote": {client.ConversationInfo{SessionID: "s1", OriginalsExist: true, Evidence: client.Evidence{Cwd: "/example/it's"}}, `cd '/example/it'\''s' && claude --resume s1`},
	} {
		if got := client.ResumeLine(tc.c); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	if got := client.ResumeLine(client.ConversationInfo{SessionID: "s1"}); got == "" || got[:6] != "claude" {
		t.Errorf("unknown cwd: %q", got)
	}
}
