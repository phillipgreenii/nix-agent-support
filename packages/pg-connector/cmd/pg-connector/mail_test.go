package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

// writeMailConfigFor writes a connector.mail registry config naming each of
// backends as a registered mail backend, in order — mirrors
// writeThreadConfigFor (thread_test.go). No XDG_STATE_HOME isolation is
// needed: mail makes no cache_dispatch.go calls at all (mail.go's header
// comment).
func writeMailConfigFor(t *testing.T, backends ...string) {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("connector:\n  mail:\n")
	for _, b := range backends {
		sb.WriteString("    - " + b + "\n")
	}
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
}

// writeMailRecordingBackend creates a fake mail backend that appends every
// received request line to the returned log file and answers byOp[op]
// (falling back to an unknown_op error), so a test can assert the EXACT
// wire op name and args keys the CLI sent. Request bodies are single-line
// JSON, one per invocation.
func writeMailRecordingBackend(t *testing.T, name string, byOp map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, name+".log")
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\nreq=$(cat)\nprintf '%s\\n' \"$req\" >> '" + logPath + "'\n")
	for op, resp := range byOp {
		sb.WriteString("if echo \"$req\" | grep -q '\"op\":\"" + op + "\"'; then cat <<'FAKE_BACKEND_EOF'\n" + resp + "\nFAKE_BACKEND_EOF\nexit 0\nfi\n")
	}
	sb.WriteString("cat <<'FAKE_BACKEND_EOF'\n{\"protocolVersion\":1,\"schemaVersion\":1,\"error\":{\"code\":\"unknown_op\",\"message\":\"unknown op\"}}\nFAKE_BACKEND_EOF\n")
	if err := os.WriteFile(filepath.Join(dir, name), []byte(sb.String()), 0o755); err != nil {
		t.Fatalf("write recording backend: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// lastMailRequest decodes the last request line the recording backend at
// logPath received, returning its op and args map.
func lastMailRequest(t *testing.T, logPath string) (string, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var req struct {
		Op   string         `json:"op"`
		Args map[string]any `json:"args"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &req); err != nil {
		t.Fatalf("decode request %q: %v", lines[len(lines)-1], err)
	}
	return req.Op, req.Args
}

const mailMsgJSON = `{"id":"<m1@example.test>","subject":"Hello","sender":"A <a@example.test>","date_received":"2026-10-05T10:00:00Z","read":false,"flagged":false,"mailbox":"INBOX","attachments":[{"id":"att-1","filename":"f.pdf"}],"body":"hi","as_of":"2026-10-05T10:00:00Z","stale":false}`

const mailListOK = `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[` + mailMsgJSON + `],"present_ids":["<m1@example.test>"],"cursor":null,"truncated":false}}`

const mailNullOK = `{"protocolVersion":1,"schemaVersion":1,"result":null}`

const mailNotFound = `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"message not found"}}`

// TestNewMailCmd_ExactVerbSet pins the verb surface: exactly list, show,
// search, mark-read, mark-unread, archive, unarchive and attachment (whose
// only subcommand is fetch) — and no delete-shaped verb (INV-MAIL-1), nor
// create, reply or changes.
func TestNewMailCmd_ExactVerbSet(t *testing.T) {
	cmd := newMailCmd()
	names := map[string]bool{}
	for _, c := range cmd.Commands() {
		names[c.Name()] = true
	}
	want := []string{"list", "show", "search", "mark-read", "mark-unread", "archive", "unarchive", "attachment"}
	for _, w := range want {
		if !names[w] {
			t.Errorf("missing verb %q; got %v", w, names)
		}
	}
	if len(names) != len(want) {
		t.Errorf("expected exactly %d verbs, got %v", len(want), names)
	}
	for _, forbidden := range []string{"create", "reply", "changes"} {
		if names[forbidden] {
			t.Errorf("unexpected %q verb: not part of this capability's CLI", forbidden)
		}
	}
	att, _, err := cmd.Find([]string{"attachment"})
	if err != nil {
		t.Fatalf("find attachment: %v", err)
	}
	if subs := att.Commands(); len(subs) != 1 || subs[0].Name() != "fetch" {
		t.Errorf("attachment subcommands = %v, want exactly [fetch]", subs)
	}
}

// TestMailHelp_ListsNoDeleteShapedVerb pins INV-MAIL-1 at the user-visible
// surface: no verb at any depth under `mail` is delete-shaped, and
// `mail --help` does not offer one.
func TestMailHelp_ListsNoDeleteShapedVerb(t *testing.T) {
	stdout, _, code := executePr(t, []string{"mail", "--help"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var walk func(prefix string, cmds []*cobra.Command)
	walk = func(prefix string, cmds []*cobra.Command) {
		for _, c := range cmds {
			name := strings.ToLower(c.Name())
			for _, bad := range []string{"delete", "remove", "rm", "trash", "expunge", "purge", "destroy"} {
				if strings.Contains(name, bad) {
					t.Errorf("delete-shaped verb %q under mail", prefix+name)
				}
			}
			walk(prefix+name+" ", c.Commands())
		}
	}
	walk("mail ", newMailCmd().Commands())
	for _, bad := range []string{"delete", "trash", "expunge"} {
		// "Archive ... never deletes" help text is allowed to say "deletes";
		// the verb table is what must not offer one.
		if strings.Contains(strings.ToLower(verbTable(stdout)), bad) {
			t.Errorf("mail --help verb table offers %q:\n%s", bad, stdout)
		}
	}
}

// verbTable returns the "Available Commands" block of cobra help output.
func verbTable(help string) string {
	i := strings.Index(help, "Available Commands:")
	if i < 0 {
		return ""
	}
	rest := help[i:]
	if j := strings.Index(rest, "\n\n"); j >= 0 {
		rest = rest[:j]
	}
	// Only the leading verb names, not the descriptions.
	var names []string
	for _, line := range strings.Split(rest, "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	return strings.Join(names, "\n")
}

// TestMail_RegistryAcceptsConnectorMail: connector.mail is a recognised
// list-valued registry key; an empty list is rejected like the other
// list-valued types.
func TestMail_RegistryAcceptsConnectorMail(t *testing.T) {
	found := false
	for _, e := range entityTypes {
		if e == "mail" {
			found = true
		}
	}
	if !found {
		t.Fatalf("entityTypes = %v, want it to include \"mail\"", entityTypes)
	}

	reg, err := parseRegistry([]byte("connector:\n  mail:\n    - pg-connector-mail-osx-bridge\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	got, err := reg.List("mail")
	if err != nil || len(got) != 1 || got[0] != "pg-connector-mail-osx-bridge" {
		t.Fatalf("List(mail) = %v, %v", got, err)
	}
	all, err := reg.AllBackends()
	if err != nil || len(all) != 1 || all[0] != "pg-connector-mail-osx-bridge" {
		t.Fatalf("AllBackends = %v, %v (a mail-only backend must be visible to the cross-capability fan-outs)", all, err)
	}

	empty, err := parseRegistry([]byte("connector:\n  mail: []\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := empty.List("mail"); err == nil {
		t.Fatal("connector.mail: [] must be rejected by validateBackendList")
	}

	// A scalar (single-valued, scm-style) registration is rejected.
	scalar, err := parseRegistry([]byte("connector:\n  mail: pg-connector-mail-osx-bridge\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := scalar.List("mail"); err == nil {
		t.Fatal("connector.mail must be list-valued, not a single value")
	}
}

func TestRun_MailList_FanOut_WireOpAndArgs(t *testing.T) {
	logPath := writeMailRecordingBackend(t, "backend-mail-list", map[string]string{"list": mailListOK})
	writeMailConfigFor(t, "backend-mail-list")

	stdout, _, code := executePr(t, []string{"mail", "list", "--mailbox", "INBOX", "--limit", "5", "--unread-only", "--ids-only"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	op, args := lastMailRequest(t, logPath)
	if op != "list" {
		t.Errorf("wire op = %q, want list", op)
	}
	if args["mailbox"] != "INBOX" || args["unread_only"] != true || args["ids_only"] != true || args["limit"] != float64(5) {
		t.Errorf("wire args = %v, want mailbox=INBOX unread_only=true limit=5 ids_only=true", args)
	}

	var outcome mailListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	if len(outcome.Entities) != 1 || outcome.Entities[0].ID != "<m1@example.test>" {
		t.Fatalf("Entities = %+v", outcome.Entities)
	}
	if len(outcome.PresentIDs) != 1 {
		t.Fatalf("PresentIDs = %+v", outcome.PresentIDs)
	}
	if len(outcome.Sources) != 1 || outcome.Sources[0].Source != "backend-mail-list" || outcome.Sources[0].Status != SourceSucceeded || outcome.Sources[0].Count != 1 {
		t.Fatalf("Sources = %+v", outcome.Sources)
	}
}

func TestRun_MailList_DefaultArgs(t *testing.T) {
	logPath := writeMailRecordingBackend(t, "backend-mail-list-defaults", map[string]string{"list": mailListOK})
	writeMailConfigFor(t, "backend-mail-list-defaults")

	if _, _, code := executePr(t, []string{"mail", "list"}); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	_, args := lastMailRequest(t, logPath)
	if args["mailbox"] != "" || args["unread_only"] != false || args["ids_only"] != false || args["limit"] != float64(0) {
		t.Errorf("wire args = %v, want all zero values", args)
	}
}

func TestRun_MailSearch_UsesSearchMessagesOp(t *testing.T) {
	logPath := writeMailRecordingBackend(t, "backend-mail-search", map[string]string{"search_messages": mailListOK})
	writeMailConfigFor(t, "backend-mail-search")

	stdout, _, code := executePr(t, []string{"mail", "search", "quarterly report", "--mailbox", "INBOX", "--limit", "3"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	op, args := lastMailRequest(t, logPath)
	if op != "search_messages" {
		t.Errorf("wire op = %q, want search_messages (never the cross-cutting \"search\")", op)
	}
	if args["query"] != "quarterly report" || args["mailbox"] != "INBOX" || args["limit"] != float64(3) {
		t.Errorf("wire args = %v", args)
	}
	var outcome mailListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v", err)
	}
	if len(outcome.Entities) != 1 || len(outcome.Sources) != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestRun_MailSearch_RequiresQuery(t *testing.T) {
	writeMailConfigFor(t, "backend-mail-unused")
	if _, _, code := executePr(t, []string{"mail", "search"}); code == 0 {
		t.Fatal("mail search with no query must fail")
	}
}

// TestRun_MailList_FanOutExitCodes pins the ordinary 0/2/3 scheme and the
// one-sources[]-row-per-backend rule across two registered backends.
func TestRun_MailList_FanOutExitCodes(t *testing.T) {
	t.Run("one degraded -> exit 2 with a row per backend", func(t *testing.T) {
		writeMailRecordingBackend(t, "backend-mail-ok", map[string]string{"list": mailListOK})
		writeMailRecordingBackend(t, "backend-mail-down", map[string]string{
			"list": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"bridge down"}}`,
		})
		writeMailConfigFor(t, "backend-mail-ok", "backend-mail-down")

		stdout, _, code := executePr(t, []string{"mail", "list"})
		if code != 2 {
			t.Fatalf("exit code = %d, want 2; stdout=%s", code, stdout)
		}
		var outcome mailListOutcome
		if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(outcome.Sources) != 2 {
			t.Fatalf("Sources = %+v, want one row per backend", outcome.Sources)
		}
		if outcome.Sources[0].Status != SourceSucceeded || outcome.Sources[1].Status != SourceDegraded {
			t.Fatalf("Sources = %+v", outcome.Sources)
		}
	})
	t.Run("all degraded -> exit 3", func(t *testing.T) {
		writeMailRecordingBackend(t, "backend-mail-down-only", map[string]string{
			"list": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"bridge down"}}`,
		})
		writeMailConfigFor(t, "backend-mail-down-only")
		if _, _, code := executePr(t, []string{"mail", "list"}); code != 3 {
			t.Fatalf("exit code = %d, want 3", code)
		}
	})
	t.Run("unknown_op is disabled not applicable -> exit 0", func(t *testing.T) {
		writeMailRecordingBackend(t, "backend-mail-noop", map[string]string{})
		writeMailConfigFor(t, "backend-mail-noop")
		stdout, _, code := executePr(t, []string{"mail", "list"})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
		}
		var outcome mailListOutcome
		if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(outcome.Sources) != 1 || outcome.Sources[0].Status != SourceDisabled || outcome.Sources[0].Reason != "not applicable" {
			t.Fatalf("Sources = %+v", outcome.Sources)
		}
	})
}

func TestRun_MailList_BackendPin(t *testing.T) {
	logA := writeMailRecordingBackend(t, "backend-mail-a", map[string]string{"list": mailListOK})
	logB := writeMailRecordingBackend(t, "backend-mail-b", map[string]string{"list": mailListOK})
	writeMailConfigFor(t, "backend-mail-a", "backend-mail-b")

	stdout, _, code := executePr(t, []string{"mail", "list", "--backend", "backend-mail-b"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if _, err := os.Stat(logA); err == nil {
		t.Error("backend-mail-a was invoked despite --backend pinning backend-mail-b")
	}
	if _, err := os.Stat(logB); err != nil {
		t.Error("backend-mail-b was not invoked")
	}
}

func TestRun_MailList_NoBackendRegistered_IsGenericFailureViaJSONEnvelope(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte("connector: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	_ = os.Unsetenv("XDG_CONFIG_HOME")

	stdout, _, code := executePr(t, []string{"mail", "list", "--backend", "nope"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v; stdout=%q", err, stdout)
	}
	if resp.Error == nil {
		t.Fatalf("resp.Error = nil, want an error envelope")
	}
}

func TestRun_MailList_HumanOutput(t *testing.T) {
	writeMailRecordingBackend(t, "backend-mail-human", map[string]string{"list": mailListOK})
	writeMailConfigFor(t, "backend-mail-human")

	stdout, _, code := executePr(t, []string{"--output", "human", "mail", "list"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "{") {
		t.Fatalf("human output must not contain raw JSON; stdout=%s", stdout)
	}
	for _, want := range []string{"messages (1)", "<m1@example.test>", "Hello", "A <a@example.test>", "unread", "INBOX", "sources:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("human output missing %q; stdout=%s", want, stdout)
		}
	}
}

func TestHumanizeMailListOutcome_ZeroMatchAndIDsOnly(t *testing.T) {
	zero := humanizeMailListOutcome(mailListOutcome{})
	if !strings.Contains(zero, "messages: (none)") {
		t.Errorf("zero match = %q", zero)
	}
	ids := humanizeMailListOutcome(mailListOutcome{PresentIDs: []string{"<a@x>", "<b@x>"}})
	if !strings.Contains(ids, "ids only") || !strings.Contains(ids, "<a@x>") || strings.Contains(ids, "(none)") {
		t.Errorf("ids only = %q", ids)
	}
}

func TestRun_MailShow_Success_And_WireArgs(t *testing.T) {
	logPath := writeMailRecordingBackend(t, "backend-mail-show", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":` + mailMsgJSON + `}`,
	})
	writeMailConfigFor(t, "backend-mail-show")

	stdout, _, code := executePr(t, []string{"mail", "show", "<m1@example.test>"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	op, args := lastMailRequest(t, logPath)
	if op != "show" || args["id"] != "<m1@example.test>" {
		t.Errorf("wire op/args = %q %v", op, args)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var m schema.MailMessage
	if err := scriptout.Decode(resp.Result, &m); err != nil || m.Subject != "Hello" || len(m.Attachments) != 1 {
		t.Fatalf("message = %+v, err=%v", m, err)
	}
}

func TestRun_MailShow_HumanOutput(t *testing.T) {
	writeMailRecordingBackend(t, "backend-mail-show-human", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":` + mailMsgJSON + `}`,
	})
	writeMailConfigFor(t, "backend-mail-show-human")

	stdout, _, code := executePr(t, []string{"--output", "human", "mail", "show", "<m1@example.test>"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	for _, want := range []string{"message <m1@example.test> [INBOX]", "subject: Hello", "from: A <a@example.test>", "read: false", "attachment: [att-1] f.pdf", "body: hi"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("human output missing %q; stdout=%s", want, stdout)
		}
	}
}

// TestRun_MailTargetedVerbs_WireOpsAndExitCodes covers every id-keyed verb:
// the exact wire op name and {id} arg, exit 0 on success, exit 4 on
// not_found (the targeted 0/4/1 scheme).
func TestRun_MailTargetedVerbs_WireOpsAndExitCodes(t *testing.T) {
	cases := []struct {
		verb   string
		wireOp string
		okResp string
	}{
		{"show", "show", `{"protocolVersion":1,"schemaVersion":1,"result":` + mailMsgJSON + `}`},
		{"mark-read", "mark_read", mailNullOK},
		{"mark-unread", "mark_unread", mailNullOK},
		{"archive", "archive", mailNullOK},
		{"unarchive", "unarchive", mailNullOK},
	}
	for _, tc := range cases {
		t.Run(tc.verb+"/success", func(t *testing.T) {
			logPath := writeMailRecordingBackend(t, "backend-mail-t-"+tc.verb, map[string]string{tc.wireOp: tc.okResp})
			writeMailConfigFor(t, "backend-mail-t-"+tc.verb)
			stdout, _, code := executePr(t, []string{"mail", tc.verb, "<m1@example.test>"})
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
			}
			op, args := lastMailRequest(t, logPath)
			if op != tc.wireOp || args["id"] != "<m1@example.test>" || len(args) != 1 {
				t.Errorf("wire op/args = %q %v, want %q with only id", op, args, tc.wireOp)
			}
		})
		t.Run(tc.verb+"/not_found_exit4", func(t *testing.T) {
			writeMailRecordingBackend(t, "backend-mail-nf-"+tc.verb, map[string]string{tc.wireOp: mailNotFound})
			writeMailConfigFor(t, "backend-mail-nf-"+tc.verb)
			stdout, _, code := executePr(t, []string{"mail", tc.verb, "<nope@example.test>"})
			if code != 4 {
				t.Fatalf("exit code = %d, want 4; stdout=%s", code, stdout)
			}
		})
		t.Run(tc.verb+"/other_error_exit1", func(t *testing.T) {
			writeMailRecordingBackend(t, "backend-mail-err-"+tc.verb, map[string]string{
				tc.wireOp: `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"bridge down"}}`,
			})
			writeMailConfigFor(t, "backend-mail-err-"+tc.verb)
			if _, _, code := executePr(t, []string{"mail", tc.verb, "<m1@example.test>"}); code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
		})
		t.Run(tc.verb+"/requires_id", func(t *testing.T) {
			writeMailConfigFor(t, "backend-mail-unused")
			if _, _, code := executePr(t, []string{"mail", tc.verb}); code == 0 {
				t.Fatal("verb with no id must fail")
			}
		})
	}
}

func TestRun_MailMutatingVerbs_HumanOutput(t *testing.T) {
	for verb, done := range map[string]string{
		"mark-read":   "marked read",
		"mark-unread": "marked unread",
		"archive":     "archived",
		"unarchive":   "unarchived",
	} {
		wireOp := strings.ReplaceAll(verb, "-", "_")
		writeMailRecordingBackend(t, "backend-mail-hm-"+verb, map[string]string{wireOp: mailNullOK})
		writeMailConfigFor(t, "backend-mail-hm-"+verb)
		stdout, _, code := executePr(t, []string{"--output", "human", "mail", verb, "<m1@example.test>"})
		if code != 0 {
			t.Fatalf("%s: exit code = %d, want 0; stdout=%s", verb, code, stdout)
		}
		if !strings.Contains(stdout, "Message <m1@example.test> "+done) {
			t.Errorf("%s: human output = %q, want it to say %q", verb, stdout, done)
		}
	}
}

func TestRun_MailTargeted_FallsThroughToNextBackendOnNotFound(t *testing.T) {
	writeMailRecordingBackend(t, "backend-mail-first", map[string]string{"archive": mailNotFound})
	logSecond := writeMailRecordingBackend(t, "backend-mail-second", map[string]string{"archive": mailNullOK})
	writeMailConfigFor(t, "backend-mail-first", "backend-mail-second")

	stdout, _, code := executePr(t, []string{"mail", "archive", "<m1@example.test>"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (second backend knows the id); stdout=%s", code, stdout)
	}
	if _, err := os.Stat(logSecond); err != nil {
		t.Error("second backend was never tried after the first answered not_found")
	}
}

func TestRun_MailTargeted_NoBackendRegistered_IsGenericFailure(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte("connector: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, _, code := executePr(t, []string{"mail", "show", "<m1@example.test>"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil || resp.Error == nil {
		t.Fatalf("stdout is not a JSON error envelope: %v; stdout=%q", err, stdout)
	}
}

func TestRun_MailAttachmentFetch_WireOpArgsAndPath(t *testing.T) {
	logPath := writeMailRecordingBackend(t, "backend-mail-att", map[string]string{
		"fetch_attachment": `{"protocolVersion":1,"schemaVersion":1,"result":{"message_id":"<m1@example.test>","attachment_id":"att-1","path":"/tmp/mail-attachments/f.pdf"}}`,
	})
	writeMailConfigFor(t, "backend-mail-att")

	stdout, _, code := executePr(t, []string{"mail", "attachment", "fetch", "<m1@example.test>", "att-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	op, args := lastMailRequest(t, logPath)
	if op != "fetch_attachment" || args["id"] != "<m1@example.test>" || args["attachment_id"] != "att-1" || len(args) != 2 {
		t.Errorf("wire op/args = %q %v, want fetch_attachment with exactly id and attachment_id", op, args)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var f schema.MailAttachmentFile
	if err := scriptout.Decode(resp.Result, &f); err != nil || f.Path != "/tmp/mail-attachments/f.pdf" {
		t.Fatalf("result = %+v, err=%v", f, err)
	}

	// Human mode prints just the saved path, opaque.
	humanOut, _, code := executePr(t, []string{"--output", "human", "mail", "attachment", "fetch", "<m1@example.test>", "att-1"})
	if code != 0 || strings.TrimSpace(humanOut) != "/tmp/mail-attachments/f.pdf" {
		t.Fatalf("human output = %q (exit %d), want the bare saved path", humanOut, code)
	}
}

func TestRun_MailAttachmentFetch_NotFound_Exit4(t *testing.T) {
	writeMailRecordingBackend(t, "backend-mail-att-nf", map[string]string{"fetch_attachment": mailNotFound})
	writeMailConfigFor(t, "backend-mail-att-nf")
	if _, _, code := executePr(t, []string{"mail", "attachment", "fetch", "<m1@example.test>", "nope"}); code != 4 {
		t.Fatalf("exit code = %d, want 4", code)
	}
}

func TestRun_MailAttachmentFetch_RequiresTwoArgs(t *testing.T) {
	writeMailConfigFor(t, "backend-mail-unused")
	if _, _, code := executePr(t, []string{"mail", "attachment", "fetch", "<m1@example.test>"}); code == 0 {
		t.Fatal("attachment fetch with one arg must fail")
	}
}
