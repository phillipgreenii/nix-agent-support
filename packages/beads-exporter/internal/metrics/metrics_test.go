package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func render(t *testing.T, r *Registry, ss []Sample) string {
	t.Helper()
	var b bytes.Buffer
	if err := r.Render(&b, ss); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return b.String()
}

func sample(family string, v float64, kv ...string) Sample {
	return Sample{Family: family, Labels: SeriesLabel(kv...), Value: v}
}

func TestEscapeLabelValue(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "alpha", "alpha"},
		{"backslash", `a\b`, `a\\b`},
		{"quote", `a"b`, `a\"b`},
		{"newline", "a\nb", `a\nb`},
		{"tab passes through", "a\tb", "a\tb"},
		{"carriage return passes through", "a\rb", "a\rb"},
		{"non-ascii passes through", "café ☃", "café ☃"},
		{"invalid utf-8 replaced", "a\xffb", "a�b"},
		{"truncated multibyte replaced", "a\xe2\x82", "a�"},
		{"all three", "\\\"\n", `\\\"\n`},
		{"backslash n literal is not a newline", `a\n`, `a\\n`},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeLabelValue(tc.in); got != tc.want {
				t.Fatalf("escape(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestEscapeIsNotGoQuoting(t *testing.T) {
	// %q would turn a tab into \t and a non-ASCII rune into \u escapes; the
	// exposition format defines neither.
	got := escapeLabelValue("a\tbé")
	if strings.Contains(got, `\t`) || strings.Contains(got, `\u`) {
		t.Fatalf("escape used Go quoting: %q", got)
	}
}

func TestHelpEscaping(t *testing.T) {
	if got := escapeHelp("a\\b\nc"); got != `a\\b\nc` {
		t.Fatalf("escapeHelp = %q", got)
	}
	if got := escapeHelp(`say "hi"`); got != `say "hi"` {
		t.Fatalf("quotes in HELP must pass through: %q", got)
	}
}

func TestRenderHelpAndTypeOnceWithDeterministicOrder(t *testing.T) {
	r, err := NewRegistry(
		Family{"zz_second", Gauge, "second family", []string{"db", "k"}},
		Family{"aa_first", Counter, "first family", []string{"db"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	ss := []Sample{
		sample("aa_first", 1, "db", "beta"),
		sample("zz_second", 5, "db", "beta", "k", "y"),
		sample("zz_second", 3, "db", "alpha", "k", "z"),
		sample("zz_second", 4, "db", "alpha", "k", "a"),
		sample("aa_first", 2, "db", "alpha"),
	}
	want := `# HELP zz_second second family
# TYPE zz_second gauge
zz_second{db="alpha",k="a"} 4
zz_second{db="alpha",k="z"} 3
zz_second{db="beta",k="y"} 5
# HELP aa_first first family
# TYPE aa_first counter
aa_first{db="alpha"} 2
aa_first{db="beta"} 1
`
	got := render(t, r, ss)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// Input order must not matter.
	rev := make([]Sample, len(ss))
	for i := range ss {
		rev[len(ss)-1-i] = ss[i]
	}
	if got2 := render(t, r, rev); got2 != want {
		t.Fatalf("output depends on input order:\n%s", got2)
	}
	if strings.Count(got, "# HELP zz_second") != 1 || strings.Count(got, "# TYPE zz_second") != 1 {
		t.Fatalf("HELP/TYPE not exactly once:\n%s", got)
	}
}

func TestRenderSkipsFamiliesWithoutSamples(t *testing.T) {
	r, _ := NewRegistry(Family{"a_metric", Gauge, "h", []string{"db"}}, Family{"b_metric", Gauge, "h", []string{"db"}})
	got := render(t, r, []Sample{sample("b_metric", 1, "db", "x")})
	if strings.Contains(got, "a_metric") {
		t.Fatalf("empty family rendered:\n%s", got)
	}
}

func TestRenderEscapesLabelValuesInOutput(t *testing.T) {
	r, _ := NewRegistry(Family{"m", Gauge, "h", []string{"db", "v"}})
	got := render(t, r, []Sample{sample("m", 1, "db", "alpha", "v", "a\"b\\c\nd\teé\xff")})
	want := "m{db=\"alpha\",v=\"a\\\"b\\\\c\\nd\teé�\"} 1\n"
	if !strings.Contains(got, want) {
		t.Fatalf("got:\n%q\nwant line:\n%q", got, want)
	}
}

func TestRenderRejectsBadSamples(t *testing.T) {
	r, _ := NewRegistry(Family{"m", Gauge, "h", []string{"db", "state"}})
	cases := []struct {
		name string
		s    []Sample
		want string
	}{
		{"unregistered family", []Sample{sample("nope", 1, "db", "a")}, "unregistered family"},
		{"too few labels", []Sample{sample("m", 1, "db", "a")}, "labels, want"},
		{"too many labels", []Sample{sample("m", 1, "db", "a", "state", "x", "extra", "y")}, "labels, want"},
		{"wrong label name", []Sample{sample("m", 1, "db", "a", "status", "x")}, `want "state"`},
		{"wrong label order", []Sample{sample("m", 1, "state", "x", "db", "a")}, "want"},
		{"duplicate series", []Sample{sample("m", 1, "db", "a", "state", "x"), sample("m", 2, "db", "a", "state", "x")}, "duplicate series"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			err := r.Render(&b, tc.s)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestFormatValue(t *testing.T) {
	cases := map[float64]string{0: "0", 1: "1", -2: "-2", 0.5: "0.5", 1700000000.25: "1700000000.25", 1e9: "1000000000"}
	for in, want := range cases {
		if got := formatValue(in); got != want {
			t.Fatalf("formatValue(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestRegistryValidation(t *testing.T) {
	if _, err := NewRegistry(Family{"", Gauge, "h", []string{"db"}}); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := NewRegistry(Family{"m", "histogram", "h", []string{"db"}}); err == nil {
		t.Fatal("bad type accepted")
	}
	if _, err := NewRegistry(Family{"m", Gauge, "h", []string{"state"}}); err == nil {
		t.Fatal("family without leading db label accepted")
	}
	if _, err := NewRegistry(Family{"m", Gauge, "h", nil}); err == nil {
		t.Fatal("family without labels accepted")
	}
	if _, err := NewRegistry(Family{"m", Gauge, "h", []string{"db"}}, Family{"m", Gauge, "h", []string{"db"}}); err == nil {
		t.Fatal("duplicate family accepted")
	}
	r, _ := NewRegistry()
	if err := r.Add(Family{"later", Gauge, "h", []string{"db"}}); err != nil {
		t.Fatalf("a later pass must be able to register: %v", err)
	}
	if len(r.Families()) != 1 {
		t.Fatalf("Families = %v", r.Families())
	}
}

// TestDefaultFamiliesAllowlist is the label allowlist per family: the exact
// label names of every family the main and throughput passes emit.
func TestDefaultFamiliesAllowlist(t *testing.T) {
	want := map[string]string{
		FamIssues:          "db,state",
		FamIssuesStored:    "db,status",
		FamQueueCandidates: "db,queue",
		FamQueueOldest:     "db,queue",
		FamByType:          "db,type",
		FamByPriority:      "db,priority",
		FamByLabel:         "db,bead_label",
		FamOldest:          "db,state",
		FamCreated24h:      "db",
		FamClosed24h:       "db",
		FamExporterUp:      "db",
		FamPassLastSuccess: "db,pass",
		FamCollectErrors:   "db,pass,reason",
		FamCollectDuration: "db,pass",
	}
	got := map[string]string{}
	for _, f := range Default().Families() {
		got[f.Name] = strings.Join(f.Labels, ",")
	}
	if len(got) != len(want) {
		t.Fatalf("%d families, want %d: %v", len(got), len(want), got)
	}
	for name, labels := range want {
		if got[name] != labels {
			t.Fatalf("family %s labels = %q, want %q", name, got[name], labels)
		}
	}
	for _, f := range Default().Families() {
		if strings.Contains(f.Name, "build_info") {
			t.Fatalf("unexpected build_info family %s", f.Name)
		}
		if f.Type == Counter && !strings.HasSuffix(f.Name, "_total") {
			t.Fatalf("counter %s lacks _total", f.Name)
		}
		if strings.Contains(f.Help, "\n") || f.Help == "" {
			t.Fatalf("family %s has bad HELP", f.Name)
		}
	}
}

func TestStrandedFamiliesAreNotRegisteredHere(t *testing.T) {
	for _, f := range Default().Families() {
		if strings.Contains(f.Name, "stranded") {
			t.Fatalf("stranded family %s must be added by the stranded pass, not here", f.Name)
		}
	}
}

func TestSeriesLabelPairs(t *testing.T) {
	ls := SeriesLabel("a", "1", "b", "2", "dangling")
	if len(ls) != 2 || ls[0] != (Label{"a", "1"}) || ls[1] != (Label{"b", "2"}) {
		t.Fatalf("SeriesLabel = %v", ls)
	}
}

// Comparisons against a constant are pinned with values that sort on BOTH sides
// of it, so a loose comparison cannot pass.
func TestRegistryValidationComparesExactly(t *testing.T) {
	for _, typ := range []Type{"aaa", "histogram", "zzz", ""} {
		if _, err := NewRegistry(Family{"m", typ, "h", []string{"db"}}); err == nil {
			t.Fatalf("type %q accepted", typ)
		}
	}
	for _, first := range []string{"a", "dba", "zz", ""} {
		if _, err := NewRegistry(Family{"m", Gauge, "h", []string{first}}); err == nil {
			t.Fatalf("first label %q accepted", first)
		}
	}
	if _, err := NewRegistry(Family{"m", Counter, "h", []string{"db"}}); err != nil {
		t.Fatalf("counter rejected: %v", err)
	}
}

func TestRenderLabelNameMismatchOnEitherSideOfTheExpectedName(t *testing.T) {
	r, _ := NewRegistry(Family{"m", Gauge, "h", []string{"db", "state"}})
	for _, name := range []string{"aaa", "stat", "statf", "zzz"} {
		var b bytes.Buffer
		if err := r.Render(&b, []Sample{sample("m", 1, "db", "a", name, "x")}); err == nil {
			t.Fatalf("label name %q accepted", name)
		}
	}
}

func TestFormatLabelsEmptyAndSeriesWithEqualEarlierLabels(t *testing.T) {
	if got := formatLabels(nil); got != "" {
		t.Fatalf("formatLabels(nil) = %q", got)
	}
	r, _ := NewRegistry(Family{"m", Gauge, "h", []string{"db", "k"}})
	got := render(t, r, []Sample{sample("m", 2, "db", "a", "k", "b"), sample("m", 1, "db", "a", "k", "a")})
	if !strings.Contains(got, "m{db=\"a\",k=\"a\"} 1\nm{db=\"a\",k=\"b\"} 2\n") {
		t.Fatalf("series with equal leading labels not ordered by the next label:\n%s", got)
	}
}
