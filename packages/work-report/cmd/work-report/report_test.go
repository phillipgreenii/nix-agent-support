package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

// reportFixture is an isolated state home, a seeded store and the fake
// generator's registration.
type reportFixture struct {
	t         *testing.T
	storePath string
	state     string // $XDG_STATE_HOME/work-report
	exe       string
}

func newReportFixture(t *testing.T) *reportFixture {
	t.Helper()
	home := isolate(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The fake generator reads WR_FAKE_GEN's presence; the value is irrelevant.
	t.Setenv(envFakeGen, "1")
	return &reportFixture{t: t, storePath: seedQueryStore(t), state: filepath.Join(home, "state", "work-report"), exe: exe}
}

// generators is the YAML of the fake generator registered as "fake".
func (f *reportFixture) generators(extra string) string {
	return fmt.Sprintf("generators:\n  fake:\n    command: [%q]\n    env: [%s, %s]\n%s", f.exe, envFakeGen, envDump, extra)
}

// run runs the CLI with a config of timezone UTC plus body, returning stdout,
// stderr and the error.
func (f *reportFixture) run(body string, args ...string) (string, string, error) {
	f.t.Helper()
	cfg := writeCfg(f.t, "timezone: UTC\n"+body)
	full := append([]string{"--config", cfg, "--store", f.storePath}, args...)
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(full)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

// recordPull adds a pull row so the store reports a last outcome for source.
func (f *reportFixture) recordPull(source, status, reason string) {
	f.t.Helper()
	st, err := store.Open(f.storePath)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	at := time.Date(2026, 3, 9, 8, 0, 0, 0, time.UTC)
	if err := st.RecordPull(context.Background(), store.PullRow{Source: source, StartedAt: at, EndedAt: at, Status: status, Reason: reason}); err != nil {
		f.t.Fatal(err)
	}
}

// runDirs lists the run directories (not the latest link) under parent.
func runDirs(t *testing.T, parent string) []string {
	t.Helper()
	ents, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, filepath.Join(parent, e.Name()))
		}
	}
	return out
}

func onlyRunDir(t *testing.T, parent string) string {
	t.Helper()
	dirs := runDirs(t, parent)
	if len(dirs) != 1 {
		t.Fatalf("want exactly one run directory under %s, got %v", parent, dirs)
	}
	return dirs[0]
}

func readManifest(t *testing.T, runDir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(runDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest.json is not JSON: %v\n%s", err, b)
	}
	return m
}

func readDump(t *testing.T, path string) fakeDump {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d fakeDump
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// reportLog returns the lines of report.jsonl under the isolated state home.
func reportLog(t *testing.T) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), "work-report", "log", "report.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("report.jsonl line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func mustContain(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, got)
		}
	}
}

func TestReportBaselineWritesRunDirectoryAndRecord(t *testing.T) {
	f := newReportFixture(t)
	out, _, err := f.run("", "report", "--range", "2026-03-01..2026-03-02")
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(f.state, "reports", "baseline")
	dir := onlyRunDir(t, parent)

	text, err := os.ReadFile(filepath.Join(dir, "baseline.md"))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "baseline.md", string(text), "- Generator: baseline", "## 2026-03-01", "## 2026-03-02", "a v2", "- 10:00 change a v2 (backend-one, x)", "backend-one: 2 entries")
	if strings.Contains(string(text), "a v1") || strings.Contains(string(text), "Kind:") {
		t.Errorf("baseline.md has a superseded observation or the retired Kind line:\n%s", text)
	}

	m := readManifest(t, dir)
	gen := m["generator"].(map[string]any)
	files := m["files"].([]any)
	if m["status"] != "ok" || m["preset"] != "baseline" || gen["name"] != "baseline" || gen["argv0"] != "builtin:baseline" || m["entry_count"].(float64) != 3 {
		t.Errorf("manifest = %v", m)
	}
	if len(files) != 1 || files[0].(map[string]any)["content_type"] != "text/markdown" || files[0].(map[string]any)["path"] != "baseline.md" {
		t.Errorf("files = %v", files)
	}
	for _, name := range []string{"stderr.log", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("run directory lacks %s: %v", name, err)
		}
	}
	if target, err := os.Readlink(filepath.Join(parent, "latest")); err != nil || target != filepath.Base(dir) {
		t.Errorf("latest -> %q, %v; want %q", target, err, filepath.Base(dir))
	}
	mustContain(t, "stdout", out, "baseline 2026-03-01..2026-03-02 -> "+dir, "entries: 3", "baseline.md  text/markdown  ", "Rendered 3 entries into 1 file.")
	if strings.Contains(out, "warning") {
		t.Errorf("an ok run with entries prints no warning:\n%s", out)
	}
}

