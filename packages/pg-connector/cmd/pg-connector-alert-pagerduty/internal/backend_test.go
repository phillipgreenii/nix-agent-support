package internal

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// stubTransport answers from fixtures and records what it was asked.
type stubTransport struct {
	incidents   map[string][]apiIncident // keyed by rawQuery
	byID        map[string]*apiIncident
	alerts      map[string][]apiIncidentAlert
	alertsErr   map[string]error
	incErr      error
	queries     []string
	tokensSeen  []string
	alertsCalls []string
}

func (s *stubTransport) Incidents(_ context.Context, _, tok, q string) ([]apiIncident, error) {
	s.queries = append(s.queries, q)
	s.tokensSeen = append(s.tokensSeen, tok)
	if s.incErr != nil {
		return nil, s.incErr
	}
	if v, ok := s.incidents[q]; ok {
		return v, nil
	}
	return s.incidents["*"], nil
}

func (s *stubTransport) Incident(_ context.Context, _, _, id string) (*apiIncident, error) {
	if s.incErr != nil {
		return nil, s.incErr
	}
	if inc, ok := s.byID[id]; ok {
		return inc, nil
	}
	return nil, errMissing
}

func (s *stubTransport) IncidentAlerts(_ context.Context, _, _, id string) ([]apiIncidentAlert, error) {
	s.alertsCalls = append(s.alertsCalls, id)
	if err := s.alertsErr[id]; err != nil {
		return nil, err
	}
	return s.alerts[id], nil
}

// fakeKeychain stands in for the macOS Keychain (never the real one).
type fakeKeychain struct {
	pw    string
	err   error
	calls [][2]string
}

func (k *fakeKeychain) Password(_ context.Context, service, account string) (string, error) {
	k.calls = append(k.calls, [2]string{service, account})
	return k.pw, k.err
}

func resolver(kc Keychain, env map[string]string) *TokenResolver {
	return &TokenResolver{keychain: kc, getenv: func(k string) string { return env[k] }}
}

func okBackend(st *stubTransport) *Backend {
	return New(st, resolver(&fakeKeychain{pw: "kc-token"}, nil))
}

func cfgCtx(cfg map[string]any) context.Context {
	raw, _ := json.Marshal(cfg)
	return scriptout.WithConfig(context.Background(), raw)
}

func baseCfg() map[string]any { return map[string]any{"base_url": "https://pd.example.localhost/"} }

func inc(id, status, urgency string) apiIncident {
	i := apiIncident{
		ID: id, IncidentNumber: 7, Title: "Title " + id, Status: status, Urgency: urgency,
		CreatedAt: "2026-10-01T10:00:00Z", HTMLURL: "https://pd.example.localhost/incidents/" + id,
	}
	i.Service.ID = "SVC1"
	i.Service.Summary = "svc one"
	return i
}

func wantErrIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want errors.Is %v", err, target)
	}
}

func TestList_AddsFiringStatusesItselfAndMapsFields(t *testing.T) {
	st := &stubTransport{incidents: map[string][]apiIncident{"*": {inc("P1", "triggered", "high")}}}
	res, err := okBackend(st).List(cfgCtx(baseCfg()), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"statuses[]=triggered&statuses[]=acknowledged"}; !reflect.DeepEqual(st.queries, want) {
		t.Fatalf("queries = %v, want %v", st.queries, want)
	}
	if len(res.Entities) != 1 || res.PresentIDs[0] != "pagerduty:P1" {
		t.Fatalf("result = %+v", res)
	}
	a := res.Entities[0]
	if a.Provider != "pagerduty" || a.Title != "Title P1" || a.Since != "2026-10-01T10:00:00Z" || a.URL == "" ||
		a.Acknowledged == nil || *a.Acknowledged || a.Stale || a.Attributes["detail.service"] != "svc one" {
		t.Fatalf("alert = %+v", a)
	}
	if st.tokensSeen[0] != "kc-token" {
		t.Fatalf("token = %q", st.tokensSeen[0])
	}
}

