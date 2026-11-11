// Command pg-git-lander is the lock-safe rebase and land tool. This file is
// the compiling starting point; later packets add the command-line surface.
package main

import (
	"fmt"
	"io"
	"os"
)

// Version is stamped by mkGoApp's default ldflag target (-X main.Version=...).
var Version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Fprintf(stdout, "pg-git-lander %s\n", Version)
		return 0
	}
	fmt.Fprintln(stderr, "usage: pg-git-lander --version")
	return 2
}
