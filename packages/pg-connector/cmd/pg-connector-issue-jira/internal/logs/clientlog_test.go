package logs

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
)

func TestDaemonUnreachableWritesOneRequestRow(t *testing.T) {
	dir := t.TempDir()
	cl := NewClientLog(dir, "c1")
	cl.Now = fixedClock(t0.Add(250 * time.Millisecond))
	cl.DaemonUnreachable("jira", "list", t0, "daemon not running: connect failed")

	if got := filepath.Base(cl.Path); got != config.ClientLogFileName {
		t.Errorf("client log file %q != config constant %q", got, config.ClientLogFileName)
	}
	rows := readRows(t, ClientLogPath(dir))
	if len(rows) != 1 {
		t.Fatalf("one row per call, got %d", len(rows))
	}
	row := rows[0]
	want := []string{"duration_ms", "error", "error_code", "instance", "kind", "level", "msg", "op", "outcome", "pid", "service", "time", "version", "wait_ms"}
	if got := keysOf(row); !reflect.DeepEqual(got, want) {
		t.Errorf("keys:\n got %v\nwant %v", got, want)
	}
	for k, v := range map[string]any{
		"kind": "request", "service": "pg-connector-issue-jira-daemon-client", "version": "c1", "instance": "jira", "op": "list",
		"outcome": "daemon_unreachable", "error_code": "unavailable", "duration_ms": float64(250), "msg": "list failed: daemon_unreachable",
	} {
		if row[k] != v {
			t.Errorf("%s = %v, want %v", k, row[k], v)
		}
	}
}

// A cause that reads like a throttle is not a Jira failure class: the client
// log records the daemon being down, nothing else.
func TestDaemonUnreachableCarriesNoFailureClass(t *testing.T) {
	dir := t.TempDir()
	NewClientLog(dir, "c1").DaemonUnreachable("jira", "list", t0, "status 429 Too Many Requests")
	if _, has := readRows(t, ClientLogPath(dir))[0]["failure_class"]; has {
		t.Error("a client row carries no failure_class")
	}
}

func TestNilClientLogIsANoOp(t *testing.T) {
	var cl *ClientLog
	cl.DaemonUnreachable("jira", "list", t0, "x")
}
