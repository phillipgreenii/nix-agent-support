//go:build unix

package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
)

func TestRunOneAndSeveralFiles(t *testing.T) {
	fx := newFixture(t)
	one := fx.run(fx.gen(okScript()))
	requireOK(t, one)
	if len(one.Record.Files) != 1 || one.Record.Files[0].Path != "report.md" {
		t.Fatalf("files = %+v", one.Record.Files)
	}

	fx2 := newFixture(t)
	s := script{
		Dirs: []string{"data"},
		Files: []scriptFile{
			{Path: "report.md", Content: "# weekly\n"},
			{Path: "data/entries.csv", Content: "a,b\n1,2\n"},
		},
	}
	b, _ := json.Marshal(map[string]any{
		"contract_version": 1,
		"files": []map[string]any{
			{"path": "report.md", "content_type": "text/markdown", "title": "Weekly report"},
			{"path": "data/entries.csv", "content_type": "text/csv"},
		},
		"summary": "Rendered 2 entries into 2 files.", "warnings": []string{"w1"},
	})
	s.Stdout = string(b)
	out := fx2.run(fx2.gen(s))
	requireOK(t, out)
	if len(out.Record.Files) != 2 {
		t.Fatalf("files = %+v", out.Record.Files)
	}
	f0, f1 := out.Record.Files[0], out.Record.Files[1]
	sum := sha256.Sum256([]byte("# weekly\n"))
	if f0.Path != "report.md" || f0.Title != "Weekly report" || f0.ContentType != "text/markdown" ||
		f0.SizeBytes != 9 || f0.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("file 0 = %+v", f0)
	}
	if f1.Path != "data/entries.csv" || f1.Title != "" || f1.SizeBytes != 8 {
		t.Fatalf("file 1 = %+v", f1)
	}
	if out.Record.Summary != "Rendered 2 entries into 2 files." || !reflect.DeepEqual(out.Record.Warnings, []string{"w1"}) {
		t.Fatalf("summary/warnings = %q %v", out.Record.Summary, out.Record.Warnings)
	}
	if _, err := os.Stat(filepath.Join(out.RunDir, "data", "entries.csv")); err != nil {
		t.Fatal(err)
	}
}

// dumped is what the fake generator saw.
type dumped struct {
	Env   []string `json:"env"`
	Args  []string `json:"args"`
	Cwd   string   `json:"cwd"`
	Stdin string   `json:"stdin"`
}

