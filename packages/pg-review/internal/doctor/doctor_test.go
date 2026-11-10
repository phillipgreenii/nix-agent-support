package doctor

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ccpool"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
)

type fakeLedger struct{ err error }

func (f fakeLedger) IntegrityCheck(context.Context) error { return f.err }
func (fakeLedger) Close() error                           { return nil }

type fakeCcpool struct {
	cap       ccpool.Capacity
	capErr    error
	startErr  error
	started   []ccpool.StartOpts
	closed    []string
	reply     string
	pending   int // Result polls answering pending first
	polls     int
	resultErr error
}

func (f *fakeCcpool) Capacity(context.Context) (ccpool.Capacity, error) { return f.cap, f.capErr }
func (f *fakeCcpool) Start(_ context.Context, o ccpool.StartOpts) error {
	f.started = append(f.started, o)
	return f.startErr
}
func (f *fakeCcpool) Reply(context.Context, string, string) (string, error) { return "t1", nil }
func (f *fakeCcpool) Result(context.Context, string) (string, ccpool.ResultStatus, error) {
	f.polls++
	if f.resultErr != nil {
		return "", ccpool.ResultUnknown, f.resultErr
	}
	if f.polls <= f.pending {
		return "", ccpool.ResultPending, nil
	}
	return f.reply, ccpool.ResultDone, nil
}
func (f *fakeCcpool) State(context.Context, string) (string, error) { return "idle", nil }
func (f *fakeCcpool) Cancel(context.Context, string) error          { return nil }
func (f *fakeCcpool) Close(_ context.Context, id, _ string) error {
	f.closed = append(f.closed, id)
	return nil
}

