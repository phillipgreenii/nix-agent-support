package originguard

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type cliRig struct {
	d      Deps
	out    *bytes.Buffer
	errb   *bytes.Buffer
	agent  fakeAgent
	sp     *fakeSpawner
	cfgDir string
}

func newCLIRig(t *testing.T, cfgText string) *cliRig {
	t.Helper()
	home := t.TempDir()
	env := Env{Home: home, Getenv: func(k string) string {
		switch k {
		case "XDG_STATE_HOME":
			return filepath.Join(home, "state")
		case "SSH_AUTH_SOCK":
			return "/env.sock"
		}
		return ""
	}}
	cfgPath := filepath.Join(home, "config.toml")
	if cfgText != "" {
		if err := os.WriteFile(cfgPath, []byte(cfgText), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	clock := newClock()
	r := &cliRig{out: &bytes.Buffer{}, errb: &bytes.Buffer{}, sp: &fakeSpawner{}}
	r.agent = fakeAgent{"/env.sock": {{ValidAfter: clock.Now().Add(-time.Hour), ValidBefore: clock.Now().Add(time.Hour)}}}
	r.d = Deps{
		Env: env, Stdout: r.out, Stderr: r.errb, Clock: clock, Dialer: r.agent,
		Spawner: r.sp, Procs: &fakeProcs{}, Timers: &fakeTimers{clock: clock},
		HasTerminal: func() bool { return false }, StderrIsTerminal: func() bool { return false },
		Presence: fakePresence{state: PresencePresent}, ConfigPath: cfgPath,
	}
	return r
}

const cliCfg = "[realm.zr]\nmatch = { host = \"github.com\", user = \"org-*\" }\n"

func (r *cliRig) run(args ...string) int {
	r.out.Reset()
	r.errb.Reset()
	return RunCLI(context.Background(), args, r.d)
}

func TestCLIExitCodes(t *testing.T) {
	r := newCLIRig(t, cliCfg)
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no command", nil, ExitUsage},
		{"unknown command", []string{"bogus"}, ExitUsage},
		{"help", []string{"help"}, ExitOK},
		{"check available", []string{"check", "--realm", "zr"}, ExitOK},
		{"check remote", []string{"check", "--remote", "org-1@github.com:example/repo.git"}, ExitOK},
		{"check unknown realm", []string{"check", "--realm", "nope"}, ExitUsage},
		{"check no selector", []string{"check"}, ExitUsage},
		{"check bad flag", []string{"check", "--zzz"}, ExitUsage},
		{"check no matching remote", []string{"check", "--remote", "git@example.com:a/b.git"}, ExitUsage},
		{"ignore unknown realm", []string{"ignore", "nope"}, ExitUsage},
		{"ignore needs one arg", []string{"ignore"}, ExitUsage},
		{"status", []string{"status"}, ExitOK},
		{"proxy without command", []string{"proxy", "--realm", "zr"}, ExitUsage},
		{"proxy without realm", []string{"proxy", "--", "x"}, ExitUsage},
	}
	for _, c := range cases {
		if got := r.run(c.args...); got != c.want {
			t.Errorf("%s: exit = %d, want %d (stderr %q)", c.name, got, c.want, r.errb.String())
		}
	}
}

func TestCLICheckUnavailableIs75(t *testing.T) {
	r := newCLIRig(t, cliCfg)
	r.agent["/env.sock"] = nil
	if got := r.run("check", "--realm", "zr"); got != ExitUnavailable {
		t.Fatalf("exit = %d", got)
	}
	if !strings.Contains(r.out.String(), "unavailable realm=zr reason=cert-missing") {
		t.Fatalf("out = %q", r.out.String())
	}
	if got := r.run("check", "--realm", "zr", "--json"); got != ExitUnavailable {
		t.Fatalf("json exit = %d", got)
	}
	var res resultJSON
	if err := json.Unmarshal(r.out.Bytes(), &res); err != nil || res.Available || res.Reason != ReasonCertMissing {
		t.Fatalf("json = %q err %v", r.out.String(), err)
	}
}

func TestCLIConfigErrorIs78(t *testing.T) {
	r := newCLIRig(t, "[realm.zr\n")
	if got := r.run("check", "--realm", "zr"); got != ExitConfig {
		t.Fatalf("exit = %d", got)
	}
}

func TestCLIIgnoreAndUnignore(t *testing.T) {
	r := newCLIRig(t, cliCfg)
	r.agent["/env.sock"] = nil
	if got := r.run("ignore", "zr"); got != ExitOK {
		t.Fatalf("ignore exit = %d", got)
	}
	if got := r.run("check", "--realm", "zr"); got != ExitOK || !strings.Contains(r.out.String(), "reason=ignored") {
		t.Fatalf("ignored realm is available: exit %d out %q", got, r.out.String())
	}
	if got := r.run("status", "--json"); got != ExitOK || !strings.Contains(r.out.String(), `"ignored":true`) {
		t.Fatalf("status out %q", r.out.String())
	}
	if got := r.run("unignore", "zr"); got != ExitOK {
		t.Fatal("unignore")
	}
	if got := r.run("check", "--realm", "zr"); got != ExitUnavailable {
		t.Fatalf("exit after unignore = %d", got)
	}
}

func TestCLIProxyConfigFaultFailsOpen(t *testing.T) {
	r := newCLIRig(t, "[realm.zr\n")
	r.sp.script = func(c *fakeChild) { c.exit(3) }
	if got := r.run("proxy", "--realm", "zr", "--", "step", "ssh"); got != 3 {
		t.Fatalf("exit = %d, want the command's own status", got)
	}
	if r.sp.count() != 1 {
		t.Fatal("the command must run unchanged")
	}
	b, _ := filepath.Glob(filepath.Join(r.d.Env.StateDir(), "events-*.jsonl"))
	if len(b) != 1 {
		t.Fatalf("event files = %v", b)
	}
	data, _ := os.ReadFile(b[0])
	if !strings.Contains(string(data), `"event":"guard_fault"`) || !strings.Contains(string(data), `"fault":"config"`) {
		t.Fatalf("events = %s", data)
	}
}

func TestCLIProxyRunsThroughGuard(t *testing.T) {
	r := newCLIRig(t, cliCfg)
	r.agent["/env.sock"] = nil
	r.d.Presence = fakePresence{state: PresenceAsleep}
	if got := r.run("proxy", "--realm", "zr", "--", "step", "ssh"); got != ExitUnavailable {
		t.Fatalf("exit = %d", got)
	}
	if got := r.errb.String(); got != "pg-origin-guard: refused (no-human)\n" {
		t.Fatalf("stderr = %q", got)
	}
}
