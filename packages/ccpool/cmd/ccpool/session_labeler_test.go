package main

// Session-label wiring: every command path that opens a store makes THAT store
// the process-global telemetry session labeler, so telemetry.SessionAttrs (and
// every per-session slog narration record built from it) resolves the
// session's marked labels. reap-all re-sets it per pool and clears it as each
// pool's store closes. Bead pg2-om899.3.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/telemetry"
	ct "github.com/phillipgreenii/claude-transcript"
)

// isolateLabelerEnv points every path config.Load/LoadForPool, the registry,
// the lock dir and the event log resolve at a fresh temp dir, and writes a
// default-pool config.toml with a test-unique tmux socket and no notifier, so
// these in-process tests never read or write the live ccpool pools/state or
// fire a desktop notification. No claude.plugin_dir is configured, so any
// Ensure stops at its preflight guard before launching anything. It also
// clears the global labeler on cleanup. Returns the temp base dir.
func isolateLabelerEnv(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	for k, v := range map[string]string{
		"HOME":                base,
		"XDG_CONFIG_HOME":     filepath.Join(base, "cfg"),
		"XDG_DATA_HOME":       filepath.Join(base, "data"),
		"XDG_STATE_HOME":      filepath.Join(base, "state"),
		"XDG_RUNTIME_DIR":     filepath.Join(base, "run"),
		"CCPOOL_REGISTRY_DIR": filepath.Join(base, "reg"),
		"CCPOOL_POOL":         "",
		"CCPOOL_EXTERNAL_ID":  "",
		"CCPOOL_AUTONOMOUS":   "",
	} {
		t.Setenv(k, v)
	}
	writePoolConfig(t, filepath.Join(base, "cfg", "ccpool"),
		"[tmux]\nsocket = \""+config.SocketFor(base)+"\"\n")
	t.Cleanup(func() { telemetry.SetSessionLabeler(nil) })
	return base
}

// writePoolConfig writes <dir>/config.toml: extra plus a none notifier.
func writePoolConfig(t *testing.T, dir, extra string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := extra + "[notify]\nadapter = \"none\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newRegisteredPool creates (and thereby registers, exactly as a first
// `ccpool --pool <dir>` does) a named pool dir under base and returns its
// canonical root.
func newRegisteredPool(t *testing.T, base, name string) string {
	t.Helper()
	pc, err := config.ResolvePool(filepath.Join(base, name))
	if err != nil {
		t.Fatalf("ResolvePool(%s): %v", name, err)
	}
	writePoolConfig(t, pc.Root, "")
	return pc.Root
}

// seedMeta writes one metadata key for externalID into the store at dbPath,
// marking it label-eligible when label is true — the same SetMeta-then-
// MarkAsLabel sequence `ccpool new --meta k=v --label k` performs.
func seedMeta(t *testing.T, dbPath, externalID, key, value string, label bool) {
	t.Helper()
	st, err := store.Open(dbPath, clock.Real{})
	if err != nil {
		t.Fatalf("seed: open %s: %v", dbPath, err)
	}
	defer func() { _ = st.Close() }()
	if err := st.SetMeta(context.Background(), externalID, key, value); err != nil {
		t.Fatalf("seed: SetMeta: %v", err)
	}
	if label {
		if err := st.MarkAsLabel(externalID, key); err != nil {
			t.Fatalf("seed: MarkAsLabel: %v", err)
		}
	}
}

// labelerCall is one observed setSessionLabeler call: whether it set (non-nil)
// or cleared (nil) the labeler, plus what telemetry.SessionAttrs resolved for
// each probed external_id immediately afterwards — i.e. against whichever
// store that call had just made the process-global labeler.
type labelerCall struct {
	set   bool
	attrs map[string][]attribute.KeyValue
}

// spyLabeler wraps the setSessionLabeler seam: each call is forwarded to the
// real telemetry.SetSessionLabeler first, then SessionAttrs is resolved for
// every probeID and recorded. The seam is restored on cleanup.
func spyLabeler(t *testing.T, probeIDs ...string) *[]labelerCall {
	t.Helper()
	var calls []labelerCall
	orig := setSessionLabeler
	setSessionLabeler = func(l sessionLabelSource) {
		orig(l)
		c := labelerCall{set: l != nil, attrs: map[string][]attribute.KeyValue{}}
		for _, id := range probeIDs {
			c.attrs[id] = telemetry.SessionAttrs(id)
		}
		calls = append(calls, c)
	}
	t.Cleanup(func() { setSessionLabeler = orig })
	return &calls
}

// captureSlog swaps the default slog logger for a JSON handler writing to a
// buffer (restored on cleanup) and returns a func decoding every record
// written so far.
func captureSlog(t *testing.T) func() []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() []map[string]any {
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("decode slog record %q: %v", line, err)
			}
			out = append(out, rec)
		}
		return out
	}
}

