package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/claudefake"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/report"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

// runReport runs `report` against a seeded store in the UTC zone and returns
// stdout, the error and the store path.
func runReport(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	isolate(t)
	storePath := seedQueryStore(t)
	cfg := writeCfg(t, "timezone: UTC\n")
	full := append([]string{"--config", cfg, "--store", storePath, "report"}, args...)
	out, err := runCLI(t, full...)
	return out, storePath, err
}

func lastReport(t *testing.T, storePath string) *store.ReportRow {
	t.Helper()
	st, err := store.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	d, err := st.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return d.LastReport
}

func reportLogLines(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), "work-report", "log", "report.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestReportBaselineMultiDayStoredAndLogged(t *testing.T) {
	out, storePath, err := runReport(t, "--range", "2026-03-01..2026-03-02")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Kind: baseline", "## 2026-03-01", "## 2026-03-02", "a v2", "- 10:00 change a v2 (backend-one, x)", "## Sources", "backend-one: 2 entries"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "a v1") {
		t.Errorf("superseded observation rendered:\n%s", out)
	}
	r := lastReport(t, storePath)
	if r == nil || r.Kind != "baseline" {
		t.Fatalf("no baseline report row stored: %+v", r)
	}
	lines := reportLogLines(t)
	if len(lines) != 1 || !strings.Contains(lines[0], `"status":"rendered"`) || !strings.Contains(lines[0], `"kind":"baseline"`) {
		t.Errorf("report.jsonl = %v", lines)
	}
}

func TestReportDefaultKindIsBaseline(t *testing.T) {
	out, _, err := runReport(t, "--range", "2026-03-01")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Kind: baseline") {
		t.Errorf("default kind is not baseline:\n%s", out)
	}
}

func TestReportNarrowedHeader(t *testing.T) {
	out, _, err := runReport(t, "--range", "2026-03-01..2026-03-02", "--label", "y", "--source", "backend-two", "--type", "issue")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Narrowing: labels=y; sources=backend-two; types=issue") {
		t.Errorf("header does not state the narrowing:\n%s", out)
	}
	if strings.Contains(out, "## 2026-03-02") {
		t.Errorf("narrowing not applied:\n%s", out)
	}
}

func TestReportEmptyRange(t *testing.T) {
	out, storePath, err := runReport(t, "--range", "2026-04-01")
	if err != nil {
		t.Fatalf("an empty range is a report, not an error: %v", err)
	}
	if !strings.Contains(out, "No entries for 2026-04-01 00:00 to 2026-04-02 00:00 UTC.") {
		t.Errorf("empty-range line missing:\n%s", out)
	}
	if lastReport(t, storePath) == nil {
		t.Error("an empty report is still stored")
	}
}

func TestReportUnknownKindIsOutcome(t *testing.T) {
	for _, kind := range []string{"bogus"} {
		t.Run(kind, func(t *testing.T) {
			_, storePath, err := runReport(t, "--kind", kind, "--range", "2026-03-01")
			var oc *reportOutcome
			if !errors.As(err, &oc) {
				t.Fatalf("err = %v, want a reportOutcome (exit 1)", err)
			}
			for _, want := range []string{kind, "no generator", "baseline, narrative", "--kind baseline"} {
				if !strings.Contains(oc.Error(), want) {
					t.Errorf("outcome %q lacks %q", oc.Error(), want)
				}
			}
			if lastReport(t, storePath) != nil {
				t.Error("an outcome must not store a report")
			}
			lines := reportLogLines(t)
			if len(lines) != 1 || !strings.Contains(lines[0], `"status":"outcome"`) {
				t.Errorf("report.jsonl = %v", lines)
			}
		})
	}
}

type unhonoringGen struct{}

func (unhonoringGen) Kind() string { return "test-unhonoring" }
func (unhonoringGen) Generate(_ context.Context, r report.Request) (report.Result, error) {
	if len(r.Narrowing.Labels) > 0 {
		return report.Result{}, fmt.Errorf("%w: labels", report.ErrNarrowingUnhonored)
	}
	return report.Result{Content: "ok"}, nil
}

