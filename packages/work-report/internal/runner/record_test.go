//go:build unix

package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
)

func keysOf(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestManifestJSONHasEveryFieldOfAnOkRun(t *testing.T) {
	fx := newFixture(t)
	req := fx.request(fx.gen(okScript()))
	req.Overrides = []string{"--range", "--label"}
	req.ExtraWarnings = []string{"source example-source is degraded"}
	out := fx.runner.Run(context.Background(), req)
	requireOK(t, out)
	m := readManifest(t, out.RunDir)

	for _, k := range []string{
		"status", "outcome_kind", "generator_exit", "signal", "timed_out", "tool_version", "contract_version",
		"run_id", "started_at", "ended_at", "generator_ms", "total_ms", "preset", "generator", "config",
		"config_sha256", "input_sha256", "range", "narrowing", "entry_count", "overrides", "files", "summary", "warnings",
	} {
		if _, ok := m[k]; !ok {
			t.Errorf("manifest.json lacks %q (has %v)", k, keysOf(m))
		}
	}
	if m["status"] != "ok" || m["outcome_kind"] != "ok" || m["timed_out"] != false || m["signal"] != nil ||
		m["generator_exit"] != float64(0) || m["tool_version"] != "test-version" || m["contract_version"] != float64(1) ||
		m["run_id"] != "20261020T012800Z-ab12" || m["started_at"] != "2026-10-20T01:28:00Z" || m["ended_at"] != "2026-10-20T01:28:00Z" ||
		m["preset"] != "example-weekly" || m["entry_count"] != float64(2) {
		t.Fatalf("scalars wrong: %v", m)
	}
	if _, has := m["reason"]; has {
		t.Fatal("an ok run has a reason")
	}
	g := m["generator"].(map[string]any)
	if g["name"] != "fake" || g["argv0"] != fx.gen(okScript()).Command[0] {
		t.Fatalf("generator = %v", g)
	}
	rng := m["range"].(map[string]any)
	if rng["spec"] != "last-week" || rng["since"] != "2026-10-12T00:00:00-04:00" || rng["before"] != "2026-10-19T00:00:00-04:00" || rng["zone"] != "America/New_York" {
		t.Fatalf("range = %v", rng)
	}
	nar := m["narrowing"].(map[string]any)
	if !reflect.DeepEqual(nar["labels"], []any{"kind:commit"}) || !reflect.DeepEqual(nar["sources"], []any{}) {
		t.Fatalf("narrowing = %v", nar)
	}
	if !reflect.DeepEqual(m["overrides"], []any{"--range", "--label"}) {
		t.Fatalf("overrides = %v", m["overrides"])
	}
	files := m["files"].([]any)
	f0 := files[0].(map[string]any)
	sum := sha256.Sum256([]byte("# report\n"))
	if f0["path"] != "report.md" || f0["content_type"] != "text/markdown" || f0["size_bytes"] != float64(9) || f0["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("files[0] = %v", f0)
	}
	if _, has := f0["title"]; has {
		t.Fatal("an untitled file has a title key")
	}
	if m["summary"] != "ok" || !reflect.DeepEqual(m["warnings"], []any{"source example-source is degraded"}) {
		t.Fatalf("summary/warnings = %v %v", m["summary"], m["warnings"])
	}
}

func TestManifestJSONOfAFailedRunHasTheFailureFields(t *testing.T) {
	fx := newFixture(t)
	req := fx.request(fx.gen(script{Stderr: "it broke\n", Exit: 1}))
	req.Preset = "" // an ad hoc run
	out := fx.runner.Run(context.Background(), req)
	requireKind(t, out, KindExitNonzero)
	m := readManifest(t, out.RunDir)
	if m["status"] != "failed" || m["outcome_kind"] != "exit_nonzero" || m["generator_exit"] != float64(1) ||
		m["signal"] != nil || m["timed_out"] != false || m["preset"] != nil || !strings.Contains(m["reason"].(string), "it broke") {
		t.Fatalf("manifest.json = %v", m)
	}
	for _, k := range []string{"files", "summary", "warnings"} {
		if _, has := m[k]; has {
			t.Fatalf("a failed run has %q", k)
		}
	}
	b, err := os.ReadFile(filepath.Join(out.RunDir, StderrName))
	if err != nil || string(b) != "it broke\n" {
		t.Fatalf("stderr.log = %q (%v)", b, err)
	}
}

func TestConfigIsRedactedByKeyNameAndHashedUnredacted(t *testing.T) {
	cfg := map[string]any{
		"model":     "sonnet",
		"monkey":    "banana",
		"apiToken":  "t0k3n",
		"nested":    map[string]any{"password": map[string]any{"a": 1.0}, "keep": "yes"},
		"list":      []any{map[string]any{"client_secret": "s"}, "plain"},
		"maxTokens": 900.0,
	}
	run := func(c map[string]any) (Outcome, map[string]any) {
		fx := newFixture(t)
		req := fx.request(fx.gen(okScript()))
		req.Config = c
		out := fx.runner.Run(context.Background(), req)
		requireOK(t, out)
		return out, readManifest(t, out.RunDir)
	}
	_, m := run(cfg)
	got := m["config"].(map[string]any)
	want := map[string]any{
		"model": "sonnet", "monkey": "***", "apiToken": "***", "maxTokens": "***",
		"nested": map[string]any{"password": "***", "keep": "yes"},
		"list":   []any{map[string]any{"client_secret": "***"}, "plain"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config = %v\nwant     %v", got, want)
	}
	if strings.Contains(mustRead(t, m), "t0k3n") || strings.Contains(mustRead(t, m), "banana") {
		t.Fatal("a secret reached manifest.json")
	}

	raw, _ := json.Marshal(cfg)
	wantSum := sha256.Sum256(raw)
	if m["config_sha256"] != hex.EncodeToString(wantSum[:]) {
		t.Fatalf("config_sha256 = %v, want the digest of the unredacted canonical JSON", m["config_sha256"])
	}
	_, m2 := run(cfg)
	if m2["config_sha256"] != m["config_sha256"] {
		t.Fatal("config_sha256 is not stable")
	}
	changed := map[string]any{}
	for k, v := range cfg {
		changed[k] = v
	}
	changed["monkey"] = "other" // redacted in the record, but the hash still identifies the config
	_, m3 := run(changed)
	if m3["config_sha256"] == m["config_sha256"] {
		t.Fatal("config_sha256 did not change with a redacted value")
	}
}

func mustRead(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInputDigestTracksTheInputNotTheClock(t *testing.T) {
	digest := func(mutate func(*fixture, *Request)) string {
		fx := newFixture(t)
		req := fx.request(fx.gen(okScript()))
		if mutate != nil {
			mutate(fx, &req)
		}
		out := fx.runner.Run(context.Background(), req)
		requireOK(t, out)
		return out.Record.InputSHA256
	}
	base := digest(nil)
	if base != digest(nil) {
		t.Fatal("input_sha256 is not stable")
	}
	if got := digest(func(fx *fixture, _ *Request) {
		fx.runner.Now = func() time.Time { return fixedNow.Add(26 * time.Hour) }
	}); got != base {
		t.Fatal("input_sha256 changed with the clock")
	}
	for name, mutate := range map[string]func(*fixture, *Request){
		"entries":   func(_ *fixture, r *Request) { r.Entries[0].Summary = "changed" },
		"narrowing": func(_ *fixture, r *Request) { r.Narrowing.Types = []string{"commit"} },
		"config":    func(_ *fixture, r *Request) { r.Config = map[string]any{"model": "opus"} },
		"range":     func(_ *fixture, r *Request) { r.Range.Spec = "yesterday" },
	} {
		if digest(mutate) == base {
			t.Errorf("input_sha256 did not change with the %s", name)
		}
	}
	// baseline is part of the digest only for a generator that receives it.
	withInput := func(text string) string {
		fx := newFixture(t)
		g := fx.gen(okScript())
		g.Inputs = []string{config.BaselineName}
		req := fx.request(g)
		req.Baseline = text
		out := fx.runner.Run(context.Background(), req)
		requireOK(t, out)
		return out.Record.InputSHA256
	}
	if withInput("a") == withInput("b") {
		t.Fatal("input_sha256 ignores the baseline text")
	}
	noInput := func(text string) string {
		return digest(func(_ *fixture, r *Request) { r.Baseline = text })
	}
	if noInput("a") != noInput("b") {
		t.Fatal("input_sha256 depends on a baseline the generator never received")
	}
}

func TestModesAreOwnerOnly(t *testing.T) {
	fx := newFixture(t)
	out := fx.run(fx.gen(okScript()))
	requireOK(t, out)
	for path, want := range map[string]os.FileMode{
		out.RunDir:                              0o700,
		fx.parent:                               0o700,
		filepath.Join(out.RunDir, ManifestName): 0o600,
		filepath.Join(out.RunDir, StderrName):   0o600,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %o, want %o", path, fi.Mode().Perm(), want)
		}
	}
}

func TestManifestIsWrittenLastAndAtomically(t *testing.T) {
	fx := newFixture(t)
	var order []string
	fx.runner.beforeWrite = func(dir, name string) {
		order = append(order, name)
		if name == ManifestName {
			// Everything else is already in place: stderr.log, the report, and no manifest yet.
			for _, must := range []string{StderrName, "report.md"} {
				if _, err := os.Stat(filepath.Join(dir, must)); err != nil {
					t.Errorf("%s missing when manifest.json is written: %v", must, err)
				}
			}
			if _, err := os.Lstat(filepath.Join(dir, ManifestName)); err == nil {
				t.Error("manifest.json exists before it is written")
			}
		}
	}
	out := fx.run(fx.gen(okScript()))
	requireOK(t, out)
	if !reflect.DeepEqual(order, []string{StderrName, ManifestName}) {
		t.Fatalf("write order = %v", order)
	}
	entries, _ := os.ReadDir(out.RunDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temporary file %q left behind", e.Name())
		}
	}
	// A crashed or killed run leaves no manifest.json: nothing is written
	// before the generator finished.
	fx2 := newFixture(t)
	ready := filepath.Join(t.TempDir(), "ready")
	cancel, result := fx2.runAsync(fx2.gen(script{ReadyFile: ready, Hang: true}))
	waitForFile(t, ready)
	dirs, _ := os.ReadDir(fx2.parent)
	if len(dirs) != 1 {
		t.Fatalf("parent entries = %v", dirs)
	}
	if _, err := os.Lstat(filepath.Join(fx2.parent, dirs[0].Name(), ManifestName)); err == nil {
		t.Fatal("manifest.json exists while the generator is still running")
	}
	cancel()
	result()
}

func TestPlantedEntriesAreReplacedNotFollowed(t *testing.T) {
	for _, name := range []string{ManifestName, StderrName} {
		t.Run("symlink "+name, func(t *testing.T) {
			fx := newFixture(t)
			outside := filepath.Join(t.TempDir(), "victim.txt")
			if err := os.WriteFile(outside, []byte("keep me"), 0o600); err != nil {
				t.Fatal(err)
			}
			s := okScript()
			s.Symlinks = []scriptLink{{Path: name, Target: outside}}
			out := fx.run(fx.gen(s))
			requireOK(t, out)
			if b, _ := os.ReadFile(outside); string(b) != "keep me" {
				t.Fatalf("the planted symlink was followed: %q", b)
			}
			fi, err := os.Lstat(filepath.Join(out.RunDir, name))
			if err != nil || !fi.Mode().IsRegular() {
				t.Fatalf("%s is not a regular file after the run: %v %v", name, fi, err)
			}
			if name == ManifestName {
				readManifest(t, out.RunDir) // valid JSON
			}
		})
		t.Run("directory "+name, func(t *testing.T) {
			fx := newFixture(t)
			s := okScript()
			s.Dirs = []string{name}
			s.Files = append(s.Files, scriptFile{Path: name + "/inside.txt", Content: "x"})
			out := fx.run(fx.gen(s))
			requireKind(t, out, KindOutputDirUnwritable)
			if !strings.Contains(out.Record.Reason, name) {
				t.Fatalf("reason = %q", out.Record.Reason)
			}
			fi, err := os.Lstat(filepath.Join(out.RunDir, name))
			if err != nil || !fi.IsDir() {
				t.Fatalf("the planted directory was removed or replaced: %v %v", fi, err)
			}
			if _, err := os.Stat(filepath.Join(out.RunDir, name, "inside.txt")); err != nil {
				t.Fatalf("something was written through the directory: %v", err)
			}
		})
	}
}

func TestFailedRunKeepsItsDirectoryAndEvidence(t *testing.T) {
	fx := newFixture(t)
	out := fx.run(fx.gen(script{Files: []scriptFile{{Path: "partial.md", Content: "half"}}, Stderr: "trace\n", Exit: 2}))
	requireKind(t, out, KindExitNonzero)
	for _, f := range []string{"partial.md", StderrName, ManifestName} {
		if _, err := os.Stat(filepath.Join(out.RunDir, f)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	// A failed run does not move latest.
	if _, err := os.Lstat(filepath.Join(fx.parent, LatestLink)); err == nil {
		t.Fatal("a failed run created latest")
	}
}