func TestReportDefaultPresetIsBaselineAndEmptyRangeWarns(t *testing.T) {
	f := newReportFixture(t)
	out, _, err := f.run("", "report", "--range", "2026-04-01")
	if err != nil {
		t.Fatalf("an empty range is a report, not an error: %v", err)
	}
	mustContain(t, "stdout", out, "baseline 2026-04-01 -> ", "entries: 0", "warning: no entries in 2026-04-01 00:00 to 2026-04-02 00:00 UTC")
	dir := onlyRunDir(t, filepath.Join(f.state, "reports", "baseline"))
	text, _ := os.ReadFile(filepath.Join(dir, "baseline.md"))
	mustContain(t, "baseline.md", string(text), "No entries for 2026-04-01 00:00 to 2026-04-02 00:00 UTC.")
}

// The human output order: replacement note, header, entries, files, summary,
// warnings.
func TestReportHumanOutputOrder(t *testing.T) {
	f := newReportFixture(t)
	f.recordPull("backend-idle", "degraded", "upstream timed out")
	body := f.generators("") + "reports:\n  weekly:\n    generator: fake\n    range: \"2026-03-01..2026-03-02\"\n    narrowing: {labels: [z]}\n"
	out, _, err := f.run(body, "report", "weekly", "--label", "x")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	prefixes := []string{
		"narrowing: labels=x (replaces preset labels=z)",
		"weekly 2026-03-01..2026-03-02 -> ",
		"entries: 2",
		"report.md  text/markdown  ",
		"fake summary",
		"warning: fake warning",
		"warning: source backend-idle: its last pull degraded: upstream timed out",
	}
	if len(lines) != len(prefixes) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(prefixes), out)
	}
	for i, p := range prefixes {
		if !strings.HasPrefix(lines[i], p) {
			t.Errorf("line %d = %q, want prefix %q", i, lines[i], p)
		}
	}
}

// A flag replaces the preset field of the same name, found with Changed: even
// a flag set to its default value replaces.
func TestReportFlagsReplacePresetFieldsDetectedByChanged(t *testing.T) {
	f := newReportFixture(t)
	body := f.generators("") + "reports:\n  weekly:\n    generator: fake\n    range: \"2026-03-01\"\n    narrowing: {labels: [z], sources: [backend-two]}\n"

	// No flag: the preset's own range and narrowing apply (labels z AND source backend-two: nothing).
	out, _, err := f.run(body, "report", "weekly")
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "no-flag output", out, "weekly 2026-03-01 -> ", "entries: 0")
	m := readManifest(t, onlyRunDir(t, filepath.Join(f.state, "reports", "weekly")))
	if rng := m["range"].(map[string]any); rng["spec"] != "2026-03-01" {
		t.Errorf("range = %v", rng)
	}
	if ov := m["overrides"].([]any); len(ov) != 0 {
		t.Errorf("overrides = %v, want none", ov)
	}

	// --range today equals the flag's default and still replaces the preset's range.
	out, _, err = f.run(body, "report", "weekly", "--range", "today", "--label", "x", "--source", "backend-one", "--type", "change", "--output-dir", filepath.Join(f.state, "other"))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "flag output", out, "weekly today -> ",
		"narrowing: labels=x (replaces preset labels=z)",
		"narrowing: sources=backend-one (replaces preset sources=backend-two)",
		"narrowing: types=change\n")
	m = readManifest(t, onlyRunDir(t, filepath.Join(f.state, "other")))
	rng := m["range"].(map[string]any)
	nar := m["narrowing"].(map[string]any)
	if rng["spec"] != "today" || fmt.Sprint(nar["labels"]) != "[x]" || fmt.Sprint(nar["sources"]) != "[backend-one]" || fmt.Sprint(nar["types"]) != "[change]" {
		t.Errorf("range = %v, narrowing = %v", rng, nar)
	}
	if got := fmt.Sprint(m["overrides"]); got != "[--range --label --source --type --output-dir]" {
		t.Errorf("overrides = %s", got)
	}
}

