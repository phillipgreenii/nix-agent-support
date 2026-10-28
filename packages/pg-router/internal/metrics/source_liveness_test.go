package metrics

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Per-source last-success timestamp, last-success age and expected-interval
// gauges (bead pg2-tv11a, DEC-OBS-4; the age gauge is bead pg2-ez292).

type sourceLivenessHarness struct {
	emitter *Emitter
	reader  *sdkmetric.ManualReader
	clk     *mockClock
}

func newSourceLivenessHarness(t *testing.T, sources map[string]time.Duration) *sourceLivenessHarness {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	clk := &mockClock{t: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	e, err := New(mp, func() map[string]int { return nil }, WithClock(clk.now), WithPullSources(sources))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &sourceLivenessHarness{emitter: e, reader: reader, clk: clk}
}

// floatGauge returns every source->value pair of the named float gauge.
func (h *sourceLivenessHarness) floatGauge(t *testing.T, name string) map[string]float64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	out := map[string]float64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[float64])
			if !ok {
				t.Fatalf("%s is %T, want a float64 gauge", name, m.Data)
			}
			if m.Unit != "s" {
				t.Errorf("%s unit = %q, want s (so the exposition suffix is _seconds)", name, m.Unit)
			}
			for _, dp := range g.DataPoints {
				v, _ := dp.Attributes.Value(attribute.Key("source"))
				out[v.AsString()] = dp.Value
			}
		}
	}
	return out
}

func unix(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func TestSourceLiveness_InitialisedToProcessStartNotZero(t *testing.T) {
	h := newSourceLivenessHarness(t, map[string]time.Duration{"pr-team": time.Minute, "thread-me": 30 * time.Minute})
	got := h.floatGauge(t, MetricSourceLastSuccess)
	for _, src := range []string{"pr-team", "thread-me"} {
		if got[src] != unix(h.clk.t) {
			t.Errorf("last-success{%s} = %v, want process start %v (never 0)", src, got[src], unix(h.clk.t))
		}
	}
	iv := h.floatGauge(t, MetricSourceExpectedInterval)
	if iv["pr-team"] != 60 || iv["thread-me"] != 1800 {
		t.Errorf("expected-interval = %v, want pr-team 60 and thread-me 1800 seconds", iv)
	}
}

func TestSourceLiveness_AdvancesOnSuccessAndPauseButNotFailure(t *testing.T) {
	h := newSourceLivenessHarness(t, map[string]time.Duration{"a": time.Minute, "b": time.Minute, "c": time.Minute})
	start := h.clk.t

	h.clk.advance(10 * time.Minute)
	h.emitter.OnSourceSucceeded("a")
	h.emitter.OnSourceFailure("b", errors.New("boom"))
	h.emitter.OnSourcePaused("c")

	got := h.floatGauge(t, MetricSourceLastSuccess)
	if got["a"] != unix(h.clk.t) {
		t.Errorf("a (succeeded) = %v, want advanced to %v", got["a"], unix(h.clk.t))
	}
	if got["b"] != unix(start) {
		t.Errorf("b (failed) = %v, want untouched at start %v: a failure must not advance the gauge", got["b"], unix(start))
	}
	if got["c"] != unix(h.clk.t) {
		t.Errorf("c (paused by a gate or halt) = %v, want advanced to %v: a pause is not a failure", got["c"], unix(h.clk.t))
	}
}

// Only the sources handed to WithPullSources are exported: a disabled,
// excluded or push source is simply never passed, and a late notification for
// an unknown name must not mint a new series.
func TestSourceLiveness_OnlyRegisteredSourcesAreExported(t *testing.T) {
	h := newSourceLivenessHarness(t, map[string]time.Duration{"pulled": time.Minute, "no-period": 0})
	h.emitter.OnSourceSucceeded("stranger")
	h.emitter.OnSourcePaused("stranger")

	for _, name := range []string{MetricSourceLastSuccess, MetricSourceLastSuccessAge, MetricSourceExpectedInterval} {
		got := h.floatGauge(t, name)
		if len(got) != 1 {
			t.Errorf("%s series = %v, want only the registered pull source with a period", name, got)
		}
		if _, ok := got["pulled"]; !ok {
			t.Errorf("%s missing the registered source: %v", name, got)
		}
	}
}

func TestSourceLiveness_NotRegisteredWithoutOption(t *testing.T) {
	h := newHarness(t)
	// The no-op hooks must be safe when no gauge exists.
	h.emitter.OnSourceSucceeded("x")
	h.emitter.OnSourcePaused("x")
	rm := h.collect(t)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == MetricSourceLastSuccess || m.Name == MetricSourceLastSuccessAge || m.Name == MetricSourceExpectedInterval {
				t.Errorf("%s registered without WithPullSources", m.Name)
			}
		}
	}
}

