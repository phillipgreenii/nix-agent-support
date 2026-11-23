package main

import (
	"bytes"
	"strings"
	"testing"
)

// parseCmuxBridgeArgs runs before anything dials the daemon, so a false
// proceed means nothing was started.
func TestParseCmuxBridgeArgsHelpPrintsUsageAndDoesNotStart(t *testing.T) {
	for _, flag := range []string{"-h", "--help", "-help"} {
		var out, errb bytes.Buffer
		proceed, code := parseCmuxBridgeArgs([]string{flag}, &out, &errb)
		if proceed || code != 0 {
			t.Fatalf("%s: proceed=%v code=%d, want false/0", flag, proceed, code)
		}
		if !strings.Contains(out.String(), "usage: pa-monitor cmux-bridge") {
			t.Errorf("%s: stdout missing usage: %q", flag, out.String())
		}
		if errb.Len() != 0 {
			t.Errorf("%s: unexpected stderr %q", flag, errb.String())
		}
	}
}

func TestParseCmuxBridgeArgsRejectsUnknownArguments(t *testing.T) {
	for _, args := range [][]string{{"--bogus"}, {"extra"}, {"-x", "y"}} {
		var out, errb bytes.Buffer
		proceed, code := parseCmuxBridgeArgs(args, &out, &errb)
		if proceed || code != exitUsage {
			t.Fatalf("%v: proceed=%v code=%d, want false/%d", args, proceed, code, exitUsage)
		}
		if !strings.Contains(errb.String(), "usage: pa-monitor cmux-bridge") || out.Len() != 0 {
			t.Errorf("%v: stderr=%q stdout=%q", args, errb.String(), out.String())
		}
	}
}

func TestParseCmuxBridgeArgsNoArgsProceeds(t *testing.T) {
	var out, errb bytes.Buffer
	if proceed, code := parseCmuxBridgeArgs(nil, &out, &errb); !proceed || code != 0 {
		t.Fatalf("proceed=%v code=%d, want true/0", proceed, code)
	}
}
