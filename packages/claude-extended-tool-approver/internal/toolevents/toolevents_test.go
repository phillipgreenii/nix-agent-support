package toolevents_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/metrics"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/setup"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/toolevents"
)

type line struct {
	Schema    string         `json:"schema"`
	EventID   string         `json:"event_id"`
	TS        int64          `json:"ts"`
	Source    string         `json:"source"`
	Kind      string         `json:"kind"`
	SessionID string         `json:"session_id"`
	ToolUseID string         `json:"tool_use_id"`
	AgentID   string         `json:"agent_id"`
	Data      map[string]any `json:"data"`
}

func readLines(t *testing.T, state, session string) []line {
	t.Helper()
	p := filepath.Join(state, "claude-tool-tracker", "events", "tool-events."+session+".ceta.jsonl")
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open event file: %v", err)
	}
	defer f.Close()
	if st, _ := f.Stat(); st.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", st.Mode().Perm())
	}
	var out []line
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var l line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		out = append(out, l)
	}
	return out
}

func bashInput(t *testing.T, mode, cmd string, unattended bool) *hookio.HookInput {
	t.Helper()
	ti, _ := json.Marshal(map[string]string{"command": cmd})
	return &hookio.HookInput{
		SessionID: "sess-1", CWD: "/srv/work/project", ToolName: "Bash", ToolInput: ti,
		PermissionMode: mode, HookEventName: "PreToolUse", ToolUseID: "toolu_1", AgentID: "agent-9",
		Unattended: unattended,
	}
}

func checkEnvelope(t *testing.T, l line, kind string) {
	t.Helper()
	if l.Schema != "tool-events/1" || l.Source != "ceta" || l.Kind != kind {
		t.Errorf("envelope = %+v, want schema tool-events/1 source ceta kind %s", l, kind)
	}
	if !regexp.MustCompile(`^[0-9a-f]{28}$`).MatchString(l.EventID) {
		t.Errorf("event_id %q is not 12+16 hex", l.EventID)
	}
	if l.TS <= 0 || l.SessionID != "sess-1" || l.ToolUseID != "toolu_1" || l.AgentID != "agent-9" || l.Data == nil {
		t.Errorf("envelope fields wrong: %+v", l)
	}
}

func TestRecordPreToolUse_RealEngineAllowAndClass(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	in := bashInput(t, "default", "git ls-files -- .env.yarn | head", false)
	verdict, final := setup.NewEngineForCWDLive(in.CWD, nil).EvaluateHookBoth(in)
	errs := []metrics.RuleErrorCount{{Rule: "r1", Count: 2, Sample: "boom"}}
	if err := toolevents.RecordPreToolUse(in, verdict, final, true, errs); err != nil {
		t.Fatal(err)
	}
	ls := readLines(t, state, "sess-1")
	kinds := []string{}
	for _, l := range ls {
		kinds = append(kinds, l.Kind)
	}
	if got := strings.Join(kinds, ","); got != "ceta.verdict,ceta.rule_errors,ceta.command_class" {
		t.Fatalf("kinds = %s", got)
	}
	checkEnvelope(t, ls[0], "ceta.verdict")
	if ls[0].Data["verdict"] != "approve" || ls[0].Data["delivered"] != "approve" || ls[0].Data["sandbox_enabled"] != true {
		t.Errorf("verdict data = %v", ls[0].Data)
	}
	if ls[0].Data["rule"] == nil || ls[0].Data["rule"] == "" {
		t.Errorf("rule missing: %v", ls[0].Data)
	}
	checkEnvelope(t, ls[1], "ceta.rule_errors")
	e, _ := ls[1].Data["errors"].(map[string]any)
	r1, _ := e["r1"].(map[string]any)
	if r1["count"] != float64(2) || r1["sample"] != "boom" {
		t.Errorf("errors = %v", ls[1].Data)
	}
	checkEnvelope(t, ls[2], "ceta.command_class")
	if c, _ := ls[2].Data["class"].(string); !strings.Contains(c, "git") {
		t.Errorf("class = %v", ls[2].Data)
	}
}

