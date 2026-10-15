package main

import (
	"bytes"
	"strings"
	"testing"
)

// helpArgs is every command whose --help the package documents.
var helpArgs = [][]string{
	{},
	{"acquire"},
	{"release"},
	{"run"},
	{"status"},
	{"pause"},
	{"resume"},
	{"config"},
	{"config", "check"},
	{"keeper"},
	{"anchor"},
}

func TestHelpPrintsUsageToStdoutAndExitsZero(t *testing.T) {
	for _, base := range helpArgs {
		for _, flagName := range []string{"--help", "-h"} {
			args := append(append([]string{}, base...), flagName)
			var out, errOut bytes.Buffer
			if code := run(args, &out, &errOut); code != exitOK {
				t.Errorf("%v: exit %d, want 0", args, code)
			}
			if !strings.Contains(out.String(), "usage: pg-keep-awake") {
				t.Errorf("%v: stdout has no usage line:\n%s", args, out.String())
			}
			if errOut.Len() != 0 {
				t.Errorf("%v: stderr is not empty: %q", args, errOut.String())
			}
		}
	}
}

func TestHelpLabelsLongRunningModes(t *testing.T) {
	for _, cmd := range []string{"keeper", "anchor"} {
		var out, errOut bytes.Buffer
		run([]string{cmd, "--help"}, &out, &errOut)
		if !strings.Contains(out.String(), "not for interactive use") {
			t.Errorf("%s --help does not say it is not for interactive use:\n%s", cmd, out.String())
		}
	}
	var out, errOut bytes.Buffer
	run([]string{"--help"}, &out, &errOut)
	if !strings.Contains(out.String(), "not for interactive use") {
		t.Errorf("top-level help does not group keeper and anchor as not for interactive use")
	}
}

func TestHelpCommandMatchesDashHelp(t *testing.T) {
	var viaHelp, viaFlag, errOut bytes.Buffer
	run([]string{"help", "config", "check"}, &viaHelp, &errOut)
	run([]string{"config", "check", "--help"}, &viaFlag, &errOut)
	if viaHelp.String() != viaFlag.String() || viaHelp.Len() == 0 {
		t.Errorf("help config check differs from config check --help:\n%s\nvs\n%s", viaHelp.String(), viaFlag.String())
	}
}

func TestMistakeStillExitsTwoWithUsageOnStderr(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"acquire", "--bogus"}, &out, &errOut); code != exitUsage {
		t.Errorf("exit %d, want %d", code, exitUsage)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "usage: pg-keep-awake acquire") {
		t.Errorf("stdout=%q stderr=%q", out.String(), errOut.String())
	}
}
