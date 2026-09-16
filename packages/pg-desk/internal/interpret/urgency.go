package interpret

import (
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// --- enrichment: kind, languages, size --------------------------------------
// Ported from packages/pg-pr/internal/enrich (classifyKind/bucketSize
// exactly; detectLanguages replaced — see interpret.go's package doc for
// why go-enry could not be added as a new dependency).

// bucketSize maps a total changed-line count to a coarse size bucket.
// Ported verbatim from enrich.bucketSize.
func bucketSize(total int) string {
	switch {
	case total < 10:
		return "XS"
	case total < 30:
		return "S"
	case total < 100:
		return "M"
	case total < 500:
		return "L"
	default:
		return "XL"
	}
}

// ccTypeRe recognizes a conventional-commit header. Ported verbatim from
// enrich.ccTypeRe.
var ccTypeRe = regexp.MustCompile(`^\s*([a-zA-Z]+)(\([^)]*\))?!?:`)

// classifyKind returns the single dominant change kind. Ported verbatim
// from enrich.classifyKind (precedence: title, then branch, then commit
// majority, then "other").
func classifyKind(title, branch string, commits []string) string {
	if k := kindFromConventional(title); k != "" {
		return k
	}
	if k := kindFromBranch(branch); k != "" {
		return k
	}
	if k := kindFromCommitMajority(commits); k != "" {
		return k
	}
	return "other"
}

func kindFromConventional(s string) string {
	m := ccTypeRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return mapCCType(strings.ToLower(m[1]))
}

func kindFromBranch(b string) string {
	seg := b
	if i := strings.IndexByte(b, '/'); i >= 0 {
		seg = b[:i]
	}
	return mapCCType(strings.ToLower(seg))
}

func kindFromCommitMajority(commits []string) string {
	counts := map[string]int{}
	for _, c := range commits {
		first := strings.SplitN(c, "\n", 2)[0]
		if k := kindFromConventional(first); k != "" {
			counts[k]++
		}
	}
	best := ""
	for k, n := range counts {
		if n > counts[best] || (n == counts[best] && (best == "" || k < best)) {
			best = k
		}
	}
	return best
}

func mapCCType(t string) string {
	switch t {
	case "feat", "feature":
		return "feature"
	case "fix", "bugfix", "hotfix":
		return "bugfix"
	case "refactor", "perf":
		return "refactor"
	case "docs", "doc":
		return "docs"
	case "test", "tests":
		return "test"
	case "chore", "build", "ci", "style":
		return "chore"
	}
	return ""
}

// extensionLanguages maps a changed-file's extension to a language name —
// the built-in replacement for go-enry (see interpret.go's package doc).
// Deliberately small: it covers the languages this workspace's own repos
// use, not every language go-enry recognizes.
var extensionLanguages = map[string]string{
	".go":    "Go",
	".py":    "Python",
	".js":    "JavaScript",
	".jsx":   "JavaScript",
	".ts":    "TypeScript",
	".tsx":   "TypeScript",
	".rb":    "Ruby",
	".java":  "Java",
	".rs":    "Rust",
	".c":     "C",
	".h":     "C",
	".cc":    "C++",
	".cpp":   "C++",
	".hpp":   "C++",
	".md":    "Markdown",
	".yaml":  "YAML",
	".yml":   "YAML",
	".json":  "JSON",
	".sh":    "Shell",
	".bash":  "Shell",
	".nix":   "Nix",
	".sql":   "SQL",
	".html":  "HTML",
	".css":   "CSS",
	".proto": "Protocol Buffer",
	".toml":  "TOML",
}

// detectLanguages maps changed-file paths to languages by extension, tallies
// by file count, and returns languages sorted by count desc then name asc —
// the same output shape as enrich.detectLanguages. Unrecognized extensions
// are skipped. Returns nil for no input (or no recognized files).
func detectLanguages(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	counts := map[string]int{}
	for _, p := range paths {
		ext := strings.ToLower(path.Ext(p))
		if lang, ok := extensionLanguages[ext]; ok {
			counts[lang]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	out := make([]string, 0, len(counts))
	for l := range counts {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if counts[out[i]] != counts[out[j]] {
			return counts[out[i]] > counts[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

func computeEnrichment(pr prShow, files []prFile, commits []prCommit) Enrichment {
	msgs := make([]string, 0, len(commits))
	for _, c := range commits {
		msgs = append(msgs, c.Message)
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return Enrichment{
		Kind:      classifyKind(pr.Title, pr.Branch, msgs),
		Languages: detectLanguages(paths),
		Size:      bucketSize(pr.Additions + pr.Deletions),
	}
}

// --- CI rollup (checks rollup signal) ---------------------------------------
// Ported from packages/pg-pr/internal/cirollup.Compute/Classify, decoding
// gather.Facts.CI's raw `ci list` fan-out payload ({"runs":[...],
// "sources":[...]}) directly rather than importing pkg/schema (see
// interpret.go's decode-shape comment). The exclusion set is derived from
// cfg.CheckInterpreters' Patterns (union across every entry regardless of
// Type — mirroring internal/snapshot/builder.go's excluderFromInterpreters),
// consuming this packet's pinned check_interpreters Config field.
// ci_only_attempts_threshold is NOT consumed here: it counts consecutive
// failing attempts ACROSS RUNS (pkg/beads/mergerequest.go's CIOnlyAttempts
// bead-metadata counter, a Phase 10 sync concern), which a stateless
// single-shot Interpret call has no history to derive — present but unused
// by this phase's own code, mirroring config.go's own established
// convention for other not-yet-consumed keys.

type ciRunFact struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type ciFanOut struct {
	Runs []ciRunFact `json:"runs"`
}

type ciRollupResult struct {
	State string // none | pending | success | failure
}

func compileExcluder(interps []config.CheckInterpreterConfig) func(name string) bool {
	var pats []*regexp.Regexp
	for _, ip := range interps {
		for _, p := range ip.Patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				continue // mis-configured pattern must not break interpretation
			}
			pats = append(pats, re)
		}
	}
	if len(pats) == 0 {
		return func(string) bool { return false }
	}
	return func(name string) bool {
		for _, re := range pats {
			if re.MatchString(name) {
				return true
			}
		}
		return false
	}
}

func computeCIRollup(raw json.RawMessage, interpreters []config.CheckInterpreterConfig) ciRollupResult {
	var fo ciFanOut
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &fo) // malformed payload degrades to no runs, not an error
	}
	excluded := compileExcluder(interpreters)
	var passed, failed, pending int
	for _, r := range fo.Runs {
		if excluded(r.Name) {
			continue
		}
		if r.Status != "completed" || r.Conclusion == "" || r.Conclusion == "pending" || r.Conclusion == "expected" {
			pending++
			continue
		}
		switch r.Conclusion {
		case "success", "neutral", "skipped":
			passed++
		default:
			failed++
		}
	}
	switch {
	case failed > 0:
		return ciRollupResult{State: "failure"}
	case pending > 0:
		return ciRollupResult{State: "pending"}
	case passed > 0:
		return ciRollupResult{State: "success"}
	default:
		return ciRollupResult{State: "none"}
	}
}

// --- base urgency scoring ---------------------------------------------------
// Ported from packages/pg-pr/internal/enrich.scoreUrgency, made
// config-vocabulary-driven per this docket's design (cfg.Urgency.Labels/
// Keywords/Thresholds — section 7.8's "Urgency configures base urgency
// scoring"). Falls back to pg-pr's own hardcoded defaults when the config
// supplies none, so the ported goldens (which assume those defaults) still
// pass byte-for-byte. Per-signal point weights (label +3, keyword +2,
// CI-failing +2, bugfix-commit +1) are NOT config keys and stay as pg-pr's
// own hardcoded constants. The layered project-health/Jira/Slack signals
// (scoreUrgencyWithHealth) are Phase 13 and are never computed here.

var defaultUrgencyLabels = map[string]bool{
	"urgent": true, "p0": true, "p1": true, "hotfix": true,
	"security": true, "incident": true, "critical": true, "sev1": true, "sev2": true,
}

var defaultUrgencyKeywords = []string{
	"production incident", "outage", "hotfix", "sev1", "sev2",
	"regression", "revert", "asap", "urgent", "critical",
}

var defaultUrgencyThresholds = map[string]int{"high": 3, "medium": 1}

func urgencyLabelSet(cfg *config.UrgencyConfig) map[string]bool {
	if cfg == nil || len(cfg.Labels) == 0 {
		return defaultUrgencyLabels
	}
	set := make(map[string]bool, len(cfg.Labels))
	for _, l := range cfg.Labels {
		set[strings.ToLower(l)] = true
	}
	return set
}

func urgencyKeywordList(cfg *config.UrgencyConfig) []string {
	if cfg == nil || len(cfg.Keywords) == 0 {
		return defaultUrgencyKeywords
	}
	return cfg.Keywords
}

func urgencyThresholds(cfg *config.UrgencyConfig) map[string]int {
	if cfg == nil || len(cfg.Thresholds) == 0 {
		return defaultUrgencyThresholds
	}
	return cfg.Thresholds
}

// levelForScore maps score onto the highest-cutoff threshold it satisfies
// (ties broken alphabetically by level name, for determinism), defaulting
// to "low" when no threshold is satisfied (or none is configured).
func levelForScore(score int, thresholds map[string]int) string {
	type entry struct {
		name   string
		cutoff int
	}
	entries := make([]entry, 0, len(thresholds))
	for name, cutoff := range thresholds {
		entries = append(entries, entry{name, cutoff})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].cutoff != entries[j].cutoff {
			return entries[i].cutoff > entries[j].cutoff
		}
		return entries[i].name < entries[j].name
	})
	for _, e := range entries {
		if score >= e.cutoff {
			return e.name
		}
	}
	return "low"
}

// scoreUrgency ports enrich.scoreUrgency's base-signal scoring exactly
// (label +3, keyword +2, CI-failing +2, bugfix-commit +1; first match per
// signal wins, matching the original's `break` after appending one reason).
func scoreUrgency(pr prShow, commits []prCommit, ci ciRollupResult, cfg *config.UrgencyConfig) (int, []string) {
	score := 0
	var reasons []string

	labels := urgencyLabelSet(cfg)
	for _, l := range pr.Labels {
		ll := strings.ToLower(strings.TrimSpace(l))
		if labels[ll] {
			score += 3
			reasons = append(reasons, "label:"+ll)
			break
		}
	}

	hay := strings.ToLower(pr.Title + "\n" + pr.Body)
	for _, kw := range urgencyKeywordList(cfg) {
		if strings.Contains(hay, strings.ToLower(kw)) {
			score += 2
			reasons = append(reasons, "keyword:"+kw)
			break
		}
	}

	if ci.State == "failure" {
		score += 2
		reasons = append(reasons, "ci-failing")
	}

	for _, c := range commits {
		first := strings.SplitN(c.Message, "\n", 2)[0]
		if kindFromConventional(first) == "bugfix" {
			score++
			reasons = append(reasons, "bugfix-commit")
			break
		}
	}

	return score, reasons
}

func computeUrgency(pr prShow, commits []prCommit, ci ciRollupResult, cfg *config.UrgencyConfig) Urgency {
	score, reasons := scoreUrgency(pr, commits, ci, cfg)
	return Urgency{
		Level:   levelForScore(score, urgencyThresholds(cfg)),
		Score:   score,
		Reasons: reasons,
	}
}
