package hookdecisions_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/provider/hookdecisions"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/provider/hookdecisions/fixture"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/schema"
)

var update = flag.Bool("update", false, "rewrite golden files")

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func baseOpts(db string) hookdecisions.Options {
	return hookdecisions.Options{
		DBPath: db,
		Since:  mustTime("2026-03-01T00:00:00Z"),
		Until:  mustTime("2026-03-10T00:00:00Z"),
		Scope:  "synthetic-scope",
	}
}

func encode(t *testing.T, rows []hookdecisions.Row) []byte {
	t.Helper()
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate("evidence-rows", b); err != nil {
		t.Fatalf("output violates evidence-rows schema: %v", err)
	}
	return append(b, '\n')
}

func TestGoldenRows(t *testing.T) {
	db := fixture.Build(t, fixture.Standard())
	o := baseOpts(db)
	o.ExcludeCwdPrefixes = []string{"/neutral/review/"}
	rows, err := hookdecisions.Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	got := encode(t, rows)
	golden := filepath.Join("..", "..", "..", "testdata", "provider-hook-decisions", "golden.json")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	// The formatter may re-indent the golden file, so compare compacted JSON.
	var g, w bytes.Buffer
	if err := json.Compact(&g, got); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&w, want); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(g.Bytes(), w.Bytes()) {
		t.Errorf("golden mismatch; rerun with -update and review.\n got: %s\nwant: %s", got, want)
	}
}

func TestWindowIsHalfOpenAndExcludedRowsDropped(t *testing.T) {
	db := fixture.Build(t, fixture.Standard())
	rows, err := hookdecisions.Run(context.Background(), baseOpts(db))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.FirstSeen < "2026-03-01T00:00:00Z" || r.LastSeen >= "2026-03-10T00:00:00Z" {
			t.Errorf("row %s outside window: %s..%s", r.ID, r.FirstSeen, r.LastSeen)
		}
	}
	// "git push" rejected: s1/a, s2/a, s4 neutral, s4 other = 4 in window
	// (Feb row, excluded row and the row at the exclusive end are dropped).
	var push *hookdecisions.Row
	for i := range rows {
		if rows[i].Kind == "rejected" && rows[i].SubjectKey == "Bash:git push" {
			push = &rows[i]
		}
	}
	if push == nil || push.Metrics["count"] != 4 || push.Metrics["sessions"] != 3 {
		t.Fatalf("git push rejected row = %+v", push)
	}
}

