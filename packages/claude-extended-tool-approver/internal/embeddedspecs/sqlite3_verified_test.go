package embeddedspecs

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Regression guard for the sqlite3 spec facts (tc-9brrd, verifying
// tc-o14i5.3.5). The first citations were recalled from sqlite.org prose, not
// checked against a binary; two were wrong (`-init FILE` is really `-init
// FILENAME`, and `-header`/`-noheader` is really one `-[no]header` line). The
// pinned fixture testdata/sqlite3-help.txt is the real `sqlite3 -help` of
// SQLite 3.51.2 (nixpkgs#sqlite). TestSqlite3CitationsMatchPinnedHelp always
// runs; TestSqlite3RealBinary additionally runs against a real binary when one
// is available (SQLITE3_BIN, else PATH), in a hermetic temp dir, and skips
// otherwise.

var quotedCitation = regexp.MustCompile(`: "((?:[^"\\]|\\.)*)"$`)

func normWS(s string) string { return strings.Join(strings.Fields(s), " ") }

// helpLinesNormalized returns each help line whitespace-collapsed.
func helpLinesNormalized(help string) []string {
	var out []string
	for _, l := range strings.Split(help, "\n") {
		if n := normWS(l); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func TestSqlite3CitationsMatchPinnedHelp(t *testing.T) {
	specs, err := LoadSpecs(dataDir)
	if err != nil {
		t.Fatalf("LoadSpecs: %v", err)
	}
	spec, ok := specs["sqlite3"]
	if !ok || spec.Command == nil {
		t.Fatal("no sqlite3 command spec in data/")
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "sqlite3-help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	help := helpLinesNormalized(string(raw))

	cmd := spec.Command
	if strings.Contains(cmd.Provenance, "no sqlite3 binary") || !strings.Contains(cmd.Provenance, "verified") {
		t.Errorf("provenance must record a real-binary verification, got %q", cmd.Provenance)
	}
	if len(cmd.Flags) == 0 {
		t.Fatal("sqlite3 spec has no flags")
	}
	for flag, fs := range cmd.Flags {
		src := fs.Citation.Source
		if strings.Contains(src, "recalled") || strings.Contains(src, "no sqlite3 binary") {
			t.Errorf("flag %s citation is unverified recall: %q", flag, src)
		}
		m := quotedCitation.FindStringSubmatch(src)
		if m == nil {
			t.Errorf("flag %s citation lacks a quoted help line: %q", flag, src)
			continue
		}
		quoted := normWS(strings.ReplaceAll(m[1], `\"`, `"`))
		if !containsLine(help, quoted) {
			t.Errorf("flag %s: cited help line %q is not a line of the real `sqlite3 -help`", flag, quoted)
		}
		// The option must be named by the cited line.
		name := strings.TrimPrefix(flag, "-")
		if !strings.Contains(quoted, "-"+name+" ") && !strings.Contains(quoted, "-[no]"+strings.TrimPrefix(name, "no")+" ") {
			t.Errorf("flag %s is not named by its cited line %q", flag, quoted)
		}
	}

	// Verified role facts: -init runs a script (config source), -cmd runs
	// text (exec-opaque); both verified behaviorally in TestSqlite3RealBinary.
	if k := cmd.Flags["-init"].Operand.Kind; k != "config-source" {
		t.Errorf("-init operand kind = %q, want config-source", k)
	}
	if k := cmd.Flags["-cmd"].Operand.Kind; k != "exec-opaque" {
		t.Errorf("-cmd operand kind = %q, want exec-opaque", k)
	}
	if cmd.UnknownFlag != "insufficient" {
		t.Errorf("unknownFlag = %q, want insufficient", cmd.UnknownFlag)
	}
}

func realSqlite3(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("SQLITE3_BIN")
	if bin == "" {
		p, err := exec.LookPath("sqlite3")
		if err != nil {
			t.Skip("no real sqlite3 (set SQLITE3_BIN or put sqlite3 on PATH; e.g. nix shell nixpkgs#sqlite)")
		}
		bin = p
	}
	return bin
}

func runSqlite3(t *testing.T, bin, dir string, args ...string) (string, int) {
	t.Helper()
	c := exec.Command(bin, args...)
	c.Dir = dir
	c.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin", "TMPDIR=" + dir}
	out, err := c.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

func TestSqlite3RealBinary(t *testing.T) {
	bin := realSqlite3(t)
	dir := t.TempDir()

	specs, err := LoadSpecs(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	spec := specs["sqlite3"]
	if spec.Command == nil {
		t.Fatal("no sqlite3 spec")
	}

	// Every cited help line exists in the real binary's -help output.
	helpOut, _ := runSqlite3(t, bin, dir, "-help")
	help := helpLinesNormalized(helpOut)
	for flag, fs := range spec.Command.Flags {
		m := quotedCitation.FindStringSubmatch(fs.Citation.Source)
		if m == nil {
			t.Errorf("flag %s: no quoted help line", flag)
			continue
		}
		quoted := normWS(strings.ReplaceAll(m[1], `\"`, `"`))
		if !containsLine(help, quoted) {
			t.Errorf("flag %s: %q not in real `sqlite3 -help` (installed version drifted?)", flag, quoted)
		}
	}

	// -init FILENAME executes the file's dot-commands (.shell): config source
	// that runs code.
	initFile := filepath.Join(dir, "init.sql")
	if err := os.WriteFile(initFile, []byte(".shell echo INITRAN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := runSqlite3(t, bin, dir, "-init", initFile, ":memory:", "select 1"); code != 0 || !strings.Contains(out, "INITRAN") {
		t.Errorf("-init did not run .shell: code=%d out=%q", code, out)
	}
	// -cmd COMMAND executes a dot-command before the SQL operands.
	if out, code := runSqlite3(t, bin, dir, "-cmd", ".shell echo CMDRAN", ":memory:", "select 1"); code != 0 || !strings.Contains(out, "CMDRAN") {
		t.Errorf("-cmd did not run .shell: code=%d out=%q", code, out)
	}
	// Unknown option: nonzero exit and an error (UnknownFlag insufficient).
	if out, code := runSqlite3(t, bin, dir, "-definitely-not-an-option"); code == 0 || !strings.Contains(out, "unknown option") {
		t.Errorf("unknown option: code=%d out=%q", code, out)
	}
	// FILENAME is created when absent (PathModify) ...
	if _, code := runSqlite3(t, bin, dir, "created.db", "select 1"); code != 0 {
		t.Errorf("creating invocation failed")
	}
	if _, err := os.Stat(filepath.Join(dir, "created.db")); err != nil {
		t.Errorf("FILENAME not created: %v", err)
	}
	// ... but -readonly on a missing file does not create it (the spec
	// conservatively keeps PathModify for the positional).
	if _, code := runSqlite3(t, bin, dir, "-readonly", "ro.db", "select 1"); code == 0 {
		t.Error("-readonly on a missing file unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "ro.db")); err == nil {
		t.Error("-readonly created the database file")
	}
}
