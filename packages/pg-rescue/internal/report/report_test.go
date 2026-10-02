package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
)

const (
	schemaPath  = "../../schemas/report.schema.json"
	fixturesDir = "../../testdata/reports"
)

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func schema(t *testing.T) *schemaValidator {
	t.Helper()
	return newSchemaValidator(t, readFile(t, schemaPath))
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	return readFile(t, filepath.Join(fixturesDir, name+".json"))
}

func decode(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSchemaVersionIsOne(t *testing.T) {
	if SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d; the schema file pins const 1", SchemaVersion)
	}
}

// The golden reports validate against the JSON Schema.
func TestGoldenReportsValidateAgainstSchema(t *testing.T) {
	v := schema(t)
	for _, name := range []string{"first-attempt", "three-prior-attempts", "stdin-mode"} {
		t.Run(name, func(t *testing.T) {
			if errs := v.Validate(fixture(t, name)); len(errs) != 0 {
				t.Errorf("%s does not validate:\n%s", name, strings.Join(errs, "\n"))
			}
		})
	}
}

func TestUnknownSchemaVersionFixtureIsRejectedByTheSchema(t *testing.T) {
	errs := schema(t).Validate(fixture(t, "unknown-schema-version"))
	if len(errs) == 0 {
		t.Fatal("schema_version 2 must not validate against the version 1 schema")
	}
	if !strings.Contains(strings.Join(errs, "\n"), "schema_version") {
		t.Errorf("violations should name schema_version, got %v", errs)
	}
}

// The Go types and the JSON Schema describe the same document: decoding a
// golden report into Report and encoding it back loses and adds nothing.
func TestGoldenReportsRoundTripThroughTheGoTypes(t *testing.T) {
	for _, name := range []string{"first-attempt", "three-prior-attempts", "stdin-mode"} {
		t.Run(name, func(t *testing.T) {
			orig := fixture(t, name)
			var r Report
			dec := json.NewDecoder(strings.NewReader(string(orig)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&r); err != nil {
				t.Fatalf("decode (unknown fields disallowed): %v", err)
			}
			again, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decode(t, orig), decode(t, again)) {
				t.Errorf("round trip changed the document:\n orig: %s\n  got: %s", orig, again)
			}
			if errs := schema(t).Validate(again); len(errs) != 0 {
				t.Errorf("re-encoded report no longer validates: %v", errs)
			}
		})
	}
}

// Every shape the wrapper can build validates, including the null-valued
// optional fields.
func TestConstructedReportsValidate(t *testing.T) {
	started := time.Date(2026, 10, 2, 14, 3, 11, 0, time.UTC)
	chain := "sync"
	verify := "true"
	parent := "20261002T135900Z-0b1c2d3e"
	vms := int64(900)
	argv := Report{
		SchemaVersion: SchemaVersion, RunID: "20261002T140311Z-7f3a9c2e", ParentRunID: &parent, Depth: 2,
		Chain: &chain, Handlers: []string{"a", "b"}, Mode: ModeArgv,
		Command:     &Command{Argv: []string{"git", "pull"}, Cwd: "/r", Exit: 130},
		Fingerprint: Fingerprint(ModeArgv, []string{"git", "pull"}, "/r", ""), OutputFile: "/r/out", Context: "", Verify: &verify,
		Host: "h", StartedAt: started,
		Attempts: []Attempt{{
			Handler: "a", Position: 1, Tags: []string{}, Outcome: contract.Failed, Reason: "exit 1", Exit: 1, DurationMS: 5,
			VerifyMS: &vms, StderrFile: "/r/e", VerifyOutputFile: "/r/v",
			Reported: contract.Reported{Summary: "s", Details: "d", DetailsFile: "/r/d", Meta: json.RawMessage(`{"k":1}`)},
		}},
	}
	stdin := Report{
		SchemaVersion: SchemaVersion, RunID: "20261002T140311Z-7f3a9c2e", Depth: 1, Handlers: []string{"a"}, Mode: ModeStdin,
		Fingerprint: Fingerprint(ModeStdin, nil, "/r", "c"), OutputFile: "/r/out", Verify: &verify, Host: "h", StartedAt: started,
		Attempts: []Attempt{},
	}
	v := schema(t)
	for name, r := range map[string]Report{"argv with everything set": argv, "stdin with nulls": stdin} {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if errs := v.Validate(b); len(errs) != 0 {
			t.Errorf("%s: %v\n%s", name, errs, b)
		}
	}
	if b, _ := json.Marshal(stdin); !strings.Contains(string(b), `"command":null`) || !strings.Contains(string(b), `"chain":null`) || !strings.Contains(string(b), `"parent_run_id":null`) {
		t.Errorf("stdin report must carry explicit nulls, got %s", b)
	}
}

