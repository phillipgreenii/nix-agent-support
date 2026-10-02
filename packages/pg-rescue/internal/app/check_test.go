package app

import (
	"strings"
	"testing"
)

func TestCheckListsHandlersAndChains(t *testing.T) {
	h := newHarness(t, goodConfig)
	h.bins = map[string]string{
		"pg-rescue-claude": "/nix/store/x/bin/pg-rescue-claude",
		"pg-rescue-notify": "/nix/store/x/bin/pg-rescue-notify",
		"pg-rescue-bead":   "/nix/store/x/bin/pg-rescue-bead",
	}
	code, stdout, stderr := h.run("check", "--config", h.cfgPath)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	want := "config: " + h.cfgPath + "\n" +
		"handler fix-large: /nix/store/x/bin/pg-rescue-claude\n" +
		"handler fix-small: /nix/store/x/bin/pg-rescue-claude\n" +
		"  description: haiku\n" +
		"  tags: agent\n" +
		"handler notify: /nix/store/x/bin/pg-rescue-notify\n" +
		"handler p1-later: /nix/store/x/bin/pg-rescue-bead\n" +
		"chain sync: fix-small, fix-large, p1-later, notify\n"
	if stdout != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", stdout, want)
	}
	if len(h.rec.calls) != 0 {
		t.Error("check must never run a chain")
	}
	if dirs := h.runDirs(); len(dirs) != 0 {
		t.Errorf("check must not create run directories: %v", dirs)
	}
}

func TestCheckExit1WhenABinaryIsMissing(t *testing.T) {
	h := newHarness(t, goodConfig)
	h.bins = map[string]string{"pg-rescue-claude": "/b/claude", "pg-rescue-bead": "/b/bead"}
	code, stdout, _ := h.run("check", "--config", h.cfgPath)
	if code != 1 {
		t.Errorf("exit = %d; want 1", code)
	}
	contains(t, "stdout", stdout, "handler notify: NOT FOUND (pg-rescue-notify)\n", "chain sync:")
}

func TestCheckExit70OnConfigError(t *testing.T) {
	h := newHarness(t, "[handler.h]\n")
	code, stdout, stderr := h.run("check", "--config", h.cfgPath)
	if code != 70 || stdout != "" {
		t.Errorf("code=%d stdout=%q", code, stdout)
	}
	contains(t, "stderr", stderr, "pg-rescue: ", "[handler.h]", `missing required key "command"`)
}

func TestCheckExit70OnMissingConfigAndBadArgs(t *testing.T) {
	h := newHarness(t, "")
	for name, args := range map[string][]string{
		"missing file": {"check", "--config", h.cfgPath},
		"bad flag":     {"check", "--nope"},
		"positional":   {"check", "sync"},
	} {
		if code, stdout, _ := h.run(args...); code != 70 || stdout != "" {
			t.Errorf("%s: code=%d stdout=%q", name, code, stdout)
		}
	}
}

func TestCheckChainFilterListsOnlyThatChain(t *testing.T) {
	text := goodConfig + "\n[chain.other]\nhandlers = [\"notify\"]\n"
	h := newHarness(t, text)
	_, all, _ := h.run("check", "--config", h.cfgPath)
	contains(t, "unfiltered", all, "chain other: notify\n", "chain sync:")

	_, only, _ := h.run("check", "--config", h.cfgPath, "--chain", "other")
	contains(t, "filtered", only, "chain other: notify\n", "handler fix-small:")
	if strings.Contains(only, "chain sync") {
		t.Errorf("--chain other still lists chain sync:\n%s", only)
	}
}

func TestCheckUnknownChainListsValidOnes(t *testing.T) {
	h := newHarness(t, goodConfig)
	code, stdout, stderr := h.run("check", "--config", h.cfgPath, "--chain", "snyc")
	if code != 70 || stdout != "" {
		t.Errorf("code=%d stdout=%q", code, stdout)
	}
	contains(t, "stderr", stderr, `unknown chain "snyc" in --chain; configured: sync`)
}
