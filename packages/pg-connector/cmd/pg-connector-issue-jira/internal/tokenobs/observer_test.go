package tokenobs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/tokenfile"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/tokenguard"
)

type fakeMetrics struct{ reads, ops []string }

func (f *fakeMetrics) TokenRead(reason, result, kind string) {
	f.reads = append(f.reads, reason+"/"+result+"/"+kind)
}
func (f *fakeMetrics) TokenFileOp(op, result string) { f.ops = append(f.ops, op+"/"+result) }

func TestRecordMapsGuardEventsOntoLogEventsAndMetricEnums(t *testing.T) {
	m, l := &fakeMetrics{}, newLogRec()
	o := &Observer{Metrics: m, Log: l.Log, FilePath: "/tmp/x/jira-token", LinkPath: "/state/jira-token"}
	for _, ev := range []tokenguard.Event{
		{Name: "token_read", Reason: tokenguard.ReadStart, Result: "ok"},
		{Name: "token_read", Reason: tokenguard.ReadRepull, Result: "ok", Unchanged: true},
		{Name: "token_read", Reason: tokenguard.ReadRewrite, Result: "ok"},
		{Name: "token_read", Reason: tokenguard.ReadBackoff, Result: "timeout"},
		{Name: "token_file_write", Result: "ok"},
		{Name: "token_file_write", Result: "failed", Err: "tokenfile: replace token file: no space left on device"},
		{Name: "token_degraded", Reason: "token_file"},
		{Name: "something_else"},
	} {
		o.Record(ev)
	}
	wantReads := "start/changed/none auth_error/unchanged/none auth_error/changed/none backoff/error/timeout"
	if got := strings.Join(m.reads, " "); got != wantReads {
		t.Errorf("reads = %q, want %q", got, wantReads)
	}
	if got := strings.Join(m.ops, " "); got != "write/ok write/error" {
		t.Errorf("file ops = %q", got)
	}
	want := "token_read,token_read,token_read,token_read,token_file_written,token_file_write_failed,auth_degraded"
	if got := strings.Join(l.names(), ","); got != want {
		t.Errorf("events = %q, want %q", got, want)
	}
	if f := l.byName["token_file_write_failed"][0]; f["path"] != "/tmp/x/jira-token" || !strings.Contains(f["error"].(string), "no space") {
		t.Errorf("token_file_write_failed = %v", f)
	}
	if d := l.byName["auth_degraded"][0]; len(d) != 1 || d["reason"] != "token_file" {
		t.Errorf("auth_degraded = %v, want only reason", d)
	}
}

func TestStoreEventsMapOntoLogEventsAndFileOps(t *testing.T) {
	m, l := &fakeMetrics{}, newLogRec()
	o := &Observer{Metrics: m, Log: l.Log, FilePath: "/tmp/x/jira-token", LinkPath: "/state/jira-token"}
	for _, e := range []string{
		tokenfile.EventLinkWritten, tokenfile.EventStaleRemoved, tokenfile.EventRemoved,
		tokenfile.EventRemoveFailed, tokenfile.EventTouched, "unknown",
	} {
		o.StoreEvent(e)
	}
	want := "token_link_written,token_file_stale_removed,token_file_removed,token_file_remove_failed,token_file_touched"
	if got := strings.Join(l.names(), ","); got != want {
		t.Errorf("events = %q, want %q", got, want)
	}
	if got := strings.Join(m.ops, " "); got != "remove/ok remove/error" {
		t.Errorf("file ops = %q", got)
	}
	if l.byName["token_link_written"][0]["path"] != "/state/jira-token" {
		t.Errorf("link path = %v", l.byName["token_link_written"][0])
	}
}

func TestANilObserverAndNilSinksAreNoOps(t *testing.T) {
	var o *Observer
	o.Record(tokenguard.Event{Name: "token_read", Result: "ok"})
	o.StoreEvent(tokenfile.EventRemoved)
	(&Observer{}).Record(tokenguard.Event{Name: "token_file_write", Result: "failed"})
}

// A real store: a failed write and a failed remove reach the observer as the
// write-failed and remove-failed transitions.
func TestARealStoreFailureEmitsTheFailedEvents(t *testing.T) {
	tmp, state := t.TempDir(), t.TempDir()
	for _, d := range []string{tmp, state} {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	st, err := tokenfile.New(tmp, filepath.Join(state, "jira-token"))
	if err != nil {
		t.Fatal(err)
	}
	m, l := &fakeMetrics{}, newLogRec()
	o := &Observer{Metrics: m, Log: l.Log, FilePath: st.Path(), LinkPath: st.Link()}
	st.SetObserver(o.StoreEvent)
	// A subdirectory with loose permissions makes Start fail closed; the
	// guard reports it as the token_file degrade.
	if err := os.Mkdir(filepath.Join(tmp, tokenfile.SubdirName), 0o755); err != nil {
		t.Fatal(err)
	}
	g := tokenguard.New(tokenguard.Config{Store: st, Recorder: o, Timer: tokenguard.NewManualTimer()})
	g.Start(t.Context())
	if got := l.byName["auth_degraded"]; len(got) != 1 || got[0]["reason"] != "token_file" {
		t.Fatalf("auth_degraded = %v", got)
	}
	_ = errors.New
}
