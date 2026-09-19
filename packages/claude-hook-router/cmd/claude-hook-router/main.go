// Command claude-hook-router is the Go runtime for the claude-hook-router
// Claude Code plugin (ADR 0071, phillipgreenii-nix-agent-support). It reads
// router-config.json, reads the incoming hook event JSON on stdin, selects
// and matcher-filters that event's delegate list, runs the sequential
// dispatch/merge loop against real delegate processes, and emits one
// hookSpecificOutput JSON to stdout.
//
// This binary MUST fail safe: any total failure (router-config.json
// missing/unparseable, stdin missing/unparseable, no CLAUDE_PLUGIN_ROOT)
// prints "{}" (abstain) and exits 0, rather than hanging or denying every
// Bash call on the machine — ADR 0071 §4 Phase B, B1 "Total-failure
// fallback"; §5.4 "Fail-safe default on total router failure".
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/claude-hook-router/internal/router"
)

// routerConfigFilename is the name router-config.json is written under by
// packet A1's nix generator, resolved relative to this binary's own
// ${CLAUDE_PLUGIN_ROOT}.
const routerConfigFilename = "router-config.json"

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr, os.Environ()))
}

func run(stdin io.Reader, stdout, stderr io.Writer, environ []string) int {
	env := envMap(environ)

	// Total-failure fallback: any error below this point still results in
	// {} on stdout, never a non-zero exit that could be mistaken for a
	// signal to deny/hang.
	output := dispatch(stdin, stderr, env)

	body, err := json.Marshal(output)
	if err != nil {
		// Should be unreachable (HookOutput always marshals), but keep the
		// fail-safe guarantee absolute.
		fmt.Fprintln(stdout, "{}")
		return 0
	}
	fmt.Fprintln(stdout, string(body))
	return 0
}

func dispatch(stdin io.Reader, stderr io.Writer, env map[string]string) router.HookOutput {
	pluginRoot := env["CLAUDE_PLUGIN_ROOT"]
	if pluginRoot == "" {
		fmt.Fprintln(stderr, "claude-hook-router: CLAUDE_PLUGIN_ROOT is not set; failing safe with {}")
		return router.HookOutput{}
	}

	cfg, err := router.LoadConfig(filepath.Join(pluginRoot, routerConfigFilename))
	if err != nil {
		fmt.Fprintf(stderr, "claude-hook-router: %v; failing safe with {}\n", err)
		return router.HookOutput{}
	}

	rawBody, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "claude-hook-router: read stdin: %v; failing safe with {}\n", err)
		return router.HookOutput{}
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		fmt.Fprintf(stderr, "claude-hook-router: parse incoming hook JSON: %v; failing safe with {}\n", err)
		return router.HookOutput{}
	}

	event, err := stringField(payload, "hook_event_name")
	if err != nil {
		fmt.Fprintf(stderr, "claude-hook-router: %v; failing safe with {}\n", err)
		return router.HookOutput{}
	}

	delegates := cfg[event]
	if len(delegates) == 0 {
		// No registration for this event at all: nothing to dispatch, and
		// this is not a failure — just an abstain.
		return router.HookOutput{}
	}

	delegates = filterByMatcher(payload, event, delegates)
	if len(delegates) == 0 {
		return router.HookOutput{}
	}

	dataDir := env["CLAUDE_PLUGIN_DATA"]
	if dataDir == "" {
		// No CLAUDE_PLUGIN_DATA supplied: still dispatch (this is not a
		// total-failure case), just without an attribution log and with
		// each delegate's own subdirectory rooted under a private temp
		// directory rather than failing the whole call.
		dataDir = filepath.Join(os.TempDir(), "claude-hook-router")
	}

	var attrib *router.AttributionLogger
	if logger, err := router.NewAttributionLogger(dataDir); err != nil {
		fmt.Fprintf(stderr, "claude-hook-router: attribution log unavailable: %v\n", err)
	} else {
		attrib = logger
	}

	result := router.Dispatch(context.Background(), event, delegates, payload, env["CLAUDE_PROJECT_DIR"], dataDir)
	for _, entry := range result.Entries {
		if err := attrib.Log(entry); err != nil {
			fmt.Fprintf(stderr, "claude-hook-router: attribution log write failed: %v\n", err)
		}
	}

	return result.Output
}

// filterByMatcher applies each delegate's own original matcher (ADR 0071
// §2.3) against the event-appropriate payload field, returning only the
// delegates whose matcher matches. A delegate is kept unfiltered (always
// dispatched) if this event has no known matcher-target field.
func filterByMatcher(payload map[string]json.RawMessage, event string, delegates []router.Delegate) []router.Delegate {
	field, ok := router.EventMatchField(event)
	if !ok {
		return delegates
	}
	value, _ := stringField(payload, field) // absent field => "" => fails a non-match-all matcher, matches match-all

	kept := make([]router.Delegate, 0, len(delegates))
	for _, d := range delegates {
		if router.MatchesEvent(d.Matcher, value) {
			kept = append(kept, d)
		}
	}
	return kept
}

func stringField(payload map[string]json.RawMessage, key string) (string, error) {
	raw, ok := payload[key]
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("field %q is not a string: %w", key, err)
	}
	return value, nil
}

func envMap(environ []string) map[string]string {
	m := make(map[string]string, len(environ))
	for _, kv := range environ {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				m[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	return m
}
