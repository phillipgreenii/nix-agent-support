// testmain_test.go: the reentrant test-helper-process wire double for
// pg-connector — this packet's own "fake pg-connector binary or a
// stubbed exec" acceptance requirement, implemented as a stubbed exec.
// Mirrors packages/pg-connector's own pkg/scriptout/exec_test.go pattern
// exactly: the test binary re-execs itself with
// GO_WANT_HELPER_PROCESS=1 set, TestHelperProcess recognizes that and
// runs helperMain instead of the real test suite, and helperMain
// impersonates pg-connector by inspecting the real CLI args this adapter
// passed it (findChildArgs) and the GO_HELPER_BEHAVIOR env var selecting
// which canned wire response/exit code to answer with.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)
	helperMain()
}

// findChildArgs recovers the real pg-connector CLI args this adapter
// passed to execCmdFactory, out of the re-exec'd test binary's own
// os.Args (helperCmdFactory below inserts "--" then the impersonated
// binary name then those args).
func findChildArgs() []string {
	for i, a := range os.Args {
		if a == "--" {
			if i+2 <= len(os.Args) {
				return os.Args[i+2:]
			}
			return nil
		}
	}
	return nil
}

// flagValue returns the value following flag in args, or "" if flag is
// absent or has no following value.
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// recordAmbientEnvIfRequested is the "the double records its own env"
// side channel this packet's own --beads-dir acceptance criterion needs:
// when the test sets GO_HELPER_ENV_RECORD_FILE (to the path of a
// t.TempDir file) and GO_HELPER_ENV_RECORD_VAR (the env var name to
// capture), the helper writes that var's own value, as this HELPER
// PROCESS observed it (i.e. what runPgConnector's cmd.Env actually
// carried), to that file — independent of which GO_HELPER_BEHAVIOR was
// also selected, so any behavior can be combined with this recording.
func recordAmbientEnvIfRequested() {
	file := os.Getenv("GO_HELPER_ENV_RECORD_FILE")
	if file == "" {
		return
	}
	varName := os.Getenv("GO_HELPER_ENV_RECORD_VAR")
	val, ok := os.LookupEnv(varName)
	if !ok {
		// Distinguishable from "set to empty string" — this packet's own
		// Contract distinguishes "not given -> do not set the var at
		// all" from any actually-set value [design: section 6.1].
		val = "<unset>"
	}
	_ = os.WriteFile(file, []byte(val), 0o600)
}