// --- the last-success AGE gauge (bead pg2-ez292) ------------------------------
//
// The persistent-failure alert reads this gauge, not time() minus the timestamp:
// the age is measured on the router's monotonic clock, which a host sleep does not
// advance, so it means "N periods of awake time without a success".

// hostClock models a laptop that can sleep. wall is the wall clock the Emitter is
// given (WithClock); slept records the windows in which the monotonic clock did not
// advance. awakeSince is what WithAwakeSince receives: the wall time elapsed since t
// minus the sleep inside that span. (A time.Time cannot carry a wall reading that
// jumped ahead of its monotonic reading, so the sleep has to be modelled outside the
// Time values; the production default is the monotonic subtraction itself, pinned by
// TestSourceLiveness_AgeUsesTheMonotonicReadingByDefault.)
type hostClock struct {
	wall  time.Time
	slept []sleepWindow
}

type sleepWindow struct{ from, to time.Time }

func (c *hostClock) now() time.Time { return c.wall }

// run is awake time: both clocks advance.
func (c *hostClock) run(d time.Duration) { c.wall = c.wall.Add(d) }

// sleep is the lid closed for d: the wall clock advances, the monotonic clock does not.
func (c *hostClock) sleep(d time.Duration) {
	c.slept = append(c.slept, sleepWindow{from: c.wall, to: c.wall.Add(d)})
	c.wall = c.wall.Add(d)
}

func (c *hostClock) awakeSince(t time.Time) time.Duration {
	d := c.wall.Sub(t)
	for _, w := range c.slept {
		lo, hi := w.from, w.to
		if lo.Before(t) {
			lo = t
		}
		if hi.After(c.wall) {
			hi = c.wall
		}
		if hi.After(lo) {
			d -= hi.Sub(lo)
		}
	}
	return d
}

func newSleepHarness(t *testing.T, sources map[string]time.Duration) (*sourceLivenessHarness, *hostClock) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	host := &hostClock{wall: time.Date(2026, 10, 12, 9, 6, 1, 0, time.UTC)}
	e, err := New(mp, func() map[string]int { return nil },
		WithClock(host.now), WithAwakeSince(host.awakeSince), WithPullSources(sources))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &sourceLivenessHarness{emitter: e, reader: reader}, host
}

func TestSourceLiveness_AgeStartsAtZeroAndGrowsWithAwakeTime(t *testing.T) {
	h, host := newSleepHarness(t, map[string]time.Duration{"a": time.Minute, "b": 30 * time.Minute})
	age := h.floatGauge(t, MetricSourceLastSuccessAge)
	if len(age) != 2 || age["a"] != 0 || age["b"] != 0 {
		t.Fatalf("age at process start = %v, want 0 for each registered source (a restart resets the clock)", age)
	}
	host.run(7 * time.Minute)
	age = h.floatGauge(t, MetricSourceLastSuccessAge)
	if age["a"] != 420 || age["b"] != 420 {
		t.Errorf("age after 7 awake minutes = %v, want 420 s each", age)
	}
}

// A success or a deliberate pause resets the age; a failure does not. Same rule as the
// timestamp gauge, because the age is derived from it.
func TestSourceLiveness_AgeResetsOnSuccessAndPauseButNotFailure(t *testing.T) {
	h, host := newSleepHarness(t, map[string]time.Duration{"a": time.Minute, "b": time.Minute, "c": time.Minute})
	host.run(10 * time.Minute)
	h.emitter.OnSourceSucceeded("a")
	h.emitter.OnSourceFailure("b", errors.New("boom"))
	h.emitter.OnSourcePaused("c")
	host.run(2 * time.Minute)
	age := h.floatGauge(t, MetricSourceLastSuccessAge)
	if age["a"] != 120 || age["c"] != 120 {
		t.Errorf("age after a success (a) or a pause (c) 2 minutes ago = %v, want 120 s", age)
	}
	if age["b"] != 720 {
		t.Errorf("age of the failing source b = %v, want 720 s (a failure must not reset it)", age["b"])
	}
}

