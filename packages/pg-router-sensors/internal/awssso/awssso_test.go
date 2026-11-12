package awssso

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
	"github.com/phillipgreenii/pg-router/schemas"
)

var t0 = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

// secrets are fixture token values; none may ever reach an output.
const (
	secretAccess  = "SECRET-ACCESS-TOKEN-VALUE-1f9c"
	secretRefresh = "SECRET-REFRESH-TOKEN-VALUE-77ab"
	secretClient  = "SECRET-CLIENT-SECRET-VALUE-c0de"
)

type rig struct {
	t     *testing.T
	cache string
	state string
	out   bytes.Buffer
	errb  bytes.Buffer
	cfg   map[string]any
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return &rig{t: t, cache: t.TempDir(), state: t.TempDir(), cfg: map[string]any{}}
}

func (r *rig) put(name string, v any) {
	r.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		r.t.Fatal(err)
	}
	r.putRaw(name, string(b))
}

func (r *rig) putRaw(name, body string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.cache, name), []byte(body), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func ts(d time.Duration) string { return t0.Add(d).Format(time.RFC3339) }

// login is a cache file named by the sha1 of the default session.
func (r *rig) login(access, registration time.Duration, refresh bool) {
	m := map[string]any{
		"startUrl": "https://example.invalid/start", "region": "us-west-2",
		"accessToken": secretAccess, "expiresAt": ts(access),
		"clientId": "cid", "clientSecret": secretClient,
	}
	if refresh {
		m["refreshToken"] = secretRefresh
	}
	if registration != 0 {
		m["registrationExpiresAt"] = ts(registration)
	}
	r.put(sha1Name("zip"), m)
}

func (r *rig) run(extra ...string) (int, map[string]any) {
	r.t.Helper()
	r.out.Reset()
	r.errb.Reset()
	cfg := map[string]any{"cache_dir": r.cache}
	for k, v := range r.cfg {
		cfg[k] = v
	}
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(cfg); err != nil {
		r.t.Fatal(err)
	}
	conf := filepath.Join(r.state, "config.toml")
	if err := os.WriteFile(conf, b.Bytes(), 0o600); err != nil {
		r.t.Fatal(err)
	}
	args := append([]string{"aws-sso-sensor", "--raw", "--state-dir", r.state, "--config", conf}, extra...)
	code := sensorcli.Run(args, sensorcli.Registry{"aws-sso-sensor": New()}, sensorcli.IO{
		Stdout: &r.out, Stderr: &r.errb, Getenv: func(string) string { return "" }, Now: func() time.Time { return t0 },
	})
	line := strings.TrimSpace(r.out.String())
	if line == "" {
		return code, nil
	}
	if len(extra) == 0 {
		if err := schemas.ValidateLevelAnswer([]byte(line)); err != nil {
			r.t.Fatalf("line %q fails the schema: %v", line, err)
		}
	}
	if len(extra) > 0 {
		return code, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		r.t.Fatal(err)
	}
	return code, m
}

func (r *rig) level() (string, float64) {
	r.t.Helper()
	code, m := r.run()
	if code != 0 || m == nil {
		r.t.Fatalf("exit %d, stderr %s", code, r.errb.String())
	}
	if m["resource"] != Resource {
		r.t.Fatalf("resource %v", m["resource"])
	}
	rd := m["reading"].(map[string]any)
	if rd["unit"] != "s_left" {
		r.t.Fatalf("unit %v", rd["unit"])
	}
	return m["level"].(string), rd["value"].(float64)
}

const hr = time.Hour

