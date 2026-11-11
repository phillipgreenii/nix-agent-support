package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/schemas"
)

// The operator level verbs: `pg-router level list|set|clear|refresh`, and `pause`
// and `resume`, which are level set / level clear on the router resource.
//
// Exported names: none (package main). Names: runLevel, runPause, runResume,
// parseOverrideFor, levelView, renderLevels, overrideText, formatRemaining.
//
// Exit codes: 0 ok, 2 usage errors detectable without the router (bad duration,
// both or neither of --for / --until-cleared, a --for over the 24 hour cap), 1
// when the router rejects the request (unknown resource or level) or is
// unreachable; level refresh exits 0 when every resource is applied, 1 when any
// is failed or no_source.

// levelCallTimeout bounds a level refresh round trip: the pull query's own hard
// deadline (20 s by default) plus margin, and several sensors run concurrently.
const levelCallTimeout = 2 * time.Minute

// errOverrideCap is the usage error of a --for over 24 hours (rejected, never clamped).
var errOverrideCap = errors.New("override capped at 24h; use --until-cleared")

// parseOverrideFor parses the --for grammar: a plain Go duration, no day form;
// missing, zero, negative or malformed is a usage error; over 24h is rejected.
func parseOverrideFor(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --for %q: want a Go duration such as 90m or 3h (no day form)", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid --for %q: must be positive", s)
	}
	if d > core.MaxOverride {
		return 0, errOverrideCap
	}
	return d, nil
}

// levelView is one entry of a level-list reply or the status levels array.
type levelView struct {
	Resource   string `json:"resource"`
	Level      string `json:"level"`
	Source     string `json:"source"`
	Owner      string `json:"owner"`
	ObservedAt string `json:"observed_at"`
	ReceivedAt string `json:"received_at"`
	ExpiresAt  string `json:"expires_at"`
	AgeS       int64  `json:"age_s"`
	Stale      bool   `json:"stale"`
	Since      string `json:"since"`
	Reading    *struct {
		Value float64 `json:"value"`
		Unit  string  `json:"unit"`
	} `json:"reading"`
	Override *struct {
		UntilCleared bool   `json:"until_cleared"`
		ExpiresAt    string `json:"expires_at"`
	} `json:"override"`
}

// formatRemaining writes d rounded DOWN to the minute with at most two units
// (3h, 1h58m, 12m, 0m).
func formatRemaining(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	m := int64(d / time.Minute)
	h, m := m/60, m%60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dm", m)
}

// overrideText is the status text of an override: "OVERRIDE by operator, expires
// in 3h" or "OVERRIDE by operator, until cleared".
func overrideText(v levelView, now time.Time) string {
	if v.Override == nil {
		return ""
	}
	who := textsafe.Sanitize(v.Owner)
	if v.Override.UntilCleared {
		return "OVERRIDE by " + who + ", until cleared"
	}
	t, err := time.Parse(time.RFC3339Nano, v.Override.ExpiresAt)
	if err != nil {
		return "OVERRIDE by " + who
	}
	return "OVERRIDE by " + who + ", expires in " + formatRemaining(t.Sub(now))
}

// renderLevels writes one line per resource.
func renderLevels(w io.Writer, levels []levelView, now time.Time) {
	if len(levels) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}
	for _, v := range levels {
		name := textsafe.Sanitize(v.Resource)
		if v.Level == "" {
			fmt.Fprintf(w, "  %s: no reading\n", name)
			continue
		}
		line := fmt.Sprintf("  %s: %s (%s", name, textsafe.Sanitize(v.Level), textsafe.Sanitize(v.Source))
		if v.Source != "operator" && v.Owner != "" {
			line += " " + textsafe.Sanitize(v.Owner)
		}
		line += fmt.Sprintf(", age %s", (time.Duration(v.AgeS) * time.Second))
		if v.Stale {
			line += ", stale"
		}
		line += ")"
		if v.Reading != nil {
			line += fmt.Sprintf(" reading %g %s", v.Reading.Value, textsafe.Sanitize(v.Reading.Unit))
		}
		if t := overrideText(v, now); t != "" {
			line += "  " + t
		}
		fmt.Fprintln(w, line)
	}
}

