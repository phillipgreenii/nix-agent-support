package schema

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonSampleRoundTripsUnderTheDocumentedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ticks.jsonl")
	want := DaemonSample{State: DaemonDegraded, StatusExit: 2, Alive: true, Restarts: 3}
	if err := Append(path, Row{Kind: KindTick, Daemon: &want}); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadAll(path)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ReadAll: %v %d", err, len(rows))
	}
	if rows[0].Daemon == nil || *rows[0].Daemon != want {
		t.Fatalf("daemon = %+v, want %+v", rows[0].Daemon, want)
	}
	b, _ := json.Marshal(rows[0])
	if !strings.Contains(string(b), `"daemon":{"state":"degraded","status_exit":2,"alive":true,"restarts":3}`) {
		t.Errorf("row JSON = %s", b)
	}
}

func TestARowWithoutASampleOmitsTheDaemonKeyAndReadsBackNil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ticks.jsonl")
	if err := Append(path, Row{Kind: KindTick}); err != nil {
		t.Fatal(err)
	}
	rows, _ := ReadAll(path)
	b, _ := json.Marshal(rows[0])
	if rows[0].Daemon != nil || strings.Contains(string(b), "daemon") {
		t.Errorf("a missing sample must stay missing (never healthy): %s", b)
	}
}

func TestDaemonStateConstantsArePinned(t *testing.T) {
	if DaemonHealthy != "healthy" || DaemonDegraded != "degraded" || DaemonDown != "down" {
		t.Error("the state strings are a contract with the report packet")
	}
}
