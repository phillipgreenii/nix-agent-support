package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/rolefile"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/wire"
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
	roleConfig := fs.String("role-config", os.Getenv(rolefile.EnvRoleConfig), "path to this process's own role config JSON (or "+rolefile.EnvRoleConfig+")")
	// --config is accepted for symmetry with dispatch and not checked: the launch
	// config is the handler's own and holds no role JSON.
	_ = fs.String("config", os.Getenv(rolefile.EnvConfig), "path to this process's own launch config JSON (or "+rolefile.EnvConfig+"); accepted, not checked")
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
		wire.WriteErrorReply(stdout, "malformed JSON: "+err.Error())
		return conformance.ExitError
	}
	if err := conformance.Check("handler.validate", v); err != nil {
		wire.WriteErrorReply(stdout, err.Error())
		return conformance.ExitError
	}
	problems, warnings := validateRoleFile(rolePath, skipPoolDirs)
	if problems == nil {
		problems = []string{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	wire.WriteReply(stdout, map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"problems":      problems,
		"warnings":      warnings,
	})
	return conformance.ExitOK
}

// validateRoleFile runs every check on the role file at path and returns the
// problems and warnings in a stable order: the order the checks run (read and
// decode, strict shape, sessionEnv keys, load rules, warnings, pool
// directories), keys sorted within a check. Nothing is deduplicated.
func validateRoleFile(path string, skipPoolDirs bool) (problems, warnings []string) {
	if path == "" {
		return []string{"no role config given (--role-config, or " + rolefile.EnvRoleConfig + ")"}, nil
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
	shape := rolefile.ShapeChecker{}
	shape.Walk("", top, reflect.TypeOf(rolefile.RoleFile{}), rolefile.TopLevelExtras)
	problems = append(problems, shape.Problems...)

	// 2. sessionEnv keys: every offending key, not just the first.
	sessionEnv, _ := top["sessionEnv"].(map[string]any)
	for _, k := range rolefile.SortedKeys(sessionEnv) {
		if err := rolefile.ValidateSessionEnv(map[string]string{k: ""}); err != nil {
			problems = append(problems, "sessionEnv: "+err.Error())
		}
	}

	// 3. The load rules dispatch applies (decode, per-type blocks, budget,
	// durations, effort, ...), so validate and dispatch agree. Skipped when the
	// shape check found a type error: the decode would only repeat it.
	if !shape.TypeError {
		if _, err := rolefile.DecodeRole(path, data, false); err != nil {
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
	for _, k := range rolefile.SortedKeys(sessionEnv) {
		if rolefile.SecretLookingKey(k) {
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
