package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/metrics"
	"github.com/phillipgreenii/x/exitcode"
)

const scenarioConfig = "schema_version = 1\n[[scope]]\nname = \"demo\"\nprefixes = [\"/work/demo\"]\n[[lens]]\nid = \"agent-workflow\"\nproviders = []\n"

var stamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T[0-9:.]+Z?|"run_id":\d+|run \d+`)

// metricsScenario runs every command that touches the ledger (human pending,
// show and rule, agent file, job run, maintain and status) over a hermetic
// ledger and returns one "exit|stdout" line per command, with timestamps and
// run ids masked.
func metricsScenario(t *testing.T) []string {
	t.Helper()
	runID, l, _ := interactiveRun(t)
	writeConfig(t, scenarioConfig)
	ids := seedFindings(t, l, syn("a", "Should a?", "E-a"))
	if _, err := l.DB().Exec(`UPDATE run SET state='Filed', ended_at=created_at WHERE state='Running'`); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprint(ids[0])
	cmds := [][]string{
		{"human", "pending"},
		{"human", "show", id},
		{"human", "rule", id, "rejected"},
		{"agent", "file", findingsPath(), "--run", strconv.FormatInt(runID, 10)},
		{"job", "run", "agent-workflow", "--scope", "demo", "--duration", "1h", "--analyst", "none"},
		{"job", "status"},
		{"job", "maintain"},
	}
	var out []string
	for _, c := range cmds {
		code, stdout, _ := run(t, c...)
		out = append(out, strings.Join(c[:2], " ")+"|"+strconv.Itoa(code)+"|"+stamp.ReplaceAllString(stdout, "~"))
	}
	return out
}

func useIsolatedPusher(t *testing.T) {
	t.Helper()
	saved := metrics.Default
	p := metrics.NewPusher()
	p.LookPath = func(string) (string, error) { return "", fmt.Errorf("no ccpool in tests") }
	p.Debug = io.Discard
	p.Timeout = 500 * time.Millisecond
	metrics.Default = p
	t.Cleanup(func() { metrics.Default = saved })
}

// TestEveryCommandIsUnchangedWithNoEndpointAndWithAnUnreachableOne runs each
// command with no OTLP endpoint, with an unreachable one (HTTP and gRPC) and
// asserts the exit codes and output are identical.
func TestEveryCommandIsUnchangedWithNoEndpointAndWithAnUnreachableOne(t *testing.T) {
	useIsolatedPusher(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")
	want := metricsScenario(t)
	for _, line := range want {
		if !strings.Contains(line, "|0|") {
			t.Fatalf("baseline scenario command failed: %s", line)
		}
	}
	for _, proto := range []string{"http/json", "grpc"} {
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
		t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", proto)
		t.Setenv(metrics.DebugEnv, "1")
		got := metricsScenario(t)
		if len(got) != len(want) {
			t.Fatalf("%s: %d results, want %d", proto, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: command %d differs:\n got %s\nwant %s", proto, i, got[i], want[i])
			}
		}
	}
}

// TestGaugesAreEmittedAtEachHook checks that a reachable endpoint receives a
// push after a ruling, after agent file completes an interactive run, at the
// end of a run and from maintain.
func TestGaugesAreEmittedAtEachHook(t *testing.T) {
	useIsolatedPusher(t)
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		posts.Add(1)
	}))
	defer srv.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/json")

	runID, l, _ := interactiveRun(t)
	writeConfig(t, scenarioConfig)
	ids := seedFindings(t, l, syn("a", "Should a?", "E-a"))
	if _, err := l.DB().Exec(`UPDATE run SET state='Filed', ended_at=created_at WHERE state='Running'`); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprint(ids[0])
	step := func(name string, args ...string) {
		t.Helper()
		before := posts.Load()
		if code, out, errs := run(t, args...); code != exitcode.OK {
			t.Fatalf("%s: exit %d %s %s", name, code, out, errs)
		}
		if posts.Load() <= before {
			t.Errorf("%s emitted no gauges", name)
		}
	}
	run(t, "human", "show", id)
	step("rule", "human", "rule", id, "rejected")
	step("agent file", "agent", "file", findingsPath(), "--run", strconv.FormatInt(runID, 10))
	step("job run", "job", "run", "agent-workflow", "--scope", "demo", "--duration", "1h", "--analyst", "none")
	step("job maintain", "job", "maintain")
}