func TestScopeFilters(t *testing.T) {
	db := fixture.Build(t, fixture.Standard())
	count := func(inc, exc []string) float64 {
		o := baseOpts(db)
		o.Queries = []string{"rejected"}
		o.CwdPrefixes, o.ExcludeCwdPrefixes = inc, exc
		rows, err := hookdecisions.Run(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		var n float64
		for _, r := range rows {
			n += r.Metrics["count"]
		}
		return n
	}
	all := count(nil, nil)
	if got := count([]string{"/work/proj"}, nil); got != 4 {
		t.Errorf("include /work/proj = %v, want 4", got)
	}
	if got := count([]string{"/work/pro"}, nil); got != 0 {
		t.Errorf("include /work/pro (component match) = %v, want 0", got)
	}
	if got := count(nil, []string{"/neutral/review"}); got != all-1 {
		t.Errorf("exclude neutral = %v, want %v", got, all-1)
	}
	if got := count([]string{"/work/proj", "/other"}, []string{"/work/proj/b"}); got != 3 {
		t.Errorf("include+exclude = %v, want 3", got)
	}
}

func TestQuerySelection(t *testing.T) {
	db := fixture.Build(t, fixture.Standard())
	o := baseOpts(db)
	o.Queries = []string{"failed"}
	rows, err := hookdecisions.Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Kind != "failed" || rows[0].SubjectKey != "Write" || rows[0].ID != "E-001" {
		t.Fatalf("rows = %+v", rows)
	}
	o.Queries = []string{"nope"}
	_, err = hookdecisions.Run(context.Background(), o)
	var ue *hookdecisions.UsageError
	if err == nil || !asUsage(err, &ue) {
		t.Fatalf("unknown query err = %v, want UsageError", err)
	}
}

func asUsage(err error, target **hookdecisions.UsageError) bool {
	u, ok := err.(*hookdecisions.UsageError)
	if ok {
		*target = u
	}
	return ok
}

func TestDeterministicIDsAndSamplesAreNotSubjects(t *testing.T) {
	db := fixture.Build(t, fixture.Standard())
	r1, err := hookdecisions.Run(context.Background(), baseOpts(db))
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := hookdecisions.Run(context.Background(), baseOpts(db))
	if !reflect.DeepEqual(r1, r2) {
		t.Fatal("two runs differ")
	}
	for i, r := range r1 {
		if want := "E-" + pad(i+1); r.ID != want {
			t.Errorf("row %d id = %s, want %s", i, r.ID, want)
		}
		if i > 0 {
			p := r1[i-1]
			if p.Kind > r.Kind || (p.Kind == r.Kind && p.SubjectKey >= r.SubjectKey) {
				t.Errorf("rows %s,%s not in (kind, subject) order", p.ID, r.ID)
			}
		}
		if strings.Contains(r.SubjectKey, "session") || len(r.Samples) == 0 || len(r.Samples) > 3 {
			t.Errorf("row %s subject %q samples %v", r.ID, r.SubjectKey, r.Samples)
		}
		for _, s := range r.Samples {
			if !strings.HasPrefix(s, "decision:") || !strings.Contains(s, " session:") {
				t.Errorf("sample %q not a reference", s)
			}
		}
	}
}

func pad(n int) string {
	s := "00" + itoa(n)
	return s[len(s)-3:]
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestOpensReadOnly(t *testing.T) {
	db := fixture.Build(t, fixture.Standard())
	h, err := hookdecisions.OpenReadOnly(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	if _, err := h.Exec(`DELETE FROM tool_decisions`); err == nil {
		t.Fatal("write through the provider's handle succeeded")
	}
	// A read-only file works too.
	if err := os.Chmod(db, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := hookdecisions.Run(context.Background(), baseOpts(db)); err != nil {
		t.Fatalf("read-only file: %v", err)
	}
}

func TestMissingDatabaseIsRuntimeError(t *testing.T) {
	o := baseOpts(filepath.Join(t.TempDir(), "absent.db"))
	_, err := hookdecisions.Run(context.Background(), o)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*hookdecisions.UsageError); ok {
		t.Fatalf("missing db reported as usage error: %v", err)
	}
	if _, statErr := os.Stat(o.DBPath); statErr == nil {
		t.Fatal("provider created the database")
	}
}

func TestSubjectKey(t *testing.T) {
	cases := []struct{ tool, in, want string }{
		{"Bash", `{"command":"git push origin main"}`, "Bash:git push"},
		{"Bash", `{"command":"FOO=1 /usr/bin/git status -s"}`, "Bash:git status"},
		{"Bash", `{"command":"git -C /x log"}`, "Bash:git"},
		{"Bash", `{"command":"ls -la /tmp"}`, "Bash:ls"},
		{"Bash", `{"command":"   "}`, "Bash:(empty)"},
		{"Bash", `not json`, "Bash:(unparsed)"},
		{"Write", `{"file_path":"/x"}`, "Write"},
	}
	for _, c := range cases {
		if got := hookdecisions.SubjectKey(c.tool, c.in); got != c.want {
			t.Errorf("SubjectKey(%s,%s) = %q, want %q", c.tool, c.in, got, c.want)
		}
	}
}

func TestUsageValidation(t *testing.T) {
	db := fixture.Build(t, nil)
	for name, mut := range map[string]func(*hookdecisions.Options){
		"no since":        func(o *hookdecisions.Options) { o.Since = time.Time{} },
		"inverted":        func(o *hookdecisions.Options) { o.Since, o.Until = o.Until, o.Since },
		"no scope":        func(o *hookdecisions.Options) { o.Scope = "" },
		"relative prefix": func(o *hookdecisions.Options) { o.CwdPrefixes = []string{"rel"} },
		"root prefix":     func(o *hookdecisions.Options) { o.ExcludeCwdPrefixes = []string{"/"} },
	} {
		o := baseOpts(db)
		mut(&o)
		_, err := hookdecisions.Run(context.Background(), o)
		if _, ok := err.(*hookdecisions.UsageError); !ok {
			t.Errorf("%s: err = %v, want UsageError", name, err)
		}
	}
}

func TestEmptyResultIsEmptyArray(t *testing.T) {
	db := fixture.Build(t, nil)
	rows, err := hookdecisions.Run(context.Background(), baseOpts(db))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encode(t, rows)); got != "[]\n" {
		t.Fatalf("got %q", got)
	}
}
