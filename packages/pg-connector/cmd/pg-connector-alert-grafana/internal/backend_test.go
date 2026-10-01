package internal

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// stubTransport is a Transport double: it records every call's filters and
// answers from byFilterKey (keyed by the filters joined with "|"), falling
// back to all.
type stubTransport struct {
	all   []apiAlert
	by    map[string][]apiAlert
	err   error
	calls [][]string
	urls  []string

	// History seam (history_test.go). rules answers Rules; frames answers
	// RuleHistory by ruleUID (a missing uid is an empty-values frame);
	// histErr/rulesErr fail the respective call. rulesCalls/historyCalls
	// record the history path so tests can prove attention never reaches it.
	rules        []apiRule
	frames       map[string]apiHistory
	rulesErr     error
	histErr      map[string]error
	rulesCalls   int
	historyCalls []historyCall
}

type historyCall struct {
	uid      string
	from, to time.Time
	limit    int
}

func (s *stubTransport) Rules(context.Context, string) ([]apiRule, error) {
	s.rulesCalls++
	if s.rulesErr != nil {
		return nil, s.rulesErr
	}
	return s.rules, nil
}

func (s *stubTransport) RuleHistory(_ context.Context, _ string, uid string, from, to time.Time, limit int) (apiHistory, error) {
	s.historyCalls = append(s.historyCalls, historyCall{uid, from, to, limit})
	if err := s.histErr[uid]; err != nil {
		return apiHistory{}, err
	}
	if f, ok := s.frames[uid]; ok {
		return f, nil
	}
	return mkFrame(), nil
}

func (s *stubTransport) Alerts(_ context.Context, baseURL string, filters []string) ([]apiAlert, error) {
	s.calls = append(s.calls, filters)
	s.urls = append(s.urls, baseURL)
	if s.err != nil {
		return nil, s.err
	}
	key := ""
	for i, f := range filters {
		if i > 0 {
			key += "|"
		}
		key += f
	}
	if v, ok := s.by[key]; ok {
		return v, nil
	}
	return s.all, nil
}

func mkAlert(fp, name, severity, state string) apiAlert {
	a := apiAlert{
		Fingerprint:  fp,
		StartsAt:     "2026-10-01T10:00:00Z",
		GeneratorURL: "http://grafana.invalid/alerting/gen/" + fp,
		Labels:       map[string]string{"alertname": name, "__alert_rule_uid__": "uid-" + fp, "grafana_folder": "Infra"},
		Annotations:  map[string]string{"summary": "sum " + name},
	}
	if severity != "" {
		a.Labels["severity"] = severity
	}
	a.Status.State = state
	a.Receivers = []struct {
		Name string `json:"name"`
	}{{Name: "slack"}, {Name: "email"}}
	return a
}

func cfgCtx(t *testing.T, cfg map[string]any) context.Context {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return scriptout.WithConfig(context.Background(), raw)
}

func baseCfg() map[string]any {
	return map[string]any{"base_url": "https://grafana.example.localhost/"}
}

func wantErrIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want errors.Is %v", err, target)
	}
}

func TestList_FiringOnlyExcludesSilencedInhibitedUnprocessed(t *testing.T) {
	st := &stubTransport{all: []apiAlert{
		mkAlert("a", "A", "critical", "active"),
		mkAlert("b", "B", "warning", "suppressed"),
		mkAlert("c", "C", "warning", "unprocessed"),
		mkAlert("d", "D", "warning", ""),
	}}
	res, err := New(st).List(cfgCtx(t, baseCfg()), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entities) != 1 || res.Entities[0].ID != "grafana:a" {
		t.Fatalf("entities = %+v", res.Entities)
	}
	if !reflect.DeepEqual(res.PresentIDs, []string{"grafana:a"}) || res.Truncated || res.Cursor != nil {
		t.Fatalf("result = %+v", res)
	}
	if len(st.calls) != 1 || len(st.calls[0]) != 0 {
		t.Fatalf("no query must send exactly one unfiltered call, got %v", st.calls)
	}
	if st.urls[0] != "https://grafana.example.localhost" {
		t.Fatalf("base url = %q, want trailing slash trimmed", st.urls[0])
	}
}