func TestReportNarrowingUnhonoredIsOutcomeNamingDimension(t *testing.T) {
	report.Register(unhonoringGen{})
	_, storePath, err := runReport(t, "--kind", "test-unhonoring", "--range", "2026-03-01", "--label", "x")
	var oc *reportOutcome
	if !errors.As(err, &oc) {
		t.Fatalf("err = %v, want a reportOutcome (exit 1)", err)
	}
	if !strings.Contains(oc.Error(), "narrowing") || !strings.Contains(oc.Error(), "labels") {
		t.Errorf("outcome does not name the dimension: %q", oc.Error())
	}
	if lastReport(t, storePath) != nil {
		t.Error("an outcome must not store a report")
	}

	// The same generator without that narrowing produces a stored report.
	out, storePath, err := runReport(t, "--kind", "test-unhonoring", "--range", "2026-03-01")
	if err != nil || out != "ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if r := lastReport(t, storePath); r == nil || r.Kind != "test-unhonoring" || r.Generator != "test-unhonoring" {
		t.Errorf("stored row = %+v", r)
	}
}

func TestReportJSONAndOutFile(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "report.md")
	stdout, _, err := runReport(t, "--range", "2026-03-01", "--label", "x", "--output", "json", "--out", outFile)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Errorf("with --out nothing goes to stdout, got %q", stdout)
	}
	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	var got reportJSON
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("--out is not the JSON response: %v\n%s", err, b)
	}
	if got.Kind != "baseline" || got.Content == "" || got.Range.Since == nil || got.Range.Before == "" {
		t.Errorf("response = %+v", got)
	}
	if len(got.Narrowing.Labels) != 1 || got.Narrowing.Labels[0] != "x" || got.Narrowing.Sources == nil || got.Narrowing.Types == nil {
		t.Errorf("narrowing = %+v", got.Narrowing)
	}
}

func TestReportPlainOutFile(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "report.md")
	stdout, _, err := runReport(t, "--range", "2026-03-01", "--out", outFile)
	if err != nil || stdout != "" {
		t.Fatalf("stdout=%q err=%v", stdout, err)
	}
	b, err := os.ReadFile(outFile)
	if err != nil || !strings.HasPrefix(string(b), "# Work report") {
		t.Errorf("out file = %q, %v", b, err)
	}
}

func TestReportBadRangeIsError(t *testing.T) {
	_, _, err := runReport(t, "--range", "nonsense")
	if err == nil {
		t.Fatal("an unrecognized range must fail")
	}
	var oc *reportOutcome
	if errors.As(err, &oc) {
		t.Error("a bad range is a usage error, not a report outcome")
	}
}

const fakeNarrative = "## What I did\nWrote the thing.\n"

func TestReportNarrativeClaudeAbsentIsOutcome(t *testing.T) {
	for _, format := range []string{"human", "json"} {
		t.Run(format, func(t *testing.T) {
			isolate(t)
			t.Setenv("PATH", t.TempDir())
			storePath := seedQueryStore(t)
			cfg := writeCfg(t, "timezone: UTC\n")
			out, err := runCLI(t, "--config", cfg, "--store", storePath, "--output", format,
				"report", "--kind", "narrative", "--range", "2026-03-01")
			var oc *reportOutcome
			if !errors.As(err, &oc) {
				t.Fatalf("err = %v, want a reportOutcome (exit 1)", err)
			}
			for _, want := range []string{"claude", "--kind baseline"} {
				if !strings.Contains(oc.Error(), want) {
					t.Errorf("outcome %q lacks %q", oc.Error(), want)
				}
			}
			if lastReport(t, storePath) != nil {
				t.Error("an outcome must not store a report")
			}
			lines := reportLogLines(t)
			if len(lines) != 1 || !strings.Contains(lines[0], `"status":"outcome"`) {
				t.Errorf("report.jsonl = %v", lines)
			}
			if format == "json" {
				var got reportOutcomeJSON
				if err := json.Unmarshal([]byte(out), &got); err != nil || !strings.Contains(got.Outcome, "claude") {
					t.Errorf("stdout = %q, %v; want {\"outcome\": ...} naming claude", out, err)
				}
			}
		})
	}
}

func TestReportNarrativeStoresAndPrints(t *testing.T) {
	isolate(t)
	rec := claudefake.Install(t, claudefake.Behavior{Stdout: fakeNarrative})
	storePath := seedQueryStore(t)
	cfg := writeCfg(t, "timezone: UTC\n")
	out, err := runCLI(t, "--config", cfg, "--store", storePath, "report", "--kind", "narrative", "--range", "2026-03-01")
	if err != nil {
		t.Fatal(err)
	}
	if out != fakeNarrative {
		t.Errorf("stdout = %q, want %q", out, fakeNarrative)
	}
	r := lastReport(t, storePath)
	if r == nil || r.Kind != "narrative" || r.Generator != "narrative" || r.Content != fakeNarrative {
		t.Fatalf("stored row = %+v", r)
	}
	lines := reportLogLines(t)
	if len(lines) != 1 || !strings.Contains(lines[0], `"status":"rendered"`) || !strings.Contains(lines[0], `"kind":"narrative"`) {
		t.Errorf("report.jsonl = %v", lines)
	}
	if n := len(rec.Calls()); n != 1 {
		t.Errorf("claude called %d times, want 1", n)
	}
}

