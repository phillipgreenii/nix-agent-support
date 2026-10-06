package report

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/rangespec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// zoneA is a fixed non-UTC zone (UTC+05:30) so no tzdata is needed.
var zoneA = time.FixedZone("ZST", 5*3600+30*60)

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("%s differs from golden:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func gen(t *testing.T, r Request) string {
	t.Helper()
	g, ok := Lookup(BaselineKind)
	if !ok {
		t.Fatal("baseline is not registered")
	}
	res, err := g.Generate(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return res.Content
}

func utc(day, hour, min int) time.Time {
	return time.Date(2026, 3, day, hour, min, 0, 0, time.UTC)
}

func entries() []store.Entry {
	return []store.Entry{
		{ID: "e1", SourceID: "backend-one", Type: "change", OccurredAt: utc(1, 10, 5), Summary: "opened a change", Labels: []string{"alpha", "beta"}, URL: "https://example.test/c/1"},
		{ID: "e2", SourceID: "backend-two", Type: "issue", OccurredAt: utc(1, 11, 30), Summary: "filed an\nissue", URL: ""},
		{ID: "e3", SourceID: "backend-one", Type: "review", OccurredAt: utc(2, 9, 0), Summary: "reviewed a change", Labels: []string{"alpha"}, URL: "https://example.test/c/2"},
	}
}

func TestBaselineMultiDay(t *testing.T) {
	got := gen(t, Request{
		Range:   rangespec.Range{Since: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Before: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)},
		Entries: entries(),
		Footer: []SourceFooter{
			{Source: "backend-one", Count: 2, LastSuccessAt: utc(2, 12, 0)},
			{Source: "backend-two", Count: 1},
			{Source: "backend-idle", Count: 0, LastSuccessAt: utc(1, 8, 0)},
		},
	})
	golden(t, "multiday", got)
	if n := strings.Count(got, "\n## 2026-03-"); n != 2 {
		t.Errorf("want one day section per day with entries (2), got %d", n)
	}
}

func TestBaselineNarrowed(t *testing.T) {
	got := gen(t, Request{
		Range:     rangespec.Range{Since: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Before: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)},
		Narrowing: Narrowing{Labels: []string{"alpha"}, Sources: []string{"backend-one"}, Types: []string{"change", "review"}},
		Entries:   []store.Entry{entries()[0], entries()[2]},
		Footer:    []SourceFooter{{Source: "backend-one", LastSuccessAt: utc(2, 12, 0)}},
	})
	golden(t, "narrowed", got)
	if !strings.Contains(got, "Narrowing: labels=alpha; sources=backend-one; types=change,review") {
		t.Errorf("header does not state the narrowing applied:\n%s", got)
	}
}

func TestBaselineEmptyRange(t *testing.T) {
	got := gen(t, Request{
		Range: rangespec.Range{Since: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC), Before: time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC)},
	})
	golden(t, "empty", got)
	want := "No entries for " + FormatRange(rangespec.Range{Since: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC), Before: time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC)}) + "."
	if !strings.Contains(got, want) {
		t.Errorf("want %q in:\n%s", want, got)
	}
	if strings.Contains(got, "\n## 2026") {
		t.Errorf("an empty range must have no day section:\n%s", got)
	}
}

func TestBaselineOpenStart(t *testing.T) {
	got := gen(t, Request{Range: rangespec.Range{OpenStart: true, Before: time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC)}})
	if !strings.Contains(got, "Range: the beginning to 2026-03-06 00:00 UTC") {
		t.Errorf("open-ended range not stated:\n%s", got)
	}
}

// A non-UTC zone moves day sections and clock times: 22:30 UTC on the 1st is
// 04:00 on the 2nd at UTC+05:30.
func TestBaselineNonUTCZone(t *testing.T) {
	es := []store.Entry{
		{ID: "z1", SourceID: "backend-one", Type: "change", OccurredAt: utc(1, 22, 30), Summary: "late change"},
		{ID: "z2", SourceID: "backend-one", Type: "change", OccurredAt: utc(1, 5, 0), Summary: "early change"},
	}
	got := gen(t, Request{
		Range:   rangespec.Range{Since: time.Date(2026, 3, 1, 0, 0, 0, 0, zoneA), Before: time.Date(2026, 3, 3, 0, 0, 0, 0, zoneA)},
		Entries: es,
		Footer:  []SourceFooter{{Source: "backend-one", LastSuccessAt: utc(1, 23, 0)}},
	})
	golden(t, "zone", got)
	for _, want := range []string{"## 2026-03-01", "## 2026-03-02", "- 10:30 change early change", "- 04:00 change late change", "ZST"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

type stubGen struct {
	kind string
	err  error
}

func (s stubGen) Kind() string { return s.kind }
func (s stubGen) Generate(context.Context, Request) (Result, error) {
	if s.err != nil {
		return Result{}, s.err
	}
	return Result{Content: "stub"}, nil
}

func TestRegistryLookupRegister(t *testing.T) {
	if _, ok := Lookup("no-such-kind"); ok {
		t.Fatal("unknown kind must not resolve")
	}
	Register(stubGen{kind: "registry-probe"})
	g, ok := Lookup("registry-probe")
	if !ok || g.Kind() != "registry-probe" {
		t.Fatalf("Lookup after Register = %v, %v", g, ok)
	}
	found := false
	for _, k := range Kinds() {
		found = found || k == BaselineKind
	}
	if !found {
		t.Errorf("Kinds() = %v lacks baseline", Kinds())
	}
}

func TestRegisterRejectsEmptyKind(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register with an empty kind must panic")
		}
	}()
	Register(stubGen{})
}

func TestNarrowingUnhonoredIsDetectable(t *testing.T) {
	err := fmt.Errorf("%w: labels", ErrNarrowingUnhonored)
	if !errors.Is(err, ErrNarrowingUnhonored) {
		t.Error("a wrapped ErrNarrowingUnhonored must satisfy errors.Is")
	}
}
