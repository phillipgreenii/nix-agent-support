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
		Title:     pr.Title,
		URL:       pr.URL,
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

// --- layered urgency: Jira half (docket pg2-2j5ac.40, Phase 13) ------------
//
// scoreUrgencyWithHealth implements urgency.go's own forward-reference
// ("The layered project-health/Jira/Slack signals (scoreUrgencyWithHealth)
// are Phase 13 and are never computed here") — only the Jira half: project
// health and the Slack incident signal stay deferred (pg2-jpfw.5). It
// FULLY REPLACES computeUrgency's own call site in Interpret (this
// packet's own freedom-boundary choice — the design pins the signal
// SOURCE, cross-referenced Jira priority/incident, not the exact
// scoring-function composition): scoreUrgencyWithHealth always computes
// the base signal first (scoreUrgency, unchanged), then layers ONE
// additional signal on top, at the SAME +3 weight tier this file's own
// scoreUrgency gives its single strongest existing signal (a matched
// urgency label) — the strongest signal this file already recognizes, and
// the natural weight for a signal analogous in strength (a linked
// incident-shaped or high-priority Jira issue), rather than inventing a
// new, unstated weight. A PR with no cross-referenced Jira issue at all,
// or config.Jira itself nil, degrades to EXACTLY computeUrgency's own
// output — the layered signal is additive only [Binding decisions].

// jiraIssueFields is the minimal subset of an `issue show` result this
// package decodes for itself — mirroring interpret.go's own decode-shape
// convention (hand-decoded, no pkg/schema import) — the three fields
// config.JiraConfig's own keys (high_priority_values, incident_labels,
// incident_issue_types) are compared against.
type jiraIssueFields struct {
	ID        string   `json:"id"`
	Priority  string   `json:"priority,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	IssueType string   `json:"issue_type,omitempty"`
}

// decodeJiraIssues decodes gather.Facts.JiraIssues (one raw `issue show`
// result per cross-referenced ticket key) into jiraIssueFields, in a
// deterministic (sorted by ticket key) order — a Go map has no stable
// iteration order, and this signal's own "first match wins" rule
// (scoreUrgencyWithHealth below) must not depend on map iteration order to
// stay reproducible under Interpret's own fixed-clock determinism
// contract. A malformed entry is skipped (soft-fail, matching
// decodePRCommits/decodePRFiles' own convention) rather than erroring the
// whole Interpret call.
func decodeJiraIssues(raw map[string]json.RawMessage) []jiraIssueFields {
	if len(raw) == 0 {
		return nil
	}
	out := make([]jiraIssueFields, 0, len(raw))
	for _, key := range sortedKeys(raw) { // interpret.go's own generic key-sort helper
		var f jiraIssueFields
		if err := json.Unmarshal(raw[key], &f); err != nil {
			continue
		}
		out = append(out, f)
	}
	return out
}

// jiraIssueSignal reports whether issue matches jiraCfg's own criteria for
// "layered health" — its Priority is one of HighPriorityValues, one of its
// Labels is in IncidentLabels, or its IssueType is in IncidentIssueTypes
// [design: 7.4, 7.8's "config keys jira.high_priority_values,
// incident_labels, incident_issue_types"]. jiraCfg is assumed non-nil (the
// caller checks first).
func jiraIssueSignal(issue jiraIssueFields, jiraCfg *config.JiraConfig) bool {
	priority := strings.ToLower(strings.TrimSpace(issue.Priority))
	for _, v := range jiraCfg.HighPriorityValues {
		if strings.ToLower(strings.TrimSpace(v)) == priority && priority != "" {
			return true
		}
	}
	issueType := strings.ToLower(strings.TrimSpace(issue.IssueType))
	for _, v := range jiraCfg.IncidentIssueTypes {
		if strings.ToLower(strings.TrimSpace(v)) == issueType && issueType != "" {
			return true
		}
	}
	incidentLabels := toSet(lowerAll(jiraCfg.IncidentLabels))
	for _, l := range issue.Labels {
		if _, ok := incidentLabels[strings.ToLower(strings.TrimSpace(l))]; ok {
			return true
		}
	}
	return false
}

// lowerAll lowercases every element of ss (used to build a case-
// insensitive lookup set, mirroring urgencyLabelSet's own convention).
func lowerAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return out
}

// scoreUrgencyWithHealth is the Jira half of layered urgency: it calls
// computeUrgency (unchanged) for the base signal, then layers ONE
// additional +3 "jira-health" reason on top when config.Jira is configured
// and at least one of jiraIssues (the PR's own cross-referenced Jira
// issue(s), from gather.Facts.JiraIssues) matches jiraIssueSignal — first
// match wins, matching scoreUrgency's own "first match per signal wins"
// convention exactly. Returns computeUrgency's own result UNCHANGED
// (literally the same value, not a recomputation) when jiraCfg is nil or
// no jiraIssues entry matches [Binding decisions: "MUST degrade to the
// base computeUrgency/scoreUrgency signal, never error and never silently
// score as 'no urgency' ... additive only"].
func scoreUrgencyWithHealth(pr prShow, commits []prCommit, ci ciRollupResult, urgencyCfg *config.UrgencyConfig, jiraIssues []jiraIssueFields, jiraCfg *config.JiraConfig) Urgency {
	base := computeUrgency(pr, commits, ci, urgencyCfg)
	if jiraCfg == nil {
		return base
	}
	for _, issue := range jiraIssues {
		if !jiraIssueSignal(issue, jiraCfg) {
			continue
		}
		score := base.Score + 3
		reasons := append(append([]string{}, base.Reasons...), "jira-health:"+issue.ID)
		return Urgency{
			Level:   levelForScore(score, urgencyThresholds(urgencyCfg)),
			Score:   score,
			Reasons: reasons,
		}
	}
	return base
}