// harness wires a Deps in which every check passes; tests break one thing.
type harness struct {
	d       *Deps
	cp      *fakeCcpool
	missing map[string]bool
	exit    map[string]int // by base command name
	out     map[string]string
	called  []string
	now     time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	// Config validation resolves provider commands on the real PATH, so give
	// it stub executables.
	bin := t.TempDir()
	for _, s := range []string{"pg-review-provider-ccaudit", "pg-review-provider-hook-decisions", "pg-review-provider-config-inventory"} {
		if err := os.WriteFile(bin+"/"+s, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	cfg, err := config.Load(config.Env{ConfigHome: root + "/c", StateHome: root + "/s", CacheHome: root + "/k"}, builtin.FS())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		cp:      &fakeCcpool{cap: ccpool.Capacity{MaxSessions: 2, Free: 1}},
		missing: map[string]bool{},
		exit:    map[string]int{},
		out: map[string]string{
			"pg-ccaudit":                    `{"files_indexed": 12}`,
			"claude-extended-tool-approver": `{"hookSpecificOutput":{"permissionDecision":"allow"}}`,
		},
		now: time.Unix(1_700_000_000, 0),
	}
	h.d = &Deps{
		Config: cfg,
		LookPath: func(n string) (string, error) {
			if h.missing[n] {
				return "", errors.New("not found")
			}
			return "/bin/" + n, nil
		},
		Exec: func(_ context.Context, name string, args []string, _ string, _ []string, _ string) (string, int, error) {
			base := name[strings.LastIndex(name, "/")+1:]
			h.called = append(h.called, base+" "+strings.Join(args, " "))
			return h.out[base], h.exit[base], nil
		},
		OpenLedger: func(context.Context) (IntegrityLedger, error) { return fakeLedger{}, nil },
		Ccpool:     h.cp,
		Now:        func() time.Time { return h.now },
		Sleep:      func(_ context.Context, d time.Duration) { h.now = h.now.Add(d) },
	}
	h.cp.reply = "" // set per test from the token
	return h
}

// answer makes the fake session reply with the token the check generated.
func (h *harness) answer(t *testing.T, includeRun, includeBin bool) {
	t.Helper()
	h.d.Ccpool = &replyingCcpool{fakeCcpool: h.cp, run: includeRun, bin: includeBin}
}

// replyingCcpool builds its reply from the PG_REVIEW_RUN it was started with.
type replyingCcpool struct {
	*fakeCcpool
	run, bin bool
}

func (r *replyingCcpool) Result(ctx context.Context, id string) (string, ccpool.ResultStatus, error) {
	_, st, err := r.fakeCcpool.Result(ctx, id)
	if st != ccpool.ResultDone {
		return "", st, err
	}
	var lines []string
	if r.run {
		for _, kv := range r.started[0].Env {
			if v, ok := strings.CutPrefix(kv, "PG_REVIEW_RUN="); ok {
				lines = append(lines, v)
			}
		}
	}
	if r.bin {
		lines = append(lines, "/run/current-system/sw/bin/pg-review")
	}
	return strings.Join(lines, "\n"), st, nil
}

func byName(rs []Result, name string) Result {
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	return Result{}
}

func TestAllPassExitsZero(t *testing.T) {
	h := newHarness(t)
	h.answer(t, true, true)
	rs := Run(context.Background(), h.d)
	if len(rs) != 7 {
		t.Fatalf("want 7 checks, got %d", len(rs))
	}
	for _, r := range rs {
		if r.Status != Pass {
			t.Errorf("%s", r)
		}
	}
	if c := ExitCode(rs); c != exitcode.OK {
		t.Errorf("exit %d", c)
	}
	if got := rs[0].String(); got != "PASS config" {
		t.Errorf("line %q", got)
	}
}

func TestEachCheckFailingAlone(t *testing.T) {
	cases := []struct {
		name   string
		check  string
		code   int
		want   string
		break_ func(h *harness)
	}{
		{"config load", CheckConfig, exitcode.ValidationRejected, "boom", func(h *harness) { h.d.Config, h.d.ConfigErr = nil, errors.New("boom") }},
		{"config problem", CheckConfig, exitcode.ValidationRejected, "problem", func(h *harness) { h.d.Config.Ccpool.ReadTools = nil; h.d.Config.Profiles[0].Effort = "bogus" }},
		{"ledger open", CheckLedger, exitcode.Ledger, "open: nope", func(h *harness) {
			h.d.OpenLedger = func(context.Context) (IntegrityLedger, error) { return nil, errors.New("nope") }
		}},
		{"ledger integrity", CheckLedger, exitcode.Ledger, "corrupt", func(h *harness) {
			h.d.OpenLedger = func(context.Context) (IntegrityLedger, error) { return fakeLedger{errors.New("corrupt")}, nil }
		}},
		{"provider absent", CheckProviders, exitcode.Provider, "pg-review-provider-hook-decisions", func(h *harness) { h.missing["pg-review-provider-hook-decisions"] = true }},
		{"provider help fails", CheckProviders, exitcode.Provider, "--help exited 3", func(h *harness) { h.exit["pg-review-provider-ccaudit"] = 3 }},
		{"recorder status fails", CheckRecorder, exitcode.Provider, "exited 1", func(h *harness) { h.exit["pg-ccaudit"] = 1 }},
		{"recorder index empty", CheckRecorder, exitcode.Provider, "no data", func(h *harness) { h.out["pg-ccaudit"] = `{"files_indexed": 0}` }},
		{"recorder missing", CheckRecorder, exitcode.Provider, "not found", func(h *harness) { h.missing["pg-ccaudit"] = true }},
		{"ccpool absent", CheckCcpool, exitcode.Provider, "ccpool not found", func(h *harness) { h.missing["ccpool"] = true }},
		{"hook asks", CheckHook, exitcode.Provider, "got ask", func(h *harness) {
			h.out["claude-extended-tool-approver"] = `{"hookSpecificOutput":{"permissionDecision":"ask"}}`
		}},
		{"hook abstains", CheckHook, exitcode.Provider, "no decision", func(h *harness) { h.out["claude-extended-tool-approver"] = `{}` }},
		{"session cannot start", CheckSession, exitcode.Provider, "ccpool new", func(h *harness) { h.cp.startErr = errors.New("x") }},
		{"session usage limit", CheckSession, exitcode.Provider, "usage limit", func(h *harness) { h.cp.cap.UsageLimit = &ccpool.UsageLimit{Window: "5h"} }},
		{"session no slot", CheckSession, exitcode.Provider, "no free slot", func(h *harness) { h.cp.cap.Free = 0 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.answer(t, true, true)
			c.break_(h)
			rs := Run(context.Background(), h.d)
			for _, r := range rs {
				if r.Name == c.check {
					if r.Status != Fail || r.Code != c.code || !strings.Contains(r.Reason, c.want) {
						t.Errorf("%s: %+v", c.check, r)
					}
				} else if r.Status == Fail && c.check != CheckConfig {
					t.Errorf("unrelated check failed: %+v", r)
				}
			}
			if ExitCode(rs) != c.code {
				t.Errorf("exit %d want %d", ExitCode(rs), c.code)
			}
		})
	}
}

func TestSessionNotVisible(t *testing.T) {
	for _, c := range []struct {
		name     string
		run, bin bool
		want     string
	}{{"no run var", false, true, "PG_REVIEW_RUN"}, {"no binary", true, false, "pg-review"}} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.answer(t, c.run, c.bin)
			r := byName(Run(context.Background(), h.d), CheckSession)
			if r.Status != Fail || !strings.Contains(r.Reason, "not visible") || !strings.Contains(r.Reason, c.want) {
				t.Errorf("%+v", r)
			}
		})
	}
}

func TestSessionTimesOutAndIsClosed(t *testing.T) {
	h := newHarness(t)
	h.cp.pending = 1 << 30
	r := byName(Run(context.Background(), h.d), CheckSession)
	if r.Status != Fail || !strings.Contains(r.Reason, "no reply") {
		t.Errorf("%+v", r)
	}
	if len(h.cp.closed) != 1 {
		t.Errorf("session not closed: %v", h.cp.closed)
	}
}

