package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// Tests for the `log-compact` socket verb and the status reply's
// queueLog.lastCompaction (bead pg2-maxn1).

// bloatedLog writes a queue.jsonl with dead (enqueued, accepted, evicted) events
// and one live event plus one gate, and returns its path.
func bloatedLog(t *testing.T, dead int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	var buf bytes.Buffer
	at := time.Now().UTC()
	write := func(rec map[string]any) {
		b, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	for i := 0; i < dead; i++ {
		id := fmt.Sprintf("dead-%d", i)
		write(map[string]any{"op": "enqueue", "eventId": id, "type": "t1", "at": at, "expiresAt": at, "enqueuedAt": at})
		write(map[string]any{"op": "accept", "eventId": id, "listenerId": "r1"})
		write(map[string]any{"op": "evict", "eventId": id})
	}
	write(map[string]any{"op": "enqueue", "eventId": "live-1", "type": "t1", "at": at, "expiresAt": at.Add(time.Hour), "enqueuedAt": at})
	write(map[string]any{"op": "gate_set", "eventId": "", "gateType": "SYSTEM_PAUSE", "at": at, "owner": "operator"})
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// fileServiceOver builds a started Service whose queue is over a real FileStore
// at path (and so holds its lock), without startup compaction.
func fileServiceOver(t *testing.T, path string, opts ...eventqueue.Option) (*Service, *eventqueue.Queue) {
	t.Helper()
	fs, err := eventqueue.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	q, err := eventqueue.New(fs, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{state: conformance.Started, q: q, bindings: testBindings(), reg: NewRegistry(nil), command: "pg-router", startedAt: time.Now()}, q
}

// A dry run reports exactly what a real run would do and changes nothing; the
// real run then does it, is recorded as the last (manual) compaction in status,
// and a second one finds no progress possible and rewrites nothing.
func TestLogCompact_DryRunThenRealThenNoProgress(t *testing.T) {
	path := bloatedLog(t, 200)
	svc, q := fileServiceOver(t, path, eventqueue.WithLogLimits(0, 1<<20))
	before, _ := os.ReadFile(path)

	dry, code := serveGateVerb(t, svc, SubcommandLogCompact, `{"schemaVersion":"1","dryRun":true}`)
	if code != conformance.ExitOK {
		t.Fatalf("dry run exit = %d; reply=%v", code, dry)
	}
	if err := conformance.Check(LogCompactReplySchema, dry); err != nil {
		t.Fatalf("dry-run reply failed %s: %v", LogCompactReplySchema, err)
	}
	if dry["dryRun"] != true || dry["compacted"] != false || dry["noProgress"] != false || dry["via"] != "daemon" ||
		dry["eventsKept"] != float64(1) || dry["eventsDropped"] != float64(200) || dry["gatesKept"] != float64(1) ||
		dry["recordsBefore"] != float64(200*3+2) || dry["limitBytes"] != float64(1<<20) {
		t.Fatalf("dry-run reply = %v", dry)
	}
	if dry["bytesAfter"].(float64) >= dry["bytesBefore"].(float64) || dry["percentAfter"].(float64) >= dry["percentBefore"].(float64) {
		t.Fatalf("a bloated log must plan to shrink: %v", dry)
	}
	if _, has := dry["durationMs"]; has {
		t.Fatalf("a dry run has no duration: %v", dry)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) || q.Compactions() != 0 {
		t.Fatal("the dry run changed the log or counted as a compaction")
	}
	if _, has := serveStatusQueueLog(t, svc)["lastCompaction"]; has {
		t.Fatal("a dry run must not appear as the last compaction")
	}

	real, code := serveGateVerb(t, svc, SubcommandLogCompact, `{"schemaVersion":"1"}`)
	if code != conformance.ExitOK {
		t.Fatalf("real run exit = %d; reply=%v", code, real)
	}
	if err := conformance.Check(LogCompactReplySchema, real); err != nil {
		t.Fatalf("real-run reply failed %s: %v", LogCompactReplySchema, err)
	}
	if real["compacted"] != true || real["dryRun"] != false || real["bytesBefore"] != dry["bytesBefore"] || real["bytesAfter"] != dry["bytesAfter"] {
		t.Fatalf("real run %v did not do what the dry run %v predicted", real, dry)
	}
	if _, has := real["durationMs"]; !has {
		t.Fatalf("a real run reports its duration: %v", real)
	}
	if fi, _ := os.Stat(path); float64(fi.Size()) != real["bytesAfter"] {
		t.Fatalf("log is %d bytes, reply said %v", fi.Size(), real["bytesAfter"])
	}
	lc, ok := serveStatusQueueLog(t, svc)["lastCompaction"].(map[string]any)
	if !ok || lc["trigger"] != "manual" || lc["bytesAfter"] != real["bytesAfter"] || lc["recordsBefore"] != real["recordsBefore"] {
		t.Fatalf("status lastCompaction = %v, want the manual run", lc)
	}

	again, code := serveGateVerb(t, svc, SubcommandLogCompact, `{"schemaVersion":"1"}`)
	if code != conformance.ExitOK || again["compacted"] != false || again["noProgress"] != true {
		t.Fatalf("second run = %v (exit %d), want a no-progress skip", again, code)
	}
	if q.Compactions() != 1 {
		t.Fatalf("Compactions = %d, want 1", q.Compactions())
	}
}

// serveStatusQueueLog returns the status reply's queueLog object, checking the
// whole reply against its (closed) schema on the way.
func serveStatusQueueLog(t *testing.T, svc *Service) map[string]any {
	t.Helper()
	reply, code := serveStatus(t, svc, statusRequest)
	if code != conformance.ExitOK {
		t.Fatalf("status exit = %d; reply=%v", code, reply)
	}
	if err := conformance.Check(StatusReplySchema, reply); err != nil {
		t.Fatalf("status reply failed its schema: %v", err)
	}
	return reply["queueLog"].(map[string]any)
}

// Startup compaction is the first lastCompaction a daemon reports.
func TestStatusQueueLog_LastCompactionAfterStartup(t *testing.T) {
	path := bloatedLog(t, 50)
	svc, _ := fileServiceOver(t, path, eventqueue.WithCompaction(0, true))
	lc, ok := serveStatusQueueLog(t, svc)["lastCompaction"].(map[string]any)
	if !ok || lc["trigger"] != "startup" || lc["recordsBefore"] != float64(50*3+2) {
		t.Fatalf("lastCompaction = %v, want the startup compaction", lc)
	}
	if _, err := time.Parse(time.RFC3339Nano, lc["at"].(string)); err != nil {
		t.Fatalf("lastCompaction.at = %v: %v", lc["at"], err)
	}
}

// A queue that never compacted (here: an in-memory store) reports none.
func TestStatusQueueLog_NoLastCompactionWhenNeverCompacted(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	if _, has := serveStatusQueueLog(t, svc)["lastCompaction"]; has {
		t.Fatal("lastCompaction present on a queue that never compacted")
	}
}

// A store that cannot compact, and malformed requests, get the protocol error
// envelope and exit 1 — never a panic or a half-formed success reply.
func TestLogCompact_RefusalsUseTheErrorEnvelope(t *testing.T) {
	svc := startedServiceForStatus(t, nil) // in-memory store: no compaction support
	for _, tc := range []struct{ name, request string }{
		{"unsupported store, real", `{"schemaVersion":"1"}`},
		{"unsupported store, dry run", `{"schemaVersion":"1","dryRun":true}`},
		{"bad schemaVersion", `{"schemaVersion":"9"}`},
		{"unknown field", `{"schemaVersion":"1","force":true}`},
		{"dryRun wrong type", `{"schemaVersion":"1","dryRun":"yes"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply, code := serveGateVerb(t, svc, SubcommandLogCompact, tc.request)
			if code != conformance.ExitError {
				t.Fatalf("exit = %d, want %d; reply=%v", code, conformance.ExitError, reply)
			}
			if err := conformance.Check(ErrorReplySchema, reply); err != nil {
				t.Fatalf("reply is not a cli.error: %v (%v)", err, reply)
			}
		})
	}
}

// NewLogCompactView: the percentages, the duration and noProgress are derived the
// way the CLI and the status reply read them.
func TestNewLogCompactView(t *testing.T) {
	plan := eventqueue.CompactPlan{BytesBefore: 500, BytesAfter: 5, RecordsBefore: 30, RecordsAfter: 2, EventsKept: 1, EventsDropped: 9, GatesKept: 1}
	stats := eventqueue.CompactStats{BytesBefore: 520, BytesAfter: 7, RecordsBefore: 31, RecordsAfter: 3, Duration: 42 * time.Millisecond}
	f := func(v float64) *float64 { return &v }
	ms := int64(42)
	for _, tc := range []struct {
		name  string
		res   eventqueue.CompactResult
		limit int64
		via   string
		want  LogCompactView
	}{
		{
			"dry run, limit set",
			eventqueue.CompactResult{DryRun: true, Plan: plan},
			1000, ViaOffline,
			LogCompactView{
				SchemaVersion: "1", DryRun: true, Via: "offline", BytesBefore: 500, BytesAfter: 5, RecordsBefore: 30, RecordsAfter: 2,
				EventsKept: 1, EventsDropped: 9, GatesKept: 1, LimitBytes: 1000, PercentBefore: f(50), PercentAfter: f(0.5),
			},
		},
		{
			"real run uses the run's own numbers, no limit",
			eventqueue.CompactResult{Compacted: true, Plan: plan, Stats: stats},
			0, ViaDaemon,
			LogCompactView{
				SchemaVersion: "1", Compacted: true, Via: "daemon", BytesBefore: 520, BytesAfter: 7, RecordsBefore: 31, RecordsAfter: 3,
				EventsKept: 1, EventsDropped: 9, GatesKept: 1, DurationMs: &ms,
			},
		},
		{
			"no progress",
			eventqueue.CompactResult{Plan: eventqueue.CompactPlan{BytesBefore: 80, BytesAfter: 80}},
			0, ViaDaemon,
			LogCompactView{SchemaVersion: "1", NoProgress: true, Via: "daemon", BytesBefore: 80, BytesAfter: 80},
		},
		{
			"torn tail is surfaced",
			eventqueue.CompactResult{DryRun: true, Plan: eventqueue.CompactPlan{BytesBefore: 100, BytesAfter: 10, Torn: true}},
			0, ViaOffline,
			LogCompactView{SchemaVersion: "1", DryRun: true, Via: "offline", BytesBefore: 100, BytesAfter: 10, Torn: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewLogCompactView(tc.res, tc.limit, tc.via)
			gb, _ := json.Marshal(got)
			wb, _ := json.Marshal(tc.want)
			if !bytes.Equal(gb, wb) {
				t.Fatalf("view\n got: %s\nwant: %s", gb, wb)
			}
			var asMap map[string]any
			if err := json.Unmarshal(gb, &asMap); err != nil {
				t.Fatal(err)
			}
			if err := conformance.Check(LogCompactReplySchema, asMap); err != nil {
				t.Fatalf("view fails its own schema: %v", err)
			}
		})
	}
}

// log-compact alone gets a longer connection deadline than the 5 s of every other
// verb: compacting a large log outlives it, and a reply lost to the deadline would
// hide a compaction that did happen.
func TestServerDeadlineFor(t *testing.T) {
	for _, sub := range []string{SubcommandStatus, SubcommandPause, SubcommandGateSet, SubcommandIngestEvent, "nonsense"} {
		if got := serverDeadlineFor(sub); got != serverCallDeadline {
			t.Errorf("serverDeadlineFor(%q) = %v, want the default %v", sub, got, serverCallDeadline)
		}
	}
	if got := serverDeadlineFor(SubcommandLogCompact); got != LogCompactCallTimeout || got <= serverCallDeadline {
		t.Errorf("serverDeadlineFor(log-compact) = %v, want %v (> %v)", got, LogCompactCallTimeout, serverCallDeadline)
	}
}