func dumpRun(t *testing.T, fx *fixture, g func(s *script) config.Generator, mutate func(*Request)) (Outcome, dumped, map[string]json.RawMessage) {
	t.Helper()
	s := okScript()
	s.Dump = "dump.json"
	gen := g(&s)
	req := fx.request(gen)
	if mutate != nil {
		mutate(&req)
	}
	out := fx.runner.Run(context.Background(), req)
	requireOK(t, out)
	b, err := os.ReadFile(filepath.Join(out.RunDir, "dump.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d dumped
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(d.Stdin), &env); err != nil {
		t.Fatalf("stdin is not a JSON object: %v\n%s", err, d.Stdin)
	}
	return out, d, env
}

func TestEnvelopeCarriesTheRunInput(t *testing.T) {
	fx := newFixture(t)
	out, d, raw := dumpRun(t, fx, func(s *script) config.Generator { return fx.gen(*s) }, nil)

	var env Envelope
	if err := json.Unmarshal([]byte(d.Stdin), &env); err != nil {
		t.Fatal(err)
	}
	if env.ContractVersion != 1 || env.Mode != "generate" {
		t.Fatalf("contract_version/mode = %d %q", env.ContractVersion, env.Mode)
	}
	if env.RunID != filepath.Base(out.RunDir) || env.RunID != "20261020T012800Z-ab12" {
		t.Fatalf("run_id = %q, run dir %q", env.RunID, out.RunDir)
	}
	if env.OutputDir != out.RunDir {
		t.Fatalf("output_dir = %q, want %q", env.OutputDir, out.RunDir)
	}
	if env.Report.Generator != "fake" || env.Report.Preset == nil || *env.Report.Preset != "example-weekly" {
		t.Fatalf("report = %+v", env.Report)
	}
	if env.Range.Spec != "last-week" || env.Range.Zone != "America/New_York" || env.Range.OpenStart ||
		env.Range.Since == nil || *env.Range.Since != "2026-10-12T00:00:00-04:00" || env.Range.Before != "2026-10-19T00:00:00-04:00" {
		t.Fatalf("range = %+v", env.Range)
	}
	if !reflect.DeepEqual(env.Narrowing, Narrowing{Labels: []string{"kind:commit"}, Sources: []string{}, Types: []string{}}) {
		t.Fatalf("narrowing = %+v", env.Narrowing)
	}
	if len(env.Entries) != 2 || env.Entries[0].ID != "e1" || env.Entries[1].ID != "e2" {
		t.Fatalf("entries = %+v", env.Entries)
	}
	e0 := env.Entries[0]
	if e0.ExternalID != "x1" || e0.SourceID != "example-source" || e0.Type != "commit" || e0.OccurredAt != "2026-10-14T00:41:12Z" ||
		e0.Summary != "first" || e0.URL != "https://example.invalid/1" || !reflect.DeepEqual(e0.Labels, []string{"kind:commit"}) ||
		string(e0.Fields) != `{"sha":"abc"}` {
		t.Fatalf("entry 0 = %+v", e0)
	}
	// An entry without labels or fields still has both keys.
	if env.Entries[1].Labels == nil || string(env.Entries[1].Fields) != "{}" {
		t.Fatalf("entry 1 = %+v", env.Entries[1])
	}
	if len(env.Footer) != 1 || env.Footer[0].SourceID != "example-source" || env.Footer[0].Count != 2 ||
		env.Footer[0].LastSuccessAt == nil || *env.Footer[0].LastSuccessAt != "2026-10-19T21:02:00Z" || env.Footer[0].LastOutcome != "succeeded" {
		t.Fatalf("footer = %+v", env.Footer)
	}
	if !reflect.DeepEqual(env.Config, map[string]any{"model": "sonnet"}) {
		t.Fatalf("config = %v", env.Config)
	}
	// A registered generator without inputs gets no baseline key at all.
	if _, ok := raw["baseline"]; ok {
		t.Fatalf("baseline present without inputs: [baseline]: %s", raw["baseline"])
	}
}

func TestEnvelopeBaselineOnlyForInputsBaseline(t *testing.T) {
	fx := newFixture(t)
	_, _, raw := dumpRun(t, fx, func(s *script) config.Generator {
		g := fx.gen(*s)
		g.Inputs = []string{config.BaselineName}
		return g
	}, func(r *Request) { r.Baseline = "# Work report\n" })
	var got string
	if err := json.Unmarshal(raw["baseline"], &got); err != nil || got != "# Work report\n" {
		t.Fatalf("baseline = %s (%v)", raw["baseline"], err)
	}

	// With no baseline input registered the text is not sent even if given.
	fx2 := newFixture(t)
	_, _, raw2 := dumpRun(t, fx2, func(s *script) config.Generator { return fx2.gen(*s) }, func(r *Request) { r.Baseline = "# nope" })
	if _, ok := raw2["baseline"]; ok {
		t.Fatalf("baseline sent to a generator that did not ask for it")
	}
}

func TestEmptyRangeStillInvokesTheGenerator(t *testing.T) {
	fx := newFixture(t)
	out, d, raw := dumpRun(t, fx, func(s *script) config.Generator { return fx.gen(*s) }, func(r *Request) {
		r.Entries = nil
		r.Footer = nil
	})
	if string(raw["entries"]) != "[]" || string(raw["footer"]) != "[]" {
		t.Fatalf("entries = %s footer = %s, want []", raw["entries"], raw["footer"])
	}
	if out.Record.EntryCount != 0 {
		t.Fatalf("entry_count = %d", out.Record.EntryCount)
	}
	if d.Cwd == "" {
		t.Fatal("generator did not run")
	}
}

func TestOpenStartRangeAndNeverPulledFooter(t *testing.T) {
	fx := newFixture(t)
	_, _, raw := dumpRun(t, fx, func(s *script) config.Generator { return fx.gen(*s) }, func(r *Request) {
		r.Range.OpenStart = true
		r.Range.Since = time.Time{}
		r.Footer = []FooterItem{{SourceID: "never", Count: 0}}
	})
	var rng map[string]any
	_ = json.Unmarshal(raw["range"], &rng)
	if v, ok := rng["since"]; !ok || v != nil || rng["open_start"] != true {
		t.Fatalf("range = %v, want since null and open_start true", rng)
	}
	var footer []map[string]any
	_ = json.Unmarshal(raw["footer"], &footer)
	if v, ok := footer[0]["last_success_at"]; !ok || v != nil || footer[0]["last_outcome"] != "" {
		t.Fatalf("footer = %v, want last_success_at null", footer)
	}
}

func TestEnvironmentIsAnAllowlist(t *testing.T) {
	fx := newFixture(t)
	fx.env["WR_PASS"] = "passed-through"
	fx.env["WR_EMPTY"] = ""
	_, d, _ := dumpRun(t, fx, func(s *script) config.Generator {
		return fx.gen(*s, "WR_PASS", "WR_EMPTY", "WR_UNSET_NAME")
	}, nil)

	got := map[string]string{}
	for _, kv := range d.Env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	want := map[string]string{
		"PATH": "/usr/bin:/bin", "HOME": "/home-for-test", "USER": "tester", "LANG": "C", "TMPDIR": os.TempDir(),
		"XDG_CONFIG_HOME": "/xdg/config", "XDG_STATE_HOME": "/xdg/state", "XDG_CACHE_HOME": "/xdg/cache",
		"WR_FAKE_GEN": "script", "WR_PASS": "passed-through", "WR_EMPTY": "",
		"WORK_REPORT_CONTRACT": "1", "WORK_REPORT_MODE": "generate",
	}
	// Nothing but the allowlist may appear (not even PWD).
	delete(got, "WR_FAKE_SCRIPT")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %v\nwant        %v", got, want)
	}
	if _, leaked := got["AMBIENT_SECRET"]; leaked {
		t.Fatal("an ambient variable leaked")
	}
	if _, set := got["WR_UNSET_NAME"]; set {
		t.Fatal("an unset env name was set (it must be omitted)")
	}
}

