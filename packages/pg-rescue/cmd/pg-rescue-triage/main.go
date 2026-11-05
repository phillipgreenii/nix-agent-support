// Command pg-rescue-triage is the argv of the pg-router pg-rescue triage
// command role. For one pg-rescue deferral item it verifies, read-only, that
// the live state the item points at still matches, comments the outcome, and
// then closes the item (resolved), releases it (unreachable) or hands it to a
// person (drifted, intact, not-a-repo, unverifiable). It never changes the
// state the item points at, and never acts on a canonical clone.
//
// Exit codes: 0 handled (including skipped and peer-held), 1 failure,
// 2 usage error.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-rescue/internal/triage"
)

func main() {
	host, _ := os.Hostname()
	os.Exit(run(os.Args[1:], os.Getenv, host, triage.BD{Run: triage.ExecRunner}, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, host string, tr triage.Tracker, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pg-rescue-triage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	itemID := fs.String("item-id", getenv("PG_ROUTER_ITEM_ID"), "the item to triage (default $PG_ROUTER_ITEM_ID)")
	actor := fs.String("actor", getenv("BEADS_ACTOR"), "claim actor (default $BEADS_ACTOR, else pg-rescue-triage)")
	hostFlag := fs.String("host", host, "this machine's name, for the other-host check")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *itemID == "" {
		fmt.Fprintln(stderr, "pg-rescue-triage: --item-id (or PG_ROUTER_ITEM_ID) is required")
		return 2
	}
	if *actor == "" {
		*actor = "pg-rescue-triage"
	}
	if w, ok := tr.(interface{ WithActor(string) triage.Tracker }); ok {
		tr = w.WithActor(*actor)
	}
	out, err := triage.Run(tr, triage.Options{ItemID: *itemID, Actor: *actor, Host: *hostFlag})
	if err != nil {
		fmt.Fprintf(stderr, "pg-rescue-triage: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s %s %s\n", *itemID, out.Action, out.Verdict)
	return 0
}
