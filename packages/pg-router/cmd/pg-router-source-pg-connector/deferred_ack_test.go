package main

import (
	"strings"
	"testing"

	apievent "github.com/phillipgreenii/pg-router/api/event"
)

// ownedBody is a changes answer of one backend that owns its changes: rows
// with seq, and the source row's scanned next_seq (past the last row).
func daemonBody(nextSeq string, rows ...string) string {
	return `{"sources":[{"backend":"gh","status":"succeeded","version":1,"truncated":false,"next_seq":` + nextSeq + `}],` +
		`"changes":[` + strings.Join(rows, ",") + `]}`
}

func daemonRow(change, id, version string, seq string) string {
	ent := `"id":"` + id + `","title":"T"`
	if version != "" {
		ent += `,"version":"` + version + `"`
	}
	return `{"change":"` + change + `","source":"gh","seq":` + seq + `,"kinds":[],"fields":[],"entity":{` + ent + `}}`
}

func deferredArgs(extra ...string) []string {
	return append([]string{"changes", "pr", "q", "--consumer", "c1", "--retry-window", "10m", "--deferred-ack"}, extra...)
}

func TestChangesDeferredAck_PassesNoAckAndPrintsAckInOrder(t *testing.T) {
	pinEmitTime(t)
	fake := withFactory(t, "changes_body_env", "GO_HELPER_BODY="+daemonBody("9",
		daemonRow("changed", "o/r#1", "v1", "3"),
		daemonRow("changed", "o/r#1", "v2", "5"),
		daemonRow("changed", "o/r#2", "v1", "7")))

	stdout, stderr, code := runCLI(t, deferredArgs()...)
	if code != 0 {
		t.Fatalf("exit = %d (stderr=%q)", code, stderr)
	}
	if got := strings.Join(onlyCall(t, fake).Args, " "); got != "pr changes --query q --consumer c1 --no-ack --output json" {
		t.Fatalf("argv = %q", got)
	}
	items := mustUnmarshalItems(t, stdout)
	if len(items) != 3 {
		t.Fatalf("items = %d", len(items))
	}
	for i, tok := range []string{"3", "5", "9"} {
		a := items[i].Ack
		if a == nil || a.Lane != "gh" || a.Token != tok {
			t.Fatalf("items[%d].ack = %+v, want gh/%s", i, a, tok)
		}
		if !suffixedID.MatchString(items[i].ID) {
			t.Fatalf("items[%d].id = %q, want per-change id", i, items[i].ID)
		}
	}
	if items[0].ID == items[1].ID {
		t.Fatalf("two changes of one entity share id %q", items[0].ID)
	}
}

func TestChangesDeferredAck_Refusals(t *testing.T) {
	// no pg-connector call is made for a missing --retry-window
	fake := withFactory(t, "changes_ok_empty")
	_, stderr, code := runCLI(t, "changes", "pr", "q", "--consumer", "c1", "--deferred-ack")
	if code == 0 || !strings.Contains(stderr, "--retry-window") || len(fake.Calls()) != 0 {
		t.Fatalf("code=%d stderr=%q calls=%d", code, stderr, len(fake.Calls()))
	}

	// a non-owning backend (the umbrella marks it disabled) is refused by name
	withFactory(t, "changes_body_env", "GO_HELPER_BODY="+
		`{"sources":[{"backend":"beads-a","status":"disabled","reason":"does not own changes"},`+
		`{"backend":"gh","status":"succeeded","next_seq":4}],"changes":[]}`)
	stdout, stderr, code := runCLI(t, deferredArgs()...)
	if code == 0 || !strings.Contains(stderr, "beads-a") || stdout != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestChangesDeferredAck_EmptyPollAcknowledgesSucceededSources(t *testing.T) {
	fake := withFactory(t, "changes_body_env", "GO_HELPER_BODY="+
		`{"sources":[{"backend":"gh","status":"succeeded","next_seq":12},`+
		`{"backend":"gl","status":"degraded","next_seq":99,"reason":"x"}],"changes":[]}`)
	stdout, stderr, code := runCLI(t, deferredArgs()...)
	if code != 0 || strings.TrimSpace(stdout) != "[]" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %v, want changes + one ack", calls)
	}
	if got := strings.Join(calls[1].Args, " "); got != "pr changes-ack --consumer c1 --query q --next-seq 12 --backend gh --output json" {
		t.Fatalf("ack argv = %q", got)
	}
}

