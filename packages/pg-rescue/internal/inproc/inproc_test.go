package inproc

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/contracttest"
	"github.com/phillipgreenii/pg-rescue/internal/report"
)

// TestHandlerEnvMatchesContracttest guards the one duplicated list: it runs a
// real handler process (sh, which is not race-built and exits at once) under
// contracttest.Run, reads back the PG_RESCUE_* variables that run gave it, and
// requires handlerEnv to build exactly the same set from the same inputs.
func TestHandlerEnvMatchesContracttest(t *testing.T) {
	for _, name := range []string{"first-attempt", "three-prior-attempts", "stdin-mode"} {
		t.Run(name, func(t *testing.T) {
			fixture := contracttest.Fixture(name)
			res := contracttest.Run(t, []string{"sh", "-c", `printf '{"outcome":"resolved","summary":"s"}'; env | grep '^PG_RESCUE_' | sort >&2`}, fixture, nil)
			if res.Exit != 0 {
				t.Fatalf("exit %d, stderr %q", res.Exit, res.Stderr)
			}
			want := strings.Split(strings.TrimSpace(res.Stderr), "\n")

			raw, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatal(err)
			}
			var r report.Report
			if err := json.Unmarshal(raw, &r); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Dir(res.ReportPath)
			got := handlerEnv(&r, res.ReportPath, filepath.Join(dir, "output.log"), dir)
			sort.Strings(got)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("handlerEnv differs from contracttest's:\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

func TestRunSetsCwdAndEnvForTheCallAndRestoresThem(t *testing.T) {
	before, _ := os.Getwd()
	t.Setenv("INPROC_KEEP", "outer")
	var sawCwd, sawEnv, sawReport string
	entry := func(args, env []string, stdin io.Reader, stdout, stderr io.Writer) int {
		sawCwd, _ = os.Getwd()
		sawEnv = os.Getenv("INPROC_KEEP")
		sawReport = os.Getenv("PG_RESCUE_REPORT")
		_, _ = io.WriteString(stdout, `{"outcome":"declined","summary":"s"}`)
		return 2
	}
	res := Run(t, entry, nil, contracttest.Fixture("stdin-mode"), []string{"INPROC_KEEP=inner"})
	if res.Exit != 2 || res.Outcome != contract.Declined {
		t.Fatalf("result = %+v", res)
	}
	if sawEnv != "inner" || sawReport != res.ReportPath {
		t.Errorf("the call saw INPROC_KEEP=%q PG_RESCUE_REPORT=%q", sawEnv, sawReport)
	}
	if sawCwd == before {
		t.Errorf("the call ran in the test's own directory %q", sawCwd)
	}
	if after, _ := os.Getwd(); after != before {
		t.Errorf("cwd after Run = %q, want %q", after, before)
	}
	if got := os.Getenv("INPROC_KEEP"); got != "outer" {
		t.Errorf("INPROC_KEEP after Run = %q, want outer", got)
	}
	if _, set := os.LookupEnv("PG_RESCUE_REPORT"); set {
		t.Error("PG_RESCUE_REPORT leaked out of Run")
	}
}
