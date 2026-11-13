package sensorcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
	"github.com/phillipgreenii/pg-router/schemas"
)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// fake is a sensor with two resources: "alpha" (graded, driven by value) and
// "beta" (two-level).
type fake struct {
	value    float64
	failWith error
	omitBeta bool
	badLevel bool
	cfg      Config
	seen     Env
	cfgErr   error
	onlyKey  bool
}

func (f *fake) Resources() []string { return []string{"alpha", "beta"} }

func (f *fake) table() levels.Table {
	return levels.Table{
		Resource: "alpha", Unit: "GiB", Scale: levels.FourStep, Dir: levels.HigherBetter, Hyst: levels.HysteresisHigher,
		Bands: []levels.Band{
			{Level: "EXHAUSTED", Max: levels.Num(12)},
			{Level: "TIGHT", Min: levels.Num(12), Max: levels.Num(25)},
			{Level: "OK", Min: levels.Num(25), Max: levels.Num(40)},
			{Level: "PLENTY", Min: levels.Num(40)},
		},
	}
}

func (f *fake) Bands() []levels.Info {
	return []levels.Info{f.table().Info(), {Resource: "beta", Unit: ""}}
}

func (f *fake) Configure(cfg Config) error {
	f.cfg = cfg
	if f.cfgErr != nil {
		return f.cfgErr
	}
	if f.onlyKey {
		return cfg.CheckKeys("hosts")
	}
	return nil
}

func (f *fake) Measure(_ context.Context, env Env) ([]levels.Observation, error) {
	f.seen = env
	if f.failWith != nil {
		return nil, f.failWith
	}
	o, err := f.table().Observe(f.value)
	if err != nil {
		return nil, err
	}
	out := []levels.Observation{o}
	if !f.omitBeta {
		b := levels.Observation{Resource: "beta", Scale: levels.Binary, Level: "AVAILABLE"}
		if f.badLevel {
			b.Level = "BOGUS"
		}
		out = append(out, b)
	}
	return out, nil
}

type harness struct {
	dir    string
	now    time.Time
	env    map[string]string
	reg    Registry
	f      *fake
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	f := &fake{value: 30}
	h := &harness{dir: t.TempDir(), now: t0, env: map[string]string{}, f: f}
	h.reg = Registry{}
	for _, n := range []string{"host-sensor", "vpn-sensor", "git-auth-sensor", "beads-sensor", "claude-sensor", "aws-sso-sensor"} {
		h.reg[n] = f
	}
	return h
}

func (h *harness) run(args ...string) int {
	h.stdout.Reset()
	h.stderr.Reset()
	return Run(args, h.reg, IO{
		Stdout: &h.stdout, Stderr: &h.stderr,
		Getenv: func(k string) string { return h.env[k] },
		Now:    func() time.Time { return h.now },
	})
}