// runLevel dispatches `level list|set|clear|refresh`.
func runLevel(args []string) int { return runLevelIO(args, os.Stdout, os.Stderr) }

func runLevelIO(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "level: a verb is required: list, set, clear or refresh")
		return conformance.ExitUsage
	}
	switch args[0] {
	case "list":
		return runLevelList(args[1:], stdout, stderr)
	case "set":
		return runLevelSet(args[1:], stdout, stderr)
	case "clear":
		return runLevelClear(args[1:], stdout, stderr)
	case "refresh":
		return runLevelRefresh(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, helpText)
		return exitOK
	}
	fmt.Fprintf(stderr, "level: unknown verb %q: want list, set, clear or refresh\n", args[0])
	return conformance.ExitUsage
}

func levelRef(stderr io.Writer, verb, socket, token string) (core.Ref, int) {
	ref, err := locateCore(socket, token)
	if err != nil {
		reportNoCore(stderr, verb, err)
		return core.Ref{}, conformance.ExitError
	}
	return ref, exitOK
}

func runLevelList(args []string, stdout, stderr io.Writer) int {
	fs, socket, token := gateFlags("level list")
	asJSON := fs.Bool("json", false, "emit JSON")
	pos, code, ok := parseGateFlags(fs, "level list", args)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "level list: unexpected argument:", pos[0])
		return conformance.ExitUsage
	}
	ref, c := levelRef(stderr, core.SubcommandLevelList, *socket, *token)
	if c != exitOK {
		return c
	}
	var reply struct {
		Levels []levelView `json:"levels"`
	}
	raw := rawReply{}
	if c := callVerb(stderr, ref, core.SubcommandLevelList, core.LevelListReplySchema, map[string]any{"schemaVersion": schemas.SchemaVersion}, &raw, core.CallOptions{}); c != exitOK {
		return c
	}
	if *asJSON {
		fmt.Fprintln(stdout, string(raw))
		return exitOK
	}
	_ = json.Unmarshal(raw, &reply)
	renderLevels(stdout, reply.Levels, time.Now())
	return exitOK
}

// rawReply keeps a reply's bytes so --json can echo it verbatim.
type rawReply []byte

// UnmarshalJSON implements json.Unmarshaler.
func (r *rawReply) UnmarshalJSON(b []byte) error { *r = append((*r)[:0], b...); return nil }

type levelSetReply struct {
	Resource     string `json:"resource"`
	Level        string `json:"level"`
	UntilCleared bool   `json:"until_cleared"`
	ExpiresAt    string `json:"expires_at"`
}

func runLevelSet(args []string, stdout, stderr io.Writer) int {
	fs, socket, token := gateFlags("level set")
	forStr := fs.String("for", "", "override duration (a Go duration, at most 24h)")
	untilCleared := fs.Bool("until-cleared", false, "override until `level clear`")
	owner := fs.String("owner", core.LevelOwnerOperator, "who sets the override")
	asJSON := fs.Bool("json", false, "emit JSON")
	pos, code, ok := parseGateFlags(fs, "level set", args)
	if !ok {
		return code
	}
	if len(pos) != 2 {
		fmt.Fprintln(stderr, "level set: want <resource> <LEVEL> (--for <duration> | --until-cleared)")
		return conformance.ExitUsage
	}
	req, code := overrideRequest(stderr, "level set", pos[0], pos[1], *owner, *forStr, *untilCleared)
	if code != exitOK {
		return code
	}
	ref, c := levelRef(stderr, core.SubcommandLevelSet, *socket, *token)
	if c != exitOK {
		return c
	}
	var reply levelSetReply
	var raw rawReply
	if c := callVerb(stderr, ref, core.SubcommandLevelSet, core.LevelSetReplySchema, req, &raw, core.CallOptions{}); c != exitOK {
		return c
	}
	if *asJSON {
		fmt.Fprintln(stdout, string(raw))
		return exitOK
	}
	_ = json.Unmarshal(raw, &reply)
	fmt.Fprintf(stdout, "%s set to %s: %s\n", textsafe.Sanitize(reply.Resource), textsafe.Sanitize(reply.Level), expiryPhrase(reply, time.Now()))
	return exitOK
}