func TestList_QueryUsesRepeatedKeyArraysAndNarrowsOnly(t *testing.T) {
	st := &stubTransport{incidents: map[string][]apiIncident{"*": {inc("P1", "triggered", "high")}}}
	_, err := okBackend(st).List(cfgCtx(baseCfg()), schema.QueryExpr{"service_ids[]=A&service_ids[]=B&urgencies[]=high"}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := "statuses[]=triggered&statuses[]=acknowledged&service_ids[]=A&service_ids[]=B&urgencies[]=high"
	if len(st.queries) != 1 || st.queries[0] != want {
		t.Fatalf("queries = %v, want [%s]", st.queries, want)
	}
}

func TestList_QueryCannotWidenStatuses(t *testing.T) {
	for _, q := range []string{"statuses[]=resolved", "statuses=resolved", "service_ids[]=A&statuses[]=resolved", "limit=1000", "noequals"} {
		st := &stubTransport{}
		_, err := okBackend(st).List(cfgCtx(baseCfg()), schema.QueryExpr{q}, false)
		wantErrIs(t, err, scriptout.ErrInvalidArgument)
		if len(st.queries) != 0 {
			t.Fatalf("%q reached PagerDuty: %v", q, st.queries)
		}
	}
}

func TestList_FiltersNonFiringAndUnionsDeduplicated(t *testing.T) {
	st := &stubTransport{incidents: map[string][]apiIncident{
		"statuses[]=triggered&statuses[]=acknowledged&service_ids[]=A": {inc("P1", "triggered", "high"), inc("P9", "resolved", "high")},
		"statuses[]=triggered&statuses[]=acknowledged&service_ids[]=B": {inc("P1", "triggered", "high"), inc("P2", "acknowledged", "low")},
	}}
	res, err := okBackend(st).List(cfgCtx(baseCfg()), schema.QueryExpr{"service_ids[]=A", "service_ids[]=B"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"pagerduty:P1", "pagerduty:P2"}; !reflect.DeepEqual(res.PresentIDs, want) {
		t.Fatalf("PresentIDs = %v, want %v (resolved excluded, P1 deduplicated)", res.PresentIDs, want)
	}
}

func TestSeverity_HighestAmongAlertsThenUrgencyFallback(t *testing.T) {
	cases := []struct {
		name    string
		alerts  []apiIncidentAlert
		err     error
		urgency string
		want    schema.Severity
	}{
		{"highest wins", []apiIncidentAlert{{Severity: "info"}, {Severity: "critical"}, {Severity: "warning"}}, nil, "low", schema.SeverityCritical},
		{"error is high", []apiIncidentAlert{{Severity: "error"}, {Severity: "warning"}}, nil, "low", schema.SeverityHigh},
		{"warning is medium", []apiIncidentAlert{{Severity: "warning"}}, nil, "high", schema.SeverityMedium},
		{"info is low", []apiIncidentAlert{{Severity: "info"}}, nil, "high", schema.SeverityLow},
		{"call fails falls back to urgency high", nil, errors.New("boom"), "high", schema.SeverityHigh},
		{"call fails falls back to urgency low", nil, errors.New("boom"), "low", schema.SeverityLow},
		{"no alerts falls back", nil, nil, "high", schema.SeverityHigh},
		{"unknown severity falls back", []apiIncidentAlert{{Severity: "unknown"}}, nil, "low", schema.SeverityLow},
		{"nothing known omits", nil, errors.New("boom"), "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &stubTransport{
				incidents: map[string][]apiIncident{"*": {inc("P1", "triggered", c.urgency)}},
				alerts:    map[string][]apiIncidentAlert{"P1": c.alerts},
				alertsErr: map[string]error{"P1": c.err},
			}
			res, err := okBackend(st).List(cfgCtx(baseCfg()), nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Entities[0].Severity; got != c.want {
				t.Fatalf("severity = %q, want %q", got, c.want)
			}
		})
	}
}

// The extension reports the raw alert severities PagerDuty sent, so a reader can
// tell a real alert severity from the urgency fallback and spot an unmapped value.
func TestExtension_RawAlertSeveritiesAreExposed(t *testing.T) {
	cases := []struct {
		name   string
		alerts []apiIncidentAlert
		err    error
		want   []string
	}{
		{"lower-cased, deduplicated and sorted", []apiIncidentAlert{{Severity: "Warning"}, {Severity: "critical"}, {Severity: " warning "}}, nil, []string{"critical", "warning"}},
		{"an unmapped value is still reported", []apiIncidentAlert{{Severity: "unknown"}, {Severity: "info"}}, nil, []string{"info", "unknown"}},
		{"empty severities are dropped", []apiIncidentAlert{{Severity: ""}}, nil, nil},
		{"no alerts", nil, nil, nil},
		{"alerts call failed", nil, errors.New("boom"), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &stubTransport{
				incidents: map[string][]apiIncident{"*": {inc("P1", "triggered", "high")}},
				alerts:    map[string][]apiIncidentAlert{"P1": c.alerts},
				alertsErr: map[string]error{"P1": c.err},
			}
			res, err := okBackend(st).List(cfgCtx(baseCfg()), nil, false)
			if err != nil {
				t.Fatal(err)
			}
			var ext pagerdutyExtension
			if err := json.Unmarshal(res.Entities[0].Extensions["pagerduty"], &ext); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ext.AlertSeverities, c.want) {
				t.Fatalf("alert_severities = %v, want %v", ext.AlertSeverities, c.want)
			}
		})
	}
}