func helperMain() {
	recordAmbientEnvIfRequested()

	childArgs := findChildArgs()
	behavior := os.Getenv("GO_HELPER_BEHAVIOR")

	switch behavior {
	case "changes_degraded":
		// One succeeded backend, one degraded, one disabled — proving
		// degraded_sources names only the degraded one, never the
		// succeeded or disabled ones [design: section 6.1].
		_, _ = fmt.Fprint(os.Stdout, `{"sources":[`+
			`{"backend":"b1","status":"succeeded","version":1,"truncated":false},`+
			`{"backend":"b2","status":"degraded","version":2,"truncated":false},`+
			`{"backend":"b3","status":"disabled","version":0,"truncated":false}`+
			`],"changes":[`+
			`{"change":"added","source":"b1","entity":{"id":"e1","title":"Entity One"}},`+
			`{"change":"changed","source":"b2","entity":{"id":"e2","title":""}}`+
			`]}`)
		os.Exit(2)
	case "changes_ok_empty":
		_, _ = fmt.Fprint(os.Stdout, `{"sources":[],"changes":[]}`)
		os.Exit(0)
	case "total_failure":
		// A total-failure outcome still writes a well-formed wire body
		// to its OWN stdout (matching pg-connector's real
		// writeFanOutResult, which writes the JSON body before ever
		// checking exitCode) — this adapter must ignore it entirely on
		// this path and print nothing of its own [design: section 6.1].
		_, _ = fmt.Fprint(os.Stdout, `{"sources":[{"backend":"b1","status":"degraded","version":1,"truncated":false}],"changes":[]}`)
		_, _ = fmt.Fprintln(os.Stderr, "pg-connector: total failure: no backend succeeded")
		os.Exit(3)
	case "invalid_argument":
		_, _ = fmt.Fprint(os.Stdout, `{"error":{"code":"invalid_argument","message":"query not recognized"}}`)
		_, _ = fmt.Fprintln(os.Stderr, "pg-connector: invalid_argument: query \"bogus\" is not recognized by any registered backend")
		os.Exit(1)
	case "backend_not_registered":
		_, _ = fmt.Fprintln(os.Stderr, `dispatch: --backend "not-a-backend" is not registered for connector.issue (registered: [pg-connector-issue-beads])`)
		os.Exit(1)
	case "sweep_ids_by_query":
		// present_ids-only responses whose content depends on which
		// --query was passed, so a multi-query sweep invocation can be
		// exercised end-to-end (union + dedup across calls). entities
		// is ALWAYS EMPTY here, matching the real --ids-only shape
		// exactly [design: section 6.1] — a wire double with entities
		// populated instead would catch the OPPOSITE bug.
		query := flagValue(childArgs, "--query")
		var presentIDs string
		switch query {
		case "q1":
			presentIDs = `["a","b"]`
		case "q2":
			presentIDs = `["b","c"]`
		default:
			presentIDs = `[]`
		}
		_, _ = fmt.Fprintf(os.Stdout, `{"entities":[],"present_ids":%s,"sources":[]}`, presentIDs)
		os.Exit(0)
	case "list_full_ok":
		_, _ = fmt.Fprint(os.Stdout, `{"entities":[`+
			`{"id":"i1","title":"Alpha task one","issue_type":"task","metadata":{"k":"v1"}},`+
			`{"id":"i2","title":"Beta task two","issue_type":"task","metadata":{"k":"v2"}},`+
			`{"id":"i3","title":"Alpha bug three","issue_type":"bug","metadata":{"k":"v3"}}`+
			`],"present_ids":["i1","i2","i3"],"sources":[{"source":"b1","status":"succeeded","count":3}]}`)
		os.Exit(0)
	case "list_full_title_fallback":
		// One entity with an empty title, proving the "else the id"
		// fallback [design: section 6.1].
		_, _ = fmt.Fprint(os.Stdout, `{"entities":[{"id":"i9","title":"","issue_type":"task","metadata":{}}],"present_ids":["i9"],"sources":[]}`)
		os.Exit(0)
	default:
		_, _ = fmt.Fprintln(os.Stderr, "unknown GO_HELPER_BEHAVIOR: "+behavior)
		os.Exit(99)
	}
}

func helperCmdFactory(behavior string, extraEnv ...string) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(append(
			os.Environ(),
			"GO_WANT_HELPER_PROCESS=1",
			"GO_HELPER_BEHAVIOR="+behavior,
		), extraEnv...)
		return cmd
	}
}

func withFactory(t *testing.T, behavior string, extraEnv ...string) {
	t.Helper()
	orig := execCmdFactory
	execCmdFactory = helperCmdFactory(behavior, extraEnv...)
	t.Cleanup(func() { execCmdFactory = orig })
}

// runCLI drives newRootCmd() end-to-end (cobra arg parsing + RunE),
// returning stdout, stderr, and the same exit code main()'s run() would
// have returned — used by every _test.go file in this package rather
// than calling run() (which would want real os.Stdout/os.Stderr).
func runCLI(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	var out, errOut writerBuf
	exitCode = run(args, &out, &errOut)
	return out.String(), errOut.String(), exitCode
}

// writerBuf is a minimal io.Writer + String() buffer, avoiding a direct
// bytes.Buffer import collision with per-file imports.
type writerBuf struct{ b []byte }

func (w *writerBuf) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}
func (w *writerBuf) String() string { return string(w.b) }

// mustUnmarshalItems decodes stdout as this adapter's own rawItem array
// output shape.
func mustUnmarshalItems(t *testing.T, stdout string) []rawItem {
	t.Helper()
	var items []rawItem
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("unmarshal items: %v (stdout=%q)", err, stdout)
	}
	return items
}

// readFile reads path (a small test fixture/recording file), failing the
// test on any error.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

var _ io.Writer = (*writerBuf)(nil)
