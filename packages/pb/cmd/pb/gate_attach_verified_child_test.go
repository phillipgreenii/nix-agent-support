package main

import (
	"bytes"
	"errors"
	"fmt"
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

// TestWriteAttachResult pins what attach-verified-child prints (pg2-0jt3s): a
// result only once the child exists, so a failure before creation leaves stdout
// empty while a partial failure still reports the child and its gates.
func TestWriteAttachResult(t *testing.T) {
	partial := gate.AttachResult{
		ChildID: "pg2-child",
		Gates:   []gate.CreatedGate{{GateID: "pg2-gate1"}},
	}
	tests := []struct {
		name   string
		out    gate.AttachResult
		asJSON bool
		want   string
	}{
		{"json, nothing created", gate.AttachResult{}, true, ""},
		{"text, nothing created", gate.AttachResult{}, false, ""},
		{"json, child created", partial, true, `{"child":"pg2-child","gates":[{"gate-id":"pg2-gate1"`},
		{"text, child created", partial, false, "child=pg2-child gates=1\n"},
		{"json, child without gates", gate.AttachResult{ChildID: "pg2-child"}, true, `{"child":"pg2-child","gates":null`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeAttachResult(&buf, tt.out, tt.asJSON)
			got := buf.String()
			if tt.want == "" {
				if got != "" {
					t.Fatalf("stdout = %q, want empty", got)
				}
				return
			}
			if !strings.HasPrefix(got, tt.want) {
				t.Fatalf("stdout = %q, want prefix %q", got, tt.want)
			}
		})
	}
}

func TestAttachExitCode(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want int
	}{
		{errors.New("bead not found"), 1},
		{fmt.Errorf("%w: gate for r@c: x", gate.ErrGatingIncomplete), 3},
		{fmt.Errorf("%w: child: x", gate.ErrChildMayBeWorkable), 4},
	} {
		if got := attachExitCode(tt.err); got != tt.want {
			t.Errorf("attachExitCode(%v) = %d, want %d", tt.err, got, tt.want)
		}
	}
}
