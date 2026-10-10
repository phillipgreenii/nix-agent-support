package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-calendar-task-focus/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-calendar-task-focus/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// newTestTable is the production table over the real HTTP client.
func newTestTable() scriptout.DispatchTable {
	return newDispatchTable(internal.New(internal.NewHTTPClient("")))
}

// fakeDaemonURL serves an empty calendar and an empty attention feed.
func fakeDaemonURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/calendar":
			_, _ = w.Write([]byte(`{"as_of":"2026-10-10T15:00:00.000Z","stale":false,"calendar_id":"focus-cycles","calendar":"Focus cycles","events":[]}`))
		case "/api/v1/attention":
			_, _ = w.Write([]byte(`{"as_of":"2026-10-10T15:00:00.000Z","items":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The merged table answers exactly the calendar and attention ops plus
// capabilities, and answers no auth_status: nothing to authenticate.
func TestDispatchTable_OpsAreCalendarPlusAttentionWithoutAuth(t *testing.T) {
	table := newTestTable()
	got := table.Ops()
	want := []string{"capabilities", "list", "list_attention", "list_events"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ops = %v, want %v", got, want)
	}
	if _, ok := table[scriptout.OpAuthStatus]; ok {
		t.Error("auth_status registered although this backend resolves no credential")
	}
}

func TestBackend_ImplementsNoAuthChecker(t *testing.T) {
	var b any = internal.New(internal.NewHTTPClient(""))
	if _, ok := b.(provider.AuthChecker); ok {
		t.Error("Backend implements AuthChecker; the daemon takes no credential")
	}
}

func TestCapabilities_AdvertisesBothSchemaVersions(t *testing.T) {
	var out bytes.Buffer
	if code := scriptout.ServeOne(newTestTable(), strings.NewReader(`{"op":"capabilities"}`), &out); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var caps scriptout.CapabilitiesResponse
	if err := json.Unmarshal(out.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	wantVersions := map[string]int{"calendar": schema.CalendarSchemaVersion, "attention": schema.AttentionSchemaVersion}
	if !reflect.DeepEqual(caps.SchemaVersions, wantVersions) {
		t.Errorf("schema versions = %v, want %v", caps.SchemaVersions, wantVersions)
	}
	if caps.ProtocolVersion != scriptout.ProtocolVersion {
		t.Errorf("protocol version = %d", caps.ProtocolVersion)
	}
}

// The production wiring (instrument over newDispatchTable) leaves a start row
// and a final row per call in the log file the backend owns, resolved from ITS
// OWN environment.
func TestInstrument_WritesStartAndFinalRowPerCallToTheBackendsOwnLog(t *testing.T) {
	stateHome := t.TempDir()
	getenv := func(k string) string {
		if k == "XDG_STATE_HOME" {
			return stateHome
		}
		return ""
	}
	url := fakeDaemonURL(t)
	table := instrument(newTestTable(), getenv)
	var out bytes.Buffer
	req := `{"op":"list_attention","config":{"base_url":"` + url + `"}}`
	if code := scriptout.ServeOne(table, strings.NewReader(req), &out); code != 0 {
		t.Fatalf("exit = %d, out = %s", code, out.String())
	}

	path := filepath.Join(stateHome, "pg-connector-calendar-task-focus", "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("event log not written at %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want a start row and a final row:\n%s", len(lines), raw)
	}
	var start, final map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &start); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &final); err != nil {
		t.Fatal(err)
	}
	if start["phase"] != "start" || start["op"] != "list_attention" || start["service"] != "pg-connector-calendar-task-focus" {
		t.Errorf("start row = %v", start)
	}
	if final["op"] != "list_attention" || final["level"] != "info" || final["msg"] != "list_attention ok" ||
		final["daemon_requests"] != float64(1) || final["daemon_status"] != float64(200) {
		t.Errorf("final row = %v", final)
	}
}

// A call that fails is attributable from its final row alone: the stage and the
// wire error code.
func TestInstrument_UnreachableDaemonFinalRowNamesTheStage(t *testing.T) {
	stateHome := t.TempDir()
	getenv := func(k string) string {
		if k == "XDG_STATE_HOME" {
			return stateHome
		}
		return ""
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	table := instrument(newTestTable(), getenv)
	var out bytes.Buffer
	scriptout.ServeOne(table, strings.NewReader(`{"op":"list_attention","config":{"base_url":"`+url+`"}}`), &out)

	raw, err := os.ReadFile(filepath.Join(stateHome, "pg-connector-calendar-task-focus", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	var final map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &final); err != nil {
		t.Fatal(err)
	}
	if final["error_code"] != "unavailable" || final["failure_stage"] != "connect" || final["level"] != "error" {
		t.Errorf("final row = %v", final)
	}
}

// EnvPath=off must not write anything.
func TestInstrument_DisabledLeavesTableAlone(t *testing.T) {
	dir := t.TempDir()
	getenv := func(k string) string {
		switch k {
		case eventlog.EnvPath:
			return "off"
		case "XDG_STATE_HOME":
			return dir
		}
		return ""
	}
	url := fakeDaemonURL(t)
	table := instrument(newTestTable(), getenv)
	var out bytes.Buffer
	scriptout.ServeOne(table, strings.NewReader(`{"op":"list_attention","config":{"base_url":"`+url+`"}}`), &out)
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("disabled event log wrote files: %v", entries)
	}
}
