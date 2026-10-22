package spec

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-wrap/internal/fail"
	"github.com/phillipgreenii/pg-wrap/internal/wrap"
)

const full = `
schema_version = 1
wrap = [
  "/usr/bin/env A=1",
  ["/usr/bin/env", "B=2"],
]
strict_env = true
keep = ["TMUX", "TMUX_PANE"]
exec = ["/usr/bin/true", "--flag"]
`

func TestDecodeFullSpec(t *testing.T) {
	s, err := Decode("s.toml", []byte(full))
	if err != nil {
		t.Fatal(err)
	}
	want := &Spec{
		SchemaVersion: 1,
		Wrap:          []wrap.Entry{{Shell: "/usr/bin/env A=1"}, {Argv: []string{"/usr/bin/env", "B=2"}}},
		StrictEnv:     true,
		Keep:          []string{"TMUX", "TMUX_PANE"},
		Exec:          []string{"/usr/bin/true", "--flag"},
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("got %#v\nwant %#v", s, want)
	}
}

func TestDecodeMinimalSpecIsValid(t *testing.T) {
	s, err := Decode("s.toml", []byte("schema_version = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Wrap) != 0 || s.StrictEnv || len(s.Keep) != 0 || len(s.Exec) != 0 {
		t.Fatalf("an empty wrap with no exec is valid and carries no defaults: %#v", s)
	}
	if got := s.Payload([]string{"a", "b"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("payload = %q", got)
	}
}

// Every shape the decoder refuses is a stage-spec failure with the spec's path.
func TestDecodeRefusals(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"unknown key", "schema_version = 1\nbogus = 1\n", "unknown key bogus"},
		{"unknown key in a newer-looking spec", "schema_version = 1\nwrapper = []\n", "unknown key wrapper"},
		{"wrong type: schema_version string", "schema_version = \"1\"\n", "schema_version"},
		{"wrong type: schema_version float", "schema_version = 1.5\n", "schema_version"},
		{"wrong type: strict_env string", "schema_version = 1\nstrict_env = \"yes\"\n", "strict_env"},
		{"wrong type: keep not a list", "schema_version = 1\nkeep = \"TMUX\"\n", "keep"},
		{"wrong type: keep element", "schema_version = 1\nkeep = [1]\n", "keep"},
		{"wrong type: exec not a list", "schema_version = 1\nexec = \"/bin/true\"\n", "exec"},
		{"wrong type: wrap a string", "schema_version = 1\nwrap = \"/usr/bin/env\"\n", "wrap"},
		{"wrong type: wrap element a number", "schema_version = 1\nwrap = [1]\n", "wrap[0]"},
		{"wrong type: wrap element a table", "schema_version = 1\nwrap = [{a = 1}]\n", "wrap[0]"},
		{"wrong type: array entry holds a number", "schema_version = 1\nwrap = [[\"/x\", 2]]\n", "wrap[0][1]"},
		{"missing schema_version", "strict_env = true\n", "schema_version is required"},
		{"empty file", "", "schema_version is required"},
		{"schema_version zero", "schema_version = 0\n", "not supported"},
		{"not toml", "schema_version = = 1\n", "s.toml"},
		{"empty string entry", "schema_version = 1\nwrap = [\"\"]\n", "empty string entry"},
		{"empty array entry", "schema_version = 1\nwrap = [[]]\n", "empty array entry"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decode("s.toml", []byte(c.text))
			var fe *fail.Error
			if !errors.As(err, &fe) {
				t.Fatalf("want a *fail.Error, got %v", err)
			}
			if fe.Stage != fail.StageSpec {
				t.Fatalf("stage = %q, want spec", fe.Stage)
			}
			if !strings.HasPrefix(err.Error(), "pg-wrap[spec]: ") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("message %q lacks the pg-wrap[spec]: prefix or %q", err, c.want)
			}
		})
	}
}

// A newer schema_version is refused by version, with the exact message naming
// both versions, even when the spec also carries keys this pg-wrap does not know.
func TestDecodeNewerSchemaVersionMessage(t *testing.T) {
	_, err := Decode("/etc/x.toml", []byte("schema_version = 2\nfuture_key = true\n"))
	want := "pg-wrap[spec]: /etc/x.toml has schema_version 2, this pg-wrap supports <= 1."
	if err == nil || err.Error() != want {
		t.Fatalf("got %v\nwant %s", err, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err == nil || !strings.HasPrefix(err.Error(), "pg-wrap[spec]: cannot read ") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadReadsAFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.toml")
	if err := os.WriteFile(p, []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p)
	if err != nil || s.SchemaVersion != 1 {
		t.Fatalf("%v %v", s, err)
	}
}

func TestPayloadPutsExecFirst(t *testing.T) {
	s := &Spec{Exec: []string{"/usr/bin/real", "--x"}}
	args := []string{"run", "--", "", "a b"}
	got := s.Payload(args)
	want := []string{"/usr/bin/real", "--x", "run", "--", "", "a b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	if len(s.Exec) != 2 {
		t.Fatal("Payload must not modify the spec")
	}
}

// Refs applies the absolute-path rule to every string entry's first word, every
// array entry's first element and exec's first element.
func TestRefsAbsolutePathRule(t *testing.T) {
	s := &Spec{
		Wrap: []wrap.Entry{
			{Shell: "/usr/bin/env A=1"},
			{Shell: "env A=1"},
			{Argv: []string{"relative", "x"}},
			{Argv: []string{"/usr/bin/env", "relative-later"}},
		},
		Exec: []string{"real"},
	}
	refs := s.Refs()
	if len(refs) != 5 {
		t.Fatalf("got %d refs, want 5", len(refs))
	}
	badAt := []bool{false, true, true, false, true}
	stages := []string{"layer 0", "layer 1", "layer 2", "layer 3", "spec"}
	for i, r := range refs {
		if (r.Err != nil) != badAt[i] {
			t.Errorf("ref %d (%s %q): err=%v", i, r.What, r.Path, r.Err)
		}
		if r.Stage != stages[i] {
			t.Errorf("ref %d stage = %q, want %q", i, r.Stage, stages[i])
		}
	}
	if refs[4].What != "exec" || refs[4].Path != "real" {
		t.Errorf("exec ref = %+v", refs[4])
	}
}

func TestPathProblem(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "exe")
	plain := filepath.Join(dir, "plain")
	link := filepath.Join(dir, "broken")
	okLink := filepath.Join(dir, "link-to-exe")
	for p, mode := range map[string]os.FileMode{exe: 0o755, plain: 0o644} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		// WriteFile honours the umask; set the mode explicitly.
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "no-such-target"), link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, okLink); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want string // substring of the problem; "" means no problem
	}{
		{exe, ""},
		{okLink, ""},
		{dir, "is a directory"},
		{link, "broken symlink"},
		{plain, "is not executable"},
		{filepath.Join(dir, "absent"), "no such file"},
	}
	for _, c := range cases {
		got := PathProblem(c.path)
		if c.want == "" {
			if got != "" {
				t.Errorf("%s: unexpected problem %q", c.path, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) || !strings.HasPrefix(got, c.path+":") {
			t.Errorf("%s: got %q, want it to start with the path and contain %q", c.path, got, c.want)
		}
	}
}
