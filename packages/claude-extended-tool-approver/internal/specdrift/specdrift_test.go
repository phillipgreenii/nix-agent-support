package specdrift

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/embeddedspecs"
)

// fixtureFS builds a tiny in-memory data/*.json fixture: one ordinary
// command ("jq") and both shell-builtin exemptions (cd, export) — enough to
// exercise CommandNames/Record/Check without touching the real 46-file
// embeddedspecs.FS or any real binary (this repo's own unit-test isolation
// rule: a test MUST generate its scenario in a temp/in-memory fixture, not
// mutate real tracked files).
func fixtureFS() fstest.MapFS {
	spec := func(name string) string {
		return `{"version":"v1","kind":"command","name":"` + name + `","command":{"name":"` + name + `","provenance":"test fixture","citations":{},"positionals":{"citation":{"source":"x"}},"stdin":"none","stdout":"none","unknownFlag":"reject"}}`
	}
	return fstest.MapFS{
		"data/jq.json":     {Data: []byte(spec("jq"))},
		"data/cd.json":     {Data: []byte(spec("cd"))},
		"data/export.json": {Data: []byte(spec("export"))},
		// help-hashes.json itself lives alongside the spec files in the real
		// data/ directory (embed.go's glob covers it) — CommandNames must
		// skip it (no "kind":"command"), not choke on or misparse it.
		"data/help-hashes.json": {Data: []byte(`{"cd":null,"export":null,"jq":"deadbeef"}`)},
	}
}

func TestSpecDriftCommandNamesSkipsHelpHashesFile(t *testing.T) {
	names, err := CommandNames(fixtureFS(), "data")
	if err != nil {
		t.Fatalf("CommandNames: %v", err)
	}
	want := []string{"cd", "export", "jq"}
	if len(names) != len(want) {
		t.Fatalf("CommandNames = %v, want %v", names, want)
	}
	for i, n := range want {
		if names[i] != n {
			t.Errorf("CommandNames[%d] = %q, want %q (full: %v)", i, names[i], n, names)
		}
	}
}

func TestSpecDriftRecordExemptsShellBuiltins(t *testing.T) {
	stub := func(name string) (string, error) { return "usage: " + name, nil }
	hashes, err := Record(fixtureFS(), "data", "linux", stub)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if hashes["cd"] != nil {
		t.Errorf("Record: cd = %v, want nil (exempt)", hashes["cd"])
	}
	if hashes["export"] != nil {
		t.Errorf("Record: export = %v, want nil (exempt)", hashes["export"])
	}
	if hashes["jq"] == nil || *hashes["jq"] != Hash("usage: jq") {
		t.Errorf("Record: jq = %v, want hash of %q", hashes["jq"], "usage: jq")
	}
}

// TestSpecDriftCheckDetectsDrift is this packet's required proof (Validation:
// "a test asserting the check DOES fail when a spec's recorded hash is
// deliberately mismatched against a stubbed/fixture --help capture"): a
// help-hashes.json entry that disagrees with the live (stubbed) --help
// text MUST be reported as a Drift.
func TestSpecDriftCheckDetectsDrift(t *testing.T) {
	liveHelp := "usage: jq [OPTIONS] FILTER"
	recorded := map[string]*string{
		"cd":     nil,
		"export": nil,
		"jq":     strPtr("0000000000000000000000000000000000000000000000000000000000000000"), // deliberately wrong
	}
	stub := func(name string) (string, error) {
		if name == "jq" {
			return liveHelp, nil
		}
		return "usage: " + name, nil
	}

	drifts, err := Check(fixtureFS(), "data", "linux", recorded, stub)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(drifts) != 1 {
		t.Fatalf("Check drifts = %v, want exactly 1 (jq mismatch)", drifts)
	}
	if drifts[0].Name != "jq" {
		t.Errorf("Check drift.Name = %q, want %q", drifts[0].Name, "jq")
	}

	// Negative control: the correct hash produces no drift.
	correct := Hash(liveHelp)
	recorded["jq"] = &correct
	drifts, err = Check(fixtureFS(), "data", "linux", recorded, stub)
	if err != nil {
		t.Fatalf("Check (clean): %v", err)
	}
	if len(drifts) != 0 {
		t.Fatalf("Check (clean) drifts = %v, want none", drifts)
	}
}

func TestSpecDriftCheckReportsMissingEntry(t *testing.T) {
	recorded := map[string]*string{
		"cd":     nil,
		"export": nil,
		// jq intentionally absent.
	}
	stub := func(name string) (string, error) { return "usage: " + name, nil }
	drifts, err := Check(fixtureFS(), "data", "linux", recorded, stub)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(drifts) != 1 || drifts[0].Name != "jq" || drifts[0].Reason != "missing from help-hashes.json" {
		t.Fatalf("Check drifts = %+v, want one jq/\"missing from help-hashes.json\" drift", drifts)
	}
}

func TestSpecDriftCheckReportsUnreachableBinary(t *testing.T) {
	recorded := map[string]*string{
		"cd":     nil,
		"export": nil,
		"jq":     strPtr(Hash("usage: jq")),
	}
	stub := func(name string) (string, error) {
		if name == "jq" {
			return "", errNotFound
		}
		return "usage: " + name, nil
	}
	drifts, err := Check(fixtureFS(), "data", "linux", recorded, stub)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(drifts) != 1 || drifts[0].Name != "jq" {
		t.Fatalf("Check drifts = %+v, want one jq drift", drifts)
	}
}

