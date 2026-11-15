package tokenobs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/logs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/telemetry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/tokenfile"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/tokenguard"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/keychain"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const (
	fixtureToken   = "FIXTURE-TOKEN-do-not-use-0001"
	fixtureRotated = "FIXTURE-ROTATED-0002"
)

// secretForms are the token and the digest in the encodings a leak would use.
func secretForms(tokens ...string) []string {
	var out []string
	for _, tok := range tokens {
		d := sha256.Sum256([]byte(tok))
		out = append(out, tok, hex.EncodeToString(d[:]), base64.StdEncoding.EncodeToString(d[:]),
			base64.RawURLEncoding.EncodeToString(d[:]), string(d[:]))
	}
	return out
}

func leaks(text string, secrets []string) []string {
	var found []string
	for _, s := range secrets {
		if strings.Contains(text, s) {
			found = append(found, s)
		}
	}
	return found
}

// logRec captures log events.
type logRec struct {
	mu     sync.Mutex
	events []string
	text   []string
	byName map[string][]map[string]any
}

func newLogRec() *logRec { return &logRec{byName: map[string][]map[string]any{}} }

func (l *logRec) Log(change string, kv ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[fmt.Sprint(kv[i])] = kv[i+1]
	}
	l.events = append(l.events, change)
	l.byName[change] = append(l.byName[change], m)
	l.text = append(l.text, fmt.Sprint(change, kv))
}

func (l *logRec) names() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// linkPjira reads the token through the stable link like the real pjira and
// answers 401 unless it holds the accepted token.
type linkPjira struct {
	mu       sync.Mutex
	link     string
	accepted string
	launches int
	argv     []string
}

func (p *linkPjira) Binary() string { return "/abs/pjira" }

func (p *linkPjira) Run(_ context.Context, args ...string) (string, error) {
	p.mu.Lock()
	p.launches++
	p.argv = append(p.argv, strings.Join(args, " "))
	acc := p.accepted
	p.mu.Unlock()
	b, err := os.ReadFile(p.link)
	if err != nil {
		return "", fmt.Errorf("pjira: read token file: %w", err)
	}
	if string(b) != acc {
		return "", fmt.Errorf("pjira issue PROJ-1: status 401 Unauthorized")
	}
	return "{}", nil
}

func (p *linkPjira) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.launches
}

type harness struct {
	t      *testing.T
	clock  *governor.ManualClock
	guard  *tokenguard.Guard
	store  *tokenfile.Store
	pjira  *linkPjira
	logs   *logRec
	tel    *telemetry.Telemetry
	reader *metric.ManualReader
	runner *origin.Runner
	rt     *router.Router
	state  string
	tmp    string
	spends []governor.SpendEvent
}

