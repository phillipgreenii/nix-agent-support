package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phillipgreenii/pg-router-handlers/internal/rolefile"
	"github.com/phillipgreenii/pg-router-handlers/internal/wire"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

// runValidate implements the optional `validate` INTF-HANDLER subcommand
// (INV-CCH-36): the adapter's ONE validation entry point, run by its nix module
// on every rendered role config during the build and called by pg-router doctor
// and at daemon startup. It reads the request's role object and decodes the
// role's config with the very decoder dispatch uses, so validate and dispatch
// cannot disagree; on top of that it checks that the pool directory the role
// references exists and warns about secret-looking sessionEnv keys. It runs no
// payload, touches no tracker and never opens a pg-wrap spec.
//
// Exit 0 with a reply means "ran" (an empty problems list means valid, and
// problems are listed, never an exit code). A request without a usable role is
// an error reply with conformance.ExitUsage.
func runValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	// --config is accepted for symmetry with dispatch and not checked: the launch
	// config is the handler's own and holds no role config.
	_ = fs.String("config", os.Getenv(rolefile.EnvConfig), "accepted, not checked")
	skipPoolDirs := fs.Bool("skip-pool-dirs", false, "skip ONLY the pool-directory existence check (for the nix build sandbox, where the pools do not exist yet); startup and doctor never pass it")
	if exit, done := parseFlags("validate", fs, args); done {
		return exit
	}
	return serveValidate(os.Stdin, os.Stdout, *skipPoolDirs)
}

// serveValidate is runValidate's testable core.
func serveValidate(stdin io.Reader, stdout io.Writer, skipPoolDirs bool) int {
	_, ro, exit, ok := readRequest("validate", "handler.validate", stdin, stdout)
	if !ok {
		return exit
	}
	_, problems := decodeRole(ro)
	var warnings []string
	for _, k := range sessionEnvKeys(ro.Config) {
		if rolefile.SecretLookingKey(k) {
			warnings = append(warnings, fmt.Sprintf("sessionEnv key %q looks like it carries a secret (name matches TOKEN, SECRET, KEY or PASSWORD): sessionEnv is visible in the session environment and event records; keep secrets in the prefix tool's own spec", k))
		}
	}
	if !skipPoolDirs {
		if p := poolDirProblem(ro.Config); p != "" {
			problems = append(problems, p)
		}
	}
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

// sessionEnvKeys returns the sorted keys of the config's sessionEnv map, or nil
// when it has none or is not a map (the shape check reports that).
func sessionEnvKeys(cfg map[string]any) []string {
	for k, v := range cfg {
		if strings.EqualFold(k, "sessionEnv") {
			env, _ := v.(map[string]any)
			return rolefile.SortedKeys(env)
		}
	}
	return nil
}

// poolDirProblem checks the pool directory the role uses: its own ccpool.poolDir,
// else this process's CCPOOL_POOL. It returns "" when the directory is usable or
// when none is named (the default pool is ccpool's own business).
func poolDirProblem(cfg map[string]any) string {
	dir, src := "", ""
	for k, v := range cfg {
		if !strings.EqualFold(k, "ccpool") {
			continue
		}
		cc, _ := v.(map[string]any)
		for ck, cv := range cc {
			if d, ok := cv.(string); ok && d != "" && strings.EqualFold(ck, "poolDir") {
				dir, src = d, "ccpool.poolDir"
			}
		}
	}
	if dir == "" {
		if d := os.Getenv("CCPOOL_POOL"); d != "" {
			dir, src = d, "CCPOOL_POOL"
		}
	}
	if dir == "" {
		return ""
	}
	switch fi, err := os.Stat(dir); {
	case err != nil:
		return fmt.Sprintf("pool directory %s (%s) is not usable: %v", dir, src, err)
	case !fi.IsDir():
		return fmt.Sprintf("pool directory %s (%s) is not a directory", dir, src)
	}
	return ""
}