func TestVerdicts(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(r *rig)
		want      string
		wantLeft  float64
		wantStdin string
	}{
		{"live access token, no refresh", func(r *rig) { r.login(2*hr, 0, false) }, "AVAILABLE", 7200, ""},
		{"live access token with refresh and registration", func(r *rig) { r.login(1*hr, 20*hr, true) }, "AVAILABLE", 72000, ""},
		{"expired access, refresh, valid registration", func(r *rig) { r.login(-1*hr, 5*hr, true) }, "AVAILABLE", 18000, ""},
		{"expired access, no refresh", func(r *rig) { r.login(-1*hr, 0, false) }, "UNAVAILABLE", 0, ""},
		{"expired access, registration valid but no refresh", func(r *rig) { r.login(-1*hr, 5*hr, false) }, "UNAVAILABLE", 0, ""},
		{"refresh with expired registration", func(r *rig) { r.login(-1*hr, -30*time.Minute, true) }, "UNAVAILABLE", 0, ""},
		{"refresh, no registration field, expired access", func(r *rig) { r.login(-1*hr, 0, true) }, "AVAILABLE", 0, ""},
		{"no cache dir", func(r *rig) { r.cache = filepath.Join(r.cache, "absent") }, "UNAVAILABLE", 0, ""},
		{"empty cache dir", func(r *rig) {}, "UNAVAILABLE", 0, ""},
		{"file for another session", func(r *rig) {
			r.put(sha1Name("other"), map[string]any{"expiresAt": ts(5 * hr), "accessToken": secretAccess})
		}, "UNAVAILABLE", 0, ""},
		{"malformed JSON ignored", func(r *rig) { r.putRaw(sha1Name("zip"), "{not json") }, "UNAVAILABLE", 0, ""},
		{"malformed file beside a good one", func(r *rig) {
			r.putRaw("zzz.json", "{not json")
			r.login(2*hr, 0, false)
		}, "AVAILABLE", 7200, ""},
		{"unparseable expiry ignored", func(r *rig) {
			r.put(sha1Name("zip"), map[string]any{"expiresAt": "tomorrow", "accessToken": secretAccess})
		}, "UNAVAILABLE", 0, ""},
		{"matched by sessionName field", func(r *rig) {
			r.put("custom-name.json", map[string]any{"sessionName": "zip", "expiresAt": ts(3 * hr), "accessToken": secretAccess})
		}, "AVAILABLE", 10800, ""},
		{"legacy AWS CLI timestamp", func(r *rig) {
			r.put(sha1Name("zip"), map[string]any{"expiresAt": "2026-11-12T15:00:00UTC", "accessToken": secretAccess})
		}, "AVAILABLE", 10800, ""},
		{"best of several matches wins", func(r *rig) {
			r.login(-1*hr, 0, false)
			r.put("b.json", map[string]any{"sessionName": "zip", "expiresAt": ts(4 * hr)})
		}, "AVAILABLE", 14400, ""},
		{"non-json file ignored", func(r *rig) { r.putRaw("notes.txt", "x"); r.login(2*hr, 0, false) }, "AVAILABLE", 7200, ""},
		{"empty refresh token is no refresh token", func(r *rig) {
			r.put(sha1Name("zip"), map[string]any{"expiresAt": ts(-hr), "refreshToken": "", "registrationExpiresAt": ts(5 * hr)})
		}, "UNAVAILABLE", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			c.setup(r)
			got, left := r.level()
			if got != c.want || left != c.wantLeft {
				t.Errorf("level %s left %v, want %s %v (stderr %s)", got, left, c.want, c.wantLeft, r.errb.String())
			}
		})
	}
}

func TestMargin(t *testing.T) {
	cases := []struct {
		name   string
		margin string
		left   time.Duration
		want   string
	}{
		{"default margin, inside", "", 29 * time.Minute, "UNAVAILABLE"},
		{"default margin, exactly", "", 30 * time.Minute, "AVAILABLE"},
		{"custom margin, inside", "2h", 90 * time.Minute, "UNAVAILABLE"},
		{"custom margin, outside", "2h", 3 * hr, "AVAILABLE"},
		{"zero margin, one second left", "0s", time.Second, "AVAILABLE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			if c.margin != "" {
				r.cfg["margin"] = c.margin
			}
			r.login(c.left, 0, false)
			if got, _ := r.level(); got != c.want {
				t.Errorf("level %s, want %s", got, c.want)
			}
			// The margin applies to the renewal horizon with a refresh token too.
			r2 := newRig(t)
			if c.margin != "" {
				r2.cfg["margin"] = c.margin
			}
			r2.login(-hr, c.left, true)
			if got, _ := r2.level(); got != c.want {
				t.Errorf("refresh: level %s, want %s", got, c.want)
			}
		})
	}
}

