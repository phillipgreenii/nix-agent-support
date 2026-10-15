// Command pg-caffeinate-shim stands in for /usr/bin/caffeinate and forwards
// each keep-awake request to pg-keep-awake. This scaffold carries no feature
// logic; the 04-shimargs packet and later ones fill it in.
package main

import (
	"fmt"
	"os"
)

// Version is stamped by mkGoApp's default ldflag target (-X main.Version=...).
var Version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Printf("pg-caffeinate-shim %s\n", Version)
		return
	}
	fmt.Fprintln(os.Stderr, "pg-caffeinate-shim: not implemented yet")
	os.Exit(2)
}