func expiryPhrase(r levelSetReply, now time.Time) string {
	if r.UntilCleared {
		return "OVERRIDE until cleared"
	}
	if t, err := time.Parse(time.RFC3339Nano, r.ExpiresAt); err == nil {
		return "OVERRIDE, expires in " + formatRemaining(t.Sub(now))
	}
	return "OVERRIDE"
}

// overrideRequest builds the level-set request, or reports the usage error.
func overrideRequest(stderr io.Writer, name, resource, level, owner, forStr string, untilCleared bool) (map[string]any, int) {
	if (forStr == "") == !untilCleared {
		fmt.Fprintln(stderr, name+": exactly one of --for and --until-cleared is required")
		return nil, conformance.ExitUsage
	}
	req := map[string]any{"schemaVersion": schemas.SchemaVersion, "resource": resource, "level": level, "owner": owner}
	if untilCleared {
		req["until_cleared"] = true
		return req, exitOK
	}
	d, err := parseOverrideFor(forStr)
	if err != nil {
		fmt.Fprintln(stderr, name+":", err)
		return nil, conformance.ExitUsage
	}
	req["for_s"] = int64(d / time.Second)
	return req, exitOK
}

type levelClearReply struct {
	Cleared []string `json:"cleared"`
}

func runLevelClear(args []string, stdout, stderr io.Writer) int {
	fs, socket, token := gateFlags("level clear")
	all := fs.Bool("all", false, "clear every operator override")
	by := fs.String("by", defaultGateCaller, "who clears it (debug only)")
	pos, code, ok := parseGateFlags(fs, "level clear", args)
	if !ok {
		return code
	}
	if (len(pos) == 1) == *all || len(pos) > 1 {
		fmt.Fprintln(stderr, "level clear: want exactly one of <resource> and --all")
		return conformance.ExitUsage
	}
	req := map[string]any{"schemaVersion": schemas.SchemaVersion, "by": *by}
	if *all {
		req["all"] = true
	} else {
		req["resource"] = pos[0]
	}
	ref, c := levelRef(stderr, core.SubcommandLevelClear, *socket, *token)
	if c != exitOK {
		return c
	}
	var reply levelClearReply
	if c := callVerb(stderr, ref, core.SubcommandLevelClear, core.LevelClearReplySchema, req, &reply, core.CallOptions{}); c != exitOK {
		return c
	}
	if len(reply.Cleared) == 0 {
		fmt.Fprintln(stdout, "no operator override to clear")
		return exitOK
	}
	fmt.Fprintln(stdout, "cleared: "+textsafe.Sanitize(strings.Join(reply.Cleared, ", ")))
	return exitOK
}

type levelRefreshReply struct {
	Results []struct {
		Resource string `json:"resource"`
		Outcome  string `json:"outcome"`
		Level    string `json:"level"`
		Reason   string `json:"reason"`
	} `json:"results"`
}