func TestSeverity_UnauthorizedAlertsCallIsUnauthenticated(t *testing.T) {
	st := &stubTransport{
		incidents: map[string][]apiIncident{"*": {inc("P1", "triggered", "high")}},
		alertsErr: map[string]error{"P1": errUnauthorized},
	}
	_, err := okBackend(st).List(cfgCtx(baseCfg()), nil, false)
	wantErrIs(t, err, scriptout.ErrUnauthenticated)
}

func TestAttention_AcknowledgedStaysAndRanksBelowUnacknowledgedOfSameSeverity(t *testing.T) {
	st := &stubTransport{
		incidents: map[string][]apiIncident{"*": {inc("PACK", "acknowledged", "high"), inc("PTRIG", "triggered", "high")}},
		alerts: map[string][]apiIncidentAlert{
			"PACK":  {{Severity: "critical"}},
			"PTRIG": {{Severity: "critical"}},
		},
	}
	items, err := okBackend(st).ListAttention(cfgCtx(baseCfg()))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v, acknowledged must stay in attention", items)
	}
	by := map[string]schema.AttentionItem{}
	for _, it := range items {
		by[it.ID] = it
	}
	ack, trig := by["pagerduty:PACK"], by["pagerduty:PTRIG"]
	if trig.Severity != schema.SeverityCritical || trig.Summary != "Title PTRIG" || trig.Type != "alert" {
		t.Fatalf("unacknowledged = %+v", trig)
	}
	if ack.Severity != schema.SeverityHigh || ack.Summary != "Title PACK (acknowledged)" {
		t.Fatalf("acknowledged = %+v, want one level lower (high) with marker", ack)
	}
	rank := map[schema.Severity]int{schema.SeverityCritical: 4, schema.SeverityHigh: 3, schema.SeverityMedium: 2, schema.SeverityLow: 1}
	if rank[ack.Severity] >= rank[trig.Severity] {
		t.Fatalf("acknowledged %q does not rank below unacknowledged %q", ack.Severity, trig.Severity)
	}
}

