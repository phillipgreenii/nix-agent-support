package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/gate"
)

func TestParseGateSpecs(t *testing.T) {
	specs, err := parseGateSpecs([]string{"repo-a=sha1", "repo-b=sha2"})
	if err != nil {
		t.Fatalf("parseGateSpecs: %v", err)
	}
	if len(specs) != 2 || specs[0].Repo != "repo-a" || specs[0].Commit != "sha1" ||
		specs[1].Repo != "repo-b" || specs[1].Commit != "sha2" {
		t.Errorf("specs = %+v", specs)
	}
	for _, bad := range []string{"", "repo-a", "=sha1", "repo-a="} {
		if _, err := parseGateSpecs([]string{bad}); err == nil {
			t.Errorf("parseGateSpecs(%q): expected error", bad)
		}
	}
}

func TestReadBodyFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const body = "## Checks\n\n- run `tldr wsplan`\n"
	got, err := readBodyFile(write("checks.md", body))
	if err != nil || got != body {
		t.Errorf("readBodyFile = %q, %v; want the file verbatim", got, err)
	}
	// A named but empty file is a mistake, not a request for the derived text.
	for name, content := range map[string]string{"empty.md": "", "blank.md": " \n\t\n"} {
		if _, err := readBodyFile(write(name, content)); err == nil || !strings.Contains(err.Error(), "--body-file") {
			t.Errorf("%s: err = %v, want an error naming --body-file", name, err)
		}
	}
	if _, err := readBodyFile(filepath.Join(dir, "missing.md")); err == nil || !strings.Contains(err.Error(), "--body-file") {
		t.Errorf("missing file: err = %v, want an error naming --body-file", err)
	}
}

func TestAttachVerifiedChildCmd_contentFlagsAndHelp(t *testing.T) {
	cmd := newGateAttachVerifiedChildCmd()
	for _, name := range []string{"body-file", "acceptance"} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("--%s is not a flag of attach-verified-child", name)
		}
		if f.DefValue != "" {
			t.Errorf("--%s defaults to %q, want unset (the child's text is then derived)", name, f.DefValue)
		}
	}
	// The help lists the labels that are not copied from the one list the
	// behaviour reads, so it cannot drift from it.
	for _, l := range gate.WorkflowStateLabels {
		if !strings.Contains(cmd.Long, l) {
			t.Errorf("long help does not name the non-inherited label %q", l)
		}
	}
	if !strings.Contains(cmd.Long, "has-acceptance-criteria is always set") {
		t.Error("long help must say the child always carries has-acceptance-criteria")
	}
}

func TestAttachVerifiedChildCmd_requiredFlags(t *testing.T) {
	cmd := newGateAttachVerifiedChildCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{}) // nothing supplied
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected required-flag error")
	}
}
