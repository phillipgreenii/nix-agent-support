package main

import "testing"

func TestParseHeadSHAFromOutput_JSONLine(t *testing.T) {
	out := "some log\nrunning orchestrator\n{\"head_sha\":\"abc123def\"}\ndone\n"
	sha := parseHeadSHAFromOutput(out)
	if sha != "abc123def" {
		t.Fatalf("head sha = %q, want abc123def", sha)
	}
}

func TestParseHeadSHAFromOutput_LastWins(t *testing.T) {
	out := `{"head_sha":"first"}` + "\n" + `{"head_sha":"second"}` + "\n"
	sha := parseHeadSHAFromOutput(out)
	if sha != "second" {
		t.Fatalf("head sha = %q, want second (last wins)", sha)
	}
}

func TestParseHeadSHAFromOutput_None(t *testing.T) {
	if sha := parseHeadSHAFromOutput("no json here\n"); sha != "" {
		t.Fatalf("head sha = %q, want empty", sha)
	}
}
