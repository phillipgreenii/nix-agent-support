package main

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

// runValidate implements the optional `validate` INTF-HANDLER subcommand
// (INV-CCH-36): the adapter's ONE validation entry point, called by
// the nix build on every rendered role JSON, by pg-router at startup and by its
// doctor. It checks the role JSON strictly, the sessionEnv keys, and that each
// pool directory the role references exists. It runs no payload, never runs or
// opens pg-wrap or its spec (PG_WRAP_SPEC is just another sessionEnv value
// here), and verifies nothing about pg-router.
//
// Exit 0 with a reply means "ran" (an empty problems list means valid); a
// malformed request is an error reply and exit 1; exit 2 is reserved for a
// usage error, which the core reads as "not supported", so a missing role
// config is a PROBLEM in the reply, never a usage error.
func runValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	roleConfig := fs.String("role-config", os.Getenv(envRoleConfig), "path to this process's own role config JSON (or "+envRoleConfig+")")
	// --config is accepted for symmetry with dispatch and not checked: the launch
	// config is the handler's own and holds no role JSON.
	_ = fs.String("config", os.Getenv(envConfig), "path to this process's own launch config JSON (or "+envConfig+"); accepted, not checked")
	skipPoolDirs := fs.Bool("skip-pool-dirs", false, "skip ONLY the pool-directory existence check (for the nix build sandbox, where the pools do not exist yet); startup and doctor never pass it")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Print(helpText)
		return conformance.ExitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "validate:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "validate: unexpected argument:", fs.Arg(0))
		fmt.Fprintln(os.Stderr, "validate takes its request as JSON on stdin, never as arguments")
		return conformance.ExitUsage
	}
	return serveValidate(os.Stdin, os.Stdout, *roleConfig, *skipPoolDirs)
}

// serveValidate is runValidate's testable core (see servePostStartup).
func serveValidate(stdin io.Reader, stdout io.Writer, rolePath string, skipPoolDirs bool) int {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "validate: read request from stdin:", err)
		return conformance.ExitError
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		writeErrorReply(stdout, "malformed JSON: "+err.Error())
		return conformance.ExitError
	}
	if err := conformance.Check("handler.validate", v); err != nil {
		writeErrorReply(stdout, err.Error())
		return conformance.ExitError
	}
	problems, warnings := validateRoleFile(rolePath, skipPoolDirs)
	if problems == nil {
		problems = []string{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	writeReply(stdout, map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"problems":      problems,
		"warnings":      warnings,
	})
	return conformance.ExitOK
}

// secretLookingKey reports whether a sessionEnv key name looks like it carries
// a secret: its name contains TOKEN, SECRET, KEY or PASSWORD. The match is a
// case-insensitive substring match, a deliberate choice: env names are
// conventionally upper-case, but a lower-case "gh_token" leaks just as much, and
// a false positive costs only a warning (the design makes this a warning, not a
// problem).
func secretLookingKey(k string) bool {
	u := strings.ToUpper(k)
	for _, frag := range []string{"TOKEN", "SECRET", "KEY", "PASSWORD"} {
		if strings.Contains(u, frag) {
			return true
		}
	}
	return false
}