func TestReportNarrativeConfigReachesGenerator(t *testing.T) {
	isolate(t)
	rec := claudefake.Install(t, claudefake.Behavior{Stdout: fakeNarrative})
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("CUSTOM PROMPT MARKER"), 0o600); err != nil {
		t.Fatal(err)
	}
	storePath := seedQueryStore(t)
	cfg := writeCfg(t, "timezone: UTC\nkinds:\n  narrative:\n    model: test-model\n    systemPromptFile: "+promptFile+"\n")
	if _, err := runCLI(t, "--config", cfg, "--store", storePath, "report", "--kind", "narrative", "--range", "2026-03-01"); err != nil {
		t.Fatal(err)
	}
	calls := rec.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	argv := strings.Join(calls[0].Args, "\x00")
	if !strings.Contains(argv, "--model\x00test-model") || !strings.Contains(argv, "CUSTOM PROMPT MARKER") {
		t.Errorf("configured model/prompt did not reach claude: %q", calls[0].Args)
	}
}

func TestReportNarrativeJSONWrapsContent(t *testing.T) {
	isolate(t)
	claudefake.Install(t, claudefake.Behavior{Stdout: fakeNarrative})
	storePath := seedQueryStore(t)
	cfg := writeCfg(t, "timezone: UTC\n")
	out, err := runCLI(t, "--config", cfg, "--store", storePath, "--output", "json",
		"report", "--kind", "narrative", "--range", "2026-03-01", "--label", "x")
	if err != nil {
		t.Fatal(err)
	}
	var got reportJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not the JSON response: %v\n%s", err, out)
	}
	if got.Kind != "narrative" || got.Content != fakeNarrative || got.Range.Since == nil || got.Range.Before == "" {
		t.Errorf("response = %+v", got)
	}
	if len(got.Narrowing.Labels) != 1 || got.Narrowing.Labels[0] != "x" || got.Narrowing.Sources == nil || got.Narrowing.Types == nil {
		t.Errorf("narrowing = %+v", got.Narrowing)
	}
}

func TestReportNarrativeReceivesNarrowedEntries(t *testing.T) {
	isolate(t)
	rec := claudefake.Install(t, claudefake.Behavior{Stdout: fakeNarrative})
	storePath := seedQueryStore(t)
	cfg := writeCfg(t, "timezone: UTC\n")
	// Label y matches only entry b (backend-two); a and c are narrowed out.
	if _, err := runCLI(t, "--config", cfg, "--store", storePath,
		"report", "--kind", "narrative", "--range", "2026-03-01..2026-03-02", "--label", "y"); err != nil {
		t.Fatal(err)
	}
	calls := rec.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	stdin := calls[0].Stdin
	if !strings.Contains(stdin, "issue b (backend-two") {
		t.Errorf("narrowed-in entry missing from stdin:\n%s", stdin)
	}
	for _, gone := range []string{"change a v2 (backend-one", "review c (backend-one"} {
		if strings.Contains(stdin, gone) {
			t.Errorf("narrowed-out entry %q reached claude:\n%s", gone, stdin)
		}
	}
}

func TestReportNarrativeClaudeFailureIsOutcome(t *testing.T) {
	isolate(t)
	claudefake.Install(t, claudefake.Behavior{Exit: 3})
	storePath := seedQueryStore(t)
	cfg := writeCfg(t, "timezone: UTC\n")
	_, err := runCLI(t, "--config", cfg, "--store", storePath, "report", "--kind", "narrative", "--range", "2026-03-01")
	var oc *reportOutcome
	if !errors.As(err, &oc) {
		t.Fatalf("err = %v, want a reportOutcome (exit 1)", err)
	}
	if !strings.Contains(oc.Error(), "--kind baseline") {
		t.Errorf("outcome %q does not point at --kind baseline", oc.Error())
	}
	if lastReport(t, storePath) != nil {
		t.Error("an outcome must not store a report")
	}
}
