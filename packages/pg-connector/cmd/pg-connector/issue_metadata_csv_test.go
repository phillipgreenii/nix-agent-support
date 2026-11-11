package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// metadataSentToBackend runs `issue <verb> <extra...> --metadata <tokens>`
// against an echo backend and returns the metadata map the umbrella put on
// the wire (the echo backend returns the whole request as the result).
func metadataSentToBackend(t *testing.T, verb string, tokens ...string) map[string]string {
	t.Helper()
	writeEchoFakeBackend(t, "backend-issue-meta-csv")
	writeIssueConfigFor(t, "backend-issue-meta-csv")
	args := []string{"issue", verb}
	if verb == "create" {
		args = append(args, "--title", "q1")
	} else {
		args = append(args, "issue-1")
	}
	for _, tok := range tokens {
		args = append(args, "--metadata", tok)
	}
	stdout, _, code := executePr(t, args)
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var req struct {
		Args struct {
			Metadata map[string]string `json:"metadata"`
			Fields   struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"fields"`
		} `json:"args"`
	}
	if err := json.Unmarshal(resp.Result, &req); err != nil {
		t.Fatalf("decode echoed request: %v (result=%s)", err, resp.Result)
	}
	if verb == "update" {
		return req.Args.Fields.Metadata
	}
	return req.Args.Metadata
}

// TestMetadataFlag_DecodesRFC4180Tokens pins that a --metadata token is
// decoded the way its quoting says, whatever the number of "=" it holds:
// pflag's own string-to-string flag left the doubled quotes of a quoted
// one-"=" token in the value.
func TestMetadataFlag_DecodesRFC4180Tokens(t *testing.T) {
	cases := []struct {
		name   string
		tokens []string
		want   map[string]string
	}{
		{
			"quoted JSON with commas and quotes",
			[]string{`"k1={""x"":1,""y"":2}"`, "k2=plain"},
			map[string]string{"k1": `{"x":1,"y":2}`, "k2": "plain"},
		},
		{
			"unquoted JSON with a comma stays verbatim",
			[]string{`k1={"x":1,"y":2}`},
			map[string]string{"k1": `{"x":1,"y":2}`},
		},
		{
			"quoted JSON holding equals signs",
			[]string{`"k1={""a"":""b=c"",""d"":1}"`},
			map[string]string{"k1": `{"a":"b=c","d":1}`},
		},
		{
			"several pairs in one token",
			[]string{"a=1,b=2"},
			map[string]string{"a": "1", "b": "2"},
		},
		{
			"value with equals, no quoting",
			[]string{"k=a=b"},
			map[string]string{"k": "a=b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, verb := range []string{"create", "update"} {
				if got := metadataSentToBackend(t, verb, tc.tokens...); !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s metadata = %#v, want %#v", verb, got, tc.want)
				}
			}
		})
	}
}

func TestMetadataFlag_RejectsTokenWithoutEquals(t *testing.T) {
	writeEchoFakeBackend(t, "backend-issue-meta-csv")
	writeIssueConfigFor(t, "backend-issue-meta-csv")
	if _, _, code := executePr(t, []string{"issue", "create", "--title", "q1", "--metadata", "nokey"}); code == 0 {
		t.Fatal("exit code = 0, want a usage failure")
	}
}
