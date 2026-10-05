package parity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExe(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeEnv builds an Env whose three "binaries" are inert scripts: enough for
// every harness check that does not run them.
func fakeEnv(t *testing.T) Env {
	t.Helper()
	root := t.TempDir()
	return Env{
		PgDeskBin:      writeExe(t, filepath.Join(root, "desk", "pg-desk"), "#!/bin/sh\nexit 0\n"),
		PgConnectorBin: writeExe(t, filepath.Join(root, "conn", "pg-connector"), "#!/bin/sh\nexit 0\n"),
		PgDeciderBin:   writeExe(t, filepath.Join(root, "dec", "pg-decider"), "#!/bin/sh\nexit 0\n"),
		TempDir:        filepath.Join(root, "run"),
	}
}

func fakeScenarioFixture(t *testing.T) *Fixture {
	t.Helper()
	fx, err := ParseFixture([]byte(fakeFixture))
	if err != nil {
		t.Fatal(err)
	}
	return fx
}

func TestFixtureEnvFromProcess(t *testing.T) {
	t.Setenv(envPgDeskBin, "/a/pg-desk")
	t.Setenv(envPgConnectorBin, "/b/pg-connector")
	t.Setenv(envPgDeciderBin, "/c/pg-decider")
	env, ok := EnvFromProcess()
	if !ok {
		t.Fatal("EnvFromProcess reported unset with all three set")
	}
	if env.PgDeskBin != "/a/pg-desk" || env.PgConnectorBin != "/b/pg-connector" || env.PgDeciderBin != "/c/pg-decider" {
		t.Errorf("env = %+v", env)
	}
	if env.TempDir != "" {
		t.Errorf("EnvFromProcess must leave TempDir to the caller, got %q", env.TempDir)
	}
	for _, name := range []string{envPgDeskBin, envPgConnectorBin, envPgDeciderBin} {
		t.Run("unset "+name, func(t *testing.T) {
			t.Setenv(name, "")
			if _, ok := EnvFromProcess(); ok {
				t.Errorf("EnvFromProcess must be false when %s is empty", name)
			}
		})
	}
}

func TestFixtureEnvValidate(t *testing.T) {
	good := fakeEnv(t)
	if err := good.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	rel := good
	rel.PgDeskBin = "pg-desk"
	if err := rel.validate(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("relative binary: %v", err)
	}
	missing := good
	missing.PgDeciderBin = filepath.Join(t.TempDir(), "nope")
	if err := missing.validate(); err == nil {
		t.Error("missing binary must be refused")
	}
	notExec := good
	notExec.PgConnectorBin = filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(notExec.PgConnectorBin, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := notExec.validate(); err == nil {
		t.Error("non-executable binary must be refused")
	}
	relTemp := good
	relTemp.TempDir = "relative/dir"
	if err := relTemp.validate(); err == nil {
		t.Error("relative TempDir must be refused")
	}
}

func TestFixtureStoreGuardRefusesAPathOutsideTheTempDir(t *testing.T) {
	temp := t.TempDir()
	if err := checkStoreInside(temp, filepath.Join(temp, "state")); err != nil {
		t.Errorf("a store under the temp dir was refused: %v", err)
	}
	for _, bad := range []string{
		t.TempDir(),                     // a sibling directory
		filepath.Dir(temp),              // the parent
		filepath.Join(temp, "..", "x"),  // an escape through ..
		"",                              // XDG_STATE_HOME unset: pg-desk would fall back to the real home
		"relative/state",                // not absolute
		filepath.Join(os.TempDir(), ""), // the shared temp dir itself
	} {
		if err := checkStoreInside(temp, bad); err == nil {
			t.Errorf("state home %q must be refused", bad)
		}
	}
}

func TestFixtureSandboxIsHermetic(t *testing.T) {
	// A leaked variable in the caller's environment must not reach a child.
	t.Setenv("PG_DESK_CONFIG", "/real/config.yaml")
	t.Setenv("XDG_STATE_HOME", "/real/state")
	t.Setenv("HOME", "/real/home")

	env := fakeEnv(t)
	sb, cleanup, err := newSandbox(env, fakeScenarioFixture(t), "old", "plan")
	if err != nil {
		t.Fatalf("newSandbox: %v", err)
	}
	defer cleanup()

	got := map[string]string{}
	for _, kv := range sb.env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for _, k := range []string{"PATH", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "PG_DESK_CONFIG", "HOME", "TMPDIR"} {
		if got[k] == "" {
			t.Errorf("child env lacks %s", k)
		}
	}
	if len(got) != 7 {
		t.Errorf("child env has unexpected members: %v", sb.env)
	}
	for _, k := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "PG_DESK_CONFIG", "HOME", "TMPDIR"} {
		if !strings.HasPrefix(got[k], sb.dir+string(filepath.Separator)) {
			t.Errorf("%s=%q is not under the sandbox %q", k, got[k], sb.dir)
		}
	}
	if got["PATH"] != sb.binDir {
		t.Errorf("PATH = %q, want exactly the sandbox bin dir %q", got["PATH"], sb.binDir)
	}

	// pg-connector on that PATH is the generated fake, not the real binary.
	fake, err := os.ReadFile(filepath.Join(sb.binDir, "pg-connector"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fake), "fake pg-connector") {
		t.Errorf("bin/pg-connector is not the generated fake:\n%s", fake)
	}
	if sb.binDir == filepath.Dir(env.PgConnectorBin) {
		t.Error("the real pg-connector directory is on the child PATH")
	}
	if _, err := os.Stat(filepath.Join(sb.binDir, "pg-desk")); err != nil {
		t.Errorf("pg-desk is not on the child PATH: %v", err)
	}

	cfg, err := os.ReadFile(sb.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"self_login: phillipgreenii", "remote: acme/api", "mode: plan"} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	if err := checkStoreInside(sb.dir, sb.stateHome); err != nil {
		t.Errorf("sandbox store guard: %v", err)
	}
}

func TestFixtureSandboxPrefersTheUnwrappedBinary(t *testing.T) {
	env := fakeEnv(t)
	// A nix wrapper script sits at pg-desk and the real binary at .pg-desk-wrapped.
	writeExe(t, env.PgDeskBin, "#!/bin/sh\nexport PATH="+filepath.Dir(env.PgConnectorBin)+":$PATH\nexec .pg-desk-wrapped \"$@\"\n")
	wrapped := writeExe(t, filepath.Join(filepath.Dir(env.PgDeskBin), ".pg-desk-wrapped"), "#!/bin/sh\nexit 0\n")

	sb, cleanup, err := newSandbox(env, fakeScenarioFixture(t), "new", "off")
	if err != nil {
		t.Fatalf("newSandbox: %v", err)
	}
	defer cleanup()
	if sb.deskExec != wrapped {
		t.Errorf("deskExec = %q, want the unwrapped %q", sb.deskExec, wrapped)
	}
}

func TestFixtureSandboxRefusesAWrapperItCannotUnwrap(t *testing.T) {
	env := fakeEnv(t)
	// A wrapper that puts the real pg-connector first on PATH, with no
	// .pg-desk-wrapped to use instead: the real connector would be reachable.
	writeExe(t, env.PgDeskBin, "#!/bin/sh\nexport PATH="+filepath.Dir(env.PgConnectorBin)+":$PATH\nexec /elsewhere/pg-desk \"$@\"\n")

	_, cleanup, err := newSandbox(env, fakeScenarioFixture(t), "old", "plan")
	if err == nil {
		cleanup()
		t.Fatal("a wrapper that exposes the real pg-connector must be refused")
	}
	if !strings.Contains(err.Error(), "real pg-connector") {
		t.Errorf("error = %v", err)
	}
}

func TestFixtureRunnersRejectAnInvalidEnv(t *testing.T) {
	sc := Scenario{Name: "01-x", Dir: "testdata/01-x", Entities: []string{"acme/api#1"}}
	bad := Env{}
	if _, err := RunOld(context.Background(), bad, sc); err == nil {
		t.Error("RunOld accepted an empty Env")
	}
	if _, err := RunNew(context.Background(), bad, sc); err == nil {
		t.Error("RunNew accepted an empty Env")
	}
	if errors.Is(ErrUnsupported, errors.New("x")) {
		t.Error("ErrUnsupported must be its own sentinel")
	}
}
