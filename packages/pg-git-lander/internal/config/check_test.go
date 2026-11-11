package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
)

func (f *fx) check(file string) *outcome.Outcome {
	f.t.Helper()
	o, err := f.ldr.Check(f.t.Context(), config.CheckOptions{RepoDir: f.repo.Dir, File: file})
	if err != nil {
		f.t.Fatalf("Check: %v", err)
	}
	return o
}

func warnWords(o *outcome.Outcome) []outcome.Warning {
	var w []outcome.Warning
	for _, e := range o.Warnings {
		w = append(w, e.Warning)
	}
	return w
}

func hasWarn(o *outcome.Outcome, w outcome.Warning, detailPart string) bool {
	for _, e := range o.Warnings {
		if e.Warning == w && strings.Contains(e.Detail+" "+e.Reason, detailPart) {
			return true
		}
	}
	return false
}

func TestCheckCleanConfigIsOKWithNoWarnings(t *testing.T) {
	f := newFx(t)
	f.xdgFor("", "[defaults]\nrun_git_hooks = true\n")
	o := f.check("")
	if o.Outcome != outcome.ConfigOK || o.Exit != 0 || len(o.Warnings) != 0 {
		t.Fatalf("want clean config-ok, got %+v", o)
	}
}

func TestCheckNoFilesIsOK(t *testing.T) {
	f := newFx(t)
	if o := f.check(""); o.Outcome != outcome.ConfigOK {
		t.Fatalf("got %+v", o)
	}
}

func TestCheckXDGErrorsNameFileLineKeyAndLayer(t *testing.T) {
	cases := []struct {
		name, body, key string
		line            int
	}{
		{"unknown key", "[defaults]\nrun_git_hooks = true\nbogus = 1\n", "defaults.bogus", 3},
		{"malformed glob", "[defaults]\n[[defaults.conflict]]\nglob = \"[\"\nresolve = [\"sh\"]\n", "defaults.conflict[0].glob", 3},
		{"malformed duration", "[defaults.lock]\nwait = \"soon\"\n", "defaults.lock.wait", 2},
		{"empty run argv", "[defaults.hooks]\n[[defaults.hooks.land.before]]\nrun = []\n", "defaults.hooks.land.before[0].run", 3},
		{"empty resolve argv", "[[defaults.conflict]]\nglob = \"*.lock\"\nresolve = []\n", "defaults.conflict[0].resolve", 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFx(t)
			f.writeXDG(c.body)
			o := f.check("")
			ce := o.ConfigError
			if o.Outcome != outcome.ConfigError || o.Exit != 2 || ce == nil {
				t.Fatalf("want config-error, got %+v", o)
			}
			if ce.File != filepath.Join(f.xdg, "config.toml") || ce.Line != c.line || ce.Layer != "xdg" {
				t.Fatalf("location = %+v, want line %d", ce, c.line)
			}
			if !strings.HasSuffix(ce.Key, strings.TrimPrefix(c.key, "defaults.")) {
				t.Fatalf("key = %q, want suffix of %q", ce.Key, c.key)
			}
		})
	}
}

func TestCheckXDGRootOutsideGitCloneIsRejected(t *testing.T) {
	f := newFx(t)
	notClone := t.TempDir()
	f.writeXDG("[defaults]\n\n[[repo]]\nroot = \"" + notClone + "\"\n")
	o := f.check("")
	ce := o.ConfigError
	if o.Outcome != outcome.ConfigError || ce == nil || ce.Key != "repo[0].root" || ce.Line != 4 || ce.Layer != "xdg" {
		t.Fatalf("got %+v / %+v", o, ce)
	}
	if !strings.Contains(o.Message, "not inside a git clone") {
		t.Fatalf("message = %q", o.Message)
	}
}

func TestCheckRepoFileUnknownKeyIsWarning(t *testing.T) {
	f := newFx(t)
	f.commit("cfg", map[string]string{".pg-git-lander.toml": "run_git_hooks = true\nfuture_key = 1\n"})
	o := f.check("")
	if o.Outcome != outcome.ConfigOK || !hasWarn(o, outcome.WarnUnknownRepoConfigKey, "future_key") {
		t.Fatalf("got %+v", o)
	}
	if !strings.Contains(o.Warnings[0].Detail, ".pg-git-lander.toml:2") {
		t.Fatalf("detail lacks file and line: %q", o.Warnings[0].Detail)
	}
}

func TestCheckRepoFileErrorIsRepoConfigErrorWithLocation(t *testing.T) {
	f := newFx(t)
	f.commit("cfg", map[string]string{".pg-git-lander.toml": "max_race_retries = 1\nmax_conflict_rounds = \"x\"\n"})
	o := f.check("")
	ce := o.ConfigError
	if o.Outcome != outcome.ConfigError || o.Exit != 2 || o.Reason != "repo-config-error" || ce == nil {
		t.Fatalf("got %+v", o)
	}
	if ce.Layer != config.LayerRepoFile || ce.Key != "max_conflict_rounds" || ce.Line != 2 || !strings.HasSuffix(ce.File, ":.pg-git-lander.toml") {
		t.Fatalf("location = %+v", ce)
	}
}