func TestAttention_AcknowledgedLoweringAppliesToUrgencyFallbackAndFloorsAtLow(t *testing.T) {
	st := &stubTransport{
		incidents: map[string][]apiIncident{"*": {
			inc("PH", "acknowledged", "high"), // fallback high -> medium
			inc("PL", "acknowledged", "low"),  // low stays low
			inc("PN", "acknowledged", ""),     // absent stays absent
		}},
	}
	items, err := okBackend(st).ListAttention(cfgCtx(baseCfg()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]schema.Severity{}
	for _, it := range items {
		got[it.ID] = it.Severity
	}
	want := map[string]schema.Severity{"pagerduty:PH": schema.SeverityMedium, "pagerduty:PL": schema.SeverityLow, "pagerduty:PN": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("severities = %v, want %v", got, want)
	}
}

func TestLowerOneLevel(t *testing.T) {
	cases := map[schema.Severity]schema.Severity{
		schema.SeverityCritical: schema.SeverityHigh,
		schema.SeverityHigh:     schema.SeverityMedium,
		schema.SeverityMedium:   schema.SeverityLow,
		schema.SeverityLow:      schema.SeverityLow,
		"":                      "",
	}
	for in, want := range cases {
		if got := lowerOneLevel(in); got != want {
			t.Errorf("lowerOneLevel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAttention_QueryNameResolutionAndUndefinedName(t *testing.T) {
	st := &stubTransport{incidents: map[string][]apiIncident{"*": nil}}
	cfg := baseCfg()
	cfg["attention_query"] = "mine"
	cfg["queries"] = map[string]any{"mine": []string{"service_ids[]=A"}}
	if _, err := okBackend(st).ListAttention(cfgCtx(cfg)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(st.queries[0], "&service_ids[]=A") {
		t.Fatalf("query = %v", st.queries)
	}
	cfg["attention_query"] = "nope"
	_, err := okBackend(st).ListAttention(cfgCtx(cfg))
	wantErrIs(t, err, scriptout.ErrInvalidArgument)
}

func TestShow(t *testing.T) {
	st := &stubTransport{
		byID: map[string]*apiIncident{
			"P1": ptr(inc("P1", "acknowledged", "high")),
			"P2": ptr(inc("P2", "resolved", "high")),
		},
		alerts: map[string][]apiIncidentAlert{"P1": {{Severity: "error"}}},
	}
	b := okBackend(st)
	a, err := b.Show(cfgCtx(baseCfg()), "pagerduty:P1")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "pagerduty:P1" || a.Severity != schema.SeverityHigh || a.Acknowledged == nil || !*a.Acknowledged {
		t.Fatalf("show = %+v", a)
	}
	for _, id := range []string{"pagerduty:P2", "pagerduty:ZZ", "pagerduty:", "grafana:P1", "P1"} {
		_, err := b.Show(cfgCtx(baseCfg()), id)
		wantErrIs(t, err, scriptout.ErrNotFound)
	}
}

func ptr[T any](v T) *T { return &v }

func TestFailureSemantics(t *testing.T) {
	cfg := cfgCtx(baseCfg())
	_, err := okBackend(&stubTransport{incErr: errors.New("pagerduty unreachable")}).List(cfg, nil, false)
	wantErrIs(t, err, scriptout.ErrUnavailable)
	_, err = okBackend(&stubTransport{incErr: errUnauthorized}).List(cfg, nil, false)
	wantErrIs(t, err, scriptout.ErrUnauthenticated)
	_, err = okBackend(&stubTransport{incErr: errors.New("x")}).Show(cfg, "pagerduty:P1")
	wantErrIs(t, err, scriptout.ErrUnavailable)

	bad := inc("P1", "triggered", "high")
	bad.CreatedAt = "garbage"
	_, err = okBackend(&stubTransport{incidents: map[string][]apiIncident{"*": {bad}}}).List(cfg, nil, false)
	wantErrIs(t, err, scriptout.ErrUnavailable)

	// Reachable and empty is a successful empty result.
	res, err := okBackend(&stubTransport{}).List(cfg, nil, false)
	if err != nil || len(res.Entities) != 0 || res.PresentIDs == nil {
		t.Fatalf("empty = %+v, %v", res, err)
	}
}

func TestConfig_DefaultsAndValidation(t *testing.T) {
	cfg, err := decodeConfig(context.Background())
	if err != nil || cfg.BaseURL != DefaultBaseURL || cfg.KeychainService != "pd-schedule-manager" || cfg.KeychainAccount != "pagerduty-api" {
		t.Fatalf("defaults = %+v, %v", cfg, err)
	}
	_, err = decodeConfig(cfgCtx(map[string]any{"base_url": "ftp://x"}))
	wantErrIs(t, err, scriptout.ErrInvalidArgument)
}

func TestListHistory_IsUnknownOp(t *testing.T) {
	_, err := okBackend(&stubTransport{}).ListHistory(context.Background(), timeZero, timeZero, nil)
	wantErrIs(t, err, scriptout.ErrUnknownOp)
}
