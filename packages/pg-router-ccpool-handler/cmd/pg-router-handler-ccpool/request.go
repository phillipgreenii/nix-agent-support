package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/wire"
	"github.com/phillipgreenii/pg-router/conformance"
)

// roleObject is the `role` member of a request: the role's name and its
// decoded [role.config] (numbers kept as json.Number for the shape checker).
type roleObject struct {
	Name   string
	Config map[string]any
}

// errNoRole is returned for a request without a usable role object; the
// callers map it to conformance.ExitUsage.
var errNoRole = errors.New("request has no usable role object: want {\"role\": {\"name\": \"<role>\", \"config\": {...}}}")

// parseRole extracts the role object from a request body. A missing config
// (or a null one) means {}. A missing role, a role that is not an object, or
// an empty name is errNoRole.
func parseRole(raw []byte) (roleObject, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var top map[string]any
	if err := dec.Decode(&top); err != nil {
		return roleObject{}, fmt.Errorf("decode request: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return roleObject{}, errors.New("decode request: unexpected data after the JSON value")
	}
	obj, ok := top["role"].(map[string]any)
	if !ok {
		return roleObject{}, errNoRole
	}
	name, _ := obj["name"].(string)
	if name == "" {
		return roleObject{}, errNoRole
	}
	cfg := map[string]any{}
	switch c := obj["config"].(type) {
	case nil:
	case map[string]any:
		cfg = c
	default:
		return roleObject{}, fmt.Errorf("role %q: config must be an object", name)
	}
	return roleObject{Name: name, Config: cfg}, nil
}

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
