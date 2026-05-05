package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

// FirstPrompt returns the first "user" event whose content is a plain text
// message (not a tool_result). Empty string when none found.
func FirstPrompt(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Type != "user" {
			continue
		}
		text := plainUserText(ev.Message.Content)
		if text == "" {
			continue
		}
		cleaned := cleanPromptText(text)
		if cleaned == "" || strings.HasPrefix(cleaned, "<") {
			continue // unrecognised XML envelope — try next user event
		}
		return cleaned, nil
	}
	return "", scanner.Err()
}

// plainUserText returns the first text block that is NOT a tool_result wrapper.
func plainUserText(blocks ContentList) string {
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			return b.Text
		}
	}
	return ""
}

// slashCommandEnvelopeRe matches the slash-command metadata envelope that
// Claude Code injects as the user's transcript message when a slash command
// runs. The envelope always starts with <command-message> (optionally after
// whitespace) and includes a <command-args>…</command-args> block carrying
// the arguments the user actually typed. Anchoring to the string start is
// important: a plain-text user prompt that merely *mentions* the tag (for
// instance, documentation or a plan file) must NOT be mis-extracted as a
// slash-command argument.
var slashCommandEnvelopeRe = regexp.MustCompile(
	`(?s)\A\s*<command-message>.*?<command-args>(.*?)</command-args>`,
)

// envelopeTagNames lists the Claude-Code-injected tag wrappers that should be
// stripped from user-typed text. Go's RE2 has no backreferences, so we compile
// one regex per tag instead of a single `<(name)>...</\1>` pattern.
var envelopeTagNames = []string{
	"command-message",
	"command-name",
	"command-args",
	"system-reminder",
	"local-command-stdout",
	"local-command-stderr",
	"local-command-caveat",
	"user-prompt-submit-hook",
	"caveman-message",
}

var envelopeTagRes = func() []*regexp.Regexp {
	res := make([]*regexp.Regexp, 0, len(envelopeTagNames))
	for _, name := range envelopeTagNames {
		res = append(res, regexp.MustCompile(`(?s)<`+name+`>.*?</`+name+`>`))
	}
	return res
}()

// blankLineRuns collapses 2+ consecutive blank lines (possibly with whitespace)
// into a single space.
var blankLineRuns = regexp.MustCompile(`(?:[ \t]*\r?\n){2,}`)

// cleanPromptText strips Claude Code's injected XML envelope from a user text
// block, keeping the actual prompt the user typed.
//
// Rules:
//  1. If a <command-args> block exists (slash command), return its body —
//     that's the real typed argument to the command.
//  2. Otherwise, strip any known envelope tag blocks entirely.
//  3. Trim whitespace and collapse runs of blank lines into a single space.
func cleanPromptText(s string) string {
	if m := slashCommandEnvelopeRe.FindStringSubmatch(s); m != nil {
		return normalizeWhitespace(m[1])
	}
	stripped := s
	for _, re := range envelopeTagRes {
		stripped = re.ReplaceAllString(stripped, "")
	}
	return normalizeWhitespace(stripped)
}

func normalizeWhitespace(s string) string {
	s = blankLineRuns.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