func newHarness(t *testing.T, script ...keychain.Result) *harness {
	t.Helper()
	h := &harness{t: t, logs: newLogRec(), clock: governor.NewManualClock(time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC))}
	h.tmp, h.state = t.TempDir(), t.TempDir()
	for _, d := range []string{h.tmp, h.state} {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(h.state, "jira-token")
	var err error
	if h.store, err = tokenfile.New(h.tmp, link); err != nil {
		t.Fatal(err)
	}
	h.reader = metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(h.reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	h.tel, err = telemetry.New(telemetry.Options{
		MeterProvider: mp,
		TokenState: func() (bool, string) {
			s := h.guard.State()
			return s.Degraded, s.Reason
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.tel.Close() })
	obs := &Observer{Metrics: h.tel, Log: h.logs.Log, FilePath: h.store.Path(), LinkPath: link}
	h.store.SetObserver(obs.StoreEvent)
	h.guard = tokenguard.New(tokenguard.Config{
		Keychain: keychain.NewFake(script...), Store: h.store, Service: "svc", Account: "acct",
		Clock: h.clock, Timer: tokenguard.NewManualTimer(), Recorder: obs,
	})
	t.Cleanup(h.guard.Close)

	h.pjira = &linkPjira{link: link, accepted: fixtureToken}
	cfg := config.Defaults()
	cfg.RequestRatePerSecond = 1000
	gov, err := governor.New(origin.GovernorConfig(cfg), governor.Options{
		Clock:   h.clock,
		OnSpend: func(e governor.SpendEvent) { h.spends = append(h.spends, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.runner, err = origin.New(origin.Options{
		Inner: h.pjira, Governor: gov, Concurrency: 4, DefaultPause: time.Minute,
		Clock: h.clock, Refresher: h.guard,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The real request path: router, event log middleware, detail error policy.
	el := logs.NewEventLog(h.state, "test")
	h.rt = router.New(router.Options{Instances: []string{"jira"}, Now: h.clock.Now})
	h.rt.Use(el.Middleware())
	h.rt.Register("jira", "show", scriptout.OpHandler{SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		_, err := h.runner.Run(ctx, "issue", "--", "PROJ-1")
		if err != nil {
			return nil, detail.ErrorPolicy{}.Classify(nil, 0, err)
		}
		return map[string]any{}, nil
	}})
	return h
}

func (h *harness) call() scriptout.Response {
	return h.rt.Dispatch(context.Background(), router.Call{
		Instance: "jira",
		Request:  scriptout.Request{Op: "show", Args: json.RawMessage(`{}`)},
	})
}

func (h *harness) requestRows() []map[string]any {
	h.t.Helper()
	raw, err := os.ReadFile(logs.EventLogPath(h.state))
	if err != nil {
		h.t.Fatal(err)
	}
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			h.t.Fatal(err)
		}
		rows = append(rows, m)
	}
	return rows
}

// collectText renders every metric point the telemetry holds, attributes and
// values, as text for the leak scan.
func (h *harness) collectText() string {
	h.t.Helper()
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err != nil {
		h.t.Fatal(err)
	}
	return fmt.Sprintf("%+v", rm)
}

func (h *harness) points(name string) map[string]int64 {
	h.t.Helper()
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err != nil {
		h.t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			var dps []metricdata.DataPoint[int64]
			switch d := m.Data.(type) {
			case metricdata.Gauge[int64]:
				dps = d.DataPoints
			case metricdata.Sum[int64]:
				dps = d.DataPoints
			}
			for _, p := range dps {
				var kv []string
				for _, a := range p.Attributes.ToSlice() {
					kv = append(kv, string(a.Key)+"="+a.Value.AsString())
				}
				out[strings.Join(kv, ",")] = p.Value
			}
		}
	}
	return out
}

func rejectedScript() []keychain.Result {
	return []keychain.Result{{Token: fixtureToken}, {Token: fixtureRotated}, {Token: fixtureRotated}}
}

func TestEveryLogEventIsEmittedAtItsTransitionAndNothingLeaks(t *testing.T) {
	h := newHarness(t, rejectedScript()...)
	ctx := context.Background()

	h.guard.Start(ctx) // start read, file and link written
	if got := h.logs.names(); strings.Join(got, ",") != "token_read,token_link_written,token_file_written" {
		t.Fatalf("start events = %v", got)
	}
	if r := h.logs.byName["token_read"][0]; r["reason"] != "start" || r["result"] != "changed" || r["error_kind"] != "none" {
		t.Errorf("start token_read = %v", r)
	}

	// Rotation: pjira now accepts only the rotated token.
	h.pjira.accepted = fixtureRotated
	h.clock.Advance(2 * time.Minute)
	if resp := h.call(); resp.Error != nil {
		t.Fatalf("rotated call: %v", resp.Error)
	}
	if r := h.logs.byName["token_read"][1]; r["reason"] != "auth_error" || r["result"] != "changed" {
		t.Errorf("repull token_read = %v", r)
	}
	if n := len(h.logs.byName["token_link_written"]); n != 1 {
		t.Errorf("token_link_written = %d, want 1 (a rewrite keeps the correct link)", n)
	}

	// Same token again: degraded token_rejected.
	h.pjira.accepted = "nothing"
	h.clock.Advance(2 * time.Minute)
	if resp := h.call(); resp.Error == nil {
		t.Fatal("the call must fail")
	}
	if got := h.logs.byName["auth_degraded"]; len(got) != 1 || got[0]["reason"] != "token_rejected" {
		t.Fatalf("auth_degraded = %v", got)
	}
	if r := h.logs.byName["token_read"][2]; r["reason"] != "auth_error" || r["result"] != "unchanged" {
		t.Errorf("same-token token_read = %v", r)
	}
	if _, has := h.logs.byName["auth_degraded"][0]["error_code"]; has {
		t.Error("auth_degraded carries error_code")
	}

	// Refused calls while degraded: no pjira launch, no spend, no auth event.
	launches, spends := h.pjira.count(), len(h.spends)
	rowsBefore := len(h.requestRows())
	for range 3 {
		if resp := h.call(); resp.Error == nil {
			t.Fatal("a refused call must fail")
		}
	}
	if h.pjira.count() != launches || len(h.spends) != spends {
		t.Errorf("refused calls launched pjira (%d to %d) or spent (%d to %d)", launches, h.pjira.count(), spends, len(h.spends))
	}
	rows := h.requestRows()
	for _, row := range rows[rowsBefore:] {
		if row["failure_class"] == "auth" {
			t.Errorf("a refused call emitted failure_class=auth: %v", row)
		}
	}
	if len(rows[rowsBefore:]) != 3 {
		t.Fatalf("refused request rows = %d, want 3", len(rows)-rowsBefore)
	}
	// The triggering 401 itself is still an auth failure (the Loki rule fires once).
	var sawAuth bool
	for _, row := range rows[:rowsBefore] {
		sawAuth = sawAuth || row["failure_class"] == "auth"
	}
	if !sawAuth {
		t.Error("the triggering 401 no longer carries failure_class=auth")
	}

	// Housekeeping and shutdown.
	if err := h.store.Touch(); err != nil {
		t.Fatal(err)
	}
	h.guard.SetClosing()
	if err := h.store.Remove(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"token_file_touched", "token_file_removed"} {
		if len(h.logs.byName[want]) != 1 {
			t.Errorf("%s = %d events, want 1", want, len(h.logs.byName[want]))
		}
	}

	// Metrics.
	if got := h.points(telemetry.MetricAuthDegraded); len(got) != 1 || got["reason=token_rejected"] != 1 {
		t.Errorf("auth.degraded = %v", got)
	}
	wantReads := map[string]int64{
		"error_kind=none,reason=start,result=changed":        1,
		"error_kind=none,reason=auth_error,result=changed":   1,
		"error_kind=none,reason=auth_error,result=unchanged": 1,
	}
	for k, v := range wantReads {
		if got := h.points(telemetry.MetricTokenReads)[k]; got != v {
			t.Errorf("token.reads %s = %d, want %d", k, got, v)
		}
	}
	if got := h.points(telemetry.MetricTokenFileOps); got["op=write,result=ok"] != 2 || got["op=remove,result=ok"] != 1 {
		t.Errorf("token.file_ops = %v", got)
	}

	// Leak harness: the token and its digest appear nowhere an operator or a
	// child could read: log events, metric points, the request and origin
	// rows, pjira's argv. The only file allowed to hold them is the token
	// file, and it is removed.
	secrets := secretForms(fixtureToken, fixtureRotated)
	captured := strings.Join(h.logs.text, "\n") + "\n" + h.collectText() + "\n" + strings.Join(h.pjira.argv, "\n")
	for _, row := range rows {
		b, _ := json.Marshal(row)
		captured += "\n" + string(b)
	}
	if got := leaks(captured, secrets); len(got) > 0 {
		t.Fatalf("token or digest leaked: %d hits", len(got))
	}
	if len(captured) < 400 {
		t.Fatalf("captured only %d bytes; the drive did not exercise the paths", len(captured))
	}
	// Negative control: the scanner sees a planted token.
	if len(leaks(captured+"\ntoken="+fixtureToken, secrets)) == 0 {
		t.Fatal("negative control: the planted token went undetected")
	}
}

func TestRefusedCallsAreSeenAsUnauthenticatedResultsButNeverAsAuthFailureClass(t *testing.T) {
	h := newHarness(t, keychain.Fail(keychain.KindLocked))
	h.guard.Start(context.Background()) // token_unavailable
	resp := h.call()
	if resp.Error == nil || resp.Error.Code != "unauthenticated" {
		t.Fatalf("refused call = %+v, want an unauthenticated answer", resp.Error)
	}
	for _, row := range h.requestRows() {
		if row["failure_class"] != nil {
			t.Errorf("row carries failure_class %v", row["failure_class"])
		}
	}
	if h.pjira.count() != 0 {
		t.Errorf("pjira launched %d times while degraded", h.pjira.count())
	}
	if got := h.points(telemetry.MetricAuthDegraded); got["reason=token_unavailable"] != 1 {
		t.Errorf("auth.degraded = %v", got)
	}
	if got := h.points(telemetry.MetricTokenReads); got["error_kind=locked,reason=start,result=error"] != 1 {
		t.Errorf("token.reads = %v", got)
	}
	if got := h.logs.byName["token_read"]; len(got) != 1 || got[0]["error_kind"] != "locked" {
		t.Errorf("token_read = %v", got)
	}
}