func runLevelRefresh(args []string, stdout, stderr io.Writer) int {
	fs, socket, token := gateFlags("level refresh")
	asJSON := fs.Bool("json", false, "emit JSON")
	pos, code, ok := parseGateFlags(fs, "level refresh", args)
	if !ok {
		return code
	}
	if pos == nil {
		pos = []string{}
	}
	ref, c := levelRef(stderr, core.SubcommandLevelRefresh, *socket, *token)
	if c != exitOK {
		return c
	}
	var raw rawReply
	req := map[string]any{"schemaVersion": schemas.SchemaVersion, "resources": pos}
	if c := callVerb(stderr, ref, core.SubcommandLevelRefresh, core.LevelRefreshReplySchema, req, &raw, core.CallOptions{CallTimeout: levelCallTimeout}); c != exitOK {
		return c
	}
	var reply levelRefreshReply
	_ = json.Unmarshal(raw, &reply)
	bad := false
	for _, r := range reply.Results {
		if r.Outcome != "applied" {
			bad = true
		}
	}
	if *asJSON {
		fmt.Fprintln(stdout, string(raw))
	} else {
		for _, r := range reply.Results {
			line := "  " + textsafe.Sanitize(r.Resource) + ": " + r.Outcome
			if r.Level != "" {
				line += " " + textsafe.Sanitize(r.Level)
			}
			if r.Reason != "" {
				line += " (" + textsafe.Sanitize(r.Reason) + ")"
			}
			fmt.Fprintln(stdout, line)
		}
		if len(reply.Results) == 0 {
			fmt.Fprintln(stdout, "  (no level source)")
		}
	}
	if bad {
		return exitGeneric
	}
	return exitOK
}

// runPause implements `pause [--for DUR] [--owner who]`: level set on the router
// resource, level PAUSED, until cleared unless --for is given.
func runPause(args []string) int { return runPauseIO(args, os.Stdout, os.Stderr) }

func runPauseIO(args []string, stdout, stderr io.Writer) int {
	fs, socket, token := gateFlags("pause")
	forStr := fs.String("for", "", "pause for this long (a Go duration, at most 24h); default until resumed")
	owner := fs.String("owner", defaultGateCaller, "who pauses")
	pos, code, ok := parseGateFlags(fs, "pause", args)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "pause: unexpected argument:", pos[0])
		return conformance.ExitUsage
	}
	req, code := overrideRequest(stderr, "pause", core.RouterResource, core.LevelPaused, *owner, *forStr, *forStr == "")
	if code != exitOK {
		return code
	}
	ref, c := levelRef(stderr, "pause", *socket, *token)
	if c != exitOK {
		return c
	}
	var reply levelSetReply
	if c := callVerb(stderr, ref, core.SubcommandLevelSet, core.LevelSetReplySchema, req, &reply, core.CallOptions{}); c != exitOK {
		return c
	}
	if reply.UntilCleared {
		fmt.Fprintln(stdout, "paused until resumed (pg-router resume)")
	} else {
		fmt.Fprintln(stdout, "paused, "+strings.TrimPrefix(expiryPhrase(reply, time.Now()), "OVERRIDE, "))
	}
	return exitOK
}

// runResume implements `resume [--all] [--by who]`: level clear on the router
// resource, or on every resource with an operator override with --all.
func runResume(args []string) int { return runResumeIO(args, os.Stdout, os.Stderr) }

func runResumeIO(args []string, stdout, stderr io.Writer) int {
	fs, socket, token := gateFlags("resume")
	all := fs.Bool("all", false, "clear every operator override on every resource")
	by := fs.String("by", defaultGateCaller, "who resumes (debug only)")
	pos, code, ok := parseGateFlags(fs, "resume", args)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "resume: unexpected argument:", pos[0])
		return conformance.ExitUsage
	}
	req := map[string]any{"schemaVersion": schemas.SchemaVersion, "by": *by}
	if *all {
		req["all"] = true
	} else {
		req["resource"] = core.RouterResource
	}
	ref, c := levelRef(stderr, "resume", *socket, *token)
	if c != exitOK {
		return c
	}
	var reply levelClearReply
	if c := callVerb(stderr, ref, core.SubcommandLevelClear, core.LevelClearReplySchema, req, &reply, core.CallOptions{}); c != exitOK {
		return c
	}
	switch {
	case len(reply.Cleared) == 0 && *all:
		fmt.Fprintln(stdout, "no operator override to clear")
	case len(reply.Cleared) == 0:
		fmt.Fprintln(stdout, "not paused")
	case *all:
		fmt.Fprintln(stdout, "cleared: "+textsafe.Sanitize(strings.Join(reply.Cleared, ", ")))
	default:
		fmt.Fprintln(stdout, "resumed")
	}
	return exitOK
}

var _ = flag.ErrHelp