func TestChangesDeferredAck_EmptyPollAckFailureExitsNonZero(t *testing.T) {
	withFactory(t, "changes_body_env_ack_fails", "GO_HELPER_BODY="+daemonBody("4"))
	stdout, _, code := runCLI(t, deferredArgs()...)
	if code == 0 || stdout != "" {
		t.Fatalf("code=%d stdout=%q", code, stdout)
	}
}

func TestChangesDeferredAck_FailedCallAcknowledgesNothing(t *testing.T) {
	fake := withFactory(t, "total_failure")
	_, _, code := runCLI(t, deferredArgs()...)
	if code == 0 || len(fake.Calls()) != 1 {
		t.Fatalf("code=%d calls=%d", code, len(fake.Calls()))
	}
}

func TestChangesDeferredAck_NonEmptyPollDoesNotAcknowledge(t *testing.T) {
	fake := withFactory(t, "changes_body_env", "GO_HELPER_BODY="+daemonBody("5", daemonRow("changed", "e", "v", "5")))
	if _, _, code := runCLI(t, deferredArgs()...); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if n := len(fake.Calls()); n != 1 {
		t.Fatalf("calls = %d, want 1 (no changes-ack)", n)
	}
}

func deferredIDs(t *testing.T, body string, skew int) []apievent.Item {
	t.Helper()
	pinEmitTime(t)
	emitSkew = 0
	if skew != 0 {
		emitSkew = 5 * 1e9
		t.Cleanup(func() { emitSkew = 0 })
	}
	withFactory(t, "changes_body_env", "GO_HELPER_BODY="+body)
	stdout, stderr, code := runCLI(t, deferredArgs()...)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	return mustUnmarshalItems(t, stdout)
}

func TestChangesDeferredAck_ItemIDUnderRedelivery(t *testing.T) {
	// redelivered row (same entity, same version) -> same id
	a := deferredIDs(t, daemonBody("3", daemonRow("changed", "e", "v1", "3")), 0)
	b := deferredIDs(t, daemonBody("3", daemonRow("changed", "e", "v1", "3")), 0)
	if a[0].ID != b[0].ID {
		t.Fatalf("redelivered row ids differ: %q vs %q", a[0].ID, b[0].ID)
	}
	// a new change of the entity while the frontier is held -> new id
	c := deferredIDs(t, daemonBody("6", daemonRow("changed", "e", "v2", "6")), 0)
	if a[0].ID == c[0].ID {
		t.Fatalf("new change reused id %q", a[0].ID)
	}
	// an added row with neither marker: one seq -> one id across emit times;
	// a different seq (a re-entry) -> a new id
	row := func(seq string) string { return daemonRow("added", "bead-1", "", seq) }
	d1 := deferredIDs(t, daemonBody("4", row("4")), 0)
	d2 := deferredIDs(t, daemonBody("4", row("4")), 1)
	d3 := deferredIDs(t, daemonBody("8", row("8")), 0)
	if d1[0].ID != d2[0].ID {
		t.Fatalf("same seq, different emitted_at: %q vs %q", d1[0].ID, d2[0].ID)
	}
	if d1[0].ID == d3[0].ID {
		t.Fatalf("re-entry reused id %q", d1[0].ID)
	}
	if d1[0].Metadata["emitted_at"] == d2[0].Metadata["emitted_at"] {
		t.Fatal("test did not vary emitted_at")
	}
}

func TestChanges_WithoutDeferredAck_PrintsNoAck(t *testing.T) {
	withFactory(t, "changes_body_env", "GO_HELPER_BODY="+daemonBody("5", daemonRow("changed", "e", "v", "5")))
	stdout, _, code := runCLI(t, "changes", "pr", "q", "--consumer", "c1")
	if code != 0 || strings.Contains(stdout, `"ack"`) {
		t.Fatalf("code=%d stdout=%q", code, stdout)
	}
}

