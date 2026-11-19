package rolefile

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/handlers/wire"
)

// RoleObject is the `role` member of a request: the role's name and its
// decoded [role.config] (numbers kept as json.Number for the shape checker).
type RoleObject struct {
	Name   string
	Config map[string]any
}

// ErrNoRole is returned for a request without a usable role object; the
// callers map it to conformance.ExitUsage.
var ErrNoRole = errors.New("request has no usable role object: want {\"role\": {\"name\": \"<role>\", \"config\": {...}}}")

// ParseRole extracts the role object from a request body. A missing config
// (or a null one) means {}. A missing role, a role that is not an object, or
// an empty name is ErrNoRole.
func ParseRole(raw []byte) (RoleObject, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var top map[string]any
	if err := dec.Decode(&top); err != nil {
		return RoleObject{}, fmt.Errorf("decode request: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return RoleObject{}, errors.New("decode request: unexpected data after the JSON value")
	}
	obj, ok := top["role"].(map[string]any)
	if !ok {
		return RoleObject{}, ErrNoRole
	}
	name, _ := obj["name"].(string)
	if name == "" {
		return RoleObject{}, ErrNoRole
	}
	cfg := map[string]any{}
	switch c := obj["config"].(type) {
	case nil:
	case map[string]any:
		cfg = c
	default:
		return RoleObject{}, fmt.Errorf("role %q: config must be an object", name)
	}
	return RoleObject{Name: name, Config: cfg}, nil
}

// ReadRequest reads one request from stdin, checks it against the named wire
// schema ("handler.dispatch", ...) and returns the raw body and the role it
// names. On any failure it has already written the schema-legal error reply
// and returns the exit code to use (ok false):
//
//   - unreadable stdin, malformed JSON or a schema violation: conformance.ExitError;
//   - a request without a usable role object: conformance.ExitUsage.
func ReadRequest(name, schema string, stdin io.Reader, stdout io.Writer) (raw []byte, ro RoleObject, exit int, ok bool) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, name+": read request from stdin:", err)
		return nil, RoleObject{}, conformance.ExitError, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		wire.WriteErrorReply(stdout, "malformed JSON: "+err.Error())
		return nil, RoleObject{}, conformance.ExitError, false
	}
	if err := conformance.Check(schema, v); err != nil {
		wire.WriteErrorReply(stdout, err.Error())
		return nil, RoleObject{}, conformance.ExitError, false
	}
	ro, err = ParseRole(raw)
	if err != nil {
		wire.WriteErrorReply(stdout, err.Error())
		if errors.Is(err, ErrNoRole) {
			return nil, RoleObject{}, conformance.ExitUsage, false
		}
		return nil, RoleObject{}, conformance.ExitError, false
	}
	return raw, ro, conformance.ExitOK, true
}

// ParseFlags parses args into fs and maps its outcome to an exit code: help is
// ExitOK, a bad flag or a positional argument is ExitUsage. done is false when
// the subcommand should carry on.
func ParseFlags(helpText, name string, fs *flag.FlagSet, args []string) (exit int, done bool) {
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
