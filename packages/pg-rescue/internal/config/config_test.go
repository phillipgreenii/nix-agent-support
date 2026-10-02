package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const fullConfig = `
redact = ['ghp_[A-Za-z0-9]{36}', 'Authorization: \S+']

[handler.flake-lock-conflict]
command = ["pg-rescue-flake-lock-conflict"]
timeout = "2m"
tags = ["deterministic"]

[handler.fix-small]
command = ["pg-rescue-claude", "--model", "haiku"]
timeout = "200ms"
description = "haiku, 2m, no MCP"
tags = ["agent"]
env = { FOO = "bar" }

[handler.notify]
command = ["pg-rescue-notify"]

[chain.sync]
handlers = ["flake-lock-conflict", "fix-small", "notify", "notify"]
`

func mustParse(t *testing.T, text string) *Config {
	t.Helper()
	cfg, err := Parse("/cfg.toml", []byte(text), "/home/x")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

func parseErr(t *testing.T, text string) *Error {
	t.Helper()
	_, err := Parse("/cfg.toml", []byte(text), "/home/x")
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("Parse error = %v; want *Error", err)
	}
	return ce
}

func TestParseFullConfig(t *testing.T) {
	cfg := mustParse(t, fullConfig)
	if len(cfg.Redact) != 2 || !cfg.Redact[0].MatchString("ghp_"+strings.Repeat("a", 36)) {
		t.Errorf("redact patterns not compiled as RE2: %v", cfg.Redact)
	}
	if got, want := cfg.HandlerNames(), []string{"fix-small", "flake-lock-conflict", "notify"}; !reflect.DeepEqual(got, want) {
		t.Errorf("handlers = %v want %v", got, want)
	}
	fs := cfg.Handlers["fix-small"]
	if fs.Timeout != 200*time.Millisecond {
		t.Errorf("sub-second timeout = %v", fs.Timeout)
	}
	if fs.Description != "haiku, 2m, no MCP" || !reflect.DeepEqual(fs.Tags, []string{"agent"}) ||
		!reflect.DeepEqual(fs.Command, []string{"pg-rescue-claude", "--model", "haiku"}) || fs.Env["FOO"] != "bar" {
		t.Errorf("fix-small = %+v", fs)
	}
	if got := cfg.Chains["sync"].Handlers; !reflect.DeepEqual(got, []string{"flake-lock-conflict", "fix-small", "notify", "notify"}) {
		t.Errorf("chain keeps order and repeats: %v", got)
	}
}

func TestDefaults(t *testing.T) {
	cfg := mustParse(t, "[handler.h]\ncommand = [\"x\"]\n")
	h := cfg.Handlers["h"]
	if h.Timeout != 5*time.Minute || DefaultTimeout != 5*time.Minute {
		t.Errorf("default timeout = %v; want 5m", h.Timeout)
	}
	if h.EnvFile != "" || h.Description != "" || len(h.Tags) != 0 || len(h.Env) != 0 {
		t.Errorf("optional keys not zero: %+v", h)
	}
	if len(cfg.Redact) != 0 || len(cfg.Chains) != 0 {
		t.Errorf("top-level defaults: redact=%v chains=%v", cfg.Redact, cfg.Chains)
	}
	if mustParse(t, "").Handlers == nil {
		t.Error("an empty config is valid")
	}
}

func TestDurationSyntax(t *testing.T) {
	for in, want := range map[string]time.Duration{"90s": 90 * time.Second, "1h30m": 90 * time.Minute, "50ms": 50 * time.Millisecond} {
		cfg := mustParse(t, "[handler.h]\ncommand=[\"x\"]\ntimeout=\""+in+"\"\n")
		if got := cfg.Handlers["h"].Timeout; got != want {
			t.Errorf("timeout %q = %v want %v", in, got, want)
		}
	}
	for _, bad := range []string{"5", "5x", "", "0s", "-1m"} {
		ce := parseErr(t, "[handler.h]\ncommand=[\"x\"]\ntimeout=\""+bad+"\"\n")
		msg := ce.Error()
		if !strings.Contains(msg, "/cfg.toml") || !strings.Contains(msg, "[handler.h]") || !strings.Contains(msg, `key "timeout"`) {
			t.Errorf("timeout %q: message does not name file, table and key: %s", bad, msg)
		}
	}
}