func TestSettle_RunsChangesAckForTheLane(t *testing.T) {
	fake := withFactory(t, "changes_ok_empty")
	stdout, stderr, code := runCLI(t, "settle", "pr", "q", "--consumer", "c1", "--lane", "gh", "--through", "17")
	if code != 0 || stdout != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := strings.Join(onlyCall(t, fake).Args, " "); got != "pr changes-ack --consumer c1 --query q --next-seq 17 --backend gh --output json" {
		t.Fatalf("argv = %q", got)
	}
	// query omitted
	fake = withFactory(t, "changes_ok_empty")
	if _, _, code := runCLI(t, "settle", "pr", "--consumer", "c1", "--lane", "gh", "--through", "0"); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if got := strings.Join(onlyCall(t, fake).Args, " "); got != "pr changes-ack --consumer c1 --next-seq 0 --backend gh --output json" {
		t.Fatalf("argv = %q", got)
	}
}

func TestSettle_FailureAndBadToken(t *testing.T) {
	withFactory(t, "changes_body_env_ack_fails")
	if _, _, code := runCLI(t, "settle", "pr", "q", "--consumer", "c1", "--lane", "gh", "--through", "5"); code == 0 {
		t.Fatal("refused changes-ack exited 0")
	}
	fake := withFactory(t, "changes_ok_empty")
	for _, tok := range []string{"abc", "-1", ""} {
		if _, _, code := runCLI(t, "settle", "pr", "q", "--consumer", "c1", "--lane", "gh", "--through", tok); code == 0 {
			t.Fatalf("token %q accepted", tok)
		}
	}
	if len(fake.Calls()) != 0 {
		t.Fatalf("bad token reached pg-connector: %v", fake.Calls())
	}
}

// isChangesAck reports whether a pg-connector call is a changes-ack.
func isChangesAck(args []string) bool {
	return len(args) > 1 && args[1] == "changes-ack"
}

// hasArg reports whether args contains want as one element.
func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// An opted-in query passes --no-ack on every changes call, and a
// changes-ack is sent only by settle and by the empty poll: a poll that
// delivers events acknowledges nothing, so the cursor advances only when the
// core reports settlement (or when nothing is owed behind it).
func TestChanges_DeferredAck_PassesNoAck_CursorAdvancesOnlyOnSettle(t *testing.T) {
	// A poll that delivers events: one changes call with --no-ack, no ack.
	pinEmitTime(t)
	fake := withFactory(t, "changes_body_env", "GO_HELPER_BODY="+daemonBody("9",
		daemonRow("changed", "o/r#1", "v1", "3")))
	if _, stderr, code := runCLI(t, deferredArgs()...); code != 0 {
		t.Fatalf("poll exit = %d (stderr=%q)", code, stderr)
	}
	calls := fake.Calls()
	if len(calls) != 1 || isChangesAck(calls[0].Args) || !hasArg(calls[0].Args, "--no-ack") {
		t.Fatalf("delivering poll calls = %v, want exactly one changes call with --no-ack", calls)
	}

	// settle is a changes-ack sender, and only through the token it is given.
	fake = withFactory(t, "changes_ok_empty")
	if _, stderr, code := runCLI(t, "settle", "pr", "q", "--consumer", "c1", "--lane", "gh", "--through", "9"); code != 0 {
		t.Fatalf("settle exit = %d (stderr=%q)", code, stderr)
	}
	calls = fake.Calls()
	if len(calls) != 1 || !isChangesAck(calls[0].Args) {
		t.Fatalf("settle calls = %v, want exactly one changes-ack", calls)
	}

	// The empty poll: changes with --no-ack, then its own changes-ack.
	fake = withFactory(t, "changes_body_env", "GO_HELPER_BODY="+daemonBody("12"))
	if _, stderr, code := runCLI(t, deferredArgs()...); code != 0 {
		t.Fatalf("empty poll exit = %d (stderr=%q)", code, stderr)
	}
	calls = fake.Calls()
	if len(calls) != 2 || isChangesAck(calls[0].Args) || !hasArg(calls[0].Args, "--no-ack") || !isChangesAck(calls[1].Args) {
		t.Fatalf("empty poll calls = %v, want changes --no-ack then one changes-ack", calls)
	}
	if hasArg(calls[1].Args, "--no-ack") {
		t.Fatalf("changes-ack argv %v carries --no-ack", calls[1].Args)
	}
}