// findRecord returns the first record with the given msg and external_id.
func findRecord(t *testing.T, recs []map[string]any, msg, externalID string) map[string]any {
	t.Helper()
	for _, r := range recs {
		if r["msg"] == msg && r["external_id"] == externalID {
			return r
		}
	}
	t.Fatalf("no %q record for external_id=%q among %d records: %v", msg, externalID, len(recs), recs)
	return nil
}

var wantReview = []attribute.KeyValue{attribute.String("pgrouter.role", "review")}

// TestBuildServiceFor_wiresItsStoreAsSessionLabeler covers the shared
// cancel/close/reap/capacity (and reap-all) store opener: once it returns,
// SessionAttrs resolves the opened store's marked labels. A key written with
// no --label (is_label=0) stays unlabelled, and opening the store does not
// backfill is_label.
func TestBuildServiceFor_wiresItsStoreAsSessionLabeler(t *testing.T) {
	isolateLabelerEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	seedMeta(t, cfg.DBPath, "ext-lbl", "pgrouter.role", "review", true)
	seedMeta(t, cfg.DBPath, "ext-lbl", "pgrouter.bead", "zr-1", false)
	seedMeta(t, cfg.DBPath, "ext-plain", "pgrouter.role", "worker", false)

	_, st, code := buildServiceFor(cfg)
	if code != 0 {
		t.Fatalf("buildServiceFor code = %d, want 0", code)
	}
	defer func() { _ = st.Close() }()

	if got := telemetry.SessionAttrs("ext-lbl"); !reflect.DeepEqual(got, wantReview) {
		t.Errorf("SessionAttrs(ext-lbl) = %v, want %v (only the marked key)", got, wantReview)
	}
	if got := telemetry.SessionAttrs("ext-plain"); len(got) != 0 {
		t.Errorf("SessionAttrs(ext-plain) = %v, want empty: an is_label=0 row is unlabelled", got)
	}
	// No backfill: the unmarked key is still metadata, still not a label.
	if labels, err := st.Labels("ext-plain"); err != nil || len(labels) != 0 {
		t.Errorf("Labels(ext-plain) = %v, %v; want empty (no is_label backfill)", labels, err)
	}
	if v, ok, err := st.GetMeta(context.Background(), "ext-plain", "pgrouter.role"); err != nil || !ok || v != "worker" {
		t.Errorf("GetMeta(ext-plain, pgrouter.role) = %q, %v, %v; want worker, true, nil", v, ok, err)
	}
}