func TestEnvironUsesTheSameAllowlistForValidateMode(t *testing.T) {
	lookup := func(k string) (string, bool) {
		m := map[string]string{"PATH": "/p", "HOME": "/h", "TOKEN_NAME": "t", "WORK_REPORT_MODE": "hijack"}
		v, ok := m[k]
		return v, ok
	}
	g := config.Generator{Env: []string{"TOKEN_NAME", "ABSENT", "WORK_REPORT_MODE", "PATH"}}
	got := Environ(g, ModeValidate, lookup)
	want := []string{"PATH=/p", "HOME=/h", "TOKEN_NAME=t", "WORK_REPORT_CONTRACT=1", "WORK_REPORT_MODE=validate"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Environ = %v, want %v", got, want)
	}
}

func TestArgvIsNotShellExpandedAndCwdIsTheRunDirectory(t *testing.T) {
	fx := newFixture(t)
	out, d, _ := dumpRun(t, fx, func(s *script) config.Generator {
		g := fx.gen(*s)
		g.Command = append(g.Command, "$HOME", "*", "a b;c", "`id`")
		return g
	}, nil)
	if want := []string{"$HOME", "*", "a b;c", "`id`"}; !reflect.DeepEqual(d.Args, want) {
		t.Fatalf("args = %q, want %q", d.Args, want)
	}
	gotCwd, _ := filepath.EvalSymlinks(d.Cwd)
	wantCwd, _ := filepath.EvalSymlinks(out.RunDir)
	if gotCwd != wantCwd {
		t.Fatalf("cwd = %q, want the run directory %q", gotCwd, wantCwd)
	}
}

