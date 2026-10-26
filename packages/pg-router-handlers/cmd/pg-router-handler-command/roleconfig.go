package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/phillipgreenii/pg-router-handlers/internal/prompt"
	"github.com/phillipgreenii/pg-router-handlers/internal/rolefile"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
)

// configPath is the path every problem about the role's config is reported
// under; the strict shape check names fields relative to it.
const configPath = "role.config"

// roleConfig is the shape of a command role's [role.config], this adapter's
// own contract (name and survives_shutdown are not config). Anything else is an
// unknown key.
type roleConfig struct {
	// StrictBaseEnv (INV-CCH-32): run the argv under the minimal base env.
	// Absent = false.
	StrictBaseEnv bool `json:"strictBaseEnv"`
	// Command is required; its argv elements are templated.
	Command *struct {
		Argv []string `json:"argv"`
	} `json:"command"`
}

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

// decodeRole strictly decodes ro.Config into a command roles.Role and returns
// EVERY problem it finds, in a stable order: the strict shape (an unknown key
// or a wrong type anywhere, keys sorted), then the load rules (command and a
// non-empty argv required, each argv element a parseable template). The role is
// meaningful only when problems is empty.
func decodeRole(ro roleObject) (roles.Role, []string) {
	shape := rolefile.ShapeChecker{}
	shape.Walk(configPath, ro.Config, reflect.TypeOf(roleConfig{}), nil)
	problems := append([]string(nil), shape.Problems...)

	// A type error means the typed decode would only repeat it (or fail on the
	// first one); the load rules need a decoded value.
	if shape.TypeError {
		return roles.Role{}, problems
	}
	b, err := json.Marshal(ro.Config)
	if err != nil { // unreachable: the config came out of a JSON decode
		return roles.Role{}, append(problems, fmt.Sprintf("%s: %v", configPath, err))
	}
	var rc roleConfig
	if err := json.Unmarshal(b, &rc); err != nil {
		return roles.Role{}, append(problems, fmt.Sprintf("%s: %v", configPath, err))
	}
	switch {
	case rc.Command == nil:
		problems = append(problems, configPath+".command: required (a role of this adapter runs command.argv)")
	case len(rc.Command.Argv) == 0:
		problems = append(problems, configPath+".command.argv: must hold at least one element")
	default:
		if rc.Command.Argv[0] == "" {
			problems = append(problems, configPath+".command.argv[0]: the program to run is empty")
		}
		for i, a := range rc.Command.Argv {
			if _, err := prompt.Parse("argv", a); err != nil {
				problems = append(problems, fmt.Sprintf("%s.command.argv[%d]: template: %v", configPath, i, err))
			}
		}
	}
	if len(problems) > 0 {
		return roles.Role{}, problems
	}
	return roles.Role{
		Name: ro.Name, StrictBaseEnv: rc.StrictBaseEnv,
		Command: &roles.CommandConfig{Argv: rc.Command.Argv},
	}, nil
}