func TestCheckFileValidatesAnUncommittedRepoFile(t *testing.T) {
	f := newFx(t)
	// The committed file is broken; the uncommitted fix passes.
	f.commit("cfg", map[string]string{".pg-git-lander.toml": "max_race_retries = \"bad\"\n"})
	if o := f.check(""); o.Outcome != outcome.ConfigError {
		t.Fatalf("committed file should fail: %+v", o)
	}
	good := filepath.Join(t.TempDir(), "fixed.toml")
	if err := os.WriteFile(good, []byte("max_race_retries = 3\nnewer = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := f.check(good)
	if o.Outcome != outcome.ConfigOK || !hasWarn(o, outcome.WarnUnknownRepoConfigKey, "newer") ||
		!strings.Contains(o.Warnings[0].Detail, good+":2") {
		t.Fatalf("fixed file: %+v", o)
	}
	bad := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(bad, []byte("\nmax_race_retries = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o = f.check(bad)
	if o.Outcome != outcome.ConfigError || o.ConfigError.File != bad || o.ConfigError.Line != 2 || o.ConfigError.Layer != config.LayerRepoFile {
		t.Fatalf("bad file: %+v", o)
	}
	if o := f.check(filepath.Join(t.TempDir(), "missing.toml")); o.Outcome != outcome.ConfigError || o.ConfigError.Layer != config.LayerRepoFile {
		t.Fatalf("missing file: %+v", o)
	}
}

func TestCheckRelativeAndMissingCommands(t *testing.T) {
	f := newFx(t)
	f.commit("cfg", map[string]string{".pg-git-lander.toml": `
[[conflict]]
glob = "*.lock"
resolve = ["./update-deps.sh"]
verify = ["no-such-command-xyzzy"]

[[hooks.land.before]]
run = ["sh", "-c", "true"]
`})
	o := f.check("")
	if o.Outcome != outcome.ConfigOK {
		t.Fatalf("got %+v", o)
	}
	if !hasWarn(o, outcome.WarnRelativeCommandPath, "update-deps.sh") ||
		!hasWarn(o, outcome.WarnCommandNotFound, "./update-deps.sh") ||
		!hasWarn(o, outcome.WarnCommandNotFound, "no-such-command-xyzzy") {
		t.Fatalf("warnings = %+v", o.Warnings)
	}
	for _, w := range o.Warnings {
		if strings.Contains(w.Detail, "sh -c") || (w.Warning == outcome.WarnCommandNotFound && strings.Contains(w.Detail, " sh ")) {
			t.Fatalf("sh is on PATH: %+v", w)
		}
	}
	// Once the script exists on the primary branch, only the relative-path
	// warning remains for it.
	f.commit("script", map[string]string{"update-deps.sh": "#!/bin/sh\n"})
	if err := os.Chmod(filepath.Join(f.repo.Dir, "update-deps.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	o = f.check("")
	if !hasWarn(o, outcome.WarnRelativeCommandPath, "update-deps.sh") || hasWarn(o, outcome.WarnCommandNotFound, "./update-deps.sh") {
		t.Fatalf("after creating the script: %+v", o.Warnings)
	}
}

func TestCheckXDGCommandsAreChecked(t *testing.T) {
	f := newFx(t)
	f.writeXDG("[[defaults.conflict]]\nglob = \"*.lock\"\nresolve = [\"/no/such/absolute/tool\"]\n")
	o := f.check("")
	if o.Outcome != outcome.ConfigOK || !hasWarn(o, outcome.WarnCommandNotFound, "/no/such/absolute/tool") {
		t.Fatalf("got %+v", o)
	}
}

func TestCheckLossyKeepOursDriverFromConfigAndAttribute(t *testing.T) {
	f := newFx(t)
	f.git("config", "merge.flake-lock-keep-ours.driver", "true")
	f.git("config", "merge.flake-lock-keep-ours.name", "keep ours")
	f.commit("attrs", map[string]string{".gitattributes": "flake.lock merge=flake-lock-keep-ours\n"})
	o := f.check("")
	if o.Outcome != outcome.ConfigOK || !hasWarn(o, outcome.WarnLossyMergeDriver, "flake-lock-keep-ours") {
		t.Fatalf("got %+v", o)
	}
	if n := len(warnWords(o)); n != 1 {
		t.Fatalf("want exactly one warning, got %+v", o.Warnings)
	}
}

func TestCheckLossyDriverFromAttributeNameAlone(t *testing.T) {
	f := newFx(t)
	f.commit("attrs", map[string]string{".gitattributes": "*.lock merge=keep-ours\n"})
	if o := f.check(""); !hasWarn(o, outcome.WarnLossyMergeDriver, "keep-ours") {
		t.Fatalf("got %+v", o.Warnings)
	}
}

func TestCheckHarmlessMergeDriverDoesNotWarn(t *testing.T) {
	f := newFx(t)
	f.git("config", "merge.lockfile.driver", "my-merge %O %A %B")
	f.commit("attrs", map[string]string{".gitattributes": "flake.lock merge=lockfile\n*.txt merge=union\n"})
	if o := f.check(""); hasWarn(o, outcome.WarnLossyMergeDriver, "") {
		t.Fatalf("unexpected warning: %+v", o.Warnings)
	}
}

func TestCheckLossyDriverRecognisedByItsCommand(t *testing.T) {
	f := newFx(t)
	f.git("config", "merge.mine.driver", "true") // innocuous name, keeps %A whole
	f.commit("attrs", map[string]string{".gitattributes": "flake.lock merge=mine\n"})
	if o := f.check(""); !hasWarn(o, outcome.WarnLossyMergeDriver, "merge.mine.driver") {
		t.Fatalf("got %+v", o.Warnings)
	}
}
