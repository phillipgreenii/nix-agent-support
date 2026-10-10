package daemon_test

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"gopkg.in/yaml.v3"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
)

// scrape reads /metrics from the daemon and parses it: the REAL exposition.
func (e *env) scrape() map[string]*dto.MetricFamily {
	e.t.Helper()
	r := e.raw("GET", "/metrics", nil, nil)
	if r.Status != 200 {
		e.t.Fatalf("/metrics: %d", r.Status)
	}
	p := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := p.TextToMetricFamilies(strings.NewReader(string(r.Body)))
	if err != nil {
		e.t.Fatalf("/metrics is not Prometheus text: %v", err)
	}
	return fams
}

func labelValues(f *dto.MetricFamily, label string) []string {
	var out []string
	for _, m := range f.Metric {
		for _, l := range m.Label {
			if l.GetName() == label {
				out = append(out, l.GetValue())
			}
		}
	}
	return out
}

func sampleValue(f *dto.MetricFamily, match map[string]string) (float64, bool) {
next:
	for _, m := range f.Metric {
		for k, v := range match {
			found := false
			for _, l := range m.Label {
				if l.GetName() == k && l.GetValue() == v {
					found = true
				}
			}
			if !found {
				continue next
			}
		}
		switch {
		case m.Counter != nil:
			return m.Counter.GetValue(), true
		case m.Gauge != nil:
			return m.Gauge.GetValue(), true
		}
	}
	return 0, false
}

// The catalog of the design: every name, its type and its labels.
var catalog = map[string]struct {
	typ    dto.MetricType
	labels []string
}{
	"pg_task_focus_http_requests_total":                          {dto.MetricType_COUNTER, []string{"client", "method", "route", "status"}},
	"pg_task_focus_http_request_duration_seconds":                {dto.MetricType_HISTOGRAM, []string{"route"}},
	"pg_task_focus_events_appended_total":                        {dto.MetricType_COUNTER, []string{"type"}},
	"pg_task_focus_append_duration_seconds":                      {dto.MetricType_HISTOGRAM, nil},
	"pg_task_focus_fsync_duration_seconds":                       {dto.MetricType_HISTOGRAM, nil},
	"pg_task_focus_append_failures_total":                        {dto.MetricType_COUNTER, []string{"stage"}},
	"pg_task_focus_rejections_total":                             {dto.MetricType_COUNTER, []string{"reason"}},
	"pg_task_focus_config_reload_total":                          {dto.MetricType_COUNTER, []string{"result"}},
	"pg_task_focus_corrections_applied_total":                    {dto.MetricType_COUNTER, []string{"kind"}},
	"pg_task_focus_alerts_played_total":                          {dto.MetricType_COUNTER, []string{"kind"}},
	"pg_task_focus_alert_failures_total":                         {dto.MetricType_COUNTER, nil},
	"pg_task_focus_sse_events_sent_total":                        {dto.MetricType_COUNTER, nil},
	"pg_task_focus_ready":                                        {dto.MetricType_GAUGE, nil},
	"pg_task_focus_start_timestamp_seconds":                      {dto.MetricType_GAUGE, nil},
	"pg_task_focus_build_info":                                   {dto.MetricType_GAUGE, []string{"go_version", "schema_version", "version"}},
	"pg_task_focus_startup_recovery":                             {dto.MetricType_GAUGE, []string{"kind"}},
	"pg_task_focus_store_size_bytes":                             {dto.MetricType_GAUGE, nil},
	"pg_task_focus_store_writable":                               {dto.MetricType_GAUGE, nil},
	"pg_task_focus_store_read_only":                              {dto.MetricType_GAUGE, nil},
	"pg_task_focus_replay_duration_seconds":                      {dto.MetricType_GAUGE, nil},
	"pg_task_focus_replay_event_count":                           {dto.MetricType_GAUGE, nil},
	"pg_task_focus_last_append_timestamp_seconds":                {dto.MetricType_GAUGE, nil},
	"pg_task_focus_config_valid":                                 {dto.MetricType_GAUGE, nil},
	"pg_task_focus_last_config_reload_success_timestamp_seconds": {dto.MetricType_GAUGE, nil},
	"pg_task_focus_sse_clients":                                  {dto.MetricType_GAUGE, nil},
	"pg_task_focus_state_version":                                {dto.MetricType_GAUGE, nil},
	"pg_task_focus_tasks":                                        {dto.MetricType_GAUGE, []string{"cadence", "status"}},
	"pg_task_focus_tasks_overdue":                                {dto.MetricType_GAUGE, []string{"cadence"}},
	"pg_task_focus_task_resolutions":                             {dto.MetricType_GAUGE, []string{"cadence", "outcome"}},
	"pg_task_focus_task_resolutions_30d":                         {dto.MetricType_GAUGE, []string{"cadence", "outcome"}},
	"pg_task_focus_cycle_active":                                 {dto.MetricType_GAUGE, []string{"type"}},
	"pg_task_focus_cycle_overtime_seconds":                       {dto.MetricType_GAUGE, []string{"type"}},
	"pg_task_focus_cycle_paused_seconds":                         {dto.MetricType_GAUGE, []string{"type"}},
	"pg_task_focus_cycle_seconds_in_active_day":                  {dto.MetricType_GAUGE, []string{"type"}},
	"pg_task_focus_attention_items":                              {dto.MetricType_GAUGE, []string{"severity", "type"}},
	"pg_task_focus_next_reminder_timestamp_seconds":              {dto.MetricType_GAUGE, nil},
}

