package core

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sort"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/schemas"
)

// SubcommandMonRead is the INTF-MON pull-direction wire verb (Task 3.6):
// a caller already registered as kind=monitor reads the core's current
// metric values for its registration's config-resolved subset
// (Options.MonitorSubsets, Task 3.6-prereq). The push direction stays
// register-held for a future phase (pg2-ov09n).
const SubcommandMonRead = "mon.read"

// The message types backing this subcommand (schemas/, checked via package
// conformance — INV-INTF-2). Both schemas already existed with goldens
// before this task (Task 3.6 Contract); this file only wires the handler.
const (
	MonReadRequestSchema = "mon.read"
	MonReadReplySchema   = "mon.read-reply"
)

// monReadRequest is the decoded mon.read envelope. Unlike self-status's
// separate tracking `id` / `participantId` pair, `id` here does double duty
// as both the tracking id AND the caller's own registration id: INTF-MON's
// pull direction is a synchronous request/reply over one connection (not a
// decoupled callback push), and mon.read.schema.json declares no separate
// participant-identity field — see the Task 3.6 Binding decisions on how a
// caller's id resolves against the registry.
type monReadRequest struct {
	ID      string   `json:"id"`
	Metrics []string `json:"metrics"`
}

// monReadValue is one entry of the reply's `values` array.
type monReadValue struct {
	Name   string         `json:"name"`
	Value  float64        `json:"value"`
	Labels map[string]any `json:"labels,omitempty"`
}

// monReadReply is the cli mon.read-reply shape.
type monReadReply struct {
	SchemaVersion string         `json:"schemaVersion"`
	ID            string         `json:"id"`
	Values        []monReadValue `json:"values"`
}

// handleMonRead runs the `mon.read` verb (INTF-MON pull; Task 3.6). The
// caller's `id` MUST already name a kind=monitor registration — "gated on
// the caller having previously registered" (Task 3.6 Objective) — an id
// with no registration, or one registered as a different kind, is refused
// with the protocol error envelope rather than answered with an empty
// reply, the same report-don't-guess posture selfstatus.go's unknown-
// participant handling already uses.
//
// The reply is composed strictly from Task 3.3's Emitter counters — read
// back via Options.MetricsReader (Task 3.6-prereq) — filtered to that
// registration's config-resolved Subset (Task 3.6 Binding decisions: "The
// reply is composed strictly from Task 3.3's Emitter counters filtered to
// the sink's configured subset — no new counters are introduced"). The
// request's own `metrics` list can only narrow within that subset, never
// widen it: a requested name outside Subset is silently omitted, never an
// error — the subset is a ceiling the caller does not get to raise by
// asking.
func (s *Service) handleMonRead(stdin io.Reader, stdout io.Writer) int {
	data, err := io.ReadAll(stdin)
	if err != nil {
		writeBody(stdout, errorReply("mon.read: read request: "+err.Error()))
		return conformance.ExitError
	}
	if err := conformance.CheckBytes(MonReadRequestSchema, data); err != nil {
		writeBody(stdout, errorReply("mon.read: "+err.Error()))
		return conformance.ExitError
	}
	var req monReadRequest
	if err := json.Unmarshal(data, &req); err != nil {
		// Unreachable once CheckBytes has passed: the schema already proved this
		// decodes as a well-typed object with these fields — see selfstatus.go's
		// identical note.
		writeBody(stdout, errorReply("mon.read: malformed request: "+err.Error()))
		return conformance.ExitError
	}

	reg, ok := s.reg.Get(req.ID)
	if !ok || reg.Kind != KindMonitor {
		slog.Warn("core: mon.read rejected for an id with no kind=monitor registration", "id", req.ID)
		writeBody(stdout, errorReply("mon.read: no kind=monitor registration for id "+req.ID))
		return conformance.ExitError
	}

	values := []monReadValue{}
	if s.metricsReader != nil {
		rm, err := s.metricsReader.Snapshot(context.Background())
		if err != nil {
			writeBody(stdout, errorReply("mon.read: snapshot: "+err.Error()))
			return conformance.ExitError
		}
		values = composeMonReadValues(rm, req.Metrics, reg.Subset)
	}
	// A nil MetricsReader (no read-back wired, e.g. a deployment-bound
	// external MeterProvider — see internal/metrics.NewReadableProvider's
	// doc) answers with an empty values list rather than an error: the
	// registration and request are both valid, there is simply nothing to
	// read back from, the same "absence means zero" posture the catalog's
	// own observable gauges already use.

	body, err := json.Marshal(monReadReply{SchemaVersion: schemas.SchemaVersion, ID: req.ID, Values: values})
	if err != nil { // unreachable: monReadReply holds only strings, float64s and string-keyed maps of JSON-safe scalars
		writeBody(stdout, errorReply("mon.read: marshal reply: "+err.Error()))
		return conformance.ExitError
	}
	writeBody(stdout, body)
	return conformance.ExitOK
}