// TestSecretsNeverPrinted: token values in fixtures never reach stdout, stderr
// or the returned observations, in the available and unavailable cases, under
// --raw and with state, and with the sensor's debug-ish paths (margin failure).
func TestSecretsNeverPrinted(t *testing.T) {
	setups := map[string]func(r *rig){
		"available":   func(r *rig) { r.login(-hr, 5*hr, true) },
		"unavailable": func(r *rig) { r.login(-hr, -hr, true) },
		"margin":      func(r *rig) { r.login(10*time.Minute, 0, false) },
		"malformed":   func(r *rig) { r.putRaw(sha1Name("zip"), `{"accessToken":"`+secretAccess+`", broken`) },
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			setup(r)
			for _, extra := range [][]string{nil, {"--bands"}} {
				r.run(extra...)
				all := r.out.String() + r.errb.String()
				for _, s := range []string{secretAccess, secretRefresh, secretClient} {
					if strings.Contains(all, s) {
						t.Errorf("secret %q leaked into output %q", s, all)
					}
				}
			}
			// Returned structs: measure directly and render with %+v.
			s := New()
			s.CacheDir = r.cache
			obs, err := s.Measure(nil, sensorcli.Env{Now: t0, Stderr: &r.errb, Getenv: func(string) string { return "" }}) //nolint:staticcheck // ctx unused
			if err != nil {
				t.Fatal(err)
			}
			dump := strings.Join([]string{fmt.Sprintf("%+v %#v", obs, obs[0].Reading), r.errb.String()}, "\n")
			for _, sec := range []string{secretAccess, secretRefresh, secretClient} {
				if strings.Contains(dump, sec) {
					t.Errorf("secret %q leaked into returned observations", sec)
				}
			}
		})
	}
}

func TestHomeDefault(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".aws", "sso", "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"expiresAt": ts(3 * hr)})
	if err := os.WriteFile(filepath.Join(dir, sha1Name("zip")), b, 0o600); err != nil {
		t.Fatal(err)
	}
	s := New()
	var errb bytes.Buffer
	obs, err := s.Measure(nil, sensorcli.Env{Now: t0, Stderr: &errb, Getenv: func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}}) //nolint:staticcheck // ctx unused
	if err != nil || obs[0].Level != "AVAILABLE" {
		t.Fatalf("obs %+v err %v", obs, err)
	}
	// No HOME and no cache_dir: UNAVAILABLE, not an error.
	obs, err = New().Measure(nil, sensorcli.Env{Now: t0, Stderr: &errb, Getenv: func(string) string { return "" }}) //nolint:staticcheck // ctx unused
	if err != nil || obs[0].Level != "UNAVAILABLE" {
		t.Fatalf("no HOME: obs %+v err %v", obs, err)
	}
}

func TestStartURLMatch(t *testing.T) {
	r := newRig(t)
	r.cfg["start_url"] = "https://example.invalid/start"
	r.put("legacy.json", map[string]any{"startUrl": "https://example.invalid/start", "expiresAt": ts(2 * hr)})
	if got, _ := r.level(); got != "AVAILABLE" {
		t.Errorf("level %s", got)
	}
}

func TestConfigureRejects(t *testing.T) {
	for name, cfg := range map[string]map[string]any{
		"unknown key":    {"bogus": 1},
		"empty session":  {"sso_session": ""},
		"session type":   {"sso_session": 3},
		"bad margin":     {"margin": "soon"},
		"cache_dir type": {"cache_dir": 4},
		"start_url type": {"start_url": 4},
	} {
		if err := New().Configure(sensorcli.Config{Raw: cfg}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	s := New()
	if err := s.Configure(sensorcli.Config{Raw: map[string]any{"sso_session": "other"}}); err != nil || s.Session != "other" {
		t.Errorf("configure: %v %q", err, s.Session)
	}
}

func TestResourceFilterAndBands(t *testing.T) {
	s := New()
	if got := s.Resources(); len(got) != 1 || got[0] != Resource {
		t.Errorf("resources %v", got)
	}
	if len(s.Bands()) != 1 {
		t.Errorf("bands %v", s.Bands())
	}
	if _, err := s.Measure(nil, sensorcli.Env{Resource: "other"}); err == nil { //nolint:staticcheck // ctx unused
		t.Error("other resource must not be measured")
	}
}

func TestDirectoryNamedJSONIgnored(t *testing.T) {
	r := newRig(t)
	if err := os.Mkdir(filepath.Join(r.cache, "x.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.level(); got != "UNAVAILABLE" {
		t.Errorf("level %s", got)
	}
}

func TestRefreshBypassesDwell(t *testing.T) {
	s := New()
	s.CacheDir = t.TempDir()
	obs, _ := s.Measure(nil, sensorcli.Env{Now: t0, Refresh: true, Stderr: &bytes.Buffer{}, Getenv: func(string) string { return "" }}) //nolint:staticcheck // ctx unused
	if !obs[0].BypassDwell {
		t.Error("refresh must bypass the dwell")
	}
}