// TestNewAndReply_narrationCarriesStoreLabels drives runNew/runReply
// in-process. With no plugin_dir configured, Ensure stops at its preflight
// guard — after the command opened its store, before any tmux launch — and
// emits the per-session "launch outcome" narration record, which must carry
// the session's marked label (and no unmarked metadata).
func TestNewAndReply_narrationCarriesStoreLabels(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T) int
	}{
		{"new", func(t *testing.T) int { return runNew([]string{"ext-lbl", "--cwd", t.TempDir()}) }},
		{"reply", func(t *testing.T) int { return runReply([]string{"ext-lbl", "hello"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateLabelerEnv(t)
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			seedMeta(t, cfg.DBPath, "ext-lbl", "pgrouter.role", "review", true)
			seedMeta(t, cfg.DBPath, "ext-lbl", "pgrouter.bead", "zr-1", false)
			records := captureSlog(t)

			if code := tc.run(t); code != 1 {
				t.Fatalf("%s exit = %d, want 1 (preflight: no plugin_dir)", tc.name, code)
			}

			rec := findRecord(t, records(), "ccpool: launch outcome", "ext-lbl")
			if rec["pgrouter.role"] != "review" {
				t.Errorf("launch outcome record pgrouter.role = %v, want review; record=%v", rec["pgrouter.role"], rec)
			}
			if _, ok := rec["pgrouter.bead"]; ok {
				t.Errorf("unmarked metadata pgrouter.bead leaked into the record: %v", rec)
			}
		})
	}
}

// TestRunHook_retryExhaustedNarrationCarriesStoreLabels drives runHook
// in-process for a StopFailure whose retry budget is spent: the hook's own
// store must be the labeler when the "retry policy exhausted" narration fires.
func TestRunHook_retryExhaustedNarrationCarriesStoreLabels(t *testing.T) {
	isolateLabelerEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	tp := writeAPIErrorTranscript(t, ct.ErrServerError, "API Error: 500 Internal server error")
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Insert(context.Background(), store.Session{
		ExternalID: "ext-lbl", ClaudeSessionID: "csid-x", State: store.Working,
		TmuxSession: "cc-ext-lbl", TranscriptPath: tp, RetryCount: 3, // == default MaxAttempts
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	seedMeta(t, cfg.DBPath, "ext-lbl", "pgrouter.role", "review", true)

	stdin := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(stdin, []byte(fmt.Sprintf(failPayloadRetry, tp)), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(stdin)
	if err != nil {
		t.Fatal(err)
	}
	origStdin := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = origStdin; _ = f.Close() })
	records := captureSlog(t)

	if code := runHook([]string{"fail"}); code != 0 {
		t.Fatalf("runHook exit = %d, want 0 (never-fail)", code)
	}

	rec := findRecord(t, records(), "ccpool: retry policy exhausted", "ext-lbl")
	if rec["pgrouter.role"] != "review" {
		t.Errorf("retry exhausted record pgrouter.role = %v, want review; record=%v", rec["pgrouter.role"], rec)
	}
}

// TestRunHook_retryExhaustedMetricCarriesPoolAndLabels drives runHook in
// pool-dir mode (CCPOOL_POOL set, as main does for --pool) for a StopFailure
// whose retry budget is spent: the hook's retryActuator must be wired with
// that pool and the config's allowlist, so the ccpool_retry_exhausted_total
// record carries pool=<basename> and the allowlisted pgrouter.role, and not
// the unallowlisted pgrouter.bead.
func TestRunHook_retryExhaustedMetricCarriesPoolAndLabels(t *testing.T) {
	base := isolateLabelerEnv(t)
	pool := newRegisteredPool(t, base, "pg-router-ccpool-review")
	t.Setenv("CCPOOL_POOL", pool)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	tp := writeAPIErrorTranscript(t, ct.ErrServerError, "API Error: 500 Internal server error")
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Insert(context.Background(), store.Session{
		ExternalID: "ext-lbl", ClaudeSessionID: "csid-x", State: store.Working,
		TmuxSession: "cc-ext-lbl", TranscriptPath: tp, RetryCount: 3, // == default MaxAttempts
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	seedMeta(t, cfg.DBPath, "ext-lbl", "pgrouter.role", "review", true)
	seedMeta(t, cfg.DBPath, "ext-lbl", "pgrouter.bead", "zr-secret", true)

	f, err := os.Open(writeHookPayload(t, fmt.Sprintf(failPayloadRetry, tp)))
	if err != nil {
		t.Fatal(err)
	}
	origStdin := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = origStdin; _ = f.Close() })

	origExhausted := recordRetryExhausted
	t.Cleanup(func() { recordRetryExhausted = origExhausted })
	var got []attribute.KeyValue
	recordRetryExhausted = func(attrs []attribute.KeyValue) { got = attrs }

	if code := runHook([]string{"fail"}); code != 0 {
		t.Fatalf("runHook exit = %d, want 0 (never-fail)", code)
	}

	m := map[string]string{}
	for _, kv := range got {
		m[string(kv.Key)] = kv.Value.String()
	}
	want := map[string]string{"pool": "pg-router-ccpool-review", "pgrouter.role": "review"}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("retry-exhausted metric attrs = %v, want %v", m, want)
	}
}

// writeHookPayload writes a hook stdin payload file and returns its path.
func writeHookPayload(t *testing.T, payload string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunReapAll_eachPoolResolvesAgainstItsOwnStore is the two-pool reap-all
// scenario: one process sweeps the default pool plus two registered pools
// whose sessions carry DIFFERENT role labels. The labeler must be re-set to
// each pool's own store for that pool's iteration (so alpha resolves only
// while pool A is live, bravo only while pool B is) and cleared as each
// pool's store closes.
func TestRunReapAll_eachPoolResolvesAgainstItsOwnStore(t *testing.T) {
	base := isolateLabelerEnv(t)
	poolA := newRegisteredPool(t, base, "poolA")
	poolB := newRegisteredPool(t, base, "poolB")
	seedMeta(t, filepath.Join(poolA, "store.db"), "alpha", "pgrouter.role", "review", true)
	seedMeta(t, filepath.Join(poolB, "store.db"), "bravo", "pgrouter.role", "worker", true)
	calls := spyLabeler(t, "alpha", "bravo")

	if rc := runReapAll(nil); rc != 0 {
		t.Fatalf("reap-all rc = %d, want 0", rc)
	}

	// default pool + pool A + pool B, each: set, then clear on close.
	if len(*calls) != 6 {
		t.Fatalf("setSessionLabeler calls = %d, want 6 (set+clear per pool): %+v", len(*calls), *calls)
	}
	wantWorker := []attribute.KeyValue{attribute.String("pgrouter.role", "worker")}
	var sawA, sawB, sawDefault int
	for i, c := range *calls {
		if c.set != (i%2 == 0) {
			t.Fatalf("call %d set=%v; want strict set/clear alternation: %+v", i, c.set, *calls)
		}
		if !c.set {
			if len(c.attrs["alpha"]) != 0 || len(c.attrs["bravo"]) != 0 {
				t.Errorf("call %d (clear): SessionAttrs still resolved %v", i, c.attrs)
			}
			continue
		}
		switch {
		case reflect.DeepEqual(c.attrs["alpha"], wantReview) && len(c.attrs["bravo"]) == 0:
			sawA++
		case reflect.DeepEqual(c.attrs["bravo"], wantWorker) && len(c.attrs["alpha"]) == 0:
			sawB++
		case len(c.attrs["alpha"]) == 0 && len(c.attrs["bravo"]) == 0:
			sawDefault++
		default:
			t.Errorf("call %d resolved against a mixed/foreign store: %v", i, c.attrs)
		}
	}
	if sawA != 1 || sawB != 1 || sawDefault != 1 {
		t.Errorf("per-pool resolutions: poolA=%d poolB=%d default=%d, want 1 each: %+v", sawA, sawB, sawDefault, *calls)
	}
}

// TestRunReapAll_unopenableStoreLeavesLabelerUnset: a registered pool whose
// store cannot open is skipped with no labeler call (degrade to a no-op), the
// other pools still get their set+clear, and the sweep ends cleared.
func TestRunReapAll_unopenableStoreLeavesLabelerUnset(t *testing.T) {
	base := isolateLabelerEnv(t)
	_ = newRegisteredPool(t, base, "good")
	bad := newRegisteredPool(t, base, "bad")
	if err := os.WriteFile(filepath.Join(bad, "store.db"), []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := spyLabeler(t)

	if rc := runReapAll(nil); rc != 1 {
		t.Fatalf("reap-all rc = %d, want 1 (the bad pool errored)", rc)
	}

	// default + good only: the bad pool contributes no call at all.
	if len(*calls) != 4 {
		t.Fatalf("setSessionLabeler calls = %d, want 4: %+v", len(*calls), *calls)
	}
	for i, c := range *calls {
		if c.set != (i%2 == 0) {
			t.Fatalf("call %d set=%v; want strict set/clear alternation: %+v", i, c.set, *calls)
		}
	}
}

// TestStoreOpeners_unopenableStoreNeverSetsLabeler: every single-shot store
// opener degrades to a no-op on a store that cannot open — it never hands
// telemetry a labeler.
func TestStoreOpeners_unopenableStoreNeverSetsLabeler(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, cfg config.Config)
	}{
		{"buildServiceFor", func(t *testing.T, cfg config.Config) {
			if _, _, code := buildServiceFor(cfg); code != 1 {
				t.Errorf("buildServiceFor code = %d, want 1", code)
			}
		}},
		{"new", func(t *testing.T, _ config.Config) {
			if code := runNew([]string{"ext-lbl", "--cwd", t.TempDir()}); code != 1 {
				t.Errorf("runNew = %d, want 1", code)
			}
		}},
		{"reply", func(t *testing.T, _ config.Config) {
			if code := runReply([]string{"ext-lbl", "hello"}); code != 1 {
				t.Errorf("runReply = %d, want 1", code)
			}
		}},
		{"hook", func(t *testing.T, _ config.Config) {
			if code := runHook([]string{"stop"}); code != 0 {
				t.Errorf("runHook = %d, want 0 (never-fail)", code)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateLabelerEnv(t)
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfg.DBPath, []byte("not a sqlite database"), 0o600); err != nil {
				t.Fatal(err)
			}
			calls := spyLabeler(t)
			_ = captureSlog(t) // keep the expected open-failure logs out of test output

			tc.run(t, cfg)

			if len(*calls) != 0 {
				t.Errorf("setSessionLabeler called %d times on an unopenable store, want 0: %+v", len(*calls), *calls)
			}
		})
	}
}