func TestUnknownKeysAreRejectedWithValidNames(t *testing.T) {
	tests := []struct {
		name, text string
		want       []string
	}{
		{"top level", "reddact = []\n", []string{"/cfg.toml", "top level", `"reddact"`, "valid keys: chain, handler, redact"}},
		{
			"handler", "[handler.h]\ncommand=[\"x\"]\ncomand=1\n",
			[]string{"[handler.h]", `unknown key "comand"`, "valid keys: command, description, env, env_file, tags, timeout"},
		},
		{
			"chain", "[handler.h]\ncommand=[\"x\"]\n[chain.c]\nhandlers=[\"h\"]\nextra=1\n",
			[]string{"[chain.c]", `unknown key "extra"`, "valid keys: handlers"},
		},
		{"unknown top-level table", "[bogus]\nx=1\n", []string{"top level", `"bogus`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := parseErr(t, tc.text).Error()
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("message %q lacks %q", msg, w)
				}
			}
		})
	}
}

func TestWholeFileIsValidatedNotOnlyOneChain(t *testing.T) {
	ce := parseErr(t, `
redact = ["("]

[handler.a]
timeout = "x"

[handler.b]
command = ["b"]

[chain.used]
handlers = ["b"]

[chain.unused]
handlers = ["ghost"]
`)
	if len(ce.Problems) != 4 {
		t.Fatalf("want 4 problems (redact, command, timeout, unknown handler), got %d: %v", len(ce.Problems), ce.Problems)
	}
	msg := ce.Error()
	for _, w := range []string{
		`"redact"[0]`, `[handler.a]: missing required key "command"`, `[handler.a]: key "timeout"`,
		`[chain.unused]: key "handlers"[0]: unknown handler "ghost"; configured: a, b`,
	} {
		if !strings.Contains(msg, w) {
			t.Errorf("message lacks %q:\n%s", w, msg)
		}
	}
}

func TestSingleProblemIsOneLine(t *testing.T) {
	msg := parseErr(t, "[handler.h]\n").Error()
	if strings.Contains(msg, "\n") || !strings.HasPrefix(msg, "config /cfg.toml: [handler.h]: missing required key") {
		t.Errorf("message = %q", msg)
	}
}

func TestInvalidTOMLNamesFileAndLine(t *testing.T) {
	msg := parseErr(t, "[handler.h]\ncommand = = 1\n").Error()
	if !strings.Contains(msg, "/cfg.toml") || !strings.Contains(msg, "invalid TOML") || !strings.Contains(msg, "line 2") {
		t.Errorf("message = %q", msg)
	}
}

func TestWrongTypeIsAConfigError(t *testing.T) {
	msg := parseErr(t, "[handler.h]\ncommand = \"not-a-list\"\n").Error()
	if !strings.Contains(msg, "/cfg.toml") || !strings.Contains(msg, "line 2") {
		t.Errorf("message = %q", msg)
	}
}

func TestCommandValidation(t *testing.T) {
	for name, text := range map[string]string{
		"missing":      "[handler.h]\n",
		"empty list":   "[handler.h]\ncommand = []\n",
		"empty argv0":  "[handler.h]\ncommand = [\"\"]\n",
		"bad env name": "[handler.h]\ncommand=[\"x\"]\n[handler.h.env]\n\"A=B\" = \"v\"\n",
		"bad name":     "[handler.\"a,b\"]\ncommand=[\"x\"]\n",
	} {
		if msg := parseErr(t, text).Error(); !strings.Contains(msg, "[handler.") {
			t.Errorf("%s: %s", name, msg)
		}
	}
}

