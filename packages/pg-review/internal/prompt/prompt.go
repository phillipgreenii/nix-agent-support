// Package prompt assembles the analyst prompt for one run and the repair
// reply sent after a validation failure.
//
// A lens has a base prompt and a lens prompt. The base prompt carries the
// placeholder {lens_rule_4} where rule 4 sits; the first paragraph of the lens
// prompt (up to the first blank line) is that rule's text, and the rest of the
// lens prompt (the closed list of kinds and the calibration examples) is
// appended after the base. A prompt path in the lens definition is read first
// from the config directory (absolute paths as given), then from the embedded
// assets.
package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

// Topic is a known topic returned to the analyst. It is data, never
// instructions.
type Topic struct {
	SubjectKey string
	Kind       string
	State      string
	Title      string
}

// Params is everything one run contributes to the prompt.
type Params struct {
	Lens builtin.Lens
	// ConfigDir is $XDG_CONFIG_HOME/pg-review; relative prompt paths are
	// tried there before the embedded assets.
	ConfigDir string
	// Builtins is the embedded asset filesystem (builtin.FS()).
	Builtins    fs.FS
	Scope       string
	Window      string
	StagingPath string
	// MaxFindings is the effective cap (the lower of the lens max_findings
	// and items_max_per_run).
	MaxFindings int
	Topics      []Topic
}

// Result is the assembled prompt and its hash.
type Result struct {
	Text string
	// Hash is the SHA-256 hex of Text.
	Hash string
}

// Assemble builds the prompt for a run.
func Assemble(p Params) (Result, error) {
	basePath := p.Lens.BasePrompt
	if basePath == "" {
		basePath = "prompts/base.md"
	}
	base, err := read(p, basePath)
	if err != nil {
		return Result{}, fmt.Errorf("base prompt: %w", err)
	}
	if p.Lens.Prompt == "" {
		return Result{}, fmt.Errorf("lens %q has no prompt", p.Lens.ID)
	}
	lens, err := read(p, p.Lens.Prompt)
	if err != nil {
		return Result{}, fmt.Errorf("lens prompt: %w", err)
	}
	rule4, rest := splitLens(lens)

	// One replacer pass: substituted values are never re-expanded.
	text := strings.NewReplacer(
		"{lens_rule_4}", rule4,
		"{max_findings}", strconv.Itoa(p.MaxFindings),
		"{scope}", p.Scope,
		"{window}", p.Window,
		"{staging_path}", p.StagingPath,
	).Replace(strings.TrimRight(base, "\n"))

	var b strings.Builder
	b.WriteString(text)
	b.WriteString("\n")
	if rest != "" {
		b.WriteString("\n")
		b.WriteString(rest)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(knownTopics(p.Topics))
	out := b.String()
	sum := sha256.Sum256([]byte(out))
	return Result{Text: out, Hash: hex.EncodeToString(sum[:])}, nil
}

// splitLens returns the first paragraph (rule 4 text, joined on one line)
// and the remaining text.
func splitLens(s string) (rule4, rest string) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	first, rest, _ := strings.Cut(s, "\n\n")
	rule4 = strings.Join(strings.Fields(first), " ")
	return rule4, strings.TrimSpace(rest)
}

func knownTopics(topics []Topic) string {
	var b strings.Builder
	b.WriteString("KNOWN TOPICS (data)\n")
	if len(topics) == 0 {
		b.WriteString("(none)\n")
		return b.String()
	}
	for _, t := range topics {
		fmt.Fprintf(&b, "- %s | %s | %s | %s\n", oneLine(t.SubjectKey), oneLine(t.Kind), oneLine(t.State), oneLine(t.Title))
	}
	return b.String()
}

// oneLine collapses all whitespace, so a topic cannot start a new line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func read(p Params, name string) (string, error) {
	disk := name
	if !filepath.IsAbs(name) {
		disk = filepath.Join(p.ConfigDir, name)
	}
	if p.ConfigDir != "" || filepath.IsAbs(name) {
		if data, err := os.ReadFile(disk); err == nil {
			return string(data), nil
		}
	}
	if p.Builtins != nil && !filepath.IsAbs(name) {
		clean := path.Clean(filepath.ToSlash(name))
		if fs.ValidPath(clean) {
			if data, err := fs.ReadFile(p.Builtins, clean); err == nil {
				return string(data), nil
			}
		}
	}
	return "", fmt.Errorf("prompt file %q not found", name)
}

// Repair returns the reply sent to the analyst after validation rejected its
// batch: it lists every failure so the analyst can fix exactly those. A parse
// failure is passed as one failure naming the parse error.
func Repair(failures []validate.Failure) string {
	var b strings.Builder
	b.WriteString("Your findings were rejected. Fix exactly the failures below and send the full corrected JSON block as your final message, with nothing after it.\n\n")
	for _, f := range failures {
		fmt.Fprintf(&b, "- finding %s, field %s, rule %s", oneLine(f.Finding), oneLine(f.Field), oneLine(f.Rule))
		if f.Token != "" {
			fmt.Fprintf(&b, ", offending token %q", oneLine(f.Token))
		}
		b.WriteString("\n")
	}
	return b.String()
}