func TestList_QueryCannotWidenPastFiringOnly(t *testing.T) {
	// Even a query matching everything (including silenced) yields only active.
	st := &stubTransport{all: []apiAlert{mkAlert("a", "A", "", "active"), mkAlert("b", "B", "", "suppressed")}}
	res, err := New(st).List(cfgCtx(t, baseCfg()), schema.QueryExpr{`{}`}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entities) != 1 || res.Entities[0].ID != "grafana:a" {
		t.Fatalf("entities = %+v", res.Entities)
	}
}

func TestList_TranslatesMatchersToFilters(t *testing.T) {
	st := &stubTransport{}
	_, err := New(st).List(cfgCtx(t, baseCfg()), schema.QueryExpr{`{severity=~"critical|warning", alertname!="X"}`}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{`severity=~"critical|warning"`, `alertname!="X"`}}
	if !reflect.DeepEqual(st.calls, want) {
		t.Fatalf("calls = %#v, want %#v", st.calls, want)
	}
}

func TestList_ListValuedQueryUnionsDeduplicatedByID(t *testing.T) {
	st := &stubTransport{by: map[string][]apiAlert{
		`severity="critical"`: {mkAlert("a", "A", "critical", "active"), mkAlert("b", "B", "critical", "active")},
		`alertname="B"`:       {mkAlert("b", "B", "critical", "active"), mkAlert("c", "C", "", "active")},
	}}
	res, err := New(st).List(cfgCtx(t, baseCfg()), schema.QueryExpr{`{severity="critical"}`, `{alertname="B"}`}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"grafana:a", "grafana:b", "grafana:c"}; !reflect.DeepEqual(res.PresentIDs, want) {
		t.Fatalf("PresentIDs = %v, want %v", res.PresentIDs, want)
	}
}

