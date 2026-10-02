package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// Tests for `pg-router log compact [--dry-run] [--json]` (bead pg2-maxn1).

// writeBloatedLog writes dir/queue.jsonl with `dead` evicted events (three records
// each), one live event and one gate, and returns its path.
func writeBloatedLog(t *testing.T, dir string, dead int) string {
	t.Helper()
	path := filepath.Join(dir, "queue.jsonl")
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

func fileSum(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func dirNames(t *testing.T, dir string) string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

// runLogCompactCapture runs logCompact and returns its stdout, stderr and exit.
func runLogCompactCapture(t *testing.T, o logCompactOpts) (string, string, int) {
	t.Helper()
	var stdout, stderr strings.Builder
	code := logCompact(&stdout, &stderr, o)
	return stdout.String(), stderr.String(), code
}

func decodeLogCompactJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("--json output %q is not JSON: %v", out, err)
	}
	if err := conformance.Check(core.LogCompactReplySchema, m); err != nil {
		t.Fatalf("--json output fails %s: %v\n%s", core.LogCompactReplySchema, err, out)
	}
	return m
}

// startCoreOverFile brings up a real core whose queue is a real FileStore over
// logDir/queue.jsonl (so the daemon holds the log's lock, as in production).
func startCoreOverFile(t *testing.T, logDir string) (*core.Service, *eventqueue.Queue) {
	t.Helper()
	fs, err := eventqueue.NewFileStore(filepath.Join(logDir, "queue.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	q, err := eventqueue.New(fs, eventqueue.WithLogLimits(0, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := core.Listen(core.Options{LogDir: logDir, Queue: q, Bindings: core.NewBindings("t1")})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Accept(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Accept = %v, want nil", err)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := core.Discover(logDir); err == nil {
			return svc, q
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("core never became discoverable")
	return nil, nil
}

// Against a running daemon the request goes over its socket: the dry run changes
// nothing and predicts the real run, which compacts inside the daemon (the daemon
// holds the lock, so no second process may touch the file) and is then recorded
// as the last (manual) compaction.
func TestLogCompact_AgainstRunningDaemon(t *testing.T) {
	dir := shortDir(t)
	path := writeBloatedLog(t, dir, 300)
	_, q := startCoreOverFile(t, dir)
	sum := fileSum(t, path)

	out, errOut, code := runLogCompactCapture(t, logCompactOpts{dryRun: true, asJSON: true, logDir: dir})
	if code != exitOK {
		t.Fatalf("dry run exit = %d; stderr=%s", code, errOut)
	}
	dry := decodeLogCompactJSON(t, out)
	if dry["via"] != "daemon" || dry["dryRun"] != true || dry["compacted"] != false || dry["eventsDropped"] != float64(300) || dry["eventsKept"] != float64(1) {
		t.Fatalf("dry-run reply = %v", dry)
	}
	if fileSum(t, path) != sum || q.Compactions() != 0 {
		t.Fatal("the dry run changed the log or counted as a compaction")
	}

	human, errOut, code := runLogCompactCapture(t, logCompactOpts{dryRun: true, logDir: dir})
	if code != exitOK {
		t.Fatalf("human dry run exit = %d; stderr=%s", code, errOut)
	}
	for _, want := range []string{"dry run, via the running daemon", "nothing was changed", "before:", "after:", "events: 1 kept, 300 dropped", "gates kept: 1", "% of the 1.0 MiB limit", "a real run would shrink the log by"} {
		if !strings.Contains(human, want) {
			t.Errorf("dry-run summary lacks %q:\n%s", want, human)
		}
	}

	out, errOut, code = runLogCompactCapture(t, logCompactOpts{asJSON: true, logDir: dir})
	if code != exitOK {
		t.Fatalf("real run exit = %d; stderr=%s", code, errOut)
	}
	real := decodeLogCompactJSON(t, out)
	if real["compacted"] != true || real["bytesAfter"] != dry["bytesAfter"] || real["bytesBefore"] != dry["bytesBefore"] {
		t.Fatalf("real run %v did not do what the dry run %v predicted", real, dry)
	}
	if fi, _ := os.Stat(path); float64(fi.Size()) != real["bytesAfter"] {
		t.Fatalf("log is %d bytes, reply said %v", fi.Size(), real["bytesAfter"])
	}
	if info, ok := q.LastCompaction(); !ok || info.Trigger != "manual" {
		t.Fatalf("LastCompaction = %+v, %v; want manual", info, ok)
	}

	human, _, code = runLogCompactCapture(t, logCompactOpts{logDir: dir})
	if code != exitOK || !strings.Contains(human, "nothing to do") || !strings.Contains(human, "no progress is possible") {
		t.Fatalf("second run = %d:\n%s", code, human)
	}
}

// With no daemon the dry run reads the file and nothing else: byte-identical log,
// no temp file, no lock file created; the real run then compacts it offline.
func TestLogCompact_OfflineDryRunThenReal(t *testing.T) {
	dir := shortDir(t)
	path := writeBloatedLog(t, dir, 300)
	sum, names := fileSum(t, path), dirNames(t, dir)
	limit := func() int64 { return 1 << 20 }

	out, errOut, code := runLogCompactCapture(t, logCompactOpts{dryRun: true, asJSON: true, logDir: dir, limit: limit})
	if code != exitOK {
		t.Fatalf("offline dry run exit = %d; stderr=%s", code, errOut)
	}
	dry := decodeLogCompactJSON(t, out)
	if dry["via"] != "offline" || dry["dryRun"] != true || dry["eventsDropped"] != float64(300) || dry["limitBytes"] != float64(1<<20) {
		t.Fatalf("dry-run reply = %v", dry)
	}
	if _, has := dry["wouldRefuse"]; has {
		t.Fatalf("nothing holds the log, yet the dry run says a real run would be refused: %v", dry)
	}
	if fileSum(t, path) != sum || dirNames(t, dir) != names {
		t.Fatalf("the offline dry run changed the directory: %q -> %q", names, dirNames(t, dir))
	}

	human, errOut, code := runLogCompactCapture(t, logCompactOpts{dryRun: true, logDir: dir, limit: limit})
	if code != exitOK || !strings.Contains(human, "offline, no daemon running") {
		t.Fatalf("human offline dry run = %d:\n%s\n%s", code, human, errOut)
	}

	out, errOut, code = runLogCompactCapture(t, logCompactOpts{asJSON: true, logDir: dir, limit: limit})
	if code != exitOK {
		t.Fatalf("offline real run exit = %d; stderr=%s", code, errOut)
	}
	real := decodeLogCompactJSON(t, out)
	if real["compacted"] != true || real["via"] != "offline" || real["bytesAfter"] != dry["bytesAfter"] {
		t.Fatalf("real run %v vs dry run %v", real, dry)
	}
	if held, _ := eventqueue.LogLocked(path); held {
		t.Fatal("the offline run kept the log lock")
	}
	if _, err := os.Stat(path + ".compact.tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
	human, _, code = runLogCompactCapture(t, logCompactOpts{logDir: dir, limit: limit})
	if code != exitOK || !strings.Contains(human, "nothing to do") {
		t.Fatalf("second offline run = %d:\n%s", code, human)
	}
}

// A log some other process holds (no daemon answering on a socket) refuses a real
// offline run with exit 1 and a message naming the lock, leaving the log
// byte-identical; the dry run still exits 0 and says a real run would be refused.
func TestLogCompact_OfflineRefusedWhileLocked(t *testing.T) {
	dir := shortDir(t)
	path := writeBloatedLog(t, dir, 50)
	sum := fileSum(t, path)
	holder, err := eventqueue.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()

	_, errOut, code := runLogCompactCapture(t, logCompactOpts{logDir: dir})
	if code != exitGeneric {
		t.Fatalf("real run against a locked log: exit = %d, want %d", code, exitGeneric)
	}
	for _, want := range []string{"log compact:", "in use by another pg-router process", "queue.jsonl.lock"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("refusal lacks %q:\n%s", want, errOut)
		}
	}
	if fileSum(t, path) != sum {
		t.Fatal("a refused run changed the log")
	}

	out, errOut, code := runLogCompactCapture(t, logCompactOpts{dryRun: true, asJSON: true, logDir: dir})
	if code != exitOK {
		t.Fatalf("dry run against a locked log: exit = %d; stderr=%s", code, errOut)
	}
	dry := decodeLogCompactJSON(t, out)
	if s, _ := dry["wouldRefuse"].(string); !strings.Contains(s, "locked") {
		t.Fatalf("dry run must report that a real run would be refused: %v", dry)
	}
	human, _, _ := runLogCompactCapture(t, logCompactOpts{dryRun: true, logDir: dir})
	if !strings.Contains(human, "a real run would be REFUSED (exit 1): the log is locked by another pg-router process") {
		t.Fatalf("human dry run lacks the refusal:\n%s", human)
	}
	if held, _ := eventqueue.LogLocked(path); !held {
		t.Fatal("the dry run released the holder's lock")
	}
}

// A core named explicitly by --socket never falls back to the offline path: the
// operator asked for that daemon, and touching the file behind its back is exactly
// what the lock exists to prevent.
func TestLogCompact_ExplicitSocketNeverFallsBackOffline(t *testing.T) {
	dir := shortDir(t)
	path := writeBloatedLog(t, dir, 20)
	sum := fileSum(t, path)
	_, errOut, code := runLogCompactCapture(t, logCompactOpts{logDir: dir, socket: filepath.Join(dir, "nope.sock"), token: "t"})
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want %d; stderr=%s", code, conformance.ExitError, errOut)
	}
	if fileSum(t, path) != sum || dirNames(t, dir) != "queue.jsonl" {
		t.Fatalf("an unreachable explicit socket fell back to touching the log: %s", dirNames(t, dir))
	}
}

// A missing log is not an error, and creates nothing.
func TestLogCompact_OfflineNoLog(t *testing.T) {
	dir := shortDir(t)
	for _, dry := range []bool{true, false} {
		out, errOut, code := runLogCompactCapture(t, logCompactOpts{dryRun: dry, asJSON: true, logDir: dir})
		if code != exitOK {
			t.Fatalf("dry=%v: exit = %d; stderr=%s", dry, code, errOut)
		}
		if v := decodeLogCompactJSON(t, out); v["bytesBefore"] != float64(0) || v["compacted"] != false {
			t.Fatalf("dry=%v: %v", dry, v)
		}
		if got := dirNames(t, dir); got != "" {
			t.Fatalf("dry=%v: a missing log was created or locked: %q", dry, got)
		}
	}
}

func TestRoute_logCompact(t *testing.T) {
	if got := route([]string{"pg-router", "log", "compact", "--dry-run"}); got.kind != routeLog || len(got.rest) != 2 {
		t.Fatalf("route = %+v, want routeLog with rest [compact --dry-run]", got)
	}
	if !strings.Contains(usageLine, "log compact [--dry-run]") || !strings.Contains(helpText, "log compact [--dry-run] [--json]") {
		t.Fatal("log compact is missing from the usage line or the help text")
	}
}

func TestRunLog_usageErrors(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"purge"},
		{"compact", "extra"},
		{"compact", "--no-such-flag"},
	} {
		if got := runLog(args); got != conformance.ExitUsage {
			t.Errorf("runLog(%v) = %d, want %d (usage)", args, got, conformance.ExitUsage)
		}
	}
}

