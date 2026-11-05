package parser_test

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// ParseTail has no json.Valid pre-check: an append source's Parse rejects
// invalid JSON itself, and the tail is then left alone (false, nil).
func TestParseTailLeavesInvalidTailAlone(t *testing.T) {
	for _, tc := range []struct{ name, file, tail string }{
		{"transcript torn", "example-session-1.jsonl", `{"type":"user","uuid":"u1","sessionId":"example-session-1","mess`},
		{"transcript trailing junk", "example-session-1.jsonl", `{"type":"user","uuid":"u1","sessionId":"example-session-1"} x`},
		{"transcript wrong type", "example-session-1.jsonl", `{"type":"user","isSidechain":"yes"}`},
		{"transcript not an object", "example-session-1.jsonl", `[1,2`},
		{"history torn", "history.jsonl", `{"display":"hi","timestamp":17`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := testkit.New(t)
			id := k.Add("example-proj", tc.file, "")
			k.Append(id, tc.tail)
			ok, err := k.Parser.ParseTail(id, 1)
			if err != nil || ok {
				t.Fatalf("ParseTail = %v, %v; want false, nil", ok, err)
			}
			if n := k.Count(`SELECT COUNT(*) FROM records`); n != 0 {
				t.Fatalf("%d records stored for an invalid tail", n)
			}
		})
	}
}
