package telemetry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// catalogPath is the committed catalog, relative to this package's directory.
const catalogPath = "../../../../grafana/pg-connector-issue-jira-metrics.txt"

type catalogEntry struct{ typ, labels string }

// readCatalog parses the three-column catalog: Prometheus name, type, comma
// separated label keys or "-".
func readCatalog(t *testing.T) map[string]catalogEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(catalogPath))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]catalogEntry{}
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			t.Fatalf("catalog line %d %q: want 3 fields", i+1, line)
		}
		if _, dup := out[f[0]]; dup {
			t.Errorf("catalog lists %s twice", f[0])
		}
		out[f[0]] = catalogEntry{typ: f[1], labels: f[2]}
	}
	return out
}

// metricConstants reads every Metric* string constant of telemetry.go and
// returns its Prometheus-form name (dots replaced by underscores).
func metricConstants(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "telemetry.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, id := range vs.Names {
				if !strings.HasPrefix(id.Name, "Metric") {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok {
					t.Fatalf("%s is not a string literal", id.Name)
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				names = append(names, strings.ReplaceAll(v, ".", "_"))
			}
		}
	}
	sort.Strings(names)
	return names
}

func TestEveryMetricConstantIsInTheCatalogAndTheCatalogHasNothingElse(t *testing.T) {
	cat := readCatalog(t)
	consts := metricConstants(t)
	if len(consts) == 0 {
		t.Fatal("no Metric* constant found")
	}
	inConsts := map[string]bool{}
	for _, n := range consts {
		if inConsts[n] {
			t.Errorf("constant %s defined twice", n)
		}
		inConsts[n] = true
		if _, ok := cat[n]; !ok {
			t.Errorf("series %s is a Metric* constant but is missing from the catalog", n)
		}
		if !strings.HasPrefix(n, "pg_connector_issue_jira_") {
			t.Errorf("series %s does not carry the service prefix", n)
		}
		if strings.HasSuffix(n, "_total") {
			t.Errorf("series %s ends in _total", n)
		}
	}
	for n := range cat {
		if !inConsts[n] {
			t.Errorf("series %s is in the catalog but is not a Metric* constant", n)
		}
	}
}

func TestCatalogTypeAndLabelsMatchWhatTheInstrumentsEmit(t *testing.T) {
	cat := readCatalog(t)
	// A reading that carries every key, so every origin.* series is emitted.
	f, rs := originFixture(t, 20)
	rs.Record(internal.RateLimit{
		Status: 429, Limit: i64(100), Remaining: i64(12), NearLimit: bl(true),
		RetryAfterSeconds: 11, Reset: "2026-10-12T12:00:11Z", Reason: "jira-burst-based",
	}, t0)
	f.exerciseEverything()
	got := f.collect()
	if len(got) != len(cat) {
		t.Errorf("emitted %d series, catalog lists %d", len(got), len(cat))
	}
	for name, m := range got {
		prom := strings.ReplaceAll(name, ".", "_")
		want, ok := cat[prom]
		if !ok {
			t.Errorf("emitted series %s is not in the catalog", prom)
			continue
		}
		typ, keys := shape(t, m)
		if typ != want.typ {
			t.Errorf("%s: emitted as %s, catalog says %s", prom, typ, want.typ)
		}
		if keys != want.labels {
			t.Errorf("%s: emitted label keys %q, catalog says %q", prom, keys, want.labels)
		}
	}
	for prom := range cat {
		found := false
		for name := range got {
			if strings.ReplaceAll(name, ".", "_") == prom {
				found = true
			}
		}
		if !found {
			t.Errorf("catalog series %s was never emitted", prom)
		}
	}
}

// shape returns the catalog type of an instrument and its sorted label keys
// joined by commas ("-" for none).
func shape(t *testing.T, m metricdata.Metrics) (typ, keys string) {
	t.Helper()
	set := map[string]bool{}
	collect := func(kv []string) {
		for _, k := range kv {
			set[k] = true
		}
	}
	switch d := m.Data.(type) {
	case metricdata.Gauge[int64]:
		typ = "gauge"
		for _, p := range d.DataPoints {
			collect(attrKeys(p.Attributes.ToSlice()))
		}
	case metricdata.Gauge[float64]:
		typ = "gauge"
		for _, p := range d.DataPoints {
			collect(attrKeys(p.Attributes.ToSlice()))
		}
	case metricdata.Sum[int64]:
		typ = "gauge"
		if d.IsMonotonic {
			typ = "counter"
		}
		for _, p := range d.DataPoints {
			collect(attrKeys(p.Attributes.ToSlice()))
		}
	case metricdata.Histogram[float64]:
		typ = "histogram"
		for _, p := range d.DataPoints {
			collect(attrKeys(p.Attributes.ToSlice()))
		}
	default:
		t.Fatalf("%s: unexpected data type %T", m.Name, m.Data)
	}
	var ks []string
	for k := range set {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	if len(ks) == 0 {
		return typ, "-"
	}
	return typ, strings.Join(ks, ",")
}

func attrKeys(kvs []attribute.KeyValue) []string {
	out := make([]string, 0, len(kvs))
	for _, kv := range kvs {
		out = append(out, string(kv.Key))
	}
	return out
}