// The human summary for each shape of result.
func TestRenderLogCompact(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	ms := int64(41)
	base := core.LogCompactView{
		SchemaVersion: "1", Via: core.ViaDaemon, BytesBefore: 33282518, BytesAfter: 4096, RecordsBefore: 182157, RecordsAfter: 9,
		EventsKept: 2, EventsDropped: 14106, GatesKept: 1,
	}
	for _, tc := range []struct {
		name  string
		mod   func(*core.LogCompactView)
		wants []string
		nots  []string
	}{
		{
			"dry run", func(v *core.LogCompactView) {
				v.DryRun = true
				v.LimitBytes = 67108864
				v.PercentBefore = f(49.6)
				v.PercentAfter = f(0)
			},
			[]string{"dry run, via the running daemon", "31.7 MiB (33282518 bytes), 49.6% of the 64.0 MiB limit, 182157 records", "4.0 KiB (4096 bytes), 0.0% of the 64.0 MiB limit, 9 records", "2 kept, 14106 dropped", "a real run would shrink the log by 31.7 MiB"},
			nil,
		},
		{
			"dry run, no progress", func(v *core.LogCompactView) { v.DryRun = true; v.NoProgress = true },
			[]string{"a real run would do nothing"},
			[]string{"would shrink"},
		},
		{"dry run, torn", func(v *core.LogCompactView) { v.DryRun = true; v.Torn = true }, []string{"undecodable line"}, nil},
		{
			"dry run, would refuse", func(v *core.LogCompactView) { v.DryRun = true; v.Via = core.ViaOffline; v.WouldRefuse = "locked" },
			[]string{"offline, no daemon running", "a real run would be REFUSED (exit 1): locked"},
			[]string{"would shrink"},
		},
		{
			"real run", func(v *core.LogCompactView) { v.Compacted = true; v.DurationMs = &ms },
			[]string{"log compacted (via the running daemon, 41ms)", "after:  4.0 KiB"},
			[]string{"dry run", "nothing to do"},
		},
		{
			"nothing to do", func(v *core.LogCompactView) { v.NoProgress = true },
			[]string{"nothing to do", "no progress is possible"},
			[]string{"after:", "compacted ("},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := base
			tc.mod(&v)
			var out strings.Builder
			renderLogCompact(&out, v)
			for _, w := range tc.wants {
				if !strings.Contains(out.String(), w) {
					t.Errorf("summary lacks %q:\n%s", w, out.String())
				}
			}
			for _, n := range tc.nots {
				if strings.Contains(out.String(), n) {
					t.Errorf("summary must not contain %q:\n%s", n, out.String())
				}
			}
		})
	}
}

// offlineLogLimit follows PG_ROUTER_MAX_LOG_BYTES and tolerates a broken config.
func TestOfflineLogLimit(t *testing.T) {
	t.Setenv("PG_ROUTER_MAX_LOG_BYTES", "128MiB")
	t.Setenv("PG_ROUTER_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	if got := offlineLogLimit(); got != 128<<20 {
		t.Fatalf("offlineLogLimit = %d, want %d", got, 128<<20)
	}
	t.Setenv("PG_ROUTER_MAX_LOG_BYTES", "lots")
	if got := offlineLogLimit(); got != 0 {
		t.Fatalf("offlineLogLimit with an unusable config = %d, want 0 (no percentages)", got)
	}
}