func (h *harness) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(h.stdout.String()), "\n") {
		if l == "" {
			continue
		}
		if err := schemas.ValidateLevelAnswer([]byte(l)); err != nil {
			t.Fatalf("line %q fails the level-answer schema: %v", l, err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func (h *harness) writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(h.dir, "cfg.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRun_unknownOrMissingSubcommandExits2WithUsage(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{}, {"--raw"}, {"nope"}} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("args %v: exit %d, want 2", args, code)
		}
		if !strings.Contains(h.stderr.String(), "usage: pg-router-sensors") || h.stdout.Len() != 0 {
			t.Errorf("args %v: stderr %q stdout %q", args, h.stderr.String(), h.stdout.String())
		}
	}
	// A subcommand whose packet has not landed is simply not registered.
	delete(h.reg, "claude-sensor")
	if code := h.run("claude-sensor"); code != ExitUsage {
		t.Errorf("unregistered claude-sensor exit %d, want 2", code)
	}
	if strings.Contains(h.stderr.String(), "claude-sensor,") || !strings.Contains(h.stderr.String(), "host-sensor") {
		t.Errorf("usage must list only registered subcommands: %q", h.stderr.String())
	}
	if code := Run([]string{"x"}, nil, IO{Stdout: &h.stdout, Stderr: &h.stderr}); code != ExitUsage {
		t.Errorf("empty registry exit %d, want 2", code)
	}
}

// TestRun_unloadableConfigExits2PerSubcommand: an unreadable file, invalid TOML
// or an invalid key exits 2 with the cause on stderr, for every subcommand.
func TestRun_unloadableConfigExits2PerSubcommand(t *testing.T) {
	for _, sub := range []string{"host-sensor", "vpn-sensor", "git-auth-sensor", "beads-sensor", "claude-sensor", "aws-sso-sensor"} {
		t.Run(sub, func(t *testing.T) {
			h := newHarness(t)
			h.f.onlyKey = true
			state := "--state-dir=" + h.dir
			cases := map[string][]string{
				"unreadable": {sub, "--config", filepath.Join(h.dir, "missing.toml"), state},
				"bad toml":   {sub, "--config", h.writeConfig(t, "this is = = not toml"), state},
				"bad key":    {sub, "--config", h.writeConfig(t, "bogus = 1\n"), state},
				"bad dwell":  {sub, "--config", h.writeConfig(t, "dwell = 5\n"), state},
				"dwell fmt":  {sub, "--config", h.writeConfig(t, "dwell = \"soon\"\n"), state},
				"valid_for":  {sub, "--config", h.writeConfig(t, "valid_for_s = 0\n"), state},
			}
			for name, args := range cases {
				if code := h.run(args...); code != ExitUsage {
					t.Errorf("%s: exit %d, want 2 (stderr %q)", name, code, h.stderr.String())
				}
				if h.stderr.Len() == 0 || h.stdout.Len() != 0 {
					t.Errorf("%s: want a cause on stderr and nothing on stdout; stderr %q", name, h.stderr.String())
				}
			}
			h.f.cfgErr = errors.New("sensor rejects this")
			if code := h.run(sub, state); code != ExitUsage || !strings.Contains(h.stderr.String(), "sensor rejects this") {
				t.Errorf("sensor Configure error: exit %d stderr %q", code, h.stderr.String())
			}
		})
	}
}

func TestRun_flagErrorsExit2(t *testing.T) {
	h := newHarness(t)
	if code := h.run("host-sensor", "--only", "alpha"); code != ExitUsage {
		t.Errorf("--only is not a flag: exit %d, want 2", code)
	}
	if code := h.run("host-sensor", "extra"); code != ExitUsage || !strings.Contains(h.stderr.String(), "unexpected argument") {
		t.Errorf("stray argument: exit %d stderr %q", code, h.stderr.String())
	}
	if code := h.run("host-sensor", "--resource", "zeta", "--state-dir", h.dir); code != ExitUsage || !strings.Contains(h.stderr.String(), "unknown resource") {
		t.Errorf("unknown resource: exit %d stderr %q", code, h.stderr.String())
	}
}

func TestRun_answersEveryResourceOneLineEach(t *testing.T) {
	h := newHarness(t)
	if code := h.run("host-sensor", "--state-dir", h.dir); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	ls := h.lines(t)
	if len(ls) != 2 {
		t.Fatalf("lines = %v", ls)
	}
	// A fresh state starts at the worst level: the herd is not released.
	if ls[0]["resource"] != "alpha" || ls[0]["level"] != "EXHAUSTED" || ls[0]["owner"] != "host-sensor" {
		t.Errorf("alpha = %v", ls[0])
	}
	if ls[1]["resource"] != "beta" || ls[1]["level"] != "UNAVAILABLE" {
		t.Errorf("beta = %v", ls[1])
	}
	if ls[0]["valid_for_s"].(float64) != DefaultValidForS || ls[0]["observed_at"] != "2026-11-11T12:00:00Z" {
		t.Errorf("defaults wrong: %v", ls[0])
	}
	if r := ls[0]["reading"].(map[string]any); r["value"].(float64) != 30 || r["unit"] != "GiB" {
		t.Errorf("reading = %v", r)
	}
	if _, ok := ls[1]["reading"]; ok {
		t.Errorf("beta has no reading: %v", ls[1])
	}
	if _, err := os.Stat(levels.StatePath(h.dir, "host-sensor")); err != nil {
		t.Errorf("state file not written: %v", err)
	}
}

func TestRun_riseIsOneLevelPerDwellAcrossInvocations(t *testing.T) {
	h := newHarness(t)
	h.f.value = 500 // PLENTY, far above any margin
	cfg := h.writeConfig(t, "dwell = \"60s\"\nvalid_for_s = 90\n")
	want := []string{"EXHAUSTED", "EXHAUSTED", "TIGHT", "OK", "PLENTY", "PLENTY"}
	steps := []time.Duration{0, 30, 60, 120, 180, 240}
	for i, step := range steps {
		h.now = t0.Add(step * time.Second)
		if code := h.run("host-sensor", "--state-dir", h.dir, "--config", cfg, "--resource", "alpha"); code != ExitOK {
			t.Fatalf("step %d exit %d", i, code)
		}
		ls := h.lines(t)
		if len(ls) != 1 || ls[0]["level"] != want[i] {
			t.Fatalf("step %d (t+%ds): %v, want %s", i, step, ls, want[i])
		}
		if ls[0]["valid_for_s"].(float64) != 90 {
			t.Fatalf("valid_for_s = %v", ls[0]["valid_for_s"])
		}
	}
	// A fall is immediate.
	h.f.value = 1
	h.now = t0.Add(241 * time.Second)
	h.run("host-sensor", "--state-dir", h.dir, "--config", cfg, "--resource", "alpha")
	if ls := h.lines(t); ls[0]["level"] != "EXHAUSTED" {
		t.Fatalf("fall = %v", ls)
	}
}

func TestRun_lostStateFileDoesNotReleaseTheHerd(t *testing.T) {
	h := newHarness(t)
	h.f.value = 500
	h.run("host-sensor", "--state-dir", h.dir)
	h.now = t0.Add(3 * time.Minute)
	h.run("host-sensor", "--state-dir", h.dir, "--resource", "alpha")
	if ls := h.lines(t); ls[0]["level"] != "TIGHT" {
		t.Fatalf("one rise by now: %v", ls)
	}
	if err := os.WriteFile(levels.StatePath(h.dir, "host-sensor"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.now = t0.Add(4 * time.Minute)
	if code := h.run("host-sensor", "--state-dir", h.dir, "--resource", "alpha"); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if ls := h.lines(t); ls[0]["level"] != "EXHAUSTED" {
		t.Fatalf("a corrupt state file must restart at the worst level: %v", ls)
	}
	if !strings.Contains(h.stderr.String(), "state lost") {
		t.Errorf("stderr = %q, want the lost-state note", h.stderr.String())
	}
}

func TestRun_rawReadsAndWritesNoStateAndAppliesNoDwell(t *testing.T) {
	h := newHarness(t)
	h.f.value = 500
	if code := h.run("host-sensor", "--raw", "--state-dir", h.dir); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	ls := h.lines(t)
	if ls[0]["level"] != "PLENTY" || ls[1]["level"] != "AVAILABLE" {
		t.Fatalf("raw = %v, want the band level at once", ls)
	}
	if !h.f.seen.Raw || h.f.seen.Store == nil {
		t.Errorf("sensor must see Raw and an in-memory store")
	}
	if _, err := os.Stat(levels.StatePath(h.dir, "host-sensor")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("--raw wrote a state file: %v", err)
	}
	// And it ignores an existing state file.
	if err := os.WriteFile(levels.StatePath(h.dir, "host-sensor"), []byte(`{"resources":{"alpha":{"level":"TIGHT","since":"2026-11-11T12:00:00Z"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.run("host-sensor", "--raw", "--state-dir", h.dir)
	if ls := h.lines(t); ls[0]["level"] != "PLENTY" {
		t.Fatalf("raw must ignore state: %v", ls)
	}
}

func TestRun_rawRejectsALevelOutsideItsScale(t *testing.T) {
	h := newHarness(t)
	h.f.badLevel = true
	code := h.run("host-sensor", "--raw")
	if code != ExitOK || len(h.lines(t)) != 1 || !strings.Contains(h.stderr.String(), "not in its scale") {
		t.Fatalf("exit %d lines %v stderr %q", code, h.stdout.String(), h.stderr.String())
	}
}

func TestRun_bandsPrintsTablesAndExits0WithoutMeasuring(t *testing.T) {
	h := newHarness(t)
	h.f.failWith = errors.New("must not measure")
	if code := h.run("host-sensor", "--bands"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	var recs []levels.Info
	for _, l := range strings.Split(strings.TrimSpace(h.stdout.String()), "\n") {
		var r levels.Info
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	if len(recs) != 2 || recs[0].Resource != "alpha" || len(recs[0].Bands) != 4 || recs[1].Bands == nil || len(recs[1].Bands) != 0 {
		t.Fatalf("recs = %+v", recs)
	}
	if !strings.Contains(h.stdout.String(), `"bands":[]`) || !strings.Contains(h.stdout.String(), `"min":null`) {
		t.Errorf("want empty bands and null bounds in %s", h.stdout.String())
	}
	if h.f.seen.Now != (time.Time{}) {
		t.Error("--bands must not measure")
	}
	h.run("host-sensor", "--bands", "--resource", "beta")
	if strings.Count(strings.TrimSpace(h.stdout.String()), "\n") != 0 || !strings.Contains(h.stdout.String(), "beta") {
		t.Errorf("--resource must limit --bands: %s", h.stdout.String())
	}
}

func TestRun_resourceFlagLimitsTheAnswer(t *testing.T) {
	h := newHarness(t)
	if code := h.run("host-sensor", "--state-dir", h.dir, "--resource", "beta"); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	ls := h.lines(t)
	if len(ls) != 1 || ls[0]["resource"] != "beta" || h.f.seen.Resource != "beta" {
		t.Fatalf("lines %v seen %q", ls, h.f.seen.Resource)
	}
}

func TestRun_exit3WhenNothingCouldBeMeasured(t *testing.T) {
	h := newHarness(t)
	h.f.failWith = errors.New("probe exploded")
	if code := h.run("host-sensor", "--state-dir", h.dir); code != 3 || !strings.Contains(h.stderr.String(), "probe exploded") || h.stdout.Len() != 0 {
		t.Fatalf("exit %d stderr %q", code, h.stderr.String())
	}
	h.f.failWith = nil
	h.f.omitBeta = true
	if code := h.run("host-sensor", "--state-dir", h.dir, "--resource", "beta"); code != 3 || !strings.Contains(h.stderr.String(), "no resource could be answered") {
		t.Fatalf("filtered-out answer: exit %d stderr %q", code, h.stderr.String())
	}
	// One resource unanswered is still exit 0.
	if code := h.run("host-sensor", "--state-dir", h.dir); code != ExitOK || len(h.lines(t)) != 1 {
		t.Fatalf("partial answer: exit %d", code)
	}
}

func TestRun_exit3WhenAnswerIsRejectedBySchema(t *testing.T) {
	h := newHarness(t)
	h.reg["host-sensor"] = &badName{}
	if code := h.run("host-sensor", "--state-dir", h.dir); code != 3 || !strings.Contains(h.stderr.String(), "Bad Name") {
		t.Fatalf("exit %d stderr %q", code, h.stderr.String())
	}
}

type badName struct{ fake }

func (*badName) Resources() []string { return []string{"Bad Name"} }
func (*badName) Measure(context.Context, Env) ([]levels.Observation, error) {
	return []levels.Observation{{Resource: "Bad Name", Scale: levels.Binary, Level: "AVAILABLE"}}, nil
}

func TestRun_refreshEnvironmentReachesTheSensor(t *testing.T) {
	h := newHarness(t)
	h.run("host-sensor", "--state-dir", h.dir)
	if h.f.seen.Refresh {
		t.Error("Refresh must be false without PG_ROUTER_LEVEL_REFRESH=1")
	}
	h.env["PG_ROUTER_LEVEL_REFRESH"] = "1"
	h.run("host-sensor", "--state-dir", h.dir)
	if !h.f.seen.Refresh {
		t.Error("Refresh must be true with PG_ROUTER_LEVEL_REFRESH=1")
	}
}

func TestRun_defaultStateDir(t *testing.T) {
	h := newHarness(t)
	h.env["XDG_STATE_HOME"] = h.dir
	if code := h.run("host-sensor"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if _, err := os.Stat(filepath.Join(h.dir, "pg-router-sensors", "host-sensor.state.json")); err != nil {
		t.Errorf("XDG_STATE_HOME not honoured: %v", err)
	}
	delete(h.env, "XDG_STATE_HOME")
	h.env["HOME"] = h.dir
	if got := defaultStateDir(func(k string) string { return h.env[k] }); got != filepath.Join(h.dir, ".local", "state", "pg-router-sensors") {
		t.Errorf("HOME fallback = %s", got)
	}
	if got := defaultStateDir(func(string) string { return "" }); !strings.HasSuffix(got, filepath.Join(".local", "state", "pg-router-sensors")) {
		t.Errorf("no HOME fallback = %s", got)
	}
}

func TestRun_unwritableStateDirIsReportedNotFatal(t *testing.T) {
	h := newHarness(t)
	blocker := filepath.Join(h.dir, "file")
	_ = os.WriteFile(blocker, []byte("x"), 0o600)
	if code := h.run("host-sensor", "--state-dir", filepath.Join(blocker, "sub")); code != ExitOK || !strings.Contains(h.stderr.String(), "state not saved") {
		t.Fatalf("exit %d stderr %q", code, h.stderr.String())
	}
}

func TestRun_nilIOUsesProcessDefaults(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{}, Registry{}, IO{Stdout: &out, Stderr: &errOut}); code != ExitUsage {
		t.Fatalf("exit %d", code)
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestPrintBands_writeErrorExits3(t *testing.T) {
	var e bytes.Buffer
	if code := printBands(errWriter{}, &e, &fake{}, ""); code != 3 || !strings.Contains(e.String(), "closed") {
		t.Fatalf("exit %d stderr %q", code, e.String())
	}
}

func TestConfig_helpers(t *testing.T) {
	h := newHarness(t)
	path := h.writeConfig(t, `
dwell = "2m"
valid_for_s = 300
name = "x"
wait = "5s"
hosts = ["a", "b"]
[trackers]
one = "db1"
`)
	cfg, dwell, valid, err := loadConfig(path)
	if err != nil || dwell != 2*time.Minute || valid != 300 {
		t.Fatalf("loadConfig = %v %v %v", dwell, valid, err)
	}
	if _, has := cfg.Raw["dwell"]; has {
		t.Error("common keys must be removed from the sensor's table")
	}
	if s, err := cfg.String("name", "d"); err != nil || s != "x" {
		t.Errorf("String = %q %v", s, err)
	}
	if s, _ := cfg.String("absent", "d"); s != "d" {
		t.Errorf("String default = %q", s)
	}
	if _, err := cfg.String("hosts", ""); err == nil {
		t.Error("String of a list must fail")
	}
	if d, err := cfg.Duration("wait", time.Second); err != nil || d != 5*time.Second {
		t.Errorf("Duration = %v %v", d, err)
	}
	if d, _ := cfg.Duration("absent", time.Second); d != time.Second {
		t.Errorf("Duration default = %v", d)
	}
	if _, err := cfg.Duration("name", 0); err == nil {
		t.Error("Duration of a non-duration string must fail")
	}
	if _, err := cfg.Duration("hosts", 0); err == nil {
		t.Error("Duration of a list must fail")
	}
	if l, err := cfg.StringList("hosts"); err != nil || len(l) != 2 {
		t.Errorf("StringList = %v %v", l, err)
	}
	if l, err := cfg.StringList("absent"); err != nil || l != nil {
		t.Errorf("StringList absent = %v %v", l, err)
	}
	if _, err := cfg.StringList("name"); err == nil {
		t.Error("StringList of a string must fail")
	}
	mixed := Config{Raw: map[string]any{"m": []any{"a", int64(1)}}}
	if _, err := mixed.StringList("m"); err == nil {
		t.Error("StringList with a non-string element must fail")
	}
	if tb, err := cfg.Table("trackers"); err != nil || tb["one"] != "db1" {
		t.Errorf("Table = %v %v", tb, err)
	}
	if tb, err := cfg.Table("absent"); err != nil || tb != nil {
		t.Errorf("Table absent = %v %v", tb, err)
	}
	if _, err := cfg.Table("name"); err == nil {
		t.Error("Table of a string must fail")
	}
	badTable := Config{Raw: map[string]any{"t": map[string]any{"k": int64(1)}}}
	if _, err := badTable.Table("t"); err == nil {
		t.Error("Table with a non-string value must fail")
	}
	if err := cfg.CheckKeys("name", "wait", "hosts", "trackers"); err != nil {
		t.Errorf("CheckKeys = %v", err)
	}
	if err := cfg.CheckKeys("name"); err == nil || !strings.Contains(err.Error(), "hosts, trackers, wait") {
		t.Errorf("CheckKeys must name the invalid keys sorted: %v", err)
	}
}

func TestLoadConfig_absentPathMeansDefaults(t *testing.T) {
	cfg, dwell, valid, err := loadConfig("")
	if err != nil || dwell != DefaultDwell || valid != DefaultValidForS || len(cfg.Raw) != 0 {
		t.Fatalf("defaults = %v %v %v %v", cfg, dwell, valid, err)
	}
	for _, body := range []string{"dwell = \"-5s\"\n", "valid_for_s = 100000\n", "valid_for_s = \"x\"\n"} {
		p := filepath.Join(t.TempDir(), "c.toml")
		_ = os.WriteFile(p, []byte(body), 0o600)
		if _, _, _, err := loadConfig(p); err == nil {
			t.Errorf("%q must be rejected", body)
		}
	}
}
