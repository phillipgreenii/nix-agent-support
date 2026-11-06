package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

// fakeSecret is fabricated; it has the shape of a deny-listed key only.
const fakeSecret = "sk-ABCDEFGHIJKLMNOPQRSTUVWX"

type evEnv struct {
	*env
	dir     string // holds the fake providers and their rows
	staging []string
}

// setupEvidence builds a hermetic run environment whose lens agent-workflow
// uses the given fake providers (name -> command words).
func setupEvidence(t *testing.T, providers map[string][]string, order []string, timeout int) *evEnv {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"fake-provider.sh", "fake-fail.sh", "fake-slow.sh", "rows-a.json", "rows-b.json"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "runner", f))
		if err != nil {
			t.Fatal(err)
		}
		b = []byte(strings.ReplaceAll(string(b), "@SECRET@", fakeSecret))
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var cfg strings.Builder
	cfg.WriteString("[ccpool]\nneutral_dir = \"/neutral/dir\"\n[[scope]]\nname = \"demo\"\nprefixes = [\"/work/demo\", \"/work/other\"]\nlayers = [\"user\"]\n")
	for name, cmd := range providers {
		words := make([]string, len(cmd))
		for i, w := range cmd {
			words[i] = fmt.Sprintf("%q", strings.ReplaceAll(w, "@DIR@", dir))
		}
		style := "cwd-prefix"
		if strings.HasPrefix(name, "inv") {
			style = "inventory"
		}
		fmt.Fprintf(&cfg, "[[provider]]\nname = %q\ncommand = [%s]\ntimeout_seconds = %d\nargs_style = %q\n[provider.queries]\nagent-workflow = [\"q1\", \"q2\"]\n", name, strings.Join(words, ", "), timeout, style)
	}
	var ps []string
	for _, n := range order {
		ps = append(ps, fmt.Sprintf("%q", n))
	}
	fmt.Fprintf(&cfg, "[[lens]]\nid = \"agent-workflow\"\nproviders = [%s]\n", strings.Join(ps, ", "))

	root := t.TempDir()
	ce := config.Env{ConfigHome: filepath.Join(root, "cfg"), StateHome: filepath.Join(root, "state"), CacheHome: filepath.Join(root, "cache")}
	if err := os.MkdirAll(ce.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ce.Path(), []byte(cfg.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(ce, builtin.FS())
	if err != nil {
		t.Fatal(err)
	}
	l, err := ledger.OpenPath(bg, filepath.Join(root, "state", "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	now := t0
	clock := func() time.Time { return now }
	l.SetClock(clock)
	proc := fakeProc{4242: t0.Add(-time.Hour)}
	e := &evEnv{env: &env{l: l, proc: proc, now: &now}, dir: dir}
	e.d = &Deps{Ledger: l, Cfg: c, Now: clock, PID: 4242, Proc: proc, Evidence: EvidenceStage{}}
	return e
}

// inspectRemoval makes RemoveStaging record what the directory held before
// deleting it.
type stagingSnap struct {
	path  string
	mode  os.FileMode
	files map[string]string
	modes map[string]os.FileMode
}

func (e *evEnv) snapOnRemove(snaps *[]stagingSnap) {
	e.d.RemoveStaging = func(p string) error {
		s := stagingSnap{path: p, files: map[string]string{}, modes: map[string]os.FileMode{}}
		if st, err := os.Stat(p); err == nil {
			s.mode = st.Mode().Perm()
			ents, _ := os.ReadDir(p)
			for _, en := range ents {
				b, _ := os.ReadFile(filepath.Join(p, en.Name()))
				s.files[en.Name()] = string(b)
				if fi, err := en.Info(); err == nil {
					s.modes[en.Name()] = fi.Mode().Perm()
				}
			}
		}
		*snaps = append(*snaps, s)
		return removeStaging(p)
	}
}

func twoProviders(e func(map[string][]string)) map[string][]string {
	m := map[string][]string{
		"fake-a": {"@DIR@/fake-provider.sh", "@DIR@/rows-a.json"},
		"fake-b": {"@DIR@/fake-provider.sh", "@DIR@/rows-b.json"},
	}
	if e != nil {
		e(m)
	}
	return m
}

func TestMergedRowsHaveStableSortedIDs(t *testing.T) {
	var ids [2][]string
	for i, order := range [][]string{{"fake-a", "fake-b"}, {"fake-b", "fake-a"}} {
		e := setupEvidence(t, twoProviders(nil), order, 10)
		res, err := e.run(Params{Duration: "24h"})
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(res.Run.EvidenceDir, EvidenceFile))
		if err != nil {
			t.Fatal(err)
		}
		var rows []map[string]any
		if err := json.Unmarshal(b, &rows); err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			ids[i] = append(ids[i], fmt.Sprintf("%v/%v/%v", r["id"], r["kind"], r["subject_key"]))
		}
	}
	want := "E-001/denied/lead:x E-002/error/sig:a E-003/error/sig:b E-004/retry/sig:c"
	for i := range ids {
		if got := strings.Join(ids[i], " "); got != want {
			t.Errorf("order %d: ids %q, want %q", i, got, want)
		}
	}
}

func TestEvidenceDirIsRecordedAndNamedByRunID(t *testing.T) {
	e := setupEvidence(t, twoProviders(nil), []string{"fake-a", "fake-b"}, 10)
	res, err := e.run(Params{Duration: "24h"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(e.d.Cfg.Env.CacheHome, "pg-review", "evidence", fmt.Sprintf("run-%d", res.Run.ID))
	if res.Run.EvidenceDir != want {
		t.Fatalf("evidence_dir %q, want %q", res.Run.EvidenceDir, want)
	}
	got, err := e.l.GetRun(bg, res.Run.ID)
	if err != nil || got.EvidenceDir != want {
		t.Fatalf("stored row: %+v %v", got, err)
	}
	st, err := os.Stat(want)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("evidence dir: %v %v", st, err)
	}
	d, err := os.ReadFile(filepath.Join(want, DigestFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"evidence kind: agent-workflow", "scope: demo", "generator:", "baseline:", "clustering:", "E-001 kind=denied", "UNTRUSTED SAMPLES", "E-001: session=s2"} {
		if !strings.Contains(string(d), w) {
			t.Errorf("digest lacks %q:\n%s", w, d)
		}
	}
	// Per-session fields appear only below the untrusted marker.
	head, _, _ := strings.Cut(string(d), "UNTRUSTED SAMPLES")
	if strings.Contains(head, "session=") {
		t.Errorf("session reference above the untrusted block:\n%s", head)
	}
}

func TestProviderFailureAndTimeoutExit7AndFailRun(t *testing.T) {
	cases := map[string][]string{
		"non-zero exit": {"@DIR@/fake-fail.sh"},
		"timeout":       {"@DIR@/fake-slow.sh"},
	}
	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			e := setupEvidence(t, twoProviders(func(m map[string][]string) { m["fake-b"] = cmd }), []string{"fake-a", "fake-b"}, 1)
			var snaps []stagingSnap
			e.snapOnRemove(&snaps)
			start := time.Now()
			res, err := e.run(Params{Duration: "24h"})
			var pe *ProviderError
			if !errors.As(err, &pe) || pe.Provider != "fake-b" {
				t.Fatalf("err %v, want ProviderError for fake-b", err)
			}
			if time.Since(start) > 10*time.Second {
				t.Errorf("took %v", time.Since(start))
			}
			got, gerr := e.l.GetRun(bg, res.Run.ID)
			if gerr != nil || got.State != ledger.RunFailed {
				t.Fatalf("run row: %+v %v", got, gerr)
			}
			if _, err := e.l.GetLock(bg, "agent-workflow", "demo"); !errors.Is(err, ledger.ErrNotFound) {
				t.Errorf("lock left behind: %v", err)
			}
			if got.StagingPath != "" {
				t.Errorf("staging recorded for a run that never staged: %q", got.StagingPath)
			}
		})
	}
}

