// Command plugin-conformance-check is pg2-z3u4f item 2's SKETCH of the
// conformance-check tooling pg2-23z9w's part 2 called for: extract every
// fenced shell command our plugins prescribe and assert each resolves to a
// DECISIVE ceta verdict (Approve/Ask/Reject) offline, rather than an abstain
// that falls through to the auto-mode LLM classifier or a dontAsk denial.
//
// # Scope of this pass — read before trusting the numbers
//
// This is genuinely a partial implementation, not a rushed "done": pg2-z3u4f's
// own brief is explicit that items 2 and 3 are each bead-sized on their own,
// and that a well-reasoned partial beats a rushed, wrong check that would gate
// every consumer's build incorrectly. What this tool DOES and does NOT cover:
//
//   - Extracts FENCED code blocks only (```bash / ```sh / ```shell / ```zsh).
//     Inline single-backtick spans (a bare `bd show <id>` in prose) are
//     DELIBERATELY NOT extracted: most inline backtick spans in this repo's
//     plugin prose are
//     identifiers, flag names, or bead IDs, not commands, and telling the two
//     apart reliably needs its own heuristic (or a doc-authoring convention)
//     that has not been designed yet. Extracting them naively would flood the
//     report with false positives and make it useless as a build gate.
//   - Treats every PHYSICAL line inside a matched fenced block as one
//     candidate command, after joining `\`-continued lines and stripping a
//     leading `$ ` shell-prompt marker (a common doc convention). It does NOT
//     detect interleaved OUTPUT lines shown inside the same fenced block (a
//     worked example's `$ ls` followed by a literal directory listing) — those
//     misclassify as unparseable/abstain noise. Distinguishing command from
//     output would need either a stricter `$ `-prefix-only convention (not
//     followed consistently by the existing docs) or a smarter per-block
//     heuristic; neither is built here.
//   - Does NOT AUTOMATICALLY distinguish "genuinely abstains" from "the
//     extracted text is not valid shell at all" (a parse failure also folds to
//     a NoOpinion verdict, per internal/engine's unparseableExpressionFloor) —
//     both are counted together as "non-decisive". The report DOES print each
//     candidate's verdict Reason alongside it, which is usually enough for a
//     human to tell the two apart at a glance (an unparseable-shell reason
//     names the parse failure explicitly; a genuine chain-exhaustion abstain's
//     reason is empty) — but nothing here classifies that automatically.
//   - Is NOT wired into any nix build (the marketplace derivation or
//     `nix flake check`) yet. It is a standalone, runnable, tested command —
//     run it by hand (see the package README-less usage below) or from a
//     script. Wiring it in is a separate, deliberate step: at minimum it needs
//     a `checks.<system>.*` derivation in THIS repo's flake.nix pointed at
//     `claude-marketplace/`, and each OTHER consumer repo whose plugins this
//     bead also covers (phillipg-nix-ziprecruiter, phillipg-nix-repo-base)
//     needs its OWN wiring pointed at its own plugin tree — this repo's
//     "consumer-blind" rule (see this repo's CLAUDE.md) means THIS repo's
//     flake cannot itself reach across to a sibling repo's checkout.
//   - Evaluates entirely OFFLINE with no side effects: it builds the engine via
//     setup.NewEngineForCWD (no shell store, no asklog.Store opened), so unlike
//     the compiled binary's hook mode it writes NOTHING to
//     ~/.local/share/claude-extended-tool-approver/asks.db. That is deliberate:
//     a conformance check that pollutes the real decision log with synthetic
//     rows would corrupt the very measurement (`report --group-by command`)
//     pg2-23z9w and pg2-z3u4f both depend on.
//
// # Usage
//
//	go run ./cmd/plugin-conformance-check [--cwd <dir>] <root>...
//
// Each <root> is scanned recursively for SKILL.md, commands/*.md, agents/*.md,
// any file under a hooks/ directory, and *-prompt.txt files. --cwd sets the
// CWD every extracted command is evaluated against (default: the first root
// given) — most extracted commands are generic snippets not tied to a
// specific repo path, so one representative CWD per invocation is enough;
// re-run once per repo root to get repo-accurate path resolution.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/engine"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/setup"
)

// candidate is one extracted command line, with enough provenance to let a
// human find it again.
type candidate struct {
	file string
	line int // 1-based, the line the fenced block's language tag opened on
	text string
}

// fenceRe matches a fenced code block tagged bash/sh/shell/zsh. (?s) makes
// `.` match newlines so the body can span multiple lines; the tag capture
// group is unused beyond anchoring which languages count.
var fenceRe = regexp.MustCompile("(?s)```(?:bash|sh|shell|zsh)[ \\t]*\\n(.*?)```")

// isPluginSource decides whether a path is one of pg2-23z9w's named source
// classes: every SKILL.md, commands/*.md, agents/*.md, hook script, and
// prompt template. Matched by shape (basename / parent-dir name / suffix),
// not by repo, so the same binary works unchanged against any consumer's
// plugin tree.
func isPluginSource(path string) bool {
	base := filepath.Base(path)
	if base == "SKILL.md" {
		return true
	}
	if strings.HasSuffix(base, "-prompt.txt") {
		return true
	}
	parent := filepath.Base(filepath.Dir(path))
	if (parent == "commands" || parent == "agents") && strings.HasSuffix(base, ".md") {
		return true
	}
	if parent == "hooks" {
		return true
	}
	return false
}

func walkSources(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if isPluginSource(path) {
			found = append(found, path)
		}
		return nil
	})
	return found, err
}