// The schema is not vacuous: each defect in a valid report is caught.
func TestSchemaRejectsDefects(t *testing.T) {
	v := schema(t)
	clone := func() map[string]any { return decode(t, fixture(t, "three-prior-attempts")).(map[string]any) }

	top := []string{
		"schema_version", "run_id", "parent_run_id", "depth", "chain", "handlers", "mode", "command", "fingerprint",
		"output_file", "output_tail", "context", "verify", "host", "started_at", "attempts",
	}
	for _, key := range top {
		m := clone()
		delete(m, key)
		if errs := v.Validate(mustJSON(t, m)); len(errs) == 0 {
			t.Errorf("a report without %q must be invalid", key)
		}
	}
	for _, key := range []string{"handler", "position", "tags", "outcome", "reason", "exit", "duration_ms", "stderr_file", "reported"} {
		m := clone()
		delete(m["attempts"].([]any)[0].(map[string]any), key)
		if errs := v.Validate(mustJSON(t, m)); len(errs) == 0 {
			t.Errorf("an attempt without %q must be invalid", key)
		}
	}
	for _, key := range []string{"argv", "cwd", "exit"} {
		m := clone()
		delete(m["command"].(map[string]any), key)
		if errs := v.Validate(mustJSON(t, m)); len(errs) == 0 {
			t.Errorf("a command without %q must be invalid", key)
		}
	}

	mutations := map[string]func(m map[string]any){
		"unknown top-level field": func(m map[string]any) { m["extra"] = 1 },
		"unknown attempt field":   func(m map[string]any) { m["attempts"].([]any)[0].(map[string]any)["extra"] = 1 },
		"unknown reported field": func(m map[string]any) {
			m["attempts"].([]any)[1].(map[string]any)["reported"].(map[string]any)["extra"] = 1
		},
		"unknown command field":    func(m map[string]any) { m["command"].(map[string]any)["extra"] = 1 },
		"depth zero":               func(m map[string]any) { m["depth"] = 0 },
		"depth a string":           func(m map[string]any) { m["depth"] = "1" },
		"bad run id":               func(m map[string]any) { m["run_id"] = "nope" },
		"bad fingerprint":          func(m map[string]any) { m["fingerprint"] = "sha256:abc" },
		"fingerprint without alg":  func(m map[string]any) { m["fingerprint"] = strings.Repeat("a", 64) },
		"mode unknown":             func(m map[string]any) { m["mode"] = "pty" },
		"argv mode with null cmd":  func(m map[string]any) { m["command"] = nil },
		"stdin mode with a cmd":    func(m map[string]any) { m["mode"] = "stdin" },
		"empty handlers":           func(m map[string]any) { m["handlers"] = []any{} },
		"empty argv":               func(m map[string]any) { m["command"].(map[string]any)["argv"] = []any{} },
		"argv holds a number":      func(m map[string]any) { m["command"].(map[string]any)["argv"] = []any{1} },
		"attempts null":            func(m map[string]any) { m["attempts"] = nil },
		"attempt position zero":    func(m map[string]any) { m["attempts"].([]any)[0].(map[string]any)["position"] = 0 },
		"attempt outcome unknown":  func(m map[string]any) { m["attempts"].([]any)[0].(map[string]any)["outcome"] = "ok" },
		"attempt negative time":    func(m map[string]any) { m["attempts"].([]any)[0].(map[string]any)["duration_ms"] = -1 },
		"attempt empty reason":     func(m map[string]any) { m["attempts"].([]any)[0].(map[string]any)["reason"] = "" },
		"attempt tags not strings": func(m map[string]any) { m["attempts"].([]any)[0].(map[string]any)["tags"] = []any{1} },
		"meta not an object": func(m map[string]any) {
			m["attempts"].([]any)[1].(map[string]any)["reported"].(map[string]any)["meta"] = []any{}
		},
		"summary not a string": func(m map[string]any) {
			m["attempts"].([]any)[0].(map[string]any)["reported"].(map[string]any)["summary"] = 1
		},
		"started_at without Z":  func(m map[string]any) { m["started_at"] = "2026-10-02T14:03:11+02:00" },
		"started_at not a time": func(m map[string]any) { m["started_at"] = "yesterday" },
		"verify a number":       func(m map[string]any) { m["verify"] = 1 },
		"chain empty":           func(m map[string]any) { m["chain"] = "" },
	}
	for name, mutate := range mutations {
		m := clone()
		mutate(m)
		if errs := v.Validate(mustJSON(t, m)); len(errs) == 0 {
			t.Errorf("%s: report should be invalid", name)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The fingerprint is sha256 over NUL-terminated parts; an independent
// construction and a hard-coded vector pin it down.
func TestFingerprintArgvMode(t *testing.T) {
	got := Fingerprint(ModeArgv, []string{"git", "pull", "--rebase"}, "/abs/repo", "ignored context")
	const want = "sha256:a7b9c1af7f9a0f73d83ba7bacf6b5b903893657780f6202ef166538d8461fc78" // printf 'git\0pull\0--rebase\0/abs/repo\0' | shasum -a 256
	if got != want {
		t.Errorf("argv fingerprint = %s; want %s", got, want)
	}
	sum := sha256.Sum256([]byte("git\x00pull\x00--rebase\x00/abs/repo\x00"))
	if got != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Error("argv fingerprint is not sha256 over each argv element + NUL, then cwd + NUL")
	}
}

func TestFingerprintStdinMode(t *testing.T) {
	got := Fingerprint(ModeStdin, []string{"ignored", "argv"}, "/abs/repo", "check the sync log")
	const want = "sha256:b6d49e0ad3815d149cfd23f2b69940408ea4a1610685a46bb69cc76215b4c6da" // printf 'stdin\0/abs/repo\0check the sync log\0' | shasum -a 256
	if got != want {
		t.Errorf("stdin fingerprint = %s; want %s", got, want)
	}
	if got != Fingerprint(ModeStdin, nil, "/abs/repo", "check the sync log") {
		t.Error("argv must not matter in stdin mode")
	}
}

func TestFingerprintDistinguishesWhatMatters(t *testing.T) {
	base := Fingerprint(ModeArgv, []string{"git", "pull"}, "/r", "")
	for name, other := range map[string]string{
		"different cwd":               Fingerprint(ModeArgv, []string{"git", "pull"}, "/s", ""),
		"different argv":              Fingerprint(ModeArgv, []string{"git", "fetch"}, "/r", ""),
		"extra argv":                  Fingerprint(ModeArgv, []string{"git", "pull", "-q"}, "/r", ""),
		"argv boundaries are NUL-ed":  Fingerprint(ModeArgv, []string{"gitpull"}, "/r", ""),
		"argv/cwd boundary":           Fingerprint(ModeArgv, []string{"git", "pull/r"}, "", ""),
		"stdin mode":                  Fingerprint(ModeStdin, nil, "/r", ""),
		"stdin differs by context":    Fingerprint(ModeStdin, nil, "/r", "x"),
		"stdin is not an argv called": Fingerprint(ModeArgv, []string{"stdin"}, "/r", ""),
	} {
		if other == base {
			t.Errorf("%s: fingerprints must differ", name)
		}
	}
	if Fingerprint(ModeArgv, []string{"git", "pull"}, "/r", "context A") != Fingerprint(ModeArgv, []string{"git", "pull"}, "/r", "context B") {
		t.Error("context is not an input in argv mode")
	}
	if !strings.HasPrefix(base, "sha256:") || len(base) != len("sha256:")+64 {
		t.Errorf("fingerprint shape: %q", base)
	}
	if Fingerprint(ModeStdin, nil, "/r", "a") == Fingerprint(ModeStdin, nil, "/r", "b") {
		t.Error("context is an input in stdin mode")
	}
}

func TestQuoteArgv(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{nil, ""},
		{[]string{"git", "pull", "--rebase"}, "git pull --rebase"},
		{[]string{"echo", "hello world"}, "echo 'hello world'"},
		{[]string{"echo", ""}, "echo ''"},
		{[]string{"echo", "it's"}, `echo 'it'\''s'`},
		{[]string{"sh", "-c", "echo $HOME; ls"}, "sh -c 'echo $HOME; ls'"},
		{[]string{"a=b", "x@y.z:1,2", "/p/a_t-h+%"}, "a=b x@y.z:1,2 /p/a_t-h+%"},
		{[]string{"é"}, "'é'"},
		{[]string{"line\nbreak"}, "'line\nbreak'"},
	} {
		if got := QuoteArgv(tc.argv); got != tc.want {
			t.Errorf("QuoteArgv(%q) = %q; want %q", tc.argv, got, tc.want)
		}
	}
}

// eval set -- "$PG_RESCUE_CMD" must reproduce the argv exactly.
func TestQuoteArgvRoundTripsThroughEval(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash is required (the pg-rescue-go-tests check puts it in testDeps): %v", err)
	}
	argv := []string{"printf", "it's", "", "a b", `$HOME;"x"`, "`id`", "*", "\\n", "tab\there", "new\nline", "-n", "é"}
	cmd := exec.Command(bash, "-c", `eval set -- "$PG_RESCUE_CMD"; printf '%s\0' "$@"`)
	cmd.Env = append(os.Environ(), "PG_RESCUE_CMD="+QuoteArgv(argv))
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if !reflect.DeepEqual(got, argv) {
		t.Errorf("eval round trip changed argv:\n got %q\nwant %q", got, argv)
	}
}
