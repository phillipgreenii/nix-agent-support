package ingest

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLeadCmd(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", `{"command":"git status"}`, "git"},
		{"sudo peeled", `{"command":"sudo git status"}`, "git"},
		{"nice peeled", `{"command":"nice go test ./..."}`, "go"},
		{"time peeled", `{"command":"time make build"}`, "make"},
		{"command peeled", `{"command":"command -v jq"}`, "-v"},
		{"exec peeled", `{"command":"exec bash -lc x"}`, "bash"},
		{"paren peeled", `{"command":"( cd /tmp && ls )"}`, "cd"},
		{"brace peeled", `{"command":"{ ls; }"}`, "ls"},
		{"assignment peeled", `{"command":"LC_ALL=C sort file"}`, "sort"},
		{"multiple assignments peeled", `{"command":"A=1 B=2 rg pattern"}`, "rg"},
		{"assignment then wrapper", `{"command":"VAR=1 nice sleep 5"}`, "sleep"},
		{"export prefix peeled", `{"command":"export FOO=bar; ls -la"}`, "ls"},
		{"absolute path kept whole", `{"command":"/usr/bin/env python3 x.py"}`, "/usr/bin/env"},
		{"dotted binary", `{"command":"./update-locks.sh"}`, "./update-locks.sh"},
		{"timeout is not time", `{"command":"timeout 5 curl x"}`, "timeout"},
		{"leading redirect is OTHER", `{"command":">out.txt"}`, OtherCmd},
		{"empty command is OTHER", `{"command":""}`, OtherCmd},
		{"no command key is NOCMD", `{"file_path":"/tmp/x"}`, NoCmd},
		{"empty input is NOCMD", ``, NoCmd},
		{"non-object input is NOCMD", `"just a string"`, NoCmd},
		{"non-string command is NOCMD", `{"command":42}`, NoCmd},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LeadCmd(json.RawMessage(tc.input))
			if got != tc.want {
				t.Errorf("LeadCmd(%s) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// The regression this whole parser exists to prevent.
//
// The shell prototype capped tool inputs at 160 characters before parsing them,
// which cut the closing quote off every long command and dumped it into NOCMD —
// 470 rows of pure artefact. The Go path reads the full decoded input, so a
// kilobyte-long command still attributes to its real leading word.
func TestLeadCmdDoesNotTruncateLongCommands(t *testing.T) {
	long := "git " + strings.Repeat("--some-very-long-flag-value ", 60) + "status"
	if len(long) <= 160 {
		t.Fatalf("fixture is only %d chars; it must exceed the prototype's 160-char cap to be a regression test", len(long))
	}
	raw, err := json.Marshal(map[string]string{"command": long})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := LeadCmd(raw); got != "git" {
		t.Fatalf("LeadCmd on a %d-char command = %q, want \"git\" (NOCMD here would be the phantom bucket)", len(long), got)
	}
}

// Real newlines and tabs inside a command must not become part of the leading
// token. The prototype hand-unescaped `\n`/`\t` out of raw JSON text; the decoded
// string carries the real characters, so the replacement moves here.
func TestLeadCmdHandlesEmbeddedNewlines(t *testing.T) {
	raw, err := json.Marshal(map[string]string{"command": "\n\tgit status\n"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := LeadCmd(raw); got != "git" {
		t.Fatalf("LeadCmd = %q, want \"git\"", got)
	}
}
