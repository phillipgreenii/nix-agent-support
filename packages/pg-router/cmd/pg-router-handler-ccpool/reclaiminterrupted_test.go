package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/api/participant"
)

func TestReclaimInterrupted_dryRunListsRulesAndApplyRemovesExactlyThose(t *testing.T) {
	holding := filepath.Join(t.TempDir(), "w-interrupted")
	clean := filepath.Join(holding, "a-1")
	legacy := filepath.Join(holding, "legacy-1")
	for _, d := range []string{clean, legacy} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{"base_commit":"abc","branch":"b","status_porcelain":"","patch_file":"changes.patch","patch_bytes":0}`
	if err := os.WriteFile(filepath.Join(clean, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clean, "changes.patch"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if got := reclaimInterrupted([]string{"--dir", holding}, &out, &errb); got != participant.ExitOK {
		t.Fatalf("dry run exit %d: %s", got, errb.String())
	}
	dry := out.String()
	for _, want := range []string{"REMOVE " + clean, "empty patch and clean status", "KEEP " + legacy, "no manifest", "would remove 1, keep 1"} {
		if !strings.Contains(dry, want) {
			t.Errorf("dry run lacks %q:\n%s", want, dry)
		}
	}
	if _, err := os.Stat(clean); err != nil {
		t.Fatalf("dry run removed an entry: %v", err)
	}

	out.Reset()
	if got := reclaimInterrupted([]string{"--dir", holding, "--size-kb"}, &out, &errb); got != participant.ExitOK || strings.TrimSpace(out.String()) != "1" {
		t.Errorf("size-kb = %q exit %d", out.String(), got)
	}

	out.Reset()
	if got := reclaimInterrupted([]string{"--dir", holding, "--apply"}, &out, &errb); got != participant.ExitOK {
		t.Fatalf("apply exit %d: %s", got, errb.String())
	}
	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Errorf("clean entry survived apply: %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("no-manifest entry did not survive: %v", err)
	}
}

func TestReclaimInterrupted_usageErrors(t *testing.T) {
	var out, errb bytes.Buffer
	for _, args := range [][]string{{}, {"--dir", "x", "stray"}, {"--dir", "x", "--apply", "--size-kb"}, {"--bogus"}} {
		if got := reclaimInterrupted(args, &out, &errb); got != participant.ExitUsage {
			t.Errorf("%v: exit %d, want usage", args, got)
		}
	}
}
