// Command pg-router-handler-command is the command adapter of pg-router: the
// handler a [[role]] names with `handler` when the role runs a templated argv
// as a subprocess. It is today's command executor (internal/executor/
// command.go) behind the new request shape: pg-router core starts it per
// call, it reads the role's name and [role.config] from the `role` object of
// the JSON request on stdin, decodes that config strictly on every call, and
// runs the role's argv in its own process group, always under the minimal base
// env (never the handler's ambient env).
//
// Subcommands (INTF-HANDLER, DEC-WIRE-1: JSON on stdin, JSON on stdout, a
// coarse exit code): dispatch, postStartup, preShutdown, validate, version and
// help. The ccpool-specific subcommands (origin, pool-capacity, split, query)
// are not part of this binary.
//
// The shape of [role.config] is this adapter's own, defined by the decoder in
// roleconfig.go: `command.argv` (array of strings, each templated).
// Nothing else is accepted.
//
// Built from the same Go module as pg-router-ccpool-handler (bead
// pg2-12gr4.10.4); both import the shared internal library.
package main

import (
	"os"

	"github.com/phillipgreenii/pg-router/api/handlerkit"
)

var version = "dev"

func adapter() handlerkit.Adapter {
	return handlerkit.Adapter{
		Name:    "pg-router-handler-command",
		Version: version,
		Usage:   usageLine,
		Help:    helpText,
		Commands: map[string]func([]string) int{
			"dispatch":    runDispatch,
			"postStartup": func(a []string) int { return runLifecycle("postStartup", a) },
			"preShutdown": func(a []string) int { return runLifecycle("preShutdown", a) },
			"validate":    runValidate,
		},
	}
}

func main() {
	a := adapter()
	a.JSONLog = true
	handlerkit.Main(a)
}

func run(args []string) int { return handlerkit.Execute(adapter(), args, os.Stdout, os.Stderr) }
