package claudehistory

import "testing"

func TestSlashCommandReadsTypedExitAndClearOnly(t *testing.T) {
	for in, want := range map[string]string{
		"/exit": "exit", "  /exit  ": "exit", "/clear": "clear", "/clear now": "clear",
		"/exiting": "", "exit": "", "please /exit": "", "/compact": "", "": "",
	} {
		if got := SlashCommand(in); got != want {
			t.Errorf("SlashCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseIndexesTheSlashCommandUnderTheSessionId(t *testing.T) {
	recs, err := New().Parse(nil, []byte(`{"display":"/clear","timestamp":1793375050000,"project":"/example/work","sessionId":"example-old"}`))
	if err != nil || len(recs) != 1 {
		t.Fatalf("%v %v", recs, err)
	}
	if recs[0].Session != "example-old" {
		t.Fatalf("a /clear entry is stored under %q (the OLD session id)", recs[0].Session)
	}
	found := false
	for _, f := range recs[0].Fields {
		if f.Name == "slash_command" && f.S == "clear" {
			found = true
		}
	}
	if !found {
		t.Fatalf("fields %+v", recs[0].Fields)
	}
}
