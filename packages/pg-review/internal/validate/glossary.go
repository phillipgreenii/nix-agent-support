package validate

import (
	"bufio"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
)

// StateWords are the capitalized words that name a state of a review item,
// topic, run or bead. One used as a state mid-sentence needs a glossary
// entry; at the start of a sentence it reads as ordinary English and is left
// alone.
var StateWords = []string{
	"Pending", "Waiting", "Adopted", "Rejected", "Deferred", "Filed",
	"Filing", "Running", "Failed", "Skipped", "AwaitingAnalyst",
}

var (
	idRe       = regexp.MustCompile(`\b[A-Z]+-\d+[a-z]?\b`)
	pgRe       = regexp.MustCompile(`\bpg-[a-z0-9][a-z0-9-]*\b`)
	backtickRe = regexp.MustCompile("`([^`\n]+)`")
	wordRe     = regexp.MustCompile(`[A-Za-z][A-Za-z0-9-]*`)
	stateRe    = regexp.MustCompile(`(?:^|[^\s.!?:]\s+)(` + strings.Join(StateWords, "|") + `)\b`)

	assetsOnce sync.Once
	vocab      map[string]bool
	allow      map[string]bool
	assetsErr  error
)

func loadList(fsys fs.FS, name string) (map[string]bool, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	set := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		set[strings.ToLower(line)] = true
	}
	return set, sc.Err()
}

func assets() {
	assetsOnce.Do(func() {
		fsys := builtin.FS()
		if vocab, assetsErr = loadList(fsys, "vocabulary.txt"); assetsErr != nil {
			return
		}
		allow, assetsErr = loadList(fsys, "allowlist.txt")
	})
}

// Vocabulary returns the embedded project vocabulary, sorted.
func Vocabulary() []string { return keys(func() map[string]bool { assets(); return vocab }()) }

// Allowlist returns the embedded plain-English allowlist, sorted.
func Allowlist() []string { return keys(func() map[string]bool { assets(); return allow }()) }

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Terms extracts every token of text that needs a glossary entry: id
// patterns, pg- names, backticked strings, vocabulary words and state words
// used as a state. Tokens are returned in first-seen order, deduplicated.
func Terms(text string) []string {
	assets()
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, m := range idRe.FindAllString(text, -1) {
		add(trimSampleLetter(m))
	}
	for _, m := range pgRe.FindAllString(text, -1) {
		add(m)
	}
	for _, m := range backtickRe.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, w := range wordRe.FindAllString(text, -1) {
		if vocab[strings.ToLower(w)] {
			add(w)
		}
	}
	for _, m := range stateRe.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	return out
}

// trimSampleLetter drops the sample letter of an E-nnnx reference.
func trimSampleLetter(id string) string {
	if n := len(id); n > 0 && id[n-1] >= 'a' && id[n-1] <= 'z' {
		return id[:n-1]
	}
	return id
}

// GlossaryGaps returns the tokens of text that are neither defined by
// glossary (a list of terms; backticks are ignored) nor on the allowlist.
func GlossaryGaps(text string, glossary []string) []string {
	assets()
	defined := map[string]bool{}
	for _, g := range glossary {
		defined[strings.ToLower(strings.Trim(strings.TrimSpace(g), "`"))] = true
	}
	var gaps []string
	for _, t := range Terms(text) {
		k := strings.ToLower(t)
		if defined[k] || allow[k] {
			continue
		}
		gaps = append(gaps, t)
	}
	return gaps
}