func TestReportOutputParentPrecedence(t *testing.T) {
	f := newReportFixture(t)
	presetDir := filepath.Join(t.TempDir(), "preset-out")
	body := f.generators("") + "reports:\n  withdir:\n    generator: fake\n    output: {dir: " + presetDir + "}\n  nodir:\n    generator: fake\n"
	cwd := t.TempDir()
	t.Chdir(cwd)

	check := func(name, parent string, args ...string) {
		t.Helper()
		if _, _, err := f.run(body, args...); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if dirs := runDirs(t, parent); len(dirs) == 0 {
			t.Errorf("%s: no run directory under %s", name, parent)
		}
	}
	check("--output-dir relative to the current directory", filepath.Join(cwd, "rel", "out"), "report", "withdir", "--output-dir", "rel/out")
	check("--output-dir absolute beats the preset", filepath.Join(f.state, "abs"), "report", "withdir", "--output-dir", filepath.Join(f.state, "abs"))
	check("preset output.dir", presetDir, "report", "withdir")
	check("default reports/<preset>", filepath.Join(f.state, "reports", "nodir"), "report", "nodir")
	check("default reports/adhoc for --generator", filepath.Join(f.state, "reports", "adhoc"), "report", "--generator", "fake")
}

func TestReportGeneratorWithInputsBaselineGetsBaselineAndFooterOutcome(t *testing.T) {
	f := newReportFixture(t)
	f.recordPull("backend-one", "succeeded", "")
	f.recordPull("backend-two", "degraded", "rate limited")
	dump := filepath.Join(t.TempDir(), "dump.json")
	t.Setenv(envDump, dump)

	body := f.generators("    inputs: [baseline]\n") +
		"  plain:\n    command: [" + fmt.Sprintf("%q", f.exe) + "]\n    env: [" + envFakeGen + ", " + envDump + "]\n" +
		"reports:\n  withbase:\n    generator: fake\n    range: \"2026-03-01..2026-03-02\"\n  nobase:\n    generator: plain\n    range: \"2026-03-01..2026-03-02\"\n"
	out, _, err := f.run(body, "report", "withbase")
	if err != nil {
		t.Fatal(err)
	}
	env := readDump(t, dump).Envelope
	base, _ := env["baseline"].(string)
	mustContain(t, "envelope baseline", base, "# Work report", "- Generator: baseline", "a v2")
	got := map[string]string{}
	for _, it := range env["footer"].([]any) {
		m := it.(map[string]any)
		got[m["source_id"].(string)] = m["last_outcome"].(string)
	}
	if got["backend-one"] != "succeeded" || got["backend-two"] != "degraded" {
		t.Errorf("footer last_outcome = %v", got)
	}
	// The degraded source has an entry in the range; a warning names it.
	mustContain(t, "stdout", out, "warning: source backend-two: its last pull degraded: rate limited")
	m := readManifest(t, onlyRunDir(t, filepath.Join(f.state, "reports", "withbase")))
	if w := fmt.Sprint(m["warnings"]); !strings.Contains(w, "backend-two") || strings.Contains(w, "backend-one") {
		t.Errorf("manifest warnings = %s", w)
	}

	// A generator without inputs: [baseline] gets no baseline field.
	if _, _, err := f.run(body, "report", "nobase"); err != nil {
		t.Fatal(err)
	}
	if _, has := readDump(t, dump).Envelope["baseline"]; has {
		t.Error("a generator that did not ask for the baseline input received it")
	}
}

