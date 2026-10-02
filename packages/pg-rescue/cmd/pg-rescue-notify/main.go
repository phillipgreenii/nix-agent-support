// Command pg-rescue-notify is a pg-rescue reference handler: it posts a macOS
// notification about the failure and declines, so the chain continues.
package main

import (
	"os"

	"github.com/phillipgreenii/pg-rescue/internal/notify"
)

func main() {
	os.Exit(notify.Run(notify.DefaultRuntime(), os.Args[1:], os.Stdout, os.Stderr))
}