// composeMonReadValues renders rm's current data points down to the
// mon.read-reply wire shape, restricted to metric names present in BOTH
// requested (the caller's own `metrics` list) and subset (its
// registration's config-resolved catalog subset) — subset is a ceiling
// requested can narrow but never widen (Task 3.6 Binding decisions).
func composeMonReadValues(rm metricdata.ResourceMetrics, requested, subset []string) []monReadValue {
	allowed := make(map[string]bool, len(subset))
	for _, name := range subset {
		allowed[name] = true
	}
	wanted := make(map[string]bool, len(requested))
	for _, name := range requested {
		if allowed[name] {
			wanted[name] = true
		}
	}
	values := []monReadValue{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if !wanted[m.Name] {
				continue
			}
			values = append(values, monReadDataPoints(m)...)
		}
	}
	// Deterministic order: ScopeMetrics/Metrics iteration is already stable
	// (slices, not maps), but this sort makes that an explicit guarantee of
	// the wire reply rather than an accident of OTel's internal ordering.
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	return values
}

// monReadDataPoints flattens one metric's current data points into the
// {name, value, labels} wire shape. A Histogram catalog member (the
// catalog's one, pr_pool.dispatch_latency) has no single scalar projection
// defined by this task's Contract and is deliberately omitted rather than
// guessed at — a later task's concern if a sink's subset ever names it.
func monReadDataPoints(m metricdata.Metrics) []monReadValue {
	switch data := m.Data.(type) {
	case metricdata.Sum[int64]:
		return monReadPoints(m.Name, data.DataPoints)
	case metricdata.Sum[float64]:
		return monReadPoints(m.Name, data.DataPoints)
	case metricdata.Gauge[int64]:
		return monReadPoints(m.Name, data.DataPoints)
	case metricdata.Gauge[float64]:
		return monReadPoints(m.Name, data.DataPoints)
	default:
		return nil
	}
}

// monReadPoints converts one instrument kind's data points (Sum and Gauge
// share the identical metricdata.DataPoint[N] shape) into wire values.
func monReadPoints[N int64 | float64](name string, pts []metricdata.DataPoint[N]) []monReadValue {
	out := make([]monReadValue, 0, len(pts))
	for _, dp := range pts {
		out = append(out, monReadValue{Name: name, Value: float64(dp.Value), Labels: attrLabels(dp.Attributes)})
	}
	return out
}

// attrLabels renders an OTel attribute.Set as the reply's plain `labels`
// object, or nil (omitted) when the data point carries no attributes at all
// (the catalog's scalar members, e.g. pr_pool.backlog).
func attrLabels(attrs attribute.Set) map[string]any {
	if attrs.Len() == 0 {
		return nil
	}
	out := make(map[string]any, attrs.Len())
	iter := attrs.Iter()
	for iter.Next() {
		kv := iter.Attribute()
		out[string(kv.Key)] = kv.Value.AsInterface()
	}
	return out
}