// extractCandidates finds every fenced bash/sh/shell/zsh block in content and
// returns one candidate per logical line inside it: blank and `#`-comment
// lines are dropped, a leading `$ ` prompt marker is stripped, and a
// trailing `\` continuation joins onto the next physical line.
func extractCandidates(file, content string) []candidate {
	var out []candidate
	for _, m := range fenceRe.FindAllStringSubmatchIndex(content, -1) {
		bodyStart, bodyEnd := m[2], m[3]
		body := content[bodyStart:bodyEnd]
		startLine := 1 + strings.Count(content[:bodyStart], "\n")

		rawLines := strings.Split(body, "\n")
		lineNo := startLine
		var acc strings.Builder
		accStartLine := 0
		for _, raw := range rawLines {
			trimmed := strings.TrimSpace(raw)
			if acc.Len() == 0 {
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					lineNo++
					continue
				}
				trimmed = strings.TrimPrefix(trimmed, "$ ")
				accStartLine = lineNo
			}
			if strings.HasSuffix(trimmed, "\\") {
				acc.WriteString(strings.TrimSuffix(trimmed, "\\"))
				acc.WriteString(" ")
				lineNo++
				continue
			}
			acc.WriteString(trimmed)
			out = append(out, candidate{file: file, line: accStartLine, text: acc.String()})
			acc.Reset()
			lineNo++
		}
	}
	return out
}

// verdict is the classification this tool cares about: whether ceta's engine
// reached a DECISIVE terminal verdict (Approve/Ask/Reject) or not (NoOpinion —
// either genuine chain exhaustion or an unparseable extraction).
type verdict struct {
	decisive bool
	decision hookio.Decision
	reason   string
}

// firstLine returns s up to its first newline (a rule's Reason is usually
// one sentence, but is not guaranteed to be — e.g. an unparseable-expression
// floor can quote multi-line source), so the grouped report stays one line
// per candidate.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func evaluate(eng *engine.Engine, cwd, command string) verdict {
	ti, _ := json.Marshal(hookio.BashToolInput{Command: command})
	res := eng.EvaluateHook(&hookio.HookInput{
		ToolName:  "Bash",
		CWD:       cwd,
		ToolInput: ti,
	})
	return verdict{
		decisive: res.Decision != hookio.NoOpinion,
		decision: res.Decision,
		reason:   res.Reason,
	}
}

func main() {
	cwdFlag := flag.String("cwd", "", "CWD to evaluate every extracted command against (default: first root argument)")
	flag.Parse()
	roots := flag.Args()
	if len(roots) == 0 {
		fmt.Fprintln(os.Stderr, "usage: plugin-conformance-check [--cwd <dir>] <root>...")
		os.Exit(2)
	}

	cwd := *cwdFlag
	if cwd == "" {
		cwd = roots[0]
	}
	eng := setup.NewEngineForCWD(cwd)

	var all []candidate
	for _, root := range roots {
		files, err := walkSources(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plugin-conformance-check: walk %s: %v\n", root, err)
			os.Exit(1)
		}
		for _, f := range files {
			content, err := os.ReadFile(f)
			if err != nil {
				fmt.Fprintf(os.Stderr, "plugin-conformance-check: read %s: %v\n", f, err)
				continue
			}
			all = append(all, extractCandidates(f, string(content))...)
		}
	}

	// reasons pairs each non-decisive candidate with WHY the engine did not
	// decide it — surfaced in the report so a human triaging it can tell a
	// genuine chain-exhaustion abstain from an extraction artifact (a
	// placeholder-riddled template line the shell parser rejected outright)
	// without re-running each line by hand.
	var decisive, nonDecisive []candidate
	reasons := map[candidate]string{}
	byDecision := map[hookio.Decision]int{}
	for _, c := range all {
		v := evaluate(eng, cwd, c.text)
		byDecision[v.decision]++
		if v.decisive {
			decisive = append(decisive, c)
		} else {
			nonDecisive = append(nonDecisive, c)
			reasons[c] = v.reason
		}
	}

	fmt.Printf("plugin-conformance-check: %d candidates extracted from %d root(s)\n", len(all), len(roots))
	fmt.Printf("  decisive: %d (approve=%d ask=%d reject=%d)\n", len(decisive),
		byDecision[hookio.Approve], byDecision[hookio.Ask], byDecision[hookio.Reject])
	fmt.Printf("  non-decisive (abstain / unparseable): %d\n", len(nonDecisive))

	if len(nonDecisive) == 0 {
		return
	}

	// Group non-decisive candidates by their first whitespace token (a rough
	// proxy for "executable"), so the report reads as a miss-class tally —
	// the same shape as `report --group-by command` — rather than a flat list.
	byFirstToken := map[string][]candidate{}
	for _, c := range nonDecisive {
		tok := strings.Fields(c.text)
		key := "(empty)"
		if len(tok) > 0 {
			key = tok[0]
		}
		byFirstToken[key] = append(byFirstToken[key], c)
	}
	var keys []string
	for k := range byFirstToken {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(byFirstToken[keys[i]]) > len(byFirstToken[keys[j]]) })

	fmt.Println("\nnon-decisive, grouped by leading token:")
	for _, k := range keys {
		group := byFirstToken[k]
		fmt.Printf("  %-4d %s\n", len(group), k)
		for i, c := range group {
			if i >= 3 {
				fmt.Printf("       ... and %d more\n", len(group)-3)
				break
			}
			fmt.Printf("       %s:%d: %q (%s)\n", c.file, c.line, c.text, firstLine(reasons[c]))
		}
	}
}
