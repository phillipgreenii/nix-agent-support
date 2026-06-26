package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// Version is overridden at build time by mkGoApp (versionPath = "main.Version").
var Version = "dev"

// nowUTC is the only real-clock call; the unit-tested core takes Now as a param.
func nowUTC() time.Time { return time.Now().UTC() }

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "pb",
		Short:         "phillip-beads: pn:applied gate create/check",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newGateCmd())
	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "pb:", err)
		os.Exit(1)
	}
}