// A never-pulled source in the footer carries an empty last outcome, and a
// source with no entries in the range still warns when its last pull degraded.
func TestReportDegradedSourceWarnsEvenWithoutEntriesInRange(t *testing.T) {
	f := newReportFixture(t)
	f.recordPull("backend-idle", "degraded", "")
	out, _, err := f.run("", "report", "--range", "2026-03-01")
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "stdout", out, "warning: source backend-idle: its last pull degraded")
	m := readManifest(t, onlyRunDir(t, filepath.Join(f.state, "reports", "baseline")))
	if !strings.Contains(fmt.Sprint(m["warnings"]), "backend-idle") {
		t.Errorf("manifest warnings = %v", m["warnings"])
	}
}

// INV-BASELINE-1: errors in generators or reports never stop the baseline
// preset, while the affected preset fails naming the problem.
func TestReportBrokenSectionsKeepBaselineServing(t *testing.T) {
	f := newReportFixture(t)
	for name, body := range map[string]string{
		"typo key in a generator":  "generators:\n  fake:\n    comand: [x]\nreports:\n  mine:\n    generator: fake\n",
		"bad name":                 "reports:\n  Bad Name:\n    generator: baseline\n  mine:\n    generator: nosuch\n",
		"wrong type":               "generators:\n  fake:\n    command: [x]\n    timeout: [1]\nreports:\n  mine:\n    generator: fake\n    range: 7\n",
		"sections are not maps":    "generators: [a]\nreports: oops\n",
		"preset with a bad config": "reports:\n  mine:\n    generator: baseline\n    config: {a: 1}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := f.run(body, "report", "--range", "2026-03-01", "--output-dir", t.TempDir()); err != nil {
				t.Fatalf("the baseline preset must still succeed: %v", err)
			}
			_, _, err := f.run(body, "report", "mine", "--range", "2026-03-01", "--output-dir", t.TempDir())
			if err == nil {
				if name == "sections are not maps" {
					return // no preset "mine" exists, covered by the unknown-preset test
				}
				t.Fatal("the affected preset must fail")
			}
			if name == "sections are not maps" {
				if exitCode(err) != 2 {
					t.Errorf("exit %d, want 2 (unknown preset)", exitCode(err))
				}
				return
			}
			if exitCode(err) != 3 {
				t.Errorf("exit %d (%v), want 3", exitCode(err), err)
			}
			mustContain(t, "outcome", err.Error(), `preset "mine" is invalid`, "work-report report baseline")
		})
	}
}

func TestReportUnknownPresetIsUsageErrorListingNames(t *testing.T) {
	f := newReportFixture(t)
	body := f.generators("") + "reports:\n  weekly:\n    generator: fake\n"
	_, _, err := f.run(body, "report", "nosuch")
	if exitCode(err) != 2 {
		t.Fatalf("exit %d (%v), want 2", exitCode(err), err)
	}
	mustContain(t, "error", err.Error(), `"nosuch"`, "baseline", "weekly", "work-report config show")
	if lines := reportLog(t); len(lines) != 0 {
		t.Errorf("an unknown preset is a usage error and writes no telemetry line: %v", lines)
	}
}

