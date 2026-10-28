// Command contractfake is the trivial fake handler of the internal/contracttest
// tests. It is built once per test run WITHOUT the race detector
// (testenv.BuildHelper), so each child the harness starts avoids the race
// runtime's 1.0 s exit sleep. It behaves as its first argument says.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/report"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		os.Exit(98)
	}
	print := func(r contract.Result) {
		b, err := r.Render()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(70)
		}
		_, _ = os.Stdout.Write(b)
	}
	switch args[0] {
	case "describe": // resolve, describing the report and environment it was given
		b, err := os.ReadFile(os.Getenv("PG_RESCUE_REPORT"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var r report.Report
		_ = json.Unmarshal(b, &r)
		if r.SchemaVersion != report.SchemaVersion {
			fmt.Fprintf(os.Stderr, "unsupported schema_version %d\n", r.SchemaVersion)
			os.Exit(1) // the documented way to refuse a version you do not know
		}
		wd, _ := os.Getwd()
		print(contract.Result{Outcome: contract.Resolved, Summary: fmt.Sprintf(
			"run=%s handler=%s position=%s depth=%s parent=%s attempts=%d mode=%s cmd=[%s] exit=[%s] cwd=%s",
			os.Getenv("PG_RESCUE_RUN_ID"), os.Getenv("PG_RESCUE_HANDLER"), os.Getenv("PG_RESCUE_POSITION"),
			os.Getenv("PG_RESCUE_DEPTH"), os.Getenv("PG_RESCUE_PARENT_RUN_ID"), len(r.Attempts), r.Mode,
			os.Getenv("PG_RESCUE_CMD"), os.Getenv("PG_RESCUE_EXIT"), wd,
		)})
	case "exit": // exit with a code and print the rest of the args as stdout
		fmt.Print(strings.Join(args[2:], " "))
		code := 0
		_, _ = fmt.Sscan(args[1], &code)
		os.Exit(code)
	case "env":
		print(contract.Result{Outcome: contract.Declined, Summary: os.Getenv(args[1])})
		os.Exit(contract.ExitDeclined)
	case "stdin":
		b, _ := io.ReadAll(os.Stdin)
		print(contract.Result{Outcome: contract.Deferred, Summary: fmt.Sprintf("stdin bytes=%d", len(b))})
		os.Exit(contract.ExitDeferred)
	case "big": // 2 MiB on stdout; the harness must drain it without blocking
		chunk := strings.Repeat("a", 1<<16)
		for i := 0; i < 32; i++ {
			fmt.Print(chunk)
		}
		os.Exit(0)
	case "hang":
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}