// drive puts the daemon through enough of a day that every counter has a series.
func drive(e *env) {
	e.bootstrap()
	e.clock.Set(local(9, 0))
	e.ok("/api/v1/tasks/day:2026-10-07:plan-day/complete", map[string]any{})
	c := e.startCycle("deep-work")
	e.clock.Set(local(9, 10))
	e.ok("/api/v1/cycles/pause", map[string]any{"cycle_id": c})
	e.refused("/api/v1/cycles/boost", map[string]any{"minutes": 5, "cycle_id": "01JNOSUCHCYCLEXXXXXXXXXXXX"}, 404, "unknown_cycle")
	ev := e.get("/api/v1/events?type=cycle.paused")
	var list struct{ Events []struct{ ID string } }
	ev.json(e.t, &list)
	e.ok("/api/v1/events/"+list.Events[0].ID+"/retract", map[string]any{})
}

func TestMetricsCatalogNamesTypesAndLabels(t *testing.T) {
	e := newEnv(t, options{})
	drive(e)
	fams := e.scrape()
	for name, want := range catalog {
		f := fams[name]
		if f == nil {
			t.Errorf("metric %s is missing from /metrics", name)
			continue
		}
		if f.GetType() != want.typ {
			t.Errorf("%s is a %s, want %s", name, f.GetType(), want.typ)
		}
		if want.typ == dto.MetricType_COUNTER && !strings.HasSuffix(name, "_total") {
			t.Errorf("counter %s does not end in _total", name)
		}
		got := map[string]bool{}
		for _, m := range f.Metric {
			for _, l := range m.Label {
				got[l.GetName()] = true
			}
		}
		var labels []string
		for l := range got {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		if strings.Join(labels, ",") != strings.Join(want.labels, ",") {
			t.Errorf("%s has labels %v, want %v", name, labels, want.labels)
		}
	}
	for name := range fams {
		if !strings.HasPrefix(name, "pg_task_focus_") {
			t.Errorf("metric %s is not prefixed pg_task_focus_", name)
		}
		if _, ok := catalog[name]; !ok {
			t.Errorf("metric %s is exposed but not in the catalog of this test: a new metric is a deliberate choice", name)
		}
	}
}

// A cardinality guard: every label value is a route template or a member of a
// closed enumeration, never an id, a title or free text.
func TestMetricLabelsAreBounded(t *testing.T) {
	e := newEnv(t, options{})
	drive(e)
	e.raw("GET", "/api/v1/nowhere/01JABCDEFGHJKMNPQRSTVWXYZ0", nil, nil) // an unknown path must not make a label
	e.raw("GET", "/api/v1/state", nil, map[string]string{"X-Client": "CANARY-CLIENT-ZX9Q"})
	fams := e.scrape()

	routes := map[string]bool{"other": true}
	for _, r := range e.spec.Operations() {
		_, p, _ := strings.Cut(r, " ")
		routes[p] = true
	}
	reasons := map[string]bool{}
	for _, r := range wire.SortReasons(command.Reasons()) {
		reasons[r] = true
	}
	for _, r := range wire.TransportReasons() {
		reasons[string(r)] = true
	}
	cycleTypes := map[string]bool{"deep-work": true, "notifications": true, "review": true, "page-response": true}
	eventTypes := map[string]bool{}
	for _, ty := range []string{
		"period.changed", "profile.changed", "task.materialized", "task.completed", "task.skipped", "task.missed",
		"task.withdrawn", "task.reinstated", "cycle.started", "cycle.paused", "cycle.resumed", "cycle.boosted", "cycle.stopped",
		"cycle.annotated", "event.corrected", "event.retracted", "batch.committed",
	} {
		eventTypes[ty] = true
	}
	set := func(vs ...string) map[string]bool {
		m := map[string]bool{}
		for _, v := range vs {
			m[v] = true
		}
		return m
	}
	allowed := map[string]map[string]bool{
		"route":    routes,
		"method":   set("GET", "POST", "OPTIONS", "DELETE", "PUT", "HEAD", "PATCH"),
		"client":   set(obs.Clients...),
		"stage":    set(obs.AppendStages...),
		"reason":   reasons,
		"result":   set(obs.ReloadResults...),
		"kind":     set("correct", "retract", "expiry", "reminder", "torn_tail", "uncommitted_batch"),
		"cadence":  set(obs.Cadences...),
		"outcome":  set(obs.Outcomes...),
		"severity": set("low", "medium", "high"),
		"type":     nil, // by metric below
		"status":   nil,
	}
	for name, f := range fams {
		for _, m := range f.Metric {
			for _, l := range m.Label {
				k, v := l.GetName(), l.GetValue()
				switch {
				case k == "le", k == "version", k == "schema_version", k == "go_version":
				case k == "type" && name == "pg_task_focus_events_appended_total":
					if !eventTypes[v] {
						t.Errorf("%s{type=%q} is not an event type", name, v)
					}
				case k == "type" && strings.HasPrefix(name, "pg_task_focus_cycle_"):
					if !cycleTypes[v] {
						t.Errorf("%s{type=%q} is not a configured cycle type", name, v)
					}
				case k == "type" && name == "pg_task_focus_attention_items":
					if v != "task" && v != "cycle" && v != "store" {
						t.Errorf("%s{type=%q}", name, v)
					}
				case k == "status" && name == "pg_task_focus_http_requests_total":
					if len(v) != 3 {
						t.Errorf("%s{status=%q}", name, v)
					}
				case k == "status":
					if !set(obs.TaskStatuses...)[v] {
						t.Errorf("%s{status=%q}", name, v)
					}
				default:
					if allowed[k] == nil || !allowed[k][v] {
						t.Errorf("%s{%s=%q}: not a route template or a member of a closed set", name, k, v)
					}
				}
			}
		}
	}
	if strings.Contains(e.raw("GET", "/metrics", nil, nil).Body2(), "CANARY-CLIENT") {
		t.Error("an unknown X-Client value became a label")
	}
}

// Body2 is the response body as text.
func (r resp) Body2() string { return string(r.Body) }

// The first scrape carries a zero for every counter series an alert reads, so
// increase() has a baseline.
func TestFirstScrapeHasTheAlertedCounters(t *testing.T) {
	e := newEnv(t, options{})
	fams := e.scrape()
	for _, stage := range obs.AppendStages {
		v, ok := sampleValue(fams["pg_task_focus_append_failures_total"], map[string]string{"stage": stage})
		if !ok || v != 0 {
			t.Errorf("append_failures_total{stage=%q} is %v, present %v on the first scrape, want 0", stage, v, ok)
		}
	}
	if v, ok := sampleValue(fams["pg_task_focus_alert_failures_total"], nil); !ok || v != 0 {
		t.Errorf("alert_failures_total is %v (present %v) on the first scrape, want 0", v, ok)
	}
	if v, _ := sampleValue(fams["pg_task_focus_ready"], nil); v != 1 {
		t.Errorf("ready = %v", v)
	}
	if v, _ := sampleValue(fams["pg_task_focus_store_writable"], nil); v != 1 {
		t.Errorf("store_writable = %v", v)
	}
}

// The projection gauges read the log, so they are right after a restart.
func TestProjectionGaugesSurviveARestart(t *testing.T) {
	e := newEnv(t, options{})
	drive(e)
	before := e.scrape()
	e.restart(nil)
	after := e.scrape()
	for _, name := range []string{"pg_task_focus_tasks", "pg_task_focus_task_resolutions", "pg_task_focus_cycle_active"} {
		a, b := before[name], after[name]
		for _, m := range a.Metric {
			match := map[string]string{}
			for _, l := range m.Label {
				match[l.GetName()] = l.GetValue()
			}
			x, _ := sampleValue(a, match)
			y, _ := sampleValue(b, match)
			if x != y {
				t.Errorf("%s%v was %v before the restart and %v after", name, match, x, y)
			}
		}
	}
	// A completed task is counted in the whole-log resolutions and the 30-day ones.
	f := after["pg_task_focus_task_resolutions"]
	if v, _ := sampleValue(f, map[string]string{"cadence": "daily", "outcome": "completed"}); v != 1 {
		t.Errorf("task_resolutions{daily,completed} = %v, want 1", v)
	}
	// The day's running time is clipped to the active day: the deep-work cycle ran 10 minutes.
	if v, _ := sampleValue(after["pg_task_focus_cycle_seconds_in_active_day"], map[string]string{"type": "deep-work"}); v != 600 {
		t.Errorf("cycle_seconds_in_active_day{deep-work} = %v, want 600", v)
	}
}

// ---- the alert rules and the dashboard read only metrics that exist ----

var (
	metricName = regexp.MustCompile(`pg_task_focus_[a-z0-9_]+`)
	selector   = regexp.MustCompile(`(pg_task_focus_[a-z0-9_]+)\{([^}]*)\}`)
	matcher    = regexp.MustCompile(`([a-z_]+)\s*(=|!=|=~|!~)\s*"([^"]*)"`)
)

// exposedName strips a histogram's series suffix.
func exposedName(n string) string {
	for _, s := range []string{"_bucket", "_sum", "_count"} {
		if strings.HasSuffix(n, s) {
			return strings.TrimSuffix(n, s)
		}
	}
	return n
}

// resolves checks one PromQL expression against the real scrape: each
// pg_task_focus_ metric exists, and each selector's equality matcher names a
// label the metric has with a value it exposes.
func resolves(t *testing.T, where, expr string, fams map[string]*dto.MetricFamily) {
	t.Helper()
	for _, n := range metricName.FindAllString(expr, -1) {
		name := exposedName(n)
		if fams[name] == nil && fams[n] == nil {
			t.Errorf("%s reads %s, which /metrics does not expose", where, n)
		}
	}
	for _, m := range selector.FindAllStringSubmatch(expr, -1) {
		f := fams[exposedName(m[1])]
		if f == nil {
			continue
		}
		for _, mm := range matcher.FindAllStringSubmatch(m[2], -1) {
			if mm[2] != "=" {
				continue
			}
			ok := false
			for _, v := range labelValues(f, mm[1]) {
				if v == mm[3] {
					ok = true
				}
			}
			if !ok && mm[1] != "le" {
				t.Errorf("%s selects %s{%s=%q}, which /metrics does not expose", where, m[1], mm[1], mm[3])
			}
		}
	}
}

func TestAlertRulesReadRealMetrics(t *testing.T) {
	e := newEnv(t, options{})
	drive(e)
	fams := e.scrape()
	raw, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Groups []struct {
			Name, Folder string
			Rules        []struct {
				UID          string `yaml:"uid"`
				Title        string `yaml:"title"`
				For          string `yaml:"for"`
				NoDataState  string `yaml:"noDataState"`
				ExecErrState string `yaml:"execErrState"`
				Labels       map[string]string
				Annotations  map[string]string
				Data         []struct {
					RefID string `yaml:"refId"`
					Model struct {
						Expr string `yaml:"expr"`
					}
				}
			}
		}
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{ sev, forD, noData string }{
		"pg-task-focus-daemon-down":           {"critical", "5m", "Alerting"},
		"pg-task-focus-not-ready":             {"critical", "2m", "OK"},
		"pg-task-focus-store-unwritable":      {"critical", "2m", "OK"},
		"pg-task-focus-store-read-only":       {"critical", "0s", "OK"},
		"pg-task-focus-append-failures":       {"critical", "0s", "OK"},
		"pg-task-focus-config-reload-failing": {"warning", "10m", "OK"},
		"pg-task-focus-restart-loop":          {"critical", "0s", "OK"},
		"pg-task-focus-startup-recovery":      {"warning", "0s", "OK"},
		"pg-task-focus-sound-failing":         {"warning", "0s", "OK"},
		"pg-task-focus-slow-replay":           {"info", "0s", "OK"},
		"pg-task-focus-stuck-overtime":        {"warning", "5m", "OK"},
	}
	seen := map[string]bool{}
	for _, g := range doc.Groups {
		if g.Folder != "Focus" {
			t.Errorf("group %s is in folder %q, want Focus (it converges with the dashboard by title)", g.Name, g.Folder)
		}
		for _, r := range g.Rules {
			w, ok := want[r.UID]
			if !ok {
				t.Errorf("rule %s is not in the design's table", r.UID)
				continue
			}
			seen[r.UID] = true
			if r.Labels["severity"] != w.sev || r.For != w.forD || r.NoDataState != w.noData || r.ExecErrState != "Error" {
				t.Errorf("rule %s: severity %q for %q noData %q execErr %q, want %v", r.UID, r.Labels["severity"], r.For, r.NoDataState, r.ExecErrState, w)
			}
			if len(r.Annotations["description"]) < 80 || r.Annotations["summary"] == "" {
				t.Errorf("rule %s has no annotation naming the remedy", r.UID)
			}
			for _, d := range r.Data {
				if d.RefID == "A" {
					resolves(t, "rule "+r.UID, strings.ReplaceAll(d.Model.Expr, `up{job="pg-task-focus"}`, ""), fams)
				}
			}
		}
	}
	for uid := range want {
		if !seen[uid] {
			t.Errorf("the design's alert %s is not defined", uid)
		}
	}
	// up{job="pg-task-focus"} is the scrape job the darwin module registers as metricsTargets.pg-task-focus.
	if !strings.Contains(string(raw), `up{job="pg-task-focus"}`) {
		t.Error("the daemon-down rule does not read up{job=\"pg-task-focus\"}")
	}
}

func TestDashboardReadsRealMetrics(t *testing.T) {
	e := newEnv(t, options{})
	drive(e)
	fams := e.scrape()
	raw, err := os.ReadFile("../../grafana/pg-task-focus.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		UID    string
		Panels []struct {
			Title   string
			Targets []struct{ Expr string }
		}
	}
	if err := yamlJSON(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.UID != "pg-task-focus" {
		t.Errorf("dashboard uid %q: the alert annotations point at pg-task-focus", d.UID)
	}
	n := 0
	for _, p := range d.Panels {
		for _, tg := range p.Targets {
			n++
			resolves(t, "panel "+p.Title, strings.ReplaceAll(tg.Expr, `up{job="pg-task-focus"}`, ""), fams)
		}
	}
	if n < 20 {
		t.Errorf("only %d panel expressions: the dashboard lost its panels", n)
	}
}

func yamlJSON(raw []byte, v any) error { return yaml.Unmarshal(raw, v) }