// validateRoleFile runs every check on the role file at path and returns the
// problems and warnings in a stable order: the order the checks run (read and
// decode, strict shape, sessionEnv keys, load rules, warnings, pool
// directories), keys sorted within a check. Nothing is deduplicated.
func validateRoleFile(path string, skipPoolDirs bool) (problems, warnings []string) {
	if path == "" {
		return []string{"no role config given (--role-config, or " + envRoleConfig + ")"}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("read role config %s: %v", path, err)}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return []string{fmt.Sprintf("decode role config %s: %v", path, err)}, nil
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return []string{fmt.Sprintf("decode role config %s: unexpected data after the JSON value", path)}, nil
	}
	top, ok := tree.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("role config %s: the top level must be a JSON object", path)}, nil
	}

	// 1. Strict shape: an unknown key or a wrong type anywhere.
	shape := shapeChecker{}
	shape.walk("", top, reflect.TypeOf(roleFile{}), topLevelExtras)
	problems = append(problems, shape.problems...)

	// 2. sessionEnv keys: every offending key, not just the first.
	sessionEnv, _ := top["sessionEnv"].(map[string]any)
	for _, k := range sortedKeys(sessionEnv) {
		if err := validateSessionEnv(map[string]string{k: ""}); err != nil {
			problems = append(problems, "sessionEnv: "+err.Error())
		}
	}

	// 3. The load rules dispatch applies (decode, per-type blocks, budget,
	// durations, effort, ...), so validate and dispatch agree. Skipped when the
	// shape check found a type error: the decode would only repeat it.
	if !shape.typeError {
		if _, err := decodeRole(path, data, false); err != nil {
			problems = append(problems, err.Error())
		}
	}

	// 4. Warnings.
	typ, _ := top["type"].(string)
	if _, has := top["sessionEnv"]; has && typ == "command" {
		warnings = append(warnings, "sessionEnv has no effect on a command role")
	}
	if _, has := top["strictBaseEnv"]; has && typ == "ccpool" {
		warnings = append(warnings, "strictBaseEnv has no effect on a ccpool role")
	}
	for _, k := range sortedKeys(sessionEnv) {
		if secretLookingKey(k) {
			warnings = append(warnings, fmt.Sprintf("sessionEnv key %q looks like it carries a secret (name matches TOKEN, SECRET, KEY or PASSWORD): sessionEnv is visible in the session environment and event records; keep secrets in the prefix tool's own spec", k))
		}
	}

	// 5. Pool directories: the role's own poolDir, else this process's CCPOOL_POOL.
	if !skipPoolDirs && typ == "ccpool" {
		dir, src := "", ""
		if cc, ok := top["ccpool"].(map[string]any); ok {
			if d, ok := cc["poolDir"].(string); ok && d != "" {
				dir, src = d, "ccpool.poolDir"
			}
		}
		if dir == "" {
			if d := os.Getenv("CCPOOL_POOL"); d != "" {
				dir, src = d, "CCPOOL_POOL"
			}
		}
		if dir != "" {
			switch fi, err := os.Stat(dir); {
			case err != nil:
				problems = append(problems, fmt.Sprintf("pool directory %s (%s) is not usable: %v", dir, src, err))
			case !fi.IsDir():
				problems = append(problems, fmt.Sprintf("pool directory %s (%s) is not a directory", dir, src))
			}
		}
	}
	return problems, warnings
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// topLevelExtras are role-file keys that roleFile does not decode but that are
// real: survivesShutdown is rendered into every ccpool role's JSON by the nix
// module and read by pg-router core, which ignores nothing else of it. The Go
// roleFile ignores it, so a strict check MUST accept it (as a boolean).
var topLevelExtras = map[string]reflect.Type{
	"survivesShutdown": reflect.TypeOf(false),
}

// shapeChecker walks the generic decoded JSON against roleFile's Go types and
// records an unknown key or a wrong type anywhere as a problem naming the
// field, without stopping at the first. A key matches a field the way
// encoding/json does: by its json tag name, else by its Go field name, either
// way case-insensitively (roles.IsolationConfig has no tags).
type shapeChecker struct {
	problems  []string
	typeError bool
}

var textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()

func (s *shapeChecker) typeProblem(path, want string, got any) {
	s.typeError = true
	s.problems = append(s.problems, fmt.Sprintf("%s: want %s, got %s", path, want, jsonKind(got)))
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case json.Number:
		return "a number"
	case string:
		return "a string"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("%T", v)
}

func (s *shapeChecker) walk(path string, v any, t reflect.Type, extras map[string]reflect.Type) {
	if v == nil {
		return // encoding/json leaves the field alone on null
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	here := path
	if here == "" {
		here = "role config"
	}
	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		if _, ok := v.(string); !ok {
			s.typeProblem(here, "a string", v)
		}
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			s.typeProblem(here, "an object", v)
			return
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := f.Name
			if tag := strings.Split(f.Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
				name = tag
			}
			fields[strings.ToLower(name)] = f.Type
		}
		for name, ft := range extras {
			fields[strings.ToLower(name)] = ft
		}
		for _, k := range sortedKeys(obj) {
			ft, known := fields[strings.ToLower(k)]
			if !known {
				s.problems = append(s.problems, fmt.Sprintf("%s: unknown key %q", here, k))
				continue
			}
			child := k
			if path != "" {
				child = path + "." + k
			}
			s.walk(child, obj[k], ft, nil)
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			s.typeProblem(here, "an object", v)
			return
		}
		for _, k := range sortedKeys(obj) {
			s.walk(path+"."+k, obj[k], t.Elem(), nil)
		}
	case reflect.Slice:
		arr, ok := v.([]any)
		if !ok {
			s.typeProblem(here, "an array", v)
			return
		}
		for i, e := range arr {
			s.walk(fmt.Sprintf("%s[%d]", path, i), e, t.Elem(), nil)
		}
	case reflect.Bool:
		if _, ok := v.(bool); !ok {
			s.typeProblem(here, "a boolean", v)
		}
	case reflect.String:
		if _, ok := v.(string); !ok {
			s.typeProblem(here, "a string", v)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, ok := v.(json.Number)
		if !ok {
			s.typeProblem(here, "an integer", v)
			return
		}
		if i, err := n.Int64(); err != nil || reflect.Zero(t).OverflowInt(i) {
			s.typeProblem(here, "an integer", v)
		}
	default:
		// No other kind occurs in roleFile; treat it as unchecked rather than guess.
	}
}