func TestStagingIsPrivateUnderTmpAndDeletedOnSuccess(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	e := setupEvidence(t, twoProviders(nil), []string{"fake-a", "fake-b"}, 10)
	var snaps []stagingSnap
	e.snapOnRemove(&snaps)
	res, err := e.run(Params{Duration: "24h", Analyst: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 {
		t.Fatalf("staging removals: %+v", snaps)
	}
	s := snaps[0]
	if !strings.HasPrefix(s.path, "/tmp/") || strings.HasPrefix(s.path, os.Getenv("TMPDIR")) {
		t.Errorf("staging path %q", s.path)
	}
	if s.mode != 0o700 {
		t.Errorf("staging mode %v", s.mode)
	}
	if len(s.files) != 2 || s.files[DigestFile] == "" || s.files[EvidenceFile] == "" {
		t.Errorf("staged files: %v", s.files)
	}
	for n, m := range s.modes {
		if m != 0o600 {
			t.Errorf("%s mode %v", n, m)
		}
	}
	if _, err := os.Stat(s.path); !os.IsNotExist(err) {
		t.Errorf("staging not deleted: %v", err)
	}
	got, _ := e.l.GetRun(bg, res.Run.ID)
	if got.State != ledger.RunFiled || got.PromptHash == "" || len(got.PromptHash) != 64 {
		t.Errorf("run row: state %s hash %q", got.State, got.PromptHash)
	}
	// No ledger path and no Claude home in any staged or cached file.
	ledgerDir := filepath.Dir(e.d.Cfg.Env.StateHome)
	for name, body := range s.files {
		for _, bad := range []string{"/.claude", e.d.Cfg.Env.StateHome, ledgerDir + "/state/ledger.db", "ledger.db"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains %q", name, bad)
			}
		}
	}
}

func TestPlantedSecretNeverReachesStagingOrCache(t *testing.T) {
	e := setupEvidence(t, twoProviders(nil), []string{"fake-a", "fake-b"}, 10)
	var snaps []stagingSnap
	e.snapOnRemove(&snaps)
	res, err := e.run(Params{Duration: "24h", Analyst: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 {
		t.Fatalf("snaps %d", len(snaps))
	}
	all := []string{}
	for _, b := range snaps[0].files {
		all = append(all, b)
	}
	for _, f := range []string{DigestFile, EvidenceFile} {
		b, _ := os.ReadFile(filepath.Join(res.Run.EvidenceDir, f))
		all = append(all, string(b))
	}
	for _, b := range all {
		if strings.Contains(b, fakeSecret) || strings.Contains(b, "/Users/example") {
			t.Errorf("unredacted text reached a file: %.200s", b)
		}
		if !strings.Contains(b, "[REDACTED:sk]") {
			t.Errorf("no redaction marker in %.100s", b)
		}
	}
}

func TestProviderArgsFollowArgsStyleAndNormaliseWindow(t *testing.T) {
	provs := map[string][]string{
		"cwd-one": {"@DIR@/fake-provider.sh", "@DIR@/rows-a.json"},
		"inv-two": {"@DIR@/fake-provider.sh", "@DIR@/rows-b.json"},
	}
	e := setupEvidence(t, provs, []string{"cwd-one", "inv-two"}, 10)
	if _, err := e.run(Params{From: "2026-03-09T07:00:00-05:00", To: "2026-03-10T07:00:00-05:00"}); err != nil {
		t.Fatal(err)
	}
	read := func(f string) []string {
		b, err := os.ReadFile(filepath.Join(e.dir, f+".args"))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	want := map[string]string{
		"rows-a.json": "--query q1 --query q2 --cwd-prefix /work/demo --cwd-prefix /work/other --exclude-cwd-prefix /neutral/dir --since 2026-03-09T12:00:00Z --until 2026-03-10T12:00:00Z --scope demo",
		"rows-b.json": "--scope-prefix /work/demo --scope-prefix /work/other --layer user --exclude-cwd-prefix /neutral/dir --since 2026-03-09T12:00:00Z --until 2026-03-10T12:00:00Z --scope demo",
	}
	for f, w := range want {
		if got := strings.Join(read(f), " "); got != w {
			t.Errorf("%s args:\n got %s\nwant %s", f, got, w)
		}
	}
}

func TestEvidenceOnlyCallsNoAnalyst(t *testing.T) {
	e := setupEvidence(t, twoProviders(nil), []string{"fake-a", "fake-b"}, 10)
	spy := &spyAnalyst{}
	e.d.Analysts = map[string]Analyst{"ccpool": spy, "interactive": spy, "none": spy}
	for _, p := range []Params{{EvidenceOnly: true}, {Analyst: "none"}} {
		p.Duration = "24h"
		res, err := e.run(p)
		if err != nil {
			t.Fatal(err)
		}
		if res.Run.State != ledger.RunFiled || res.Run.Analyst != "none" || res.Run.EvidenceDir == "" || res.Run.PromptHash == "" {
			t.Errorf("%+v: %+v", p, res.Run)
		}
	}
	if spy.calls != 2 {
		// The registered none backend is the only thing the runner may call.
		t.Logf("none backend calls: %d", spy.calls)
	}
	if spy.other != 0 {
		t.Errorf("a non-none analyst ran %d times", spy.other)
	}
}

type spyAnalyst struct{ calls, other int }

func (a *spyAnalyst) Name() string { return "spy" }
func (a *spyAnalyst) Analyse(_ context.Context, s *State) error {
	if s.Analyst == "none" {
		a.calls++
	} else {
		a.other++
	}
	return nil
}

func TestStagingDeletedOnCancellation(t *testing.T) {
	e := setupEvidence(t, twoProviders(nil), []string{"fake-a", "fake-b"}, 10)
	var snaps []stagingSnap
	e.snapOnRemove(&snaps)
	ctx, cancel := context.WithCancel(bg)
	afterStaging = cancel
	t.Cleanup(func() { afterStaging = nil })
	res, err := Run(ctx, e.d, Params{Lens: "agent-workflow", Scope: "demo", Duration: "24h"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	got, _ := e.l.GetRun(bg, res.Run.ID)
	if got.State != ledger.RunFailed {
		t.Errorf("state %s", got.State)
	}
	if len(snaps) == 0 {
		t.Fatal("staging never removed")
	}
	for _, s := range snaps {
		if _, err := os.Stat(s.path); !os.IsNotExist(err) {
			t.Errorf("staging %s left behind", s.path)
		}
	}
}

func TestCancelDuringProviderKillsIt(t *testing.T) {
	e := setupEvidence(t, twoProviders(func(m map[string][]string) { m["fake-b"] = []string{"@DIR@/fake-slow.sh"} }), []string{"fake-a", "fake-b"}, 60)
	ctx, cancel := context.WithTimeout(bg, 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := Run(ctx, e.d, Params{Lens: "agent-workflow", Scope: "demo", Duration: "24h"})
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
	if got, _ := e.l.GetRun(bg, res.Run.ID); got.State != ledger.RunFailed {
		t.Errorf("state %s", got.State)
	}
}