func TestReportGeneratorAdHoc(t *testing.T) {
	f := newReportFixture(t)
	dump := filepath.Join(t.TempDir(), "dump.json")
	t.Setenv(envDump, dump)

	out, _, err := f.run(f.generators(""), "report", "--generator", "fake", "--range", "2026-03-01")
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "stdout", out, "adhoc 2026-03-01 -> ")
	m := readManifest(t, onlyRunDir(t, filepath.Join(f.state, "reports", "adhoc")))
	if m["preset"] != nil || m["generator"].(map[string]any)["name"] != "fake" {
		t.Errorf("manifest preset/generator = %v / %v", m["preset"], m["generator"])
	}
	env := readDump(t, dump).Envelope
	if cfg := env["config"].(map[string]any); len(cfg) != 0 {
		t.Errorf("an ad hoc run sends an empty config, got %v", cfg)
	}
	if env["report"].(map[string]any)["preset"] != nil {
		t.Errorf("envelope report = %v", env["report"])
	}

	// --generator and a preset exclude each other.
	_, _, err = f.run(f.generators(""), "report", "weekly", "--generator", "fake")
	if exitCode(err) != 2 {
		t.Errorf("both given: exit %d (%v), want 2", exitCode(err), err)
	}
}

// A generator that needs config exits 3 ad hoc and the outcome says to define
// a preset with a config.
func TestReportGeneratorRejectingEmptyConfigGetsPresetHint(t *testing.T) {
	f := newReportFixture(t)
	// The fake picks its behavior from config["fake"]; an ad hoc run has an
	// empty config, so use a script that always asks for config.
	script := filepath.Join(t.TempDir(), "needy.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\necho 'model is required' >&2\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("generators:\n  needy:\n    command: [%q]\n", script)
	out, _, err := f.run(body, "report", "--generator", "needy", "--output", "json")
	if exitCode(err) != 3 {
		t.Fatalf("exit %d (%v), want 3", exitCode(err), err)
	}
	mustContain(t, "error", err.Error(), "model is required", "define a preset with a config", "run directory kept:", "work-report report baseline")
	var oc reportOutcomeJSON
	if jerr := json.Unmarshal([]byte(out), &oc); jerr != nil || !strings.Contains(oc.Outcome, "define a preset with a config") {
		t.Errorf("stdout = %q, %v", out, jerr)
	}
	m := readManifest(t, onlyRunDir(t, filepath.Join(f.state, "reports", "adhoc")))
	if m["status"] != "failed" || m["outcome_kind"] != "config_invalid" {
		t.Errorf("manifest = %v", m)
	}
}

// D16: a --generator NAME nothing is registered under is an outcome built by
// the command layer: exit 3, no run directory, a not_found telemetry line.
func TestReportUnregisteredGeneratorIsNotFoundOutcome(t *testing.T) {
	for _, format := range []string{"human", "json"} {
		t.Run(format, func(t *testing.T) {
			f := newReportFixture(t)
			parent := filepath.Join(t.TempDir(), "out")
			out, _, err := f.run(f.generators(""), "--output", format, "report", "--generator", "nosuch", "--output-dir", parent)
			if exitCode(err) != 3 {
				t.Fatalf("exit %d (%v), want 3 (an outcome, not a usage error)", exitCode(err), err)
			}
			mustContain(t, "error", err.Error(), `"nosuch"`, "fake", "baseline", "work-report report baseline")
			if _, serr := os.Stat(parent); !os.IsNotExist(serr) {
				t.Errorf("no output directory may be created: %v", serr)
			}
			if format == "json" {
				var oc reportOutcomeJSON
				if jerr := json.Unmarshal([]byte(out), &oc); jerr != nil || !strings.Contains(oc.Outcome, "nosuch") {
					t.Errorf("stdout = %q, %v", out, jerr)
				}
			}
			lines := reportLog(t)
			if len(lines) != 1 {
				t.Fatalf("report.jsonl = %v, want one line", lines)
			}
			l := lines[0]
			if l["outcome_kind"] != "not_found" || l["status"] != "failed" || l["generator"] != "nosuch" || l["preset"] != nil || l["output_dir"] != nil || l["run_id"] == "" {
				t.Errorf("line = %v", l)
			}
		})
	}
}

func TestReportBaselineFallsBackToStdoutWhenParentUnwritable(t *testing.T) {
	f := newReportFixture(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(blocker, "reports") // a path under a regular file
	out, errOut, err := f.run("", "report", "--range", "2026-03-01", "--output-dir", parent)
	if err != nil {
		t.Fatalf("the baseline preset must fall back, not fail: %v", err)
	}
	mustContain(t, "stdout", out, "# Work report", "- Generator: baseline", "a v2")
	mustContain(t, "stderr", errOut, "warning:", "not usable", "printing the baseline report to stdout")
	if strings.Contains(out, "warning") {
		t.Errorf("the warning belongs on stderr, not in the report:\n%s", out)
	}
	if _, serr := os.Stat(parent); serr == nil {
		t.Errorf("no run directory, manifest or latest may exist")
	}
	lines := reportLog(t)
	if len(lines) != 1 || lines[0]["output_dir"] != nil || lines[0]["status"] != "ok" || lines[0]["run_id"] == "" || lines[0]["preset"] != "baseline" {
		t.Errorf("report.jsonl = %v", lines)
	}

	// A non-baseline preset fails with output_dir_unwritable instead.
	body := f.generators("") + "reports:\n  weekly:\n    generator: fake\n"
	_, _, err = f.run(body, "report", "weekly", "--output-dir", parent)
	if exitCode(err) != 3 {
		t.Fatalf("exit %d (%v), want 3", exitCode(err), err)
	}
	mustContain(t, "error", err.Error(), "work-report report baseline")
	lines = reportLog(t)
	if len(lines) != 2 || lines[1]["outcome_kind"] != "output_dir_unwritable" || lines[1]["status"] != "failed" {
		t.Errorf("report.jsonl = %v", lines)
	}
}

func TestReportOutcomeNamesReasonRunDirectoryAndBaselineHint(t *testing.T) {
	f := newReportFixture(t)
	body := f.generators("") + "reports:\n  boom:\n    generator: fake\n    config: {fake: crash}\n"
	_, _, err := f.run(body, "report", "boom", "--range", "2026-03-01")
	if exitCode(err) != 3 {
		t.Fatalf("exit %d (%v), want 3", exitCode(err), err)
	}
	dir := onlyRunDir(t, filepath.Join(f.state, "reports", "boom"))
	mustContain(t, "error", err.Error(), "generator exited with status 1", "boom: entry text echoed", "run directory kept: "+dir, "work-report report baseline")
	m := readManifest(t, dir)
	if m["status"] != "failed" || m["outcome_kind"] != "exit_nonzero" || m["generator_exit"].(float64) != 1 {
		t.Errorf("manifest = %v", m)
	}
	if _, serr := os.Lstat(filepath.Join(f.state, "reports", "boom", "latest")); !os.IsNotExist(serr) {
		t.Errorf("a failed run must not update latest: %v", serr)
	}
}

func TestReportPrint(t *testing.T) {
	t.Run("baseline prints the report only", func(t *testing.T) {
		f := newReportFixture(t)
		out, errOut, err := f.run("", "report", "--range", "2026-03-01", "--print")
		if err != nil {
			t.Fatal(err)
		}
		dir := onlyRunDir(t, filepath.Join(f.state, "reports", "baseline"))
		want, _ := os.ReadFile(filepath.Join(dir, "baseline.md"))
		if out != string(want) {
			t.Errorf("stdout is not exactly the report:\n%q\nvs\n%q", out, want)
		}
		mustContain(t, "stderr", errOut, "baseline 2026-03-01 -> "+dir, "entries: 2", "baseline.md  text/markdown", "Rendered 2 entries into 1 file.")
	})
	t.Run("several files are refused with the list and the run stays ok", func(t *testing.T) {
		f := newReportFixture(t)
		body := f.generators("") + "reports:\n  two:\n    generator: fake\n    config: {fake: twofiles}\n"
		out, _, err := f.run(body, "report", "two", "--print")
		if exitCode(err) != 3 {
			t.Fatalf("exit %d (%v), want 3", exitCode(err), err)
		}
		mustContain(t, "error", err.Error(), "--print", "a.md", "b.md", "run directory kept:")
		if out != "" {
			t.Errorf("a refused --print writes nothing to stdout, got %q", out)
		}
		m := readManifest(t, onlyRunDir(t, filepath.Join(f.state, "reports", "two")))
		if m["status"] != "ok" || m["outcome_kind"] != "ok" {
			t.Errorf("the run itself stays ok, manifest = %v", m)
		}
		if lines := reportLog(t); len(lines) != 1 || lines[0]["status"] != "ok" || lines[0]["outcome_kind"] != "ok" {
			t.Errorf("report.jsonl = %v", lines)
		}
	})
	t.Run("a non-text file is refused", func(t *testing.T) {
		f := newReportFixture(t)
		body := f.generators("") + "reports:\n  bin:\n    generator: fake\n    config: {fake: binary}\n"
		if _, _, err := f.run(body, "report", "bin", "--print"); exitCode(err) != 3 {
			t.Fatalf("exit %d (%v), want 3", exitCode(err), err)
		}
	})
	t.Run("the 8 MiB limit is inclusive", func(t *testing.T) {
		f := newReportFixture(t)
		body := f.generators("") + "reports:\n  exact:\n    generator: fake\n    config: {fake: exact}\n  big:\n    generator: fake\n    config: {fake: big}\n"
		out, _, err := f.run(body, "report", "exact", "--print")
		if err != nil || len(out) != printLimit {
			t.Fatalf("exactly 8 MiB must print: %d bytes, %v", len(out), err)
		}
		out, _, err = f.run(body, "report", "big", "--print")
		if exitCode(err) != 3 || out != "" {
			t.Fatalf("one byte over must be refused: exit %d (%v), %d bytes", exitCode(err), err, len(out))
		}
		mustContain(t, "error", err.Error(), "report.md")
	})
	t.Run("with --output json it is a usage error", func(t *testing.T) {
		f := newReportFixture(t)
		_, _, err := f.run("", "--output", "json", "report", "--print")
		if exitCode(err) != 2 {
			t.Fatalf("exit %d (%v), want 2", exitCode(err), err)
		}
	})
}

func TestReportJSONOutputIsTheManifest(t *testing.T) {
	f := newReportFixture(t)
	out, _, err := f.run("", "--output", "json", "report", "--range", "2026-03-01")
	if err != nil {
		t.Fatal(err)
	}
	var printed map[string]any
	if err := json.Unmarshal([]byte(out), &printed); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	onDisk := readManifest(t, onlyRunDir(t, filepath.Join(f.state, "reports", "baseline")))
	// ended_at and total_ms are stamped when the file is written, so compare
	// the stable identity of the run rather than the whole object.
	for _, k := range []string{"status", "run_id", "preset", "config_sha256", "input_sha256", "entry_count"} {
		if fmt.Sprint(printed[k]) != fmt.Sprint(onDisk[k]) {
			t.Errorf("%s: printed %v, on disk %v", k, printed[k], onDisk[k])
		}
	}
	if _, has := printed["files"]; !has {
		t.Errorf("printed manifest lacks files: %v", printed)
	}
}

func TestReportRetiredFlagsFailWithMigrationText(t *testing.T) {
	f := newReportFixture(t)
	_, _, err := f.run("", "report", "--kind", "baseline")
	if exitCode(err) != 2 {
		t.Fatalf("--kind: exit %d (%v), want 2", exitCode(err), err)
	}
	if !strings.Contains(err.Error(), "--kind was removed; run 'work-report report <preset>'. To keep the old narrative behavior define a preset: reports.<name>.generator: narrative") {
		t.Errorf("--kind text = %q", err)
	}
	_, _, err = f.run("", "report", "--out", filepath.Join(t.TempDir(), "x.md"))
	if exitCode(err) != 2 {
		t.Fatalf("--out: exit %d (%v), want 2", exitCode(err), err)
	}
	if want := "--out was removed; use 'work-report report --print > FILE' for a single text report, or read the files in the output directory."; err.Error() != want {
		t.Errorf("--out text = %q, want %q", err, want)
	}
	if dirs := runDirs(t, filepath.Join(f.state, "reports", "baseline")); len(dirs) != 0 {
		t.Errorf("a retired flag runs nothing: %v", dirs)
	}
	// Hidden: not advertised.
	help, _, _ := f.run("", "report", "--help")
	if strings.Contains(help, "--kind") || strings.Contains(help, "--out ") {
		t.Errorf("the retired flags must be hidden from help:\n%s", help)
	}
}

func TestReportTelemetryLineAndModes(t *testing.T) {
	f := newReportFixture(t)
	body := f.generators("") + "reports:\n  boom:\n    generator: fake\n    config: {fake: crash}\n    narrowing: {labels: [x]}\n"
	if _, _, err := f.run(body, "report", "--range", "2026-03-01"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.run(body, "report", "boom", "--range", "2026-03-01", "--type", "change"); exitCode(err) != 3 {
		t.Fatalf("exit %d (%v), want 3", exitCode(err), err)
	}
	lines := reportLog(t)
	if len(lines) != 2 {
		t.Fatalf("report.jsonl = %v", lines)
	}
	wantKeys := []string{"entry_count", "file_count", "generator", "generator_exit", "generator_ms", "narrowing", "outcome_kind", "output_dir", "overrides", "preset", "range", "reason", "run_id", "status", "tool_version", "total_ms", "ts"}
	ok, failed := lines[0], lines[1]
	if len(ok) != len(wantKeys) {
		t.Errorf("an ok line has %d keys, want %d: %v", len(ok), len(wantKeys), ok)
	}
	for _, k := range wantKeys {
		if _, has := ok[k]; !has {
			t.Errorf("ok line lacks %s", k)
		}
	}
	for _, retired := range []string{"kind", "since", "before", "duration_ms"} {
		if _, has := ok[retired]; has {
			t.Errorf("retired key %s is still written", retired)
		}
	}
	if ok["status"] != "ok" || ok["outcome_kind"] != "ok" || ok["preset"] != "baseline" || ok["generator"] != "baseline" ||
		ok["entry_count"].(float64) != 2 || ok["file_count"].(float64) != 1 || ok["generator_exit"].(float64) != 0 {
		t.Errorf("ok line = %v", ok)
	}
	if _, has := ok["stderr_tail"]; has {
		t.Error("an ok run has no stderr_tail")
	}
	if rng := ok["range"].(map[string]any); rng["spec"] != "2026-03-01" || rng["since"] != "2026-03-01T00:00:00Z" || rng["before"] != "2026-03-02T00:00:00Z" {
		t.Errorf("range = %v", rng)
	}
	if failed["status"] != "failed" || failed["outcome_kind"] != "exit_nonzero" || failed["preset"] != "boom" || failed["generator_exit"].(float64) != 1 ||
		!strings.Contains(failed["stderr_tail"].(string), "boom: entry text echoed") {
		t.Errorf("failed line = %v", failed)
	}
	if fmt.Sprint(failed["overrides"]) != "[--range --type]" {
		t.Errorf("overrides = %v", failed["overrides"])
	}

	logDir := filepath.Join(f.state, "log")
	for path, want := range map[string]os.FileMode{logDir: 0o700, filepath.Join(logDir, "report.jsonl"): 0o600} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != want {
			t.Errorf("%s mode = %o, want %o", path, st.Mode().Perm(), want)
		}
	}
}