func TestEPIPEOnStdinWithExitZeroIsNotAFailure(t *testing.T) {
	fx := newFixture(t)
	g := fx.gen(okScript()) // never reads stdin
	req := fx.request(g)
	big := strings.Repeat("x", 1<<20)
	for i := range req.Entries {
		req.Entries[i].Summary = big
	}
	out := fx.runner.Run(context.Background(), req)
	requireOK(t, out)
}

func TestEPIPEWithNonZeroExitIsExitNonzero(t *testing.T) {
	fx := newFixture(t)
	s := script{Stderr: "boom\n", Exit: 1}
	req := fx.request(fx.gen(s))
	req.Entries[0].Summary = strings.Repeat("x", 1<<20)
	out := fx.runner.Run(context.Background(), req)
	requireKind(t, out, KindExitNonzero)
	if out.Record.GeneratorExit == nil || *out.Record.GeneratorExit != 1 || !strings.Contains(out.Record.Reason, "boom") {
		t.Fatalf("record = %+v", out.Record)
	}
}

func TestExitCodesMapToOutcomeKinds(t *testing.T) {
	cases := []struct {
		exit int
		kind string
	}{{1, KindExitNonzero}, {2, KindExitNonzero}, {3, KindConfigInvalid}, {7, KindExitNonzero}}
	for _, c := range cases {
		fx := newFixture(t)
		out := fx.run(fx.gen(script{Stderr: "model: unknown option\n", Exit: c.exit}))
		requireKind(t, out, c.kind)
		if out.Record.GeneratorExit == nil || *out.Record.GeneratorExit != c.exit {
			t.Fatalf("exit %d: generator_exit = %v", c.exit, out.Record.GeneratorExit)
		}
		if !strings.Contains(out.Record.Reason, "model: unknown option") {
			t.Fatalf("exit %d: reason %q does not carry stderr", c.exit, out.Record.Reason)
		}
		if out.Stderr != "model: unknown option\n" {
			t.Fatalf("retained stderr = %q", out.Stderr)
		}
	}
}

func TestMissingExecutableIsNotFound(t *testing.T) {
	// A bare name absent from PATH is detected before any directory exists.
	fx := newFixture(t)
	g := fx.gen(okScript())
	g.Command = []string{"work-report-no-such-generator-xyz"}
	out := fx.run(g)
	requireKind(t, out, KindNotFound)
	if out.RunDir != "" {
		t.Fatalf("run dir %q created for a missing bare command", out.RunDir)
	}
	if _, err := os.Stat(fx.parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output parent was created: %v", err)
	}
	if out.Record.RunID == "" {
		t.Fatal("record has no run_id")
	}

	// A missing absolute path is found when the process starts, after the run
	// directory exists, and the failure is recorded in it.
	fx2 := newFixture(t)
	g2 := fx2.gen(okScript())
	g2.Command = []string{"/nonexistent/dir/generator"}
	out2 := fx2.run(g2)
	requireKind(t, out2, KindNotFound)
	if out2.RunDir == "" {
		t.Fatal("no run dir for a start failure")
	}
	if m := readManifest(t, out2.RunDir); m["outcome_kind"] != "not_found" {
		t.Fatalf("manifest.json = %v", m)
	}
}