// The regression for bead pg2-ez292. A laptop that sleeps for hours leaves the wall
// clock far ahead of the router's monotonic clock; the alert used to subtract the
// timestamp from time(), so every slow source looked silent for the whole sleep and
// the alert fired although nothing had failed. The age gauge must not grow across the
// jump, must still grow with awake time, and must still cross the alert threshold when
// a source really goes silent.
func TestSourceLiveness_HostSleepDoesNotAgeASource(t *testing.T) {
	h, host := newSleepHarness(t, map[string]time.Duration{"slow": 30 * time.Minute, "fast": time.Minute})
	const (
		slowThreshold = 90 * 60.0 // max(3 x 30m, 30m)
		fastThreshold = 30 * 60.0 // max(3 x 1m, 30m)
	)

	host.run(10 * time.Minute)
	h.emitter.OnSourceSucceeded("slow")
	h.emitter.OnSourceSucceeded("fast")
	succeededAt := unix(host.wall)
	host.run(5 * time.Minute)

	host.sleep(8 * time.Hour) // the lid closes: the wall clock runs on, the monotonic clock stands still

	age := h.floatGauge(t, MetricSourceLastSuccessAge)
	ts := h.floatGauge(t, MetricSourceLastSuccess)
	for _, src := range []string{"slow", "fast"} {
		if age[src] != 300 {
			t.Errorf("age[%s] = %v after a %v sleep, want 300 s (the 5 awake minutes since its success): sleep must not count", src, age[src], 8*time.Hour)
		}
		if ts[src] != succeededAt {
			t.Errorf("last-success timestamp[%s] = %v, want unchanged %v: the wall-clock gauge is kept as it was", src, ts[src], succeededAt)
		}
		// The retired expression, time() - timestamp, read this and fired. Pin that the
		// scenario really is one it would have fired on, so the assertions above are not vacuous.
		wallAge := unix(host.wall) - ts[src]
		if wallAge <= slowThreshold || wallAge <= fastThreshold {
			t.Fatalf("scenario too small: wall-clock age %v s does not exceed both thresholds", wallAge)
		}
	}

	// Awake time resumes: the age grows again, and a source that stays silent crosses its
	// threshold on awake time alone.
	host.run(26 * time.Minute)
	age = h.floatGauge(t, MetricSourceLastSuccessAge)
	if age["fast"] <= fastThreshold {
		t.Errorf("age[fast] = %v after 31 awake minutes of silence, want it over its %v s threshold: real silence must still be detectable", age["fast"], fastThreshold)
	}
	if age["slow"] >= slowThreshold {
		t.Errorf("age[slow] = %v, want it under its %v s threshold after only 31 awake minutes", age["slow"], slowThreshold)
	}

	h.emitter.OnSourceSucceeded("fast")
	if got := h.floatGauge(t, MetricSourceLastSuccessAge)["fast"]; got != 0 {
		t.Errorf("age[fast] = %v right after a success, want 0", got)
	}
}

// A sleep before the first success of a run ages nothing either: the clock starts at
// process start and the sleep is not part of it.
func TestSourceLiveness_HostSleepBeforeFirstSuccessDoesNotAgeASource(t *testing.T) {
	h, host := newSleepHarness(t, map[string]time.Duration{"s": 30 * time.Minute})
	host.run(time.Minute)
	host.sleep(3 * time.Hour)
	if got := h.floatGauge(t, MetricSourceLastSuccessAge)["s"]; got != 60 {
		t.Errorf("age = %v after 1 awake minute and a 3h sleep since process start, want 60 s", got)
	}
}

