package ingest

import (
	"encoding/json"
	"regexp"
	"strings"
)

// NoCmd is the lead_cmd recorded for a Bash call whose input carries no
// `command` key at all.
//
// In the shell prototype this bucket was a PHANTOM: worker.sh capped tool inputs
// at 160 characters before the lead-command parser ran, so every Bash call with
// a long command lost its closing quote and fell through to NOCMD — 470 rows of
// pure artefact. T-3 forbids that truncation, and this parser reads the PARSED
// input object, so NOCMD here means the key is genuinely absent.
const NoCmd = "NOCMD"

// OtherCmd is the lead_cmd recorded when a command is present but its first
// token is not a plausible command word (e.g. it starts with a redirection or a
// quote).
const OtherCmd = "OTHER"

var (
	leadGroupOpen = regexp.MustCompile(`^\s*[({]\s*`)
	leadWrapper   = regexp.MustCompile(`^\s*(?:sudo|nice|time|command|exec)\s+`)
	leadAssign    = regexp.MustCompile(`^\s*[A-Za-z_][A-Za-z_0-9]*=\S*\s+`)
	leadExport    = regexp.MustCompile(`^\s*export\s+[^;]*;\s*`)
	leadToken     = regexp.MustCompile(`^([\w./-]+)`)
)

// LeadCmd extracts the leading command word from a Bash tool input, peeling the
// prefixes that would otherwise attribute every wrapped invocation to `sudo`,
// `env`-style assignments, or a subshell paren. It is precomputed at ingest so
// per-leading-command error rates are a GROUP BY rather than a re-parse.
//
// It takes the tool call's input as raw JSON and reads `.command` as a proper
// decoded string. The prototype instead regex-matched the RAW JSON TEXT and then
// hand-unescaped `\n`, `\t` and `\"` — with an explicit tolerance for a missing
// closing quote, because its 160-char cap kept producing them. Reading the
// decoded value removes that whole class of problem, which is the concrete
// payoff T-7 is asking for.
func LeadCmd(input json.RawMessage) string {
	if len(input) == 0 {
		return NoCmd
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(input, &obj); err != nil {
		return NoCmd
	}
	raw, ok := obj["command"]
	if !ok {
		return NoCmd
	}
	var cmd string
	if err := json.Unmarshal(raw, &cmd); err != nil {
		return NoCmd
	}
	return leadCmdFromString(cmd)
}

func leadCmdFromString(cmd string) string {
	// The prototype replaced escaped \n and \t with a space; the decoded string
	// carries the real characters, so replace those.
	c := strings.NewReplacer("\n", " ", "\t", " ", "\r", " ").Replace(cmd)
	for {
		changed := false
		for _, re := range []*regexp.Regexp{leadGroupOpen, leadWrapper, leadAssign, leadExport} {
			if loc := re.FindStringIndex(c); loc != nil && loc[0] == 0 {
				c = c[loc[1]:]
				changed = true
				break
			}
		}
		if !changed {
			break
		}
	}
	// Trim leading whitespace before reading the token. The prototype did NOT,
	// so a command beginning with a newline or tab attributed to OTHER even
	// though its leading word was perfectly readable — an artefact of matching
	// against raw JSON text rather than a decoded string. Correcting it cannot
	// affect the differential comparison, which is on total calls, total errors
	// and per-TOOL counts, none of which read lead_cmd.
	c = strings.TrimLeft(c, " \t\n\r")
	if m := leadToken.FindStringSubmatch(c); m != nil {
		return m[1]
	}
	return OtherCmd
}
