package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/wire"
	"github.com/phillipgreenii/pg-router/conformance"
)

// readRequest reads one request from stdin, checks it against the named wire
// schema ("handler.dispatch", ...) and returns the raw body and the role it
// names. On any failure it has already written the schema-legal error reply
// and returns the exit code to use (ok false):
//
//   - unreadable stdin, malformed JSON or a schema violation: conformance.ExitError;
//   - a request without a usable role object: conformance.ExitUsage.
func readRequest(name, schema string, stdin io.Reader, stdout io.Writer) (raw []byte, ro roleObject, exit int, ok bool) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, name+": read request from stdin:", err)
		return nil, roleObject{}, conformance.ExitError, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		wire.WriteErrorReply(stdout, "malformed JSON: "+err.Error())
		return nil, roleObject{}, conformance.ExitError, false
	}
	if err := conformance.Check(schema, v); err != nil {
		wire.WriteErrorReply(stdout, err.Error())
		return nil, roleObject{}, conformance.ExitError, false
	}
	ro, err = parseRole(raw)
	if err != nil {
		wire.WriteErrorReply(stdout, err.Error())
		if errors.Is(err, errNoRole) {
			return nil, roleObject{}, conformance.ExitUsage, false
		}
		return nil, roleObject{}, conformance.ExitError, false
	}
	return raw, ro, conformance.ExitOK, true
}

// noArgs parses a subcommand's flags: none beyond -h. It returns done true with
// the exit code to return when the subcommand must stop (help, bad flag,
// stray argument).
func noArgs(name string, args []string) (exit int, done bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return parseFlags(name, fs, args)
}

// parseFlags parses args into fs and maps its outcome to an exit code: help is
// ExitOK, a bad flag or a positional argument is ExitUsage. done is false when
// the subcommand should carry on.
func parseFlags(name string, fs *flag.FlagSet, args []string) (exit int, done bool) {
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Print(helpText)
		return conformance.ExitOK, true
	case err != nil:
		fmt.Fprintln(os.Stderr, name+":", err)
		return conformance.ExitUsage, true
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, name+": unexpected argument:", fs.Arg(0))
		fmt.Fprintln(os.Stderr, name+" takes its request as JSON on stdin, never as arguments")
		return conformance.ExitUsage, true
	}
	return conformance.ExitOK, false
}