// Production wiring: with no injected clock the stored times come from time.Now and so
// carry Go's monotonic reading, which is what makes the default age sleep-excluding. A
// Round(0), a JSON round trip or a time.Unix on either operand would silently turn the
// age back into wall-clock time, so pin the reading's presence (Time.String prints it as
// " m=+<secs>") and that the gauge tracks real elapsed time.
func TestSourceLiveness_AgeUsesTheMonotonicReadingByDefault(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	e, err := New(mp, func() map[string]int { return nil }, WithPullSources(map[string]time.Duration{"a": time.Minute}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stored := func() string {
		e.srcMu.Lock()
		defer e.srcMu.Unlock()
		return e.srcLastSuccess["a"].String()
	}
	if s := stored(); !strings.Contains(s, " m=") {
		t.Errorf("process-start time %q carries no monotonic reading", s)
	}
	e.OnSourceSucceeded("a")
	if s := stored(); !strings.Contains(s, " m=") {
		t.Errorf("stored last-success %q carries no monotonic reading: the age would count a host sleep", s)
	}
	time.Sleep(30 * time.Millisecond)
	h := &sourceLivenessHarness{emitter: e, reader: reader}
	if age := h.floatGauge(t, MetricSourceLastSuccessAge)["a"]; age < 0.03 || age > 60 {
		t.Errorf("age = %v s after a 30ms real sleep, want a small positive reading of the elapsed time", age)
	}
}

// A restart must continue the age from the persisted last success, not re-seed
// it at process start (bead pg2-yagdb2).
func TestSourceLiveness_RestartContinuesAgeFromPersistedSuccess(t *testing.T) {
	path := t.TempDir() + "/state/" + SourceStateFileName
	sources := map[string]time.Duration{"thread-me": 30 * time.Minute, "pr-team": time.Minute}
	build := func(clk *mockClock) *sourceLivenessHarness {
		reader := sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		e, err := New(mp, func() map[string]int { return nil }, WithClock(clk.now), WithPullSources(sources), WithSourceStateFile(path))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return &sourceLivenessHarness{emitter: e, reader: reader, clk: clk}
	}
	start := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	h1 := build(&mockClock{t: start})
	h1.clk.t = start.Add(17 * time.Minute)
	h1.emitter.OnSourceSucceeded("thread-me") // last real success at 10:17
	h1.clk.t = start.Add(30 * time.Minute)    // later passes fail: no further call

	// Restart at 11:32: the age must continue from 10:17, not restart at 0.
	restart := start.Add(92 * time.Minute)
	h2 := build(&mockClock{t: restart})
	age := h2.floatGauge(t, MetricSourceLastSuccessAge)
	if want := (75 * time.Minute).Seconds(); age["thread-me"] != want {
		t.Errorf("age{thread-me} after restart = %v, want %v (continued from 10:17)", age["thread-me"], want)
	}
	if got := h2.floatGauge(t, MetricSourceLastSuccess)["thread-me"]; got != unix(start.Add(17*time.Minute)) {
		t.Errorf("last-success{thread-me} = %v, want the persisted 10:17", got)
	}
	// A source with no persisted success starts at process start, as before.
	if age["pr-team"] != 0 {
		t.Errorf("age{pr-team} = %v, want 0 (never succeeded, seeded at start)", age["pr-team"])
	}
	// A success after the restart advances and persists again.
	h2.emitter.OnSourceSucceeded("thread-me")
	h3 := build(&mockClock{t: restart.Add(time.Minute)})
	if a := h3.floatGauge(t, MetricSourceLastSuccessAge)["thread-me"]; a != 60 {
		t.Errorf("age after second restart = %v, want 60", a)
	}
}

func TestSourceLiveness_CorruptOrFutureStateFallsBackToStart(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/" + SourceStateFileName
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for name, content := range map[string]string{
		"corrupt": "{not json",
		"future":  `{"s":"2026-10-28T13:00:00Z"}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))
		clk := &mockClock{t: now}
		e, err := New(mp, func() map[string]int { return nil }, WithClock(clk.now),
			WithPullSources(map[string]time.Duration{"s": time.Minute}), WithSourceStateFile(path))
		if err != nil {
			t.Fatalf("%s: New: %v", name, err)
		}
		if !e.srcLastSuccess["s"].Equal(now) {
			t.Errorf("%s: last success = %v, want process start %v", name, e.srcLastSuccess["s"], now)
		}
	}
}
