package ccaudit

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/schema"
)

var update = flag.Bool("update", false, "rewrite golden files")

const (
	since = "2026-01-02T00:00:00Z"
	until = "2026-01-03T00:00:00Z"
)

func fixtures(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "provider-ccaudit"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// stub installs a fake pg-ccaudit on PATH that prints the fixture of the
// queried name and appends its argv to a log; it returns the log path.
func stub(t *testing.T, fail string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + log + "\"\n" +
		"if [ \"$2\" = \"" + fail + "\" ]; then echo boom >&2; exit 1; fi\n" +
		"cat \"" + fixtures(t) + "/$2.json\"\n"
	if err := os.WriteFile(filepath.Join(dir, "pg-ccaudit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func run(t *testing.T, o Options) []byte {
	t.Helper()
	if o.Since == "" {
		o.Since, o.Until = since, until
	}
	o.Scope = "demo"
	var buf bytes.Buffer
	if err := Run(context.Background(), o, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join(fixtures(t), "golden", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from golden:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
	if err := schema.Validate("evidence-rows", got); err != nil {
		t.Errorf("%s does not conform to the evidence-rows schema: %v", name, err)
	}
}

func TestGoldenPerQuery(t *testing.T) {
	stub(t, "")
	for _, q := range AllQueries {
		t.Run(q, func(t *testing.T) {
			golden(t, q, run(t, Options{Queries: []string{q}}))
		})
	}
}

func TestGoldenAllSevenMerged(t *testing.T) {
	stub(t, "")
	golden(t, "all", run(t, Options{}))
}

func TestIDsDeterministicAndSorted(t *testing.T) {
	stub(t, "")
	a, b := run(t, Options{}), run(t, Options{Queries: []string{"interruptions", "error-rate-by-tool", "top-signatures", "cost-by-signature", "retry-chains", "denied-tool-calls", "hook-refusals-in-body"}})
	if !bytes.Equal(a, b) {
		t.Error("two runs over one snapshot differ")
	}
	rows, err := Rows(context.Background(), Options{Since: since, Until: until, Scope: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		if want := "E-" + string(rune('0'+(i+1)/100)) + string(rune('0'+(i+1)/10%10)) + string(rune('0'+(i+1)%10)); r.ID != want {
			t.Errorf("row %d id = %s, want %s", i, r.ID, want)
		}
		if i > 0 {
			p := rows[i-1]
			if p.Kind > r.Kind || (p.Kind == r.Kind && p.SubjectKey >= r.SubjectKey) {
				t.Errorf("rows %d,%d out of (kind, subject key) order", i-1, i)
			}
		}
	}
}

func TestWindowBoundsNormalisedToUTC(t *testing.T) {
	log := stub(t, "")
	run(t, Options{Since: "2026-01-01T19:00:00-05:00", Until: "2026-01-02T19:00:00.250-05:00", Queries: []string{"top-signatures"}})
	b, _ := os.ReadFile(log)
	got := string(b)
	if !strings.Contains(got, "--since 2026-01-02T00:00:00Z --until 2026-01-03T00:00:00.25Z") {
		t.Errorf("bounds not normalised to UTC Z (fraction kept): %q", got)
	}
	if strings.Contains(got, "-05:00") {
		t.Errorf("offset leaked to pg-ccaudit: %q", got)
	}
}

func TestBoundWithoutOffsetIsUsageError(t *testing.T) {
	stub(t, "")
	_, err := Rows(context.Background(), Options{Since: "2026-01-02T00:00:00", Until: until, Scope: "demo"})
	if !errors.Is(err, ErrUsage) {
		t.Errorf("err = %v, want usage error", err)
	}
	_, err = Rows(context.Background(), Options{Since: until, Until: since, Scope: "demo"})
	if !errors.Is(err, ErrUsage) {
		t.Errorf("until before since: err = %v, want usage error", err)
	}
	_, err = Rows(context.Background(), Options{Since: since, Until: until, Scope: "demo", Queries: []string{"nope"}})
	if !errors.Is(err, ErrUsage) {
		t.Errorf("unknown query: err = %v, want usage error", err)
	}
}

func TestScopeFlagsPassedThroughAndJSONFormat(t *testing.T) {
	log := stub(t, "")
	run(t, Options{Queries: []string{"interruptions"}, CwdPrefixes: []string{"/srv/a", "/srv/b"}, ExcludeCwdPrefixes: []string{"/srv/neutral"}})
	b, _ := os.ReadFile(log)
	got := string(b)
	for _, want := range []string{"--cwd-prefix /srv/a", "--cwd-prefix /srv/b", "--exclude-cwd-prefix /srv/neutral", "--format json"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestQueryFailureWritesNothing(t *testing.T) {
	stub(t, "retry-chains")
	var buf bytes.Buffer
	err := Run(context.Background(), Options{Since: since, Until: until, Scope: "demo"}, &buf)
	if err == nil || buf.Len() != 0 {
		t.Errorf("err = %v, stdout = %q; want failure with empty stdout", err, buf.String())
	}
}

func TestMultilineSignatureWithTab(t *testing.T) {
	stub(t, "")
	rows, err := Rows(context.Background(), Options{Since: since, Until: until, Scope: "demo", Queries: []string{"top-signatures"}})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range rows {
		if !strings.HasPrefix(r.SubjectKey, "sig:cmd failed exit status N detail: bad fl") {
			continue
		}
		found = true
		label := strings.TrimSuffix(strings.TrimPrefix(r.SubjectKey, "sig:"), ":"+SigHash("cmd failed\nexit status 2\tdetail: bad flag --frobnicate in a very long trailing explanation"))
		if strings.ContainsAny(label, "\n\t") || utf8.RuneCountInString(label) != 40 {
			t.Errorf("label %q: want single-line, 40 runes", label)
		}
		if r.Metrics["errors"] != 7 {
			t.Errorf("errors = %v, want 7", r.Metrics["errors"])
		}
		if !strings.Contains(strings.Join(r.Samples, "|"), "\n") {
			t.Error("raw multi-line signature should survive as a sample")
		}
	}
	if !found {
		t.Fatalf("multi-line signature row missing: %+v", rows)
	}
}

func TestSessionPathSeqNeverSubjectKeys(t *testing.T) {
	stub(t, "")
	rows, err := Rows(context.Background(), Options{Since: since, Until: until, Scope: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		for _, bad := range []string{"sess-a", "sess-b", ".jsonl", "/proj/"} {
			if strings.Contains(r.SubjectKey, bad) {
				t.Errorf("subject key %q contains %q", r.SubjectKey, bad)
			}
		}
		ok := false
		for _, p := range []string{"tool:", "sig:", "lead:", "session:"} {
			ok = ok || strings.HasPrefix(r.SubjectKey, p)
		}
		if !ok {
			t.Errorf("unexpected subject key %q", r.SubjectKey)
		}
	}
}

func TestCostMergesIntoSignatureRowUsingElapsed(t *testing.T) {
	stub(t, "")
	rows, _ := Rows(context.Background(), Options{Since: since, Until: until, Scope: "demo", Queries: []string{"top-signatures", "cost-by-signature"}})
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (unranked cost row dropped when top-signatures ran)", len(rows))
	}
	for _, r := range rows {
		if _, ok := r.Metrics["duration_ms_sum"]; ok {
			t.Error("duration_ms_sum must never be reported")
		}
		if r.Metrics["elapsed_ms_sum"] == 0 || r.Metrics["errors"] == 0 || r.Query != "top-signatures" {
			t.Errorf("not merged: %+v", r)
		}
	}
	solo, _ := Rows(context.Background(), Options{Since: since, Until: until, Scope: "demo", Queries: []string{"cost-by-signature"}})
	if len(solo) != 3 {
		t.Errorf("cost alone gave %d rows, want 3", len(solo))
	}
}

func TestInterruptionsClusterWithinTenSeconds(t *testing.T) {
	stub(t, "")
	rows, _ := Rows(context.Background(), Options{Since: since, Until: until, Scope: "demo", Queries: []string{"interruptions"}})
	counts := map[float64]int{}
	for _, r := range rows {
		counts[r.Metrics["interruptions"]]++
	}
	// sess-a: two rows 8s apart cluster, the third (30 min later) stands alone; sess-b has one.
	if len(rows) != 3 || counts[2] != 1 || counts[1] != 2 {
		t.Errorf("clusters = %v over %d rows, want one pair and two singles", counts, len(rows))
	}
}

func TestSigLabel(t *testing.T) {
	cases := map[string]string{
		"open /Users/x/a.txt: no such file 123": "open <path>: no such file N",
		"and/or 7.5 retries":                    "and/or N retries",
		"  \n\t ":                               "(empty)",
		"réponse ":                              "réponse",
	}
	for in, want := range cases {
		if got := SigLabel(in); got != want {
			t.Errorf("SigLabel(%q) = %q, want %q", in, got, want)
		}
	}
	if n := utf8.RuneCountInString(SigLabel(strings.Repeat("é", 100))); n != 40 {
		t.Errorf("label runes = %d, want 40", n)
	}
	if h := SigHash("x"); len(h) != 8 {
		t.Errorf("hash %q not 8 chars", h)
	}
}