func TestRecordPreToolUse_UnattendedAskDeliveredAsReject(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	in := bashInput(t, "dontAsk", "cat /srv/work/project/.envrc", true)
	verdict, final := setup.NewEngineForCWDLiveUnattended(in.CWD, nil).EvaluateHookBoth(in)
	if verdict.Decision != hookio.Ask || final.Decision != hookio.Reject {
		t.Fatalf("fixture no longer an unattended ask->reject: %v -> %v", verdict.Decision, final.Decision)
	}
	if err := toolevents.RecordPreToolUse(in, verdict, final, false, nil); err != nil {
		t.Fatal(err)
	}
	l := readLines(t, state, "sess-1")[0]
	if l.Data["verdict"] != "ask" || l.Data["delivered"] != "reject" || l.Data["sandbox_enabled"] != false {
		t.Errorf("data = %v, want verdict ask delivered reject", l.Data)
	}
	if r, _ := l.Data["reason"].(string); !strings.Contains(r, "unattended session never asks") {
		t.Errorf("reason = %q", r)
	}
	if l.Data["rule"] != verdict.Module {
		t.Errorf("rule = %v, want %s", l.Data["rule"], verdict.Module)
	}
}

func TestPreToolUseEvents_Trace(t *testing.T) {
	in := bashInput(t, "default", "ls", false)
	v := hookio.RuleResult{Decision: hookio.Approve, Module: "m", Trace: []hookio.TraceEntry{
		{RuleName: "a", Decision: hookio.NoOpinion, Reason: "n/a"}, {RuleName: "m", Decision: hookio.Approve, Reason: "ok"},
	}}
	evs := toolevents.PreToolUseEvents(in, v, v, false, nil)
	if len(evs) != 3 || evs[1].Kind != "ceta.trace" {
		t.Fatalf("events = %+v", evs)
	}
	entries := evs[1].Data["trace"].(map[string]any)["entries"].([]any)
	first := entries[0].(map[string]any)
	if len(entries) != 2 || first["rule"] != "a" || first["decision"] != "abstain" || first["reason"] != "n/a" {
		t.Errorf("entries = %v", entries)
	}
}

func TestPreToolUseEvents_NonBashHasNoClass(t *testing.T) {
	in := &hookio.HookInput{SessionID: "s", ToolName: "Read", ToolUseID: "t"}
	for _, e := range toolevents.PreToolUseEvents(in, hookio.RuleResult{}, hookio.RuleResult{}, false, nil) {
		if e.Kind == "ceta.command_class" {
			t.Fatal("command_class for a non-Bash tool")
		}
	}
}

func TestAppend_UnwritableDirReturnsError(t *testing.T) {
	state := t.TempDir()
	// A regular file where the events directory's parent must be.
	if err := os.WriteFile(filepath.Join(state, "claude-tool-tracker"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", state)
	in := bashInput(t, "default", "ls", false)
	if err := toolevents.RecordPreToolUse(in, hookio.RuleResult{}, hookio.RuleResult{}, false, nil); err == nil {
		t.Fatal("want an error, not a panic or silence")
	}
}

func TestAppend_BadSessionAndMissingToolUseID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := toolevents.Append(toolevents.Event{Kind: "ceta.verdict", SessionID: "../x", ToolUseID: "t"}); err == nil {
		t.Error("path-like session id accepted")
	}
	if err := toolevents.Append(toolevents.Event{Kind: "ceta.verdict", SessionID: "s"}); err == nil {
		t.Error("missing tool_use_id accepted")
	}
}

func TestAppend_OversizedLineIsShortened(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	big := strings.Repeat("é", 100000)
	err := toolevents.Append(toolevents.Event{Kind: "ceta.verdict", SessionID: "sess-1", ToolUseID: "toolu_1", Data: map[string]any{"reason": big, "verdict": "ask"}})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(state, "claude-tool-tracker", "events", "tool-events.sess-1.ceta.jsonl")
	raw, _ := os.ReadFile(p)
	if len(raw) > toolevents.MaxLineBytes || raw[len(raw)-1] != '\n' || strings.Count(string(raw), "\n") != 1 {
		t.Fatalf("line size %d, newlines %d", len(raw), strings.Count(string(raw), "\n"))
	}
	var l line
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Data["_truncated"]; !ok || l.Data["verdict"] != "ask" {
		t.Errorf("data = %v", l.Data)
	}
	if !strings.Contains(l.Data["reason"].(string), "…[truncated ") {
		t.Error("reason not marked truncated")
	}
}