func TestList_IDsOnlyOmitsEntities(t *testing.T) {
	st := &stubTransport{all: []apiAlert{mkAlert("a", "A", "", "active")}}
	res, err := New(st).List(cfgCtx(t, baseCfg()), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entities != nil || !reflect.DeepEqual(res.PresentIDs, []string{"grafana:a"}) {
		t.Fatalf("result = %+v", res)
	}
}

func TestList_ReachableAndEmptyIsSuccessNotError(t *testing.T) {
	res, err := New(&stubTransport{}).List(cfgCtx(t, baseCfg()), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entities == nil || len(res.Entities) != 0 || res.PresentIDs == nil || len(res.PresentIDs) != 0 {
		t.Fatalf("want non-nil empty slices (encode as []), got %+v", res)
	}
	raw, _ := json.Marshal(res)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	if string(m["entities"]) != "[]" || string(m["present_ids"]) != "[]" {
		t.Fatalf("encoded = %s", raw)
	}
}

func TestList_TransportFailureIsUnavailable(t *testing.T) {
	_, err := New(&stubTransport{err: errors.New("boom")}).List(cfgCtx(t, baseCfg()), nil, false)
	wantErrIs(t, err, scriptout.ErrUnavailable)
}

func TestList_MalformedAlertIsUnavailable(t *testing.T) {
	bad := mkAlert("a", "A", "", "active")
	bad.StartsAt = "yesterday"
	_, err := New(&stubTransport{all: []apiAlert{bad}}).List(cfgCtx(t, baseCfg()), nil, false)
	wantErrIs(t, err, scriptout.ErrUnavailable)

	noFP := mkAlert("", "A", "", "active")
	_, err = New(&stubTransport{all: []apiAlert{noFP}}).List(cfgCtx(t, baseCfg()), nil, false)
	wantErrIs(t, err, scriptout.ErrUnavailable)
}

func TestList_BadMatcherIsInvalidArgument(t *testing.T) {
	st := &stubTransport{}
	_, err := New(st).List(cfgCtx(t, baseCfg()), schema.QueryExpr{`{severity}`}, false)
	wantErrIs(t, err, scriptout.ErrInvalidArgument)
	if len(st.calls) != 0 {
		t.Fatalf("a bad matcher must not reach the transport, calls = %v", st.calls)
	}
}

func TestConfig_BaseURLRequiredAndHTTP(t *testing.T) {
	b := New(&stubTransport{})
	_, err := b.List(context.Background(), nil, false)
	wantErrIs(t, err, scriptout.ErrInvalidArgument)
	_, err = b.List(cfgCtx(t, map[string]any{"base_url": "grafana.local"}), nil, false)
	wantErrIs(t, err, scriptout.ErrInvalidArgument)
	_, err = b.ListAttention(context.Background())
	wantErrIs(t, err, scriptout.ErrInvalidArgument)
	_, err = b.Show(context.Background(), "grafana:a")
	wantErrIs(t, err, scriptout.ErrInvalidArgument)
}

func TestShow(t *testing.T) {
	st := &stubTransport{all: []apiAlert{
		mkAlert("a", "A", "critical", "active"),
		mkAlert("s", "S", "critical", "suppressed"),
	}}
	b := New(st)
	got, err := b.Show(cfgCtx(t, baseCfg()), "grafana:a")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "grafana:a" || got.Title != "A" {
		t.Fatalf("got %+v", got)
	}
	for _, id := range []string{"grafana:s", "grafana:zzz", "a", "pagerduty:a", "grafana:", ""} {
		_, err := b.Show(cfgCtx(t, baseCfg()), id)
		if !errors.Is(err, scriptout.ErrNotFound) {
			t.Errorf("Show(%q) err = %v, want not_found", id, err)
		}
	}
	_, err = New(&stubTransport{err: errors.New("down")}).Show(cfgCtx(t, baseCfg()), "grafana:a")
	wantErrIs(t, err, scriptout.ErrUnavailable)
}

func TestToSchemaAlert_FullMapping(t *testing.T) {
	a := mkAlert("fp1", "DiskFull", "warning", "active")
	a.Annotations = map[string]string{"description": "disk is full", "summary": "ignored"}
	got, err := toSchemaAlert(a, "https://g.example", "2026-10-01T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "grafana:fp1" || got.Provider != "grafana" || got.Title != "DiskFull" {
		t.Errorf("identity: %+v", got)
	}
	if got.Description != "disk is full" {
		t.Errorf("description = %q, want description annotation over summary", got.Description)
	}
	if got.Severity != schema.SeverityMedium {
		t.Errorf("severity = %q", got.Severity)
	}
	if got.Since != "2026-10-01T10:00:00Z" || got.AsOf != "2026-10-01T12:00:00Z" || got.Stale {
		t.Errorf("times/stale: %+v", got)
	}
	if got.Acknowledged != nil {
		t.Errorf("Grafana must omit acknowledged, got %v", *got.Acknowledged)
	}
	if got.URL != "https://g.example/alerting/grafana/uid-fp1/view" {
		t.Errorf("url = %q", got.URL)
	}
	wantAttrs := map[string]string{
		"label.alertname": "DiskFull", "label.__alert_rule_uid__": "uid-fp1", "label.grafana_folder": "Infra",
		"label.severity": "warning", "annotation.description": "disk is full", "annotation.summary": "ignored",
	}
	if !reflect.DeepEqual(got.Attributes, wantAttrs) {
		t.Errorf("attributes = %v, want %v", got.Attributes, wantAttrs)
	}
	var ext grafanaExtension
	if err := json.Unmarshal(got.Extensions["grafana"], &ext); err != nil {
		t.Fatal(err)
	}
	wantExt := grafanaExtension{
		Receivers: []string{"email", "slack"}, GeneratorURL: "http://grafana.invalid/alerting/gen/fp1", RuleUID: "uid-fp1", Folder: "Infra",
	}
	if !reflect.DeepEqual(ext, wantExt) {
		t.Errorf("extension = %+v, want %+v", ext, wantExt)
	}
}

func TestToSchemaAlert_FallbacksAndIDStability(t *testing.T) {
	a := apiAlert{Fingerprint: "fp", StartsAt: "2026-10-01T10:00:00.123456789Z", GeneratorURL: "http://gen/x", Annotations: map[string]string{"summary": "S"}}
	a.Status.State = "active"
	got, err := toSchemaAlert(a, "https://g", "now")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "fp" || got.Description != "S" || got.URL != "http://gen/x" || got.Severity != "" {
		t.Errorf("fallbacks: %+v", got)
	}
	if got.Since != "2026-10-01T10:00:00Z" {
		t.Errorf("since = %q", got.Since)
	}
	// id must not depend on startsAt (INV-ALERT-3).
	a2 := a
	a2.StartsAt = "2026-10-02T10:00:00Z"
	got2, _ := toSchemaAlert(a2, "https://g", "now")
	if got.ID != got2.ID {
		t.Errorf("id changed with startsAt: %q vs %q", got.ID, got2.ID)
	}
}

func TestSeverityFromLabel_ClosedTable(t *testing.T) {
	tests := map[string]schema.Severity{
		"critical": schema.SeverityCritical,
		"error":    schema.SeverityHigh,
		"high":     schema.SeverityHigh,
		"warning":  schema.SeverityMedium,
		"info":     schema.SeverityLow,
		"Critical": schema.SeverityCritical,
		"":         "",
		"page":     "",
		"medium":   "",
		"low":      "",
	}
	for in, want := range tests {
		if got := severityFromLabel(in); got != want {
			t.Errorf("severityFromLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListAttention_Mapping(t *testing.T) {
	st := &stubTransport{all: []apiAlert{
		mkAlert("a", "Crit", "critical", "active"),
		mkAlert("b", "NoSev", "", "active"),
		mkAlert("c", "Silenced", "critical", "suppressed"),
	}}
	items, err := New(st).ListAttention(cfgCtx(t, baseCfg()))
	if err != nil {
		t.Fatal(err)
	}
	want := []schema.AttentionItem{
		{Type: "alert", ID: "grafana:a", Summary: "Crit", Severity: schema.SeverityCritical},
		{Type: "alert", ID: "grafana:b", Summary: "NoSev"},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("items = %+v, want %+v", items, want)
	}
	if len(st.calls) != 1 || len(st.calls[0]) != 0 {
		t.Fatalf("attention_query unset must be unfiltered, calls = %v", st.calls)
	}
}

func TestListAttention_EmptyIsEmptySliceNotNil(t *testing.T) {
	items, err := New(&stubTransport{}).ListAttention(cfgCtx(t, baseCfg()))
	if err != nil {
		t.Fatal(err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("items = %#v, want non-nil empty", items)
	}
}

func TestListAttention_UsesNamedAttentionQuery(t *testing.T) {
	st := &stubTransport{}
	cfg := baseCfg()
	cfg["attention_query"] = "attention"
	cfg["queries"] = map[string]any{"attention": []string{`{severity=~"critical|warning"}`}, "other": "{}"}
	if _, err := New(st).ListAttention(cfgCtx(t, cfg)); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{`severity=~"critical|warning"`}}
	if !reflect.DeepEqual(st.calls, want) {
		t.Fatalf("calls = %#v, want %#v", st.calls, want)
	}
}

func TestListAttention_UndefinedAttentionQueryIsInvalidArgument(t *testing.T) {
	st := &stubTransport{}
	cfg := baseCfg()
	cfg["attention_query"] = "missing"
	cfg["queries"] = map[string]any{"other": "{}"}
	_, err := New(st).ListAttention(cfgCtx(t, cfg))
	wantErrIs(t, err, scriptout.ErrInvalidArgument)
	if len(st.calls) != 0 {
		t.Fatalf("must fail before any fetch, calls = %v", st.calls)
	}
}

func TestListAttention_TransportFailureIsUnavailable(t *testing.T) {
	_, err := New(&stubTransport{err: errors.New("down")}).ListAttention(cfgCtx(t, baseCfg()))
	wantErrIs(t, err, scriptout.ErrUnavailable)
}

func TestToAttentionItem_AcknowledgedLowering(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name string
		ack  *bool
		sev  schema.Severity
		want schema.Severity
		sum  string
	}{
		{"nil ack does not lower", nil, schema.SeverityCritical, schema.SeverityCritical, "T"},
		{"false ack does not lower", &no, schema.SeverityCritical, schema.SeverityCritical, "T"},
		{"critical to high", &yes, schema.SeverityCritical, schema.SeverityHigh, "T (acknowledged)"},
		{"high to medium", &yes, schema.SeverityHigh, schema.SeverityMedium, "T (acknowledged)"},
		{"medium to low", &yes, schema.SeverityMedium, schema.SeverityLow, "T (acknowledged)"},
		{"low stays low", &yes, schema.SeverityLow, schema.SeverityLow, "T (acknowledged)"},
		{"absent severity stays absent", &yes, "", "", "T (acknowledged)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toAttentionItem(schema.Alert{ID: "grafana:x", Title: "T", Severity: tc.sev, Acknowledged: tc.ack})
			if got.Severity != tc.want || got.Summary != tc.sum || got.Type != "alert" || got.ID != "grafana:x" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}
