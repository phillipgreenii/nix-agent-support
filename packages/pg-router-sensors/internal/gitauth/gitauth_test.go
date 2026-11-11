package gitauth

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"

	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
	"github.com/phillipgreenii/pg-router/schemas"
)

// keygen runs the real ssh-keygen in dir (fixture helper).
func keygen(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("ssh-keygen", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TZ=UTC")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("ssh-keygen unavailable or failed: %v: %s", err, out)
	}
	return string(out)
}

// fixture holds a CA and a way to mint user certificates and plain keys.
type fixture struct {
	t   *testing.T
	dir string
	n   int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: t.TempDir()}
	keygen(t, f.dir, "-q", "-t", "ed25519", "-N", "", "-f", "ca")
	return f
}

func (f *fixture) key() string {
	f.n++
	name := "k" + string(rune('a'+f.n))
	keygen(f.t, f.dir, "-q", "-t", "ed25519", "-N", "", "-f", name)
	return name
}

// plain returns the public key line of a plain (non-certificate) key.
func (f *fixture) plain() string {
	b, err := os.ReadFile(filepath.Join(f.dir, f.key()+".pub"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// cert returns the certificate line signed for id/principal valid per -V.
func (f *fixture) cert(id, principal, validity string) string {
	name := f.key()
	args := []string{"-q", "-s", "ca", "-I", id, "-n", principal}
	if validity != "" {
		args = append(args, "-V", validity)
	}
	keygen(f.t, f.dir, append(args, name+".pub")...)
	b, err := os.ReadFile(filepath.Join(f.dir, name+"-cert.pub"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// describe is what `ssh-add -L | ssh-keygen -L -f -` prints for the lines.
func (f *fixture) describe(lines ...string) string {
	cmd := exec.Command("ssh-keygen", "-L", "-f", "-")
	cmd.Env = append(os.Environ(), "TZ=UTC")
	cmd.Stdin = strings.NewReader(strings.Join(lines, ""))
	out, _ := cmd.CombinedOutput() // the exit status is not usable
	return string(out)
}

func TestParseCerts(t *testing.T) {
	f := newFixture(t)
	valid := f.cert("alice-id", "alice", "-1h:+2h")
	expired := f.cert("old-id", "alice", "-3h:-2h")
	future := f.cert("soon-id", "bob", "+1h:+2h")
	forever := f.cert("forever-id", "carol", "")
	out := f.describe(f.plain(), valid, expired, future, forever)
	certs := ParseCerts(out)
	if len(certs) != 4 {
		t.Fatalf("want 4 certs (plain key skipped), got %d:\n%s", len(certs), out)
	}
	if certs[0].KeyID != "alice-id" || len(certs[0].Principals) != 1 || certs[0].Principals[0] != "alice" {
		t.Errorf("cert0 %+v", certs[0])
	}
	now := time.Now().UTC()
	if d := certs[0].ValidTo.Sub(now); d < 119*time.Minute || d > 121*time.Minute {
		t.Errorf("valid expiry off by %v (TZ=UTC parse)", d)
	}
	if !certs[1].ValidTo.Before(now) {
		t.Errorf("expired cert not in the past: %v", certs[1].ValidTo)
	}
	if !certs[2].ValidFrom.After(now) {
		t.Errorf("future cert from %v", certs[2].ValidFrom)
	}
	if !certs[3].Forever() || !certs[3].ValidFrom.IsZero() && certs[3].ValidFrom.After(now) {
		t.Errorf("forever cert %+v", certs[3])
	}
	if got := ParseCerts(f.describe(f.plain())); len(got) != 0 {
		t.Errorf("plain key parsed as cert: %+v", got)
	}
	if got := ParseCerts(""); len(got) != 0 {
		t.Errorf("empty: %+v", got)
	}
}

func TestParseValidForms(t *testing.T) {
	from, to := parseValid("from 2026-01-02T03:04:05 to 2026-01-03T03:04:05")
	if from.Day() != 2 || to.Day() != 3 {
		t.Error(from, to)
	}
	if f, tt := parseValid("forever"); !f.IsZero() || !tt.IsZero() {
		t.Error("forever")
	}
	if f, tt := parseValid("to garbage"); !f.IsZero() || !tt.IsZero() {
		t.Error("garbage")
	}
}

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// rig runs the real dispatch against a fake runner.
type rig struct {
	t      *testing.T
	fake   *cmdruntest.Fake
	s      *Sensor
	sock   string
	state  string
	keyOut string // ssh-add -L stdout
	kgOut  string // ssh-keygen -L stdout
	addRes *cmdrun.Result
	lsRes  cmdrun.Result
	out    bytes.Buffer
	errb   bytes.Buffer
	env    map[string]string
	conf   string
}

func newRig(t *testing.T, cfg map[string]any) *rig {
	t.Helper()
	r := &rig{
		t: t, fake: cmdruntest.New(t), state: t.TempDir(), sock: "/tmp/agent.sock",
		keyOut: "ssh-ed25519 AAAA key\n", env: map[string]string{},
	}
	r.s = New()
	r.s.Run = r.fake.Runner()
	if cfg != nil {
		var b bytes.Buffer
		if err := toml.NewEncoder(&b).Encode(cfg); err != nil {
			t.Fatal(err)
		}
		r.conf = filepath.Join(r.state, "config.toml")
		if err := os.WriteFile(r.conf, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.fake.On(cmdruntest.Match("ssh-add", "-L")).Respond(func(c cmdrun.Call) cmdrun.Result {
		if r.addRes != nil {
			return *r.addRes
		}
		return cmdruntest.Out(r.keyOut)
	})
	r.fake.On(cmdruntest.Match("ssh-keygen", "-L", "-f", "-")).Respond(func(c cmdrun.Call) cmdrun.Result {
		if string(c.Stdin) != r.keyOut {
			t.Errorf("ssh-keygen stdin %q", c.Stdin)
		}
		if !contains(c.Env, "TZ=UTC") {
			t.Errorf("ssh-keygen env %v lacks TZ=UTC", c.Env)
		}
		return cmdruntest.Out(r.kgOut)
	})
	r.fake.On(cmdruntest.MatchName("git")).Respond(func(c cmdrun.Call) cmdrun.Result {
		if !contains(c.Env, "GIT_TERMINAL_PROMPT=0") {
			t.Errorf("git env %v leaves prompts on", c.Env)
		}
		return r.lsRes
	})
	return r
}

func contains(l []string, s string) bool {
	for _, e := range l {
		if e == s {
			return true
		}
	}
	return false
}

func (r *rig) run(extra ...string) (int, map[string]any) {
	r.t.Helper()
	r.out.Reset()
	r.errb.Reset()
	getenv := func(k string) string {
		if k == "SSH_AUTH_SOCK" {
			return r.sock
		}
		return r.env[k]
	}
	args := append([]string{"git-auth-sensor", "--state-dir", r.state, "--config", r.conf}, extra...)
	code := sensorcli.Run(args, sensorcli.Registry{"git-auth-sensor": r.s}, sensorcli.IO{
		Stdout: &r.out, Stderr: &r.errb, Getenv: getenv, Now: func() time.Time { return t0 },
	})
	line := strings.TrimSpace(r.out.String())
	if line == "" {
		return code, nil
	}
	if err := schemas.ValidateLevelAnswer([]byte(line)); err != nil {
		r.t.Fatalf("line %q fails the schema: %v", line, err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		r.t.Fatal(err)
	}
	return code, m
}

func (r *rig) level(extra ...string) string {
	r.t.Helper()
	code, m := r.run(append([]string{"--raw"}, extra...)...)
	if code != 0 || m == nil {
		r.t.Fatalf("exit %d, stderr %s", code, r.errb.String())
	}
	return m["level"].(string)
}

// block renders ssh-keygen -L style text for one certificate.
func block(n int, id, principal, valid string) string {
	return "(stdin):" + string(rune('0'+n)) + ":\n" +
		"        Type: ssh-ed25519-cert-v01@openssh.com user certificate\n" +
		"        Key ID: \"" + id + "\"\n" +
		"        Serial: 0\n" +
		"        Valid: " + valid + "\n" +
		"        Principals: \n" +
		"                " + principal + "\n" +
		"        Critical Options: (none)\n"
}

func cfgKey() map[string]any {
	return map[string]any{"resource": "git_auth_main", "key_id": "me"}
}

func TestVerdicts(t *testing.T) {
	cases := []struct {
		name  string
		kg    string
		cfg   map[string]any
		sock  *string
		add   *cmdrun.Result
		keys  *string
		ls    *cmdrun.Result
		want  string
		calls int // git calls expected
	}{
		{name: "valid", kg: block(1, "me", "p", "from 2026-11-11T11:00:00 to 2026-11-11T14:00:00"), want: "AVAILABLE"},
		{name: "forever", kg: block(1, "me", "p", "forever"), want: "AVAILABLE"},
		{name: "expired", kg: block(1, "me", "p", "from 2026-11-11T08:00:00 to 2026-11-11T11:00:00"), want: "UNAVAILABLE"},
		{name: "future", kg: block(1, "me", "p", "from 2026-11-11T13:00:00 to 2026-11-11T20:00:00"), want: "UNAVAILABLE"},
		{name: "inside margin", kg: block(1, "me", "p", "from 2026-11-11T11:00:00 to 2026-11-11T12:29:00"), want: "UNAVAILABLE"},
		{name: "exactly margin", kg: block(1, "me", "p", "from 2026-11-11T11:00:00 to 2026-11-11T12:30:00"), want: "AVAILABLE"},
		{name: "no match", kg: block(1, "other", "p", "forever"), want: "UNAVAILABLE"},
		{name: "plain key only", kg: "(stdin):1 is not a certificate\n", want: "UNAVAILABLE"},
		{name: "principal", cfg: map[string]any{"resource": "git_auth_main", "principal": "p"}, kg: block(1, "x", "p", "forever"), want: "AVAILABLE"},
		{name: "principal miss", cfg: map[string]any{"resource": "git_auth_main", "principal": "q"}, kg: block(1, "x", "p", "forever"), want: "UNAVAILABLE"},
		{name: "latest expiry wins", kg: block(1, "me", "p", "from 2026-11-11T08:00:00 to 2026-11-11T11:00:00") + block(2, "me", "p", "from 2026-11-11T11:00:00 to 2026-11-11T15:00:00"), want: "AVAILABLE"},
		{name: "latest expiry loses", kg: block(1, "me", "p", "from 2026-11-11T11:00:00 to 2026-11-11T15:00:00") + block(2, "me", "p", "from 2026-11-11T08:00:00 to 2026-11-11T12:10:00"), want: "AVAILABLE"},
		{name: "forever beats dated", kg: block(1, "me", "p", "from 2026-11-11T08:00:00 to 2026-11-11T11:00:00") + block(2, "me", "p", "forever"), want: "AVAILABLE"},
		{name: "dated loses to forever first", kg: block(1, "me", "p", "forever") + block(2, "me", "p", "from 2026-11-11T08:00:00 to 2026-11-11T11:00:00"), want: "AVAILABLE"},
		{name: "ls ok", kg: block(1, "me", "p", "forever"), cfg: map[string]any{"resource": "git_auth_main", "key_id": "me", "ls_remote_url": "ssh://h/r"}, want: "AVAILABLE", calls: 1},
		{name: "ls fails", kg: block(1, "me", "p", "forever"), cfg: map[string]any{"resource": "git_auth_main", "key_id": "me", "ls_remote_url": "ssh://h/r"}, ls: &cmdrun.Result{Code: 128, Stderr: []byte("denied")}, want: "UNAVAILABLE", calls: 1},
		{name: "ls skipped when cert bad", kg: block(1, "x", "p", "forever"), cfg: map[string]any{"resource": "git_auth_main", "key_id": "me", "ls_remote_url": "ssh://h/r"}, want: "UNAVAILABLE"},
		{name: "no sock", sock: ptr(""), want: "UNAVAILABLE"},
		{name: "no identity", keys: ptr("\n"), want: "UNAVAILABLE"},
		{name: "ssh-add fails", add: &cmdrun.Result{Code: 1, Stderr: []byte("The agent has no identities.")}, want: "UNAVAILABLE"},
	}
	for _, c := range cases {
		cfg := c.cfg
		if cfg == nil {
			cfg = cfgKey()
		}
		r := newRig(t, cfg)
		r.kgOut = c.kg
		if c.sock != nil {
			r.sock = *c.sock
		}
		if c.keys != nil {
			r.keyOut = *c.keys
		}
		r.addRes = c.add
		if c.ls != nil {
			r.lsRes = *c.ls
		}
		if got := r.level(); got != c.want {
			t.Errorf("%s: level %s, want %s (stderr %s)", c.name, got, c.want, r.errb.String())
		}
		git := 0
		for _, call := range r.fake.Calls() {
			if call.Name == "git" {
				git++
				if strings.Join(call.Args, " ") != "ls-remote --exit-code ssh://h/r HEAD" {
					t.Errorf("%s: git argv %v", c.name, call.Args)
				}
			}
		}
		if git != c.calls {
			t.Errorf("%s: %d git calls, want %d", c.name, git, c.calls)
		}
	}
}

func ptr(s string) *string { return &s }

// TestOnlyAllowedArgv is SEN-4: nothing but the three probes is ever run.
func TestOnlyAllowedArgv(t *testing.T) {
	r := newRig(t, map[string]any{"resource": "git_auth_main", "key_id": "me", "ls_remote_url": "ssh://h/r"})
	r.kgOut = block(1, "me", "p", "forever")
	r.level()
	for _, c := range r.fake.Calls() {
		line := c.Name + " " + strings.Join(c.Args, " ")
		switch line {
		case "ssh-add -L", "ssh-keygen -L -f -", "git ls-remote --exit-code ssh://h/r HEAD":
		default:
			t.Errorf("disallowed argv %q", line)
		}
	}
}

func TestDwellAndRefresh(t *testing.T) {
	r := newRig(t, cfgKey())
	r.kgOut = block(1, "me", "p", "forever")
	// First stateful run starts at the worst level: the rise waits for a dwell.
	_, m := r.run()
	if m["level"] != "UNAVAILABLE" {
		t.Fatalf("first run %v", m["level"])
	}
	// Refresh skips the dwell.
	r.env["PG_ROUTER_LEVEL_REFRESH"] = "1"
	_, m = r.run()
	if m["level"] != "AVAILABLE" {
		t.Fatalf("refresh run %v", m["level"])
	}
}

func TestConfigureRejects(t *testing.T) {
	good := cfgKey()
	for name, mut := range map[string]func(map[string]any){
		"unknown key":   func(m map[string]any) { m["bogus"] = "x" },
		"no resource":   func(m map[string]any) { delete(m, "resource") },
		"bad resource":  func(m map[string]any) { m["resource"] = "main" },
		"bare prefix":   func(m map[string]any) { m["resource"] = "git_auth_" },
		"resource type": func(m map[string]any) { m["resource"] = int64(1) },
		"neither":       func(m map[string]any) { delete(m, "key_id") },
		"both":          func(m map[string]any) { m["principal"] = "p" },
		"key type":      func(m map[string]any) { m["key_id"] = int64(1) },
		"principal typ": func(m map[string]any) { delete(m, "key_id"); m["principal"] = int64(1) },
		"bad margin":    func(m map[string]any) { m["margin"] = "x" },
		"url type":      func(m map[string]any) { m["ls_remote_url"] = int64(1) },
		"url option":    func(m map[string]any) { m["ls_remote_url"] = "--upload-pack=x" },
	} {
		m := map[string]any{}
		for k, v := range good {
			m[k] = v
		}
		mut(m)
		if err := New().Configure(sensorcli.Config{Raw: m}); err == nil {
			t.Errorf("%s: must fail", name)
		}
	}
	s := New()
	if err := s.Configure(sensorcli.Config{Raw: map[string]any{"resource": "git_auth_main", "key_id": "me", "margin": "10m"}}); err != nil {
		t.Fatal(err)
	}
	if s.Margin != 10*time.Minute || strings.Join(s.Resources(), ",") != "git_auth_main" {
		t.Errorf("%+v", s)
	}
	if b := s.Bands(); len(b) != 1 || len(b[0].Bands) != 0 {
		t.Errorf("bands %+v", b)
	}
}

func TestUnconfigured(t *testing.T) {
	s := New()
	if s.Resources() != nil || s.Bands() != nil {
		t.Error("unconfigured answers nothing")
	}
	if _, err := s.Measure(nil, sensorcli.Env{}); err == nil { //nolint:staticcheck // ctx unused on this path
		t.Error("want ErrNoReading")
	}
}

func TestResourceFilter(t *testing.T) {
	r := newRig(t, nil)
	r.s.Resource = "git_auth_main"
	if _, err := r.s.Measure(nil, sensorcli.Env{Resource: "other"}); err == nil { //nolint:staticcheck // ctx unused on this path
		t.Error("other resource must not be measured")
	}
}

func TestFailText(t *testing.T) {
	if failText(cmdrun.Result{StartErr: os.ErrNotExist}) == "" || failText(cmdrun.Result{CtxErr: os.ErrDeadlineExceeded}) == "" || failText(cmdrun.Result{Code: 2}) == "" {
		t.Error("failText")
	}
}

func TestKeygenStartFailure(t *testing.T) {
	r := newRig(t, cfgKey())
	r.fake = cmdruntest.New(t)
	r.s.Run = r.fake.Runner()
	r.fake.On(cmdruntest.Match("ssh-add", "-L")).Return(cmdruntest.Out("ssh-ed25519 AAAA\n"))
	r.fake.On(cmdruntest.MatchName("ssh-keygen")).Return(cmdrun.Result{StartErr: os.ErrNotExist})
	if got := r.level(); got != "UNAVAILABLE" {
		t.Errorf("level %s", got)
	}
}