func TestBuiltinGeneratorGoesThroughTheSamePipeline(t *testing.T) {
	fx := newFixture(t)
	req := fx.request(config.Generator{Name: "baseline", Builtin: true})
	req.Preset = "baseline"
	var seenEnv Envelope
	req.Builtin = func(_ context.Context, env Envelope, dir string) (GeneratorManifest, error) {
		seenEnv = env
		if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("# baseline\n"), 0o600); err != nil {
			return GeneratorManifest{}, err
		}
		return GeneratorManifest{Files: []ManifestFile{{Path: "report.md", ContentType: "text/markdown"}}, Summary: "built in"}, nil
	}
	out := fx.runner.Run(context.Background(), req)
	requireOK(t, out)
	if out.Record.Generator.Name != "baseline" || out.Record.Generator.Argv0 != "builtin:baseline" {
		t.Fatalf("generator = %+v", out.Record.Generator)
	}
	if seenEnv.RunID != filepath.Base(out.RunDir) || len(seenEnv.Entries) != 2 {
		t.Fatalf("hook envelope = %+v", seenEnv)
	}
	m := readManifest(t, out.RunDir)
	if g := m["generator"].(map[string]any); g["argv0"] != "builtin:baseline" {
		t.Fatalf("manifest.json generator = %v", g)
	}

	// A built-in that returns an invalid manifest fails the same validation.
	req2 := fx.request(config.Generator{Name: "baseline", Builtin: true})
	req2.OutputParent = filepath.Join(fx.parent, "two")
	req2.Builtin = func(context.Context, Envelope, string) (GeneratorManifest, error) {
		return GeneratorManifest{Files: nil}, nil
	}
	out2 := fx.runner.Run(context.Background(), req2)
	requireKind(t, out2, KindInvalidManifest)

	// And a built-in error is an exit_nonzero failure.
	req3 := fx.request(config.Generator{Name: "baseline", Builtin: true})
	req3.OutputParent = filepath.Join(fx.parent, "three")
	req3.Builtin = func(context.Context, Envelope, string) (GeneratorManifest, error) {
		return GeneratorManifest{}, errors.New("render failed")
	}
	out3 := fx.runner.Run(context.Background(), req3)
	requireKind(t, out3, KindExitNonzero)
	if !strings.Contains(out3.Record.Reason, "render failed") {
		t.Fatalf("reason = %q", out3.Record.Reason)
	}
}

func TestOutcomeCarriesRecordAndStderrBeforeAnyRunDirectory(t *testing.T) {
	fx := newFixture(t)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := fx.request(fx.gen(okScript()))
	req.OutputParent = filepath.Join(blocker, "reports")
	out := fx.runner.Run(context.Background(), req)
	requireKind(t, out, KindOutputDirUnwritable)
	if out.RunDir != "" || out.Record.Reason == "" || out.Record.RunID == "" {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Record.Preset == nil || *out.Record.Preset != "example-weekly" || out.Record.EntryCount != 2 ||
		out.Record.ToolVersion != "test-version" || out.Record.InputSHA256 == "" || out.Record.ConfigSHA256 == "" {
		t.Fatalf("record lacks run input: %+v", out.Record)
	}
}

func TestRunnerIsSafeForConcurrentUse(t *testing.T) {
	// Distinct run directories under the real random source, with the race
	// detector watching the shared Runner.
	fx := newFixture(t)
	fx.runner.Rand = nil
	fx.runner.Now = nil
	const n = 8
	dirs := make(chan string, n)
	for i := 0; i < n; i++ {
		go func() {
			req := fx.request(config.Generator{Name: "baseline", Builtin: true})
			req.Builtin = func(_ context.Context, _ Envelope, dir string) (GeneratorManifest, error) {
				return GeneratorManifest{Files: []ManifestFile{{Path: "r.md", ContentType: "text/markdown"}}}, os.WriteFile(filepath.Join(dir, "r.md"), []byte("x"), 0o600)
			}
			out := fx.runner.Run(context.Background(), req)
			if !out.OK() {
				dirs <- "FAILED: " + out.Record.Reason
				return
			}
			dirs <- out.RunDir
		}()
	}
	var got []string
	for i := 0; i < n; i++ {
		got = append(got, <-dirs)
	}
	sort.Strings(got)
	for i, d := range got {
		if strings.HasPrefix(d, "FAILED") {
			t.Fatal(d)
		}
		if i > 0 && d == got[i-1] {
			t.Fatalf("two runs share %q", d)
		}
	}
	if !slices.IsSorted(got) {
		t.Fatal("unreachable")
	}
}
