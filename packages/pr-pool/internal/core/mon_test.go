package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/metrics"
)

// startedServiceWithMonitoring returns a started service carrying the two
// Task 3.6-prereq seams a mon.read handler composes its reply from:
// metricsReader (the value-read-back handle) and monitorSubsets (the
// registration-id -> catalog-subset resolver Service.Register consults).
// Built as a literal, the same pattern startedServiceWith already uses,
// because MetricsReader/MonitorSubsets have no Options-level test
// constructor of their own.
func startedServiceWithMonitoring(t *testing.T, metricsReader MetricsReader, monitorSubsets MonitorSubsetResolver) *Service {
	t.Helper()
	return &Service{
		state:          conformance.Started,
		q:              newQueue(t),
		bindings:       testBindings(),
		reg:            NewRegistry(nil),
		command:        "pr-pool",
		metricsReader:  metricsReader,
		monitorSubsets: monitorSubsets,
	}
}

// serveMonRead runs the mon.read subcommand IN PROCESS through the
// participant boundary and returns the decoded reply plus exit code — the
// same shape serveIngest (ingest_test.go) already uses.
func serveMonRead(t *testing.T, svc *Service, request string) (map[string]any, int) {
	t.Helper()
	var out strings.Builder
	code := svc.Serve(SubcommandMonRead, strings.NewReader(request), &out)
	var reply map[string]any
	if err := json.Unmarshal([]byte(out.String()), &reply); err != nil {
		t.Fatalf("reply %q is not JSON: %v", out.String(), err)
	}
	return reply, code
}

// TestServeMonRead is this task's red-first proof (Task 3.6 Step 1/5): a
// kind=monitor sink that registered IN-PROCESS (Task 3.6 Binding decisions —
// no wire-level register round-trip is simulated) reads back its
// config-resolved subset, and a name outside that subset is silently
// dropped from the reply even though the caller asked for it.
func TestServeMonRead(t *testing.T) {
	mp, reader := metrics.NewReadableProvider()
	emitter, err := metrics.New(mp, func() map[string]int { return nil })
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}
	// Drive a real, observable value for exactly one catalog member.
	emitter.OnUnconsumedExpired("review-requested")

	const monitorID = "m-1"
	subsets := func(id string) []string {
		if id == monitorID {
			return []string{metrics.MetricUnconsumedExpired}
		}
		return nil
	}
	svc := startedServiceWithMonitoring(t, reader, subsets)

	reg, err := svc.Register(monitorID, KindMonitor)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(reg.Subset) != 1 || reg.Subset[0] != metrics.MetricUnconsumedExpired {
		t.Fatalf("Register's resolved Subset = %v, want [%s]", reg.Subset, metrics.MetricUnconsumedExpired)
	}

	// Ask for the in-subset member AND one outside it (queue depth was never
	// granted to this registration) — the reply must only ever carry the
	// former.
	request := `{"schemaVersion":"1","id":"m-1","metrics":["` +
		metrics.MetricUnconsumedExpired + `","` + metrics.MetricQueueDepth + `"]}`
	reply, code := serveMonRead(t, svc, request)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d; reply=%v", code, conformance.ExitOK, reply)
	}
	if err := conformance.Check(MonReadReplySchema, reply); err != nil {
		t.Fatalf("reply failed its own schema (INV-INTF-2): %v", err)
	}
	if reply["id"] != monitorID {
		t.Fatalf("id = %v, want the tracking id echoed back", reply["id"])
	}
	values, ok := reply["values"].([]any)
	if !ok || len(values) != 1 {
		t.Fatalf("values = %v, want exactly 1 entry (the queue-depth request must be dropped, not in subset)", reply["values"])
	}
	entry, ok := values[0].(map[string]any)
	if !ok {
		t.Fatalf("values[0] = %v, want an object", values[0])
	}
	if entry["name"] != metrics.MetricUnconsumedExpired {
		t.Fatalf("values[0].name = %v, want %s", entry["name"], metrics.MetricUnconsumedExpired)
	}
	if entry["value"] != float64(1) {
		t.Fatalf("values[0].value = %v, want 1 (one OnUnconsumedExpired call)", entry["value"])
	}
	labels, ok := entry["labels"].(map[string]any)
	if !ok || labels["type"] != "review-requested" {
		t.Fatalf("values[0].labels = %v, want {type: review-requested}", entry["labels"])
	}
}

// A caller whose id never registered at all is refused, not answered with
// an empty reply — "gated on the caller having previously registered".
func TestServeMonRead_UnregisteredIDRejected(t *testing.T) {
	svc := startedServiceWithMonitoring(t, nil, nil)
	_, code := serveMonRead(t, svc, `{"schemaVersion":"1","id":"never-registered","metrics":[]}`)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want %d for an id with no registration", code, conformance.ExitError)
	}
}

// A caller registered as a DIFFERENT kind (e.g. source) must not be able to
// read metrics via mon.read just because some id happens to match.
func TestServeMonRead_WrongKindRejected(t *testing.T) {
	svc := startedServiceWithMonitoring(t, nil, nil)
	if _, err := svc.Register("src-1", KindSource); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, code := serveMonRead(t, svc, `{"schemaVersion":"1","id":"src-1","metrics":[]}`)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want %d for a non-monitor registration", code, conformance.ExitError)
	}
}

// A nil MetricsReader (no read-back wired) answers with an empty values
// list, not an error — the registration and request are both valid.
func TestServeMonRead_NilMetricsReaderIsEmptyNotError(t *testing.T) {
	subsets := func(string) []string { return []string{metrics.MetricQueueDepth} }
	svc := startedServiceWithMonitoring(t, nil, subsets)
	if _, err := svc.Register("m-2", KindMonitor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reply, code := serveMonRead(t, svc, `{"schemaVersion":"1","id":"m-2","metrics":["`+metrics.MetricQueueDepth+`"]}`)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d; reply=%v", code, conformance.ExitOK, reply)
	}
	values, ok := reply["values"].([]any)
	if !ok || len(values) != 0 {
		t.Fatalf("values = %v, want an empty list (no MetricsReader wired)", reply["values"])
	}
}
