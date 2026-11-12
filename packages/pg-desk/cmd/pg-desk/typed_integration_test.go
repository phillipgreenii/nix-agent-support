//go:build integration

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestMainExitsWithTypedVerbCode runs the real main() in a child process
// with a probe verb that returns each kind of error, and checks the child's
// exit status and that the error text still reaches stderr.
func TestMainExitsWithTypedVerbCode(t *testing.T) {
	if mode := os.Getenv("PG_DESK_TEST_EXIT_PROBE"); mode != "" {
		rootCmd.AddCommand(&cobra.Command{
			Use:           "exit-probe",
			SilenceUsage:  true,
			SilenceErrors: true,
			RunE: func(*cobra.Command, []string) error {
				switch mode {
				case "partial":
					return newExitError(exitPartial, errors.New("probe partial"))
				case "total":
					return newExitError(exitTotal, errors.New("probe total"))
				}
				return errors.New("probe plain")
			},
		})
		os.Args = []string{"pg-desk", "exit-probe"}
		main()
		os.Exit(0)
	}
	for mode, want := range map[string]int{"partial": 2, "total": 3, "plain": 1} {
		cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestMainExitsWithTypedVerbCode$")
		cmd.Env = append(os.Environ(), "PG_DESK_TEST_EXIT_PROBE="+mode)
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("%s: want a non-zero exit, got err=%v\n%s", mode, err, out)
		}
		if ee.ExitCode() != want {
			t.Errorf("%s: exit code = %d, want %d\n%s", mode, ee.ExitCode(), want, out)
		}
		if !strings.Contains(string(out), "probe "+mode) {
			t.Errorf("%s: error text not printed to stderr:\n%s", mode, out)
		}
	}
}