// TestSpecDriftCommandNamesCoversRealEmbeddedSet exercises CommandNames
// against the REAL embeddedspecs.FS (packet 1.2's 46 built-in specs), not
// the fixture above — the regression this guards is exactly the pitfall
// doc.go's "Command-name source" section documents: reading via
// specfmt.Repository/Validate instead of the raw files would silently drop
// bash/sh (both fail Validate today — internal/speclint/lint.go's
// LintInvalid doc comment). All 46 names, including bash/sh and "[", must
// come back, and cd/export (Exempt) must be among them.
func TestSpecDriftCommandNamesCoversRealEmbeddedSet(t *testing.T) {
	names, err := CommandNames(embeddedspecs.FS, "data")
	if err != nil {
		t.Fatalf("CommandNames(embeddedspecs.FS): %v", err)
	}
	if len(names) != 46 {
		t.Fatalf("CommandNames(embeddedspecs.FS) = %d names, want 46: %v", len(names), names)
	}
	want := map[string]bool{"bash": false, "sh": false, "[": false, "cd": false, "export": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Errorf("CommandNames(embeddedspecs.FS) missing expected name %q", n)
		}
	}
}

func strPtr(s string) *string { return &s }

type stubErr string

func (e stubErr) Error() string { return string(e) }

const errNotFound = stubErr("exec: \"jq\": executable file not found in $PATH")

func psFixtureFS() fstest.MapFS {
	fsys := fixtureFS()
	spec := fsys["data/jq.json"].Data
	for _, n := range []string{"ps", "pgrep"} {
		fsys["data/"+n+".json"] = &fstest.MapFile{Data: []byte(strings.ReplaceAll(string(spec), `"jq"`, `"`+n+`"`))}
	}
	return fsys
}

func TestIsExemptByPlatform(t *testing.T) {
	cases := []struct {
		goos, name string
		want       bool
	}{
		{"linux", "cd", true},
		{"darwin", "export", true},
		{"darwin", "ps", true},
		{"darwin", "pgrep", true},
		{"linux", "ps", false},
		{"linux", "pgrep", false},
		{"darwin", "bash", false},
	}
	for _, c := range cases {
		if got := IsExempt(c.goos, c.name); got != c.want {
			t.Errorf("IsExempt(%q, %q) = %v, want %v", c.goos, c.name, got, c.want)
		}
	}
}

func TestSpecDriftPlatformExemptNeverCaptured(t *testing.T) {
	recorded := map[string]*string{
		"cd": nil, "export": nil,
		"jq":    strPtr(Hash("usage: jq")),
		"ps":    strPtr("0000"), // linux hash, meaningless on darwin
		"pgrep": strPtr("0000"),
	}
	var captured []string
	stub := func(name string) (string, error) {
		captured = append(captured, name)
		if name == "ps" || name == "pgrep" {
			return "", errNotFound
		}
		return "usage: " + name, nil
	}

	drifts, err := Check(psFixtureFS(), "data", "darwin", recorded, stub)
	if err != nil {
		t.Fatalf("Check darwin: %v", err)
	}
	if len(drifts) != 0 {
		t.Fatalf("Check darwin drifts = %+v, want none", drifts)
	}
	for _, n := range captured {
		if n == "ps" || n == "pgrep" {
			t.Errorf("Check darwin captured exempt command %q", n)
		}
	}

	// Same inputs on linux: ps/pgrep are checked (unreachable binary here).
	drifts, err = Check(psFixtureFS(), "data", "linux", recorded, stub)
	if err != nil {
		t.Fatalf("Check linux: %v", err)
	}
	if len(drifts) != 2 || drifts[0].Name != "pgrep" || drifts[1].Name != "ps" {
		t.Fatalf("Check linux drifts = %+v, want pgrep and ps", drifts)
	}

	captured = nil
	hashes, err := Record(psFixtureFS(), "data", "darwin", stub)
	if err != nil {
		t.Fatalf("Record darwin: %v", err)
	}
	if hashes["ps"] != nil || hashes["pgrep"] != nil {
		t.Errorf("Record darwin ps/pgrep = %v/%v, want nil", hashes["ps"], hashes["pgrep"])
	}
	for _, n := range captured {
		if n == "ps" || n == "pgrep" {
			t.Errorf("Record darwin captured exempt command %q", n)
		}
	}
}

func TestLoadPlatformHashesAndOverlay(t *testing.T) {
	fsys := fstest.MapFS{
		"data/help-hashes.json":        {Data: []byte(`{"jq":"aa","ps":"bb"}`)},
		"data/help-hashes.darwin.json": {Data: []byte(`{"jq":"cc"}`)},
	}
	got, err := LoadPlatformHashes(fsys, "data", "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if *got["jq"] != "cc" || *got["ps"] != "bb" {
		t.Errorf("darwin merged = jq %q ps %q, want cc/bb", *got["jq"], *got["ps"])
	}
	got, err = LoadPlatformHashes(fsys, "data", "linux")
	if err != nil {
		t.Fatal(err)
	}
	if *got["jq"] != "aa" {
		t.Errorf("linux jq = %q, want aa (no overlay)", *got["jq"])
	}

	base := map[string]*string{"jq": strPtr("aa"), "ps": strPtr("bb"), "sh": strPtr("dd")}
	rec := map[string]*string{"jq": strPtr("cc"), "ps": nil, "sh": strPtr("dd"), "bash": strPtr("ee")}
	ov := Overlay("darwin", base, rec)
	if len(ov) != 2 || *ov["jq"] != "cc" || *ov["bash"] != "ee" {
		t.Errorf("Overlay = %v, want only jq=cc and bash=ee", ov)
	}
}
