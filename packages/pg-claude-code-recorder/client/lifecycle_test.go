package client_test

import (
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

const base = int64(1793375000000)

// crashRow is one crashed Conversation and one still open.
func crashRow() clienttest.LabelRow {
	return clienttest.LabelRow{Name: "wire", Steps: []clienttest.LabelStep{
		{Do: "start", Pid: 501, StartMs: base},
		{Do: "registry", Pid: 501, Session: "example-wire-crashed", Ms: base + 1000, StartMs: base, Cwd: "/example/work/sub"},
		{Do: "exit_kept", Pid: 501, Ms: base + 60000},
		{Do: "start", Pid: 502, StartMs: base},
		{Do: "registry", Pid: 502, Session: "example-wire-open", Ms: base + 1000, StartMs: base},
	}}
}

func TestConversationAndProcessOperationsOverTheWire(t *testing.T) {
	e := clienttest.RunRow(t, crashRow())
	for name, tc := range map[string]struct {
		op, body string
		code     int
		key      string
	}{
		"conversation.show":         {"conversation.show", `{"session_id":"example-wire-crashed"}`, 200, "session_id"},
		"conversation.show missing": {"conversation.show", `{}`, 400, "error"},
		"conversation.show unknown": {"conversation.show", `{"session_id":"nope"}`, 404, "error"},
		"conversation.list":         {"conversation.list", `{"ended":true}`, 200, "conversations"},
		"conversation.list bad":     {"conversation.list", `{"since":"yesterday"}`, 400, "error"},
		"process.show":              {"process.show", `{"pid":501}`, 200, "proc_start"},
		"process.show unknown":      {"process.show", `{"pid":4242}`, 404, "error"},
		"process.list":              {"process.list", `{"alive":true}`, 200, "processes"},
	} {
		code, m := httpDo(t, e.Socket, "POST", "/v1/"+tc.op, tc.body)
		if code != tc.code || m[tc.key] == nil {
			t.Errorf("%s: %d %v, want %d with %q", name, code, m, tc.code, tc.key)
		}
	}
	code, m := httpDo(t, e.Socket, "POST", "/v1/conversation.show", `{"session_id":"example-wire-crashed"}`)
	if code != 200 {
		t.Fatal(code)
	}
	for _, k := range []string{"state", "stage", "ended", "registry_present_at_exit", "exit_observed", "clear_entry", "exit_entry", "registry_stale_at_discovery", "cwd", "exit_time", "last_seen_alive", "originals_exist"} {
		if _, ok := m[k]; !ok {
			t.Errorf("conversation.show lacks %q: %v", k, m)
		}
	}
	if _, bad := m["label"]; bad {
		t.Error("the daemon returned a label; it returns evidence only")
	}
}

func TestClientConversationProcessAndCrashed(t *testing.T) {
	e := clienttest.RunRow(t, crashRow())
	c := e.Dial(ctx())
	conv, err := c.Conversation(ctx(), "example-wire-crashed")
	if err != nil || conv.State != "closed" || conv.Stage != "quiet" || !conv.Ended || conv.Cwd != "/example/work/sub" || conv.ExitTime == nil {
		t.Fatalf("%+v %v", conv, err)
	}
	if got := client.EndLabel(conv.Evidence); got != client.LabelCrashed {
		t.Fatalf("label %q", got)
	}
	open, err := c.Conversation(ctx(), "example-wire-open")
	if err != nil || open.State != "open" || open.Stage != "live" || open.Ended || client.EndLabel(open.Evidence) != "" {
		t.Fatalf("%+v %v", open, err)
	}
	if _, err := c.Conversation(ctx(), "nope"); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("unknown conversation: %v", err)
	}
	p, err := c.Process(ctx(), 501, "")
	if err != nil || p.Alive || !p.ExitObserved || !p.RegistryPresentAtExit || len(p.Versions) != 1 || len(p.Conversations) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := c.Process(ctx(), 4242, ""); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("unknown process: %v", err)
	}
	live := true
	pl, err := c.ProcessList(ctx(), client.ProcessFilter{Alive: &live})
	if err != nil || len(pl.Processes) != 1 || pl.Processes[0].Pid != 502 {
		t.Fatalf("%+v %v", pl, err)
	}
	rows, err := c.Crashed(ctx(), time.Time{})
	if err != nil || len(rows) != 1 || rows[0].SessionID != "example-wire-crashed" || rows[0].Label != client.LabelCrashed {
		t.Fatalf("crashed %+v %v", rows, err)
	}
	// No transcript was ever ingested, so the originals do not exist.
	if rows[0].OriginalsExist || rows[0].Resume != "pg-claude-code-recorder extract example-wire-crashed && cd /example/work/sub && claude --resume example-wire-crashed" {
		t.Fatalf("resume %q", rows[0].Resume)
	}
	e.Write("projects/example-proj/example-wire-crashed.jsonl",
		`{"type":"user","uuid":"a1","sessionId":"example-wire-crashed","timestamp":"2026-10-30T10:00:00Z","cwd":"/example/work/newest","message":{"role":"user"}}`+"\n")
	e.Settle()
	rows, err = c.Crashed(ctx(), time.Time{})
	if err != nil || len(rows) != 1 || !rows[0].OriginalsExist || rows[0].Cwd != "/example/work/newest" ||
		rows[0].Resume != "cd /example/work/newest && claude --resume example-wire-crashed" {
		t.Fatalf("with originals %+v %v", rows, err)
	}
	// The end time is the Process's recorded exit time.
	if rows[0].EndTime == nil || rows[0].EndTime.UnixMilli() != base+60000 || rows[0].EndTimeApprox {
		t.Fatalf("end time %v approx=%v", rows[0].EndTime, rows[0].EndTimeApprox)
	}
	if later, err := c.Crashed(ctx(), time.UnixMilli(base+120000)); err != nil || len(later) != 0 {
		t.Fatalf("since after the end: %+v %v", later, err)
	}
	res, err := c.Ended(ctx(), client.EndQuery{Label: client.LabelClosed})
	if err != nil || len(res.Rows) != 0 {
		t.Fatalf("closed: %+v %v", res, err)
	}
}

func TestConversationListFiltersReturnASupersetOfTheirLabel(t *testing.T) {
	e := clienttest.RunRow(t, crashRow())
	c := e.Dial(ctx())
	yes, no := true, false
	for name, f := range map[string]client.ConversationFilter{
		"ended":                {Ended: &yes},
		"observed and present": {ExitObserved: &yes, RegistryPresentAtExit: &yes},
		"cwd and subpaths":     {Cwd: "/example/work"},
	} {
		l, err := c.ConversationList(ctx(), f)
		if err != nil {
			t.Fatal(name, err)
		}
		found := false
		for _, x := range l.Conversations {
			found = found || x.SessionID == "example-wire-crashed"
		}
		if !found {
			t.Errorf("%s dropped the crashed row", name)
		}
	}
	l, err := c.ConversationList(ctx(), client.ConversationFilter{ExitEntry: &yes})
	if err != nil || len(l.Conversations) != 0 {
		t.Fatalf("exit_entry filter: %+v %v", l, err)
	}
	if l, err = c.ConversationList(ctx(), client.ConversationFilter{Ended: &no}); err != nil || len(l.Conversations) != 1 || l.Conversations[0].SessionID != "example-wire-open" {
		t.Fatalf("open filter: %+v %v", l, err)
	}
	if l, err = c.ConversationList(ctx(), client.ConversationFilter{Cwd: "/example/other"}); err != nil || len(l.Conversations) != 0 {
		t.Fatalf("cwd filter: %+v %v", l, err)
	}
}

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
