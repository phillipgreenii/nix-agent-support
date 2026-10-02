package claudehandler

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
)

func TestLastLines(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		n        int
		want     string
	}{
		{"fewer lines than asked", "a\nb\n", 5, "a\nb\n"},
		{"exactly n", "a\nb\n", 2, "a\nb\n"},
		{"keeps the last", "a\nb\nc\n", 2, "b\nc\n"},
		{"no final newline", "a\nb\nc", 2, "b\nc\n"},
		{"zero keeps nothing", "a\n", 0, ""},
		{"negative keeps nothing", "a\n", -3, ""},
		{"empty", "", 3, ""},
		{"single line", "only\n", 1, "only\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastLines(tc.in, tc.n); got != tc.want {
				t.Errorf("lastLines(%q, %d) = %q; want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestInterpret(t *testing.T) {
	ok := func(msg string, outcome contract.Outcome, summary, details string) {
		t.Helper()
		v, err := interpret(msg)
		if err != nil || v.Outcome != outcome || v.Summary != summary || v.Details != details {
			t.Errorf("interpret(%q) = %+v, %v; want %s %q %q", msg, v, err, outcome, summary, details)
		}
	}
	bad := func(msg, wantErr string) {
		t.Helper()
		if _, err := interpret(msg); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("interpret(%q) error = %v; want one mentioning %q", msg, err, wantErr)
		}
	}
	ok(`{"outcome":"resolved","summary":"s","details":"d"}`, contract.Resolved, "s", "d")
	ok("  \n{\"outcome\":\"declined\"}\n ", contract.Declined, "", "")
	ok(`{"outcome":"deferred","summary":"s","unknown":[1],"meta":{"a":1}}`, contract.Deferred, "s", "")
	ok("```json\n{\"outcome\":\"resolved\",\"summary\":\"fenced\"}\n```", contract.Resolved, "fenced", "")
	ok("````\n{\"outcome\":\"resolved\",\"summary\":\"long fence\"}\n````", contract.Resolved, "long fence", "")

	bad("", "empty")
	bad("   \n", "empty")
	bad("all done", "not JSON")
	bad(`{"summary":"s"}`, "no \"outcome\"")
	bad(`{"outcome":5}`, "must be a string")
	bad(`{"outcome":"failed"}`, "unknown outcome")
	bad(`{"outcome":"resolved"} extra`, "after the JSON object")
	bad(`{"outcome":"resolved","summary":1}`, "summary")
	bad(`{"outcome":"resolved","outcome":"resolved"}`, "duplicate key")
	bad(`{"outcome":"resolved","meta":[]}`, "meta")
	bad("```json\n{\"outcome\":\"resolved\"}", "not JSON") // an unclosed fence is not unwrapped
}

func TestUnfence(t *testing.T) {
	for in, want := range map[string]string{
		"```\nx\n```":             "x",
		"```json\n{\"a\":1}\n```": "{\"a\":1}",
		"````\n```\n````":         "```",
	} {
		if got, ok := unfence(in); !ok || got != want {
			t.Errorf("unfence(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"x", "```", "```\nx", "```\nx\n````", "``` x ```", "text\n```\nx\n```"} {
		if got, ok := unfence(in); ok {
			t.Errorf("unfence(%q) = %q; want no match", in, got)
		}
	}
}

func TestChildEnvBlanksEveryMarkerAndNothingElse(t *testing.T) {
	in := []string{"PATH=/bin", "CLAUDECODE=1", "A=b=c", "CLAUDE_CODE_SESSION_ID=s", "CLAUDE_CODE_NOT_A_MARKER=keep"}
	got := childEnv(in)
	seen := map[string]string{}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := seen[k]; dup {
			t.Errorf("%s appears twice in %q", k, got)
		}
		seen[k] = v
	}
	for _, m := range claudeChildMarkers {
		if v, ok := seen[m]; !ok || v != "" {
			t.Errorf("marker %s = %q, present=%v; want present and empty", m, v, ok)
		}
	}
	if seen["PATH"] != "/bin" || seen["A"] != "b=c" || seen["CLAUDE_CODE_NOT_A_MARKER"] != "keep" {
		t.Errorf("unrelated variables changed: %v", seen)
	}
	if !reflect.DeepEqual(claudeChildMarkers, []string{"CLAUDE_CODE_CHILD_SESSION", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_EXECPATH"}) {
		t.Errorf("marker set changed: %v", claudeChildMarkers)
	}
}

func TestLookupEnvTakesTheLastValue(t *testing.T) {
	if got := lookupEnv([]string{"K=1", "X=2", "K=3"}, "K"); got != "3" {
		t.Errorf("got %q", got)
	}
	if got := lookupEnv([]string{"KK=1"}, "K"); got != "" {
		t.Errorf("a longer name matched: %q", got)
	}
}

func TestEnvelopeFinalMessage(t *testing.T) {
	for _, tc := range []struct{ json, want string }{
		{`{"result":"text"}`, "text"},
		{`{"result":"text","structured_output":null}`, "text"},
		{`{"result":"text","structured_output":{"a":1}}`, `{"a":1}`},
		{`{"result":"","structured_output":{"a":1}}`, `{"a":1}`},
	} {
		e, err := parseEnvelope([]byte(tc.json))
		if err != nil {
			t.Fatal(err)
		}
		if got := e.finalMessage(); got != tc.want {
			t.Errorf("finalMessage(%s) = %q; want %q", tc.json, got, tc.want)
		}
	}
	if _, err := parseEnvelope([]byte(`[1]`)); err == nil {
		t.Error("an array is not a result object")
	}
	if _, err := parseEnvelope([]byte(`oops`)); err == nil {
		t.Error("non-JSON is not a result object")
	}
}

func TestEnvelopeModel(t *testing.T) {
	for _, tc := range []struct{ json, want string }{
		{`{}`, ""},
		{`{"model":"top"}`, "top"},
		{`{"model":"top","modelUsage":{"a":{"costUSD":9}}}`, "top"},
		{`{"modelUsage":{"b":{"costUSD":1},"a":{"costUSD":2}}}`, "a"},
		{`{"modelUsage":{"b":{"costUSD":2},"a":{"costUSD":2}}}`, "a"},
		{`{"modelUsage":{"only":{}}}`, "only"},
	} {
		e, err := parseEnvelope([]byte(tc.json))
		if err != nil {
			t.Fatal(err)
		}
		if got := e.model(); got != tc.want {
			t.Errorf("model(%s) = %q; want %q", tc.json, got, tc.want)
		}
	}
}

func TestEnvelopeMeta(t *testing.T) {
	var nilEnv *envelope
	if m := nilEnv.meta(); m == nil || len(m) != 0 {
		t.Errorf("meta of no envelope = %v; want an empty, writable map", m)
	}
	e, _ := parseEnvelope([]byte(`{"session_id":"s","total_cost_usd":0,"num_turns":0,"usage":{"x":1},"usage_other":1,"permission_denials":[{"tool_name":"Edit"},{"tool_name":"Bash"}]}`))
	m := e.meta()
	b, _ := json.Marshal(m)
	want := `{"num_turns":0,"permission_denials":["Edit","Bash"],"session_id":"s","total_cost_usd":0,"usage":{"x":1}}`
	if string(b) != want {
		t.Errorf("meta = %s; want %s (zero cost and zero turns are reported, absent fields are not)", b, want)
	}
	empty, _ := parseEnvelope([]byte(`{"usage":null,"permission_denials":[]}`))
	if m := empty.meta(); len(m) != 0 {
		t.Errorf("meta = %v; want none", m)
	}
}

func TestParseArgsDefaults(t *testing.T) {
	o, help, err := parseArgs(nil)
	if err != nil || help {
		t.Fatal(err, help)
	}
	if o.model != "sonnet" || o.permissionMode != "acceptEdits" || o.timeLimit != 5*time.Minute || o.tailLines != 200 ||
		o.allowedTools != "" || o.disallowed != "" || len(o.mcpConfigs) != 0 || o.strictMCP || len(o.claudeArgs) != 0 ||
		o.promptTemplate != "" || o.appendInstructions != "" {
		t.Errorf("defaults = %+v", o)
	}
	if _, help, _ := parseArgs([]string{"--help"}); !help {
		t.Error("--help not recognized")
	}
	if o, _, err := parseArgs([]string{"--time-limit", "200ms"}); err != nil || o.timeLimit != 200*time.Millisecond {
		t.Errorf("sub-second limit: %v %v", o.timeLimit, err)
	}
	// A claude-arg value may itself look like a flag.
	if o, _, err := parseArgs([]string{"--claude-arg", "--max-budget-usd", "--claude-arg", "1"}); err != nil || !reflect.DeepEqual(o.claudeArgs, []string{"--max-budget-usd", "1"}) {
		t.Errorf("claude-arg: %v %v", o.claudeArgs, err)
	}
}

func TestClaudeArgvOrderAndOptionalFlags(t *testing.T) {
	h := &handler{o: options{model: "m", permissionMode: "p", mcpConfigs: []string{"a", "b"}, strictMCP: true, allowedTools: "T", disallowed: "D", claudeArgs: []string{"--x", "y"}}}
	got := h.claudeArgv()
	want := []string{
		"-p", "--output-format", "json", "--model", "m", "--permission-mode", "p", "--json-schema", resultSchema,
		"--allowed-tools", "T", "--disallowed-tools", "D", "--mcp-config", "a", "--mcp-config", "b", "--strict-mcp-config", "--x", "y",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv =\n%q\nwant\n%q", got, want)
	}
	h = &handler{o: options{model: "m", permissionMode: "p"}}
	if got := h.claudeArgv(); len(got) != 9 {
		t.Errorf("with no optional arguments argv = %q", got)
	}
}

func TestResultSchemaIsValidJSON(t *testing.T) {
	if !json.Valid([]byte(resultSchema)) {
		t.Fatal("resultSchema is not valid JSON")
	}
}