func TestSessionWaitsForPendingReply(t *testing.T) {
	h := newHarness(t)
	h.cp.pending = 3
	h.answer(t, true, true)
	if r := byName(Run(context.Background(), h.d), CheckSession); r.Status != Pass {
		t.Errorf("%+v", r)
	}
}

func TestSessionStartedLikeTheRunner(t *testing.T) {
	h := newHarness(t)
	h.answer(t, true, true)
	Run(context.Background(), h.d)
	o := h.cp.started[0]
	if o.PermissionMode != "dontAsk" || o.Cwd != h.d.Config.Ccpool.NeutralDir {
		t.Errorf("%+v", o)
	}
	joined := strings.Join(o.Env, " ")
	for _, want := range []string{"PG_REVIEW_RUN=", "XDG_CONFIG_HOME=" + h.d.Config.Env.ConfigHome} {
		if !strings.Contains(joined, want) {
			t.Errorf("env %q lacks %q", joined, want)
		}
	}
	if len(h.cp.closed) != 1 {
		t.Errorf("closed %v", h.cp.closed)
	}
}

func TestSkipSessionStartsNothingAndKeepsExitZero(t *testing.T) {
	h := newHarness(t)
	h.d.SkipSession = true
	rs := Run(context.Background(), h.d)
	r := byName(rs, CheckSession)
	if r.Status != Skip || r.String() != "SKIP session" {
		t.Errorf("%+v", r)
	}
	if len(h.cp.started) != 0 {
		t.Error("a session was started")
	}
	if ExitCode(rs) != exitcode.OK {
		t.Errorf("exit %d", ExitCode(rs))
	}
}

func TestHighestCodeWins(t *testing.T) {
	h := newHarness(t)
	h.d.SkipSession = true
	h.d.Config, h.d.ConfigErr = nil, errors.New("bad")
	h.d.OpenLedger = func(context.Context) (IntegrityLedger, error) { return nil, errors.New("bad") }
	h.missing["ccpool"] = true
	if c := ExitCode(Run(context.Background(), h.d)); c != exitcode.Provider {
		t.Errorf("exit %d", c)
	}
}

func TestRecorderNeverCallsCoverageQuery(t *testing.T) {
	h := newHarness(t)
	h.answer(t, true, true)
	Run(context.Background(), h.d)
	for _, c := range h.called {
		if strings.Contains(c, "coverage") || strings.Contains(c, " query") {
			t.Errorf("health check ran %q", c)
		}
	}
	found := false
	for _, c := range h.called {
		if c == "pg-ccaudit status --json" {
			found = true
		}
	}
	if !found {
		t.Errorf("recorder status not run: %v", h.called)
	}
}

func TestBuiltInProvidersProbedWithoutConfigToml(t *testing.T) {
	h := newHarness(t)
	h.d.SkipSession = true
	if h.d.Config.ConfigPresent {
		t.Fatal("test needs no config.toml")
	}
	Run(context.Background(), h.d)
	for _, p := range []string{"pg-review-provider-ccaudit", "pg-review-provider-hook-decisions", "pg-review-provider-config-inventory"} {
		want := p + " --help"
		ok := false
		for _, c := range h.called {
			ok = ok || c == want
		}
		if !ok {
			t.Errorf("%s not probed with --help: %v", p, h.called)
		}
	}
}

func TestHookCheckSendsSampleCommand(t *testing.T) {
	h := newHarness(t)
	var stdin string
	prev := h.d.Exec
	h.d.Exec = func(ctx context.Context, name string, args []string, in string, env []string, dir string) (string, int, error) {
		if strings.HasSuffix(name, "claude-extended-tool-approver") {
			stdin = in
		}
		return prev(ctx, name, args, in, env, dir)
	}
	checkHook(context.Background(), h.d)
	if !strings.Contains(stdin, HookSample) || !strings.Contains(stdin, `"PreToolUse"`) {
		t.Errorf("stdin %q", stdin)
	}
}

func TestSessionEmptyReplyIsDiagnosed(t *testing.T) {
	h := newHarness(t)
	h.cp.reply = ""
	r := byName(Run(context.Background(), h.d), CheckSession)
	if r.Status != Fail || !strings.Contains(r.Reason, "empty reply") || !strings.Contains(r.Reason, "session state idle") {
		t.Errorf("%+v", r)
	}
}

func TestSessionAllowsProbeCommandsInBothForms(t *testing.T) {
	h := newHarness(t)
	h.answer(t, true, true)
	Run(context.Background(), h.d)
	got := strings.Join(h.cp.started[0].AllowedTools, ",")
	for _, want := range []string{"Bash(printenv PG_REVIEW_RUN)", "Bash(printenv PG_REVIEW_RUN:*)", "Bash(command -v pg-review)", "Bash(command -v pg-review:*)"} {
		if !strings.Contains(got, want) {
			t.Errorf("allowed tools %q lack %q", got, want)
		}
	}
}