func TestChainValidation(t *testing.T) {
	base := "[handler.a]\ncommand=[\"x\"]\n[handler.b]\ncommand=[\"x\"]\n"
	msg := parseErr(t, base+"[chain.c]\nhandlers=[]\n").Error()
	if !strings.Contains(msg, "[chain.c]") || !strings.Contains(msg, "non-empty") || !strings.Contains(msg, "configured: a, b") {
		t.Errorf("empty chain: %s", msg)
	}
	msg = parseErr(t, base+"[chain.c]\nhandlers=[\"a\",\"zz\"]\n").Error()
	if !strings.Contains(msg, `unknown handler "zz"; configured: a, b`) {
		t.Errorf("unknown instance: %s", msg)
	}
	msg = parseErr(t, base+"[chain.c]\n").Error()
	if !strings.Contains(msg, `key "handlers"`) {
		t.Errorf("chain without handlers: %s", msg)
	}
}

func TestEnvFilePermissions(t *testing.T) {
	dir := t.TempDir()
	envf := filepath.Join(dir, "h.env")
	if err := os.WriteFile(envf, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	text := "[handler.h]\ncommand=[\"x\"]\nenv_file=\"" + envf + "\"\n"
	if cfg := mustParse(t, text); cfg.Handlers["h"].EnvFile != envf {
		t.Errorf("0600 env_file rejected or altered: %q", cfg.Handlers["h"].EnvFile)
	}
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o660, 0o666, 0o610} {
		if err := os.Chmod(envf, mode); err != nil {
			t.Fatal(err)
		}
		msg := parseErr(t, text).Error()
		for _, w := range []string{"/cfg.toml", "[handler.h]", `key "env_file"`, envf, "unsafe permissions"} {
			if !strings.Contains(msg, w) {
				t.Errorf("mode %04o: message lacks %q: %s", mode, w, msg)
			}
		}
	}
	// A 0400 file is fine: nothing group- or world-accessible.
	if err := os.Chmod(envf, 0o400); err != nil {
		t.Fatal(err)
	}
	mustParse(t, text)
}

func TestEnvFilePathRules(t *testing.T) {
	if msg := parseErr(t, "[handler.h]\ncommand=[\"x\"]\nenv_file=\"rel/path.env\"\n").Error(); !strings.Contains(msg, "absolute") {
		t.Errorf("relative env_file: %s", msg)
	}
	cfg := mustParse(t, "[handler.h]\ncommand=[\"x\"]\nenv_file=\"~/secrets/h.env\"\n")
	if got := cfg.Handlers["h"].EnvFile; got != "/home/x/secrets/h.env" {
		t.Errorf("tilde expansion = %q", got)
	}
	// A not-yet-existing env_file is not a config error: it is read at spawn time.
	mustParse(t, "[handler.h]\ncommand=[\"x\"]\nenv_file=\"/nonexistent/dir/h.env\"\n")
}

func TestRedactMustBeRE2(t *testing.T) {
	msg := parseErr(t, `redact = ['(?=lookahead)']`).Error()
	if !strings.Contains(msg, "top level") || !strings.Contains(msg, `"redact"[0]`) || !strings.Contains(msg, "invalid regular expression") {
		t.Errorf("message = %q", msg)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.toml"), "/h")
	var ce *Error
	if !errors.As(err, &ce) || !strings.Contains(ce.Error(), "nope.toml") || !strings.Contains(ce.Error(), "cannot read file") {
		t.Errorf("error = %v", err)
	}
}

func TestUnknownNameError(t *testing.T) {
	err := UnknownNameError("handler", "fix-smal", "--handlers", []string{"fix-large", "fix-small", "notify", "p1-later"})
	want := `unknown handler "fix-smal" in --handlers; configured: fix-large, fix-small, notify, p1-later`
	if err.Error() != want {
		t.Errorf("got %q want %q", err, want)
	}
	if got := UnknownNameError("chain", "x", "--chain", nil).Error(); !strings.Contains(got, "configured: (none)") {
		t.Errorf("no configured names: %q", got)
	}
}
