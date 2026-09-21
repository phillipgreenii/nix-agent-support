package main

import (
	"fmt"
	"strings"
)

// subcommandInfo describes one dispatchable ccpool subcommand: its name, a
// one-line description for the top-level usage listing, and the function
// that runs it.
type subcommandInfo struct {
	name string
	desc string
	run  func([]string) int
}

// subcommands is the ONE place a ccpool subcommand is declared. Top-level
// usage (usageText) and dispatch (pickSubcommand/lookupSubcommand) both
// derive from this slice, in this order, so the list a user sees can never
// drift out of sync with what actually runs.
//
// pg2-htmkq: before this fix, the "known" map in main.go's pickSubcommand had
// no description and, worse, no user-visible listing at all. ANY unrecognized
// first argument -- --help, -h, help, a typo, or an unimplemented verb like
// "send" -- silently fell through to running `list`; `ccpool --help` only
// "worked" by accident, because it fell through to `list` and list's own
// flag.FlagSet happened to recognize -help/--help and printed ITS OWN usage,
// giving no hint that 16 other subcommands (e.g. `reply`, the actual verb for
// answering a needs_input session) exist at all.
var subcommands = []subcommandInfo{
	{"attach", "attach to a session's live tmux pane (interactive)", runAttach},
	{"attend", "pick a session that needs input (or is done) and attach to it", runAttend},
	{"cancel", "interrupt a session's current turn; it stays alive and idle", runCancel},
	{"close", "close a session (stop it; --purge also removes its stored data)", runClose},
	{"doctor", "print each session's pool context and cached store state", runDoctor},
	{"hook", "internal: report a turn event from the agent's own hooks", runHook},
	{"list", "list sessions in the pool", runList},
	{"meta", "get/set/list/remove a session's opaque metadata key/value tags", runMeta},
	{"new", "launch a new session", runNew},
	{"reap", "evict idle/errored sessions past their retention TTL from the default pool", runReap},
	{"reap-all", "reap the default pool and every registered pool (timer-driven sweep)", runReapAll},
	{"reply", "send a prompt into a session; wait for, or fire-and-forget, its outcome", runReply},
	{"result", "resolve a previously dispatched turn's reply from its transcript anchor", runResult},
	{"state", "print a session's live, reconciled state", runState},
	{"tail", "print a session's recent transcript output", runTail},
	{"trust", "pre-trust a cwd so an automated claude launch there doesn't stall on the folder-trust prompt", runTrust},
	{"version", "print the ccpool version", runVersion},
}

// lookupSubcommand finds name in the registry above.
func lookupSubcommand(name string) (subcommandInfo, bool) {
	for _, s := range subcommands {
		if s.name == name {
			return s, true
		}
	}
	return subcommandInfo{}, false
}

// dispatchKind classifies ccpool's first CLI argument (argv[1]).
type dispatchKind int

const (
	// dispatchKnown: argv[1] matched a registered subcommand.
	dispatchKnown dispatchKind = iota
	// dispatchHelp: no args at all, or an explicit -h/--help/help request.
	dispatchHelp
	// dispatchUnknown: argv[1] matched nothing above (a typo, or an
	// unimplemented verb like "send").
	dispatchUnknown
)

// pickSubcommand classifies argv (ccpool's os.Args, with any leading --pool
// already stripped by stripPoolFlag) into a dispatchKind, the matched
// subcommand (zero value unless dispatchKnown), and the remaining args to
// hand that subcommand.
//
// Unlike the pre-pg2-htmkq version, NONE of these three outcomes fall through
// to running `list` -- list is reached only by asking for it explicitly
// (`ccpool list`).
func pickSubcommand(argv []string) (kind dispatchKind, sub subcommandInfo, rest []string) {
	if len(argv) < 2 {
		return dispatchHelp, subcommandInfo{}, nil
	}
	first := argv[1]
	if first == "-h" || first == "--help" || first == "help" {
		return dispatchHelp, subcommandInfo{}, argv[2:]
	}
	if s, ok := lookupSubcommand(first); ok {
		return dispatchKnown, s, argv[2:]
	}
	return dispatchUnknown, subcommandInfo{name: first}, argv[2:]
}

// usageText renders the top-level usage listing: one line per subcommand
// from the registry above, in registry order, each name padded to a common
// width so the descriptions line up.
func usageText() string {
	width := 0
	for _, s := range subcommands {
		if len(s.name) > width {
			width = len(s.name)
		}
	}
	var b strings.Builder
	b.WriteString("usage: ccpool [--pool <dir>] <command> [<args>]\n\ncommands:\n")
	for _, s := range subcommands {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, s.name, s.desc)
	}
	return b.String()
}

// runVersion is the "version" subcommand's run func -- ignores args, prints
// the ccpool version (set at build time via -ldflags, main.go's `version`
// var), and always succeeds.
func runVersion(_ []string) int {
	fmt.Println(version)
	return 0
}
