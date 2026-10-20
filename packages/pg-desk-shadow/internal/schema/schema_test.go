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

func TestFeedOriginConstantsArePinned(t *testing.T) {
	if OriginDaemonFeed != "daemon-feed" || OriginDaemonCIFeed != "daemon-ci-feed" {
		t.Error("the origin strings are a contract with the report packets")
	}
}

func TestItemSerialisesReplayAndCIRollupOnlyWhenSet(t *testing.T) {
	plain, _ := json.Marshal(Item{EntityID: "o/r#1", Kinds: []string{}, Seq: 3, Origin: OriginDaemonFeed, At: "2026-01-05T10:00:00.000Z"})
	for _, key := range []string{`"replay"`, `"ci_rollup"`, `"fields"`, `"updated_at"`} {
		if strings.Contains(string(plain), key) {
			t.Errorf("an item without %s must omit it: %s", key, plain)
		}
	}
	if !strings.Contains(string(plain), `"kinds":[]`) {
		t.Errorf("kinds must be an empty array, never null: %s", plain)
	}
	full, _ := json.Marshal(Item{EntityID: "o/r#1", Kinds: []string{"checks"}, Origin: OriginDaemonCIFeed, CIRollup: "failure", Replay: true})
	for _, want := range []string{`"replay":true`, `"ci_rollup":"failure"`} {
		if !strings.Contains(string(full), want) {
			t.Errorf("item JSON lacks %s: %s", want, full)
		}
	}
	var back Item
	if err := json.Unmarshal(full, &back); err != nil || !back.Replay || back.CIRollup != "failure" {
		t.Errorf("round trip = %+v %v", back, err)
	}
}
