package validate

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// OverlapThreshold is the largest fraction of a field's 8-word n-grams that
// may also occur in the staged raw text; more than this rejects the field.
const OverlapThreshold = 0.2

// NgramWords is the n-gram size of the raw-text overlap check.
const NgramWords = 8

const defaultFieldMax = 1200

var (
	// refRe matches an evidence or query reference: E-nnn or Q-nnn, an
	// optional sample letter (E-nnnx), an optional Q row index (Q-nnn#i), an
	// optional metric key (.key) and an optional cited value (=n).
	refRe = regexp.MustCompile(`\b([EQ])-(\d{3,})([a-z])?(?:#(\d+))?(?:\.([A-Za-z_][A-Za-z0-9_]*)(?:=(-?\d+(?:\.\d+)?))?)?`)
	urlRe = regexp.MustCompile("https?://[^\\s)>\\]\"'`]+")
	fence = regexp.MustCompile("(?s)```[a-z]*\n?(.*?)```")
	cmdRe = regexp.MustCompile(`^[a-z][a-z0-9._/~-]*$`)
)

type field struct{ name, value string }

// textFields lists every free-text field of a finding (not the glossary, the
// evidence ids or the targets).
func textFields(f Finding) []field {
	out := []field{
		{"subject_key", f.SubjectKey},
		{"kind", f.Kind},
		{"title", f.Title},
		{"category", f.Category},
		{"situation", f.Situation},
		{"history", f.History},
		{"problem", f.Problem},
		{"why_problem", f.WhyProblem},
		{"impact", f.Impact},
		{"recommendation.option", f.Recommendation.Option},
		{"recommendation.why", f.Recommendation.Why},
		{"confirm_by", f.ConfirmBy},
	}
	for i, o := range f.Options {
		p := fmt.Sprintf("options[%d].", i)
		out = append(out, field{p + "name", o.Name}, field{p + "pros", o.Pros},
			field{p + "cons", o.Cons}, field{p + "if_ruled", o.IfRuled})
	}
	for i, s := range f.Sums {
		out = append(out, field{fmt.Sprintf("sums[%d].label", i), s.Label})
	}
	return out
}

// allFields adds the glossary, evidence ids and targets, for the length cap.
func allFields(f Finding) []field {
	out := textFields(f)
	for i, g := range f.Glossary {
		out = append(out, field{fmt.Sprintf("glossary[%d].term", i), g.Term},
			field{fmt.Sprintf("glossary[%d].meaning", i), g.Meaning})
	}
	for i, e := range f.Evidence {
		out = append(out, field{fmt.Sprintf("evidence[%d]", i), e})
	}
	for i, t := range f.Targets {
		out = append(out, field{fmt.Sprintf("targets[%d]", i), t})
	}
	return out
}

type checker struct {
	in       Input
	rows     map[string]EvidenceRow
	queries  map[string]QueryEntry
	subjects map[string]bool
	corpus   string
	rawGrams map[string]bool
	failures []Failure
}

// Validate checks the whole batch and returns nil when it is valid, or an
// *Error listing every failure (one invalid finding rejects the batch).
func Validate(in Input) error {
	c := &checker{in: in, rows: map[string]EvidenceRow{}, queries: map[string]QueryEntry{}, subjects: map[string]bool{}}
	var corp strings.Builder
	corp.WriteString(in.RawText)
	for _, r := range in.Evidence {
		c.rows[r.ID] = r
		c.subjects[r.SubjectKey] = true
		for _, s := range append([]string{r.SubjectKey, r.DefinedIn, r.Query}, r.Samples...) {
			corp.WriteByte('\n')
			corp.WriteString(s)
		}
	}
	for _, q := range in.QueryLog {
		c.queries[q.ID] = q
	}
	c.corpus = corp.String()
	if in.RawText != "" {
		c.rawGrams = ngrams(words(in.RawText))
	}
	if in.MaxFindings > 0 && len(in.Doc.Findings) > in.MaxFindings {
		c.fail(-1, Finding{}, "findings", RuleTooManyFindings, fmt.Sprintf("%d > %d", len(in.Doc.Findings), in.MaxFindings))
	}
	for i, f := range in.Doc.Findings {
		c.finding(i, f)
	}
	if len(c.failures) == 0 {
		return nil
	}
	return &Error{Failures: c.failures}
}

func (c *checker) fail(idx int, f Finding, fld, rule, tok string) {
	name := f.SubjectKey
	if name == "" {
		name = fmt.Sprintf("#%d", idx)
	}
	c.failures = append(c.failures, Failure{Index: idx, Finding: name, Field: fld, Rule: rule, Token: tok})
}

func (c *checker) finding(i int, f Finding) {
	fail := func(fld, rule, tok string) { c.fail(i, f, fld, rule, tok) }

	// Kind and subject.
	if f.Kind != "" && !contains(c.in.Kinds, f.Kind) {
		fail("kind", RuleUnknownKind, f.Kind)
	}
	if !c.subjects[f.SubjectKey] {
		fail("subject_key", RuleUnknownSubject, f.SubjectKey)
	}

	// Required sections.
	for _, fl := range []field{
		{"title", f.Title},
		{"category", f.Category},
		{"situation", f.Situation},
		{"history", f.History},
		{"problem", f.Problem},
		{"why_problem", f.WhyProblem},
		{"impact", f.Impact},
		{"recommendation.option", f.Recommendation.Option},
		{"recommendation.why", f.Recommendation.Why},
	} {
		if strings.TrimSpace(fl.value) == "" {
			fail(fl.name, RuleMissingSection, "")
		}
	}
	if strings.TrimSpace(f.ConfirmBy) == "" {
		fail("confirm_by", RuleMissingConfirmBy, "")
	}
	if len(f.Evidence) == 0 {
		fail("evidence", RuleMissingSection, "")
	}
	c.options(i, f)

	// Field caps.
	max := c.in.FieldMaxChars
	if max <= 0 {
		max = defaultFieldMax
	}
	for _, fl := range allFields(f) {
		if n := utf8.RuneCountInString(fl.value); n > max {
			fail(fl.name, RuleFieldTooLong, fmt.Sprintf("%d > %d", n, max))
		}
	}

	// Ids, counts and sums.
	cited := c.checkIDs(i, f)
	c.checkSums(i, f, cited)

	// Glossary, raw text, copied URLs and commands.
	var all strings.Builder
	for _, fl := range textFields(f) {
		all.WriteString(fl.value)
		all.WriteByte('\n')
	}
	own := append([]string{}, f.Evidence...)
	for _, g := range f.Glossary {
		own = append(own, g.Term)
	}
	for _, t := range GlossaryGaps(all.String(), own) {
		fail("glossary", RuleUndefinedTerm, t)
	}
	for _, fl := range textFields(f) {
		c.rawAndCopies(i, f, fl)
	}
}

func (c *checker) options(i int, f Finding) {
	if len(f.Options) < 2 {
		c.fail(i, f, "options", RuleOptionsIncomplete, "need do-nothing plus one change")
		return
	}
	nothing := false
	for k, o := range f.Options {
		if strings.Contains(strings.ToLower(o.Name), "do nothing") || strings.Contains(strings.ToLower(o.Name), "leave it") {
			nothing = true
		}
		for _, p := range [][2]string{{"name", o.Name}, {"pros", o.Pros}, {"cons", o.Cons}, {"if_ruled", o.IfRuled}} {
			if strings.TrimSpace(p[1]) == "" {
				c.fail(i, f, fmt.Sprintf("options[%d].%s", k, p[0]), RuleMissingSection, "")
			}
		}
	}
	if !nothing {
		c.fail(i, f, "options", RuleOptionsIncomplete, "no do-nothing option")
	}
}

// citedSet is the evidence and query material a finding cites.
type citedSet struct {
	rows    []EvidenceRow
	queries []string
}

func (c *checker) checkIDs(i int, f Finding) citedSet {
	var cs citedSet
	seenE, seenQ := map[string]bool{}, map[string]bool{}
	note := func(kind, id string) {
		if kind == "E" && !seenE[id] {
			seenE[id] = true
			if r, ok := c.rows[id]; ok {
				cs.rows = append(cs.rows, r)
			}
		}
		if kind == "Q" && !seenQ[id] {
			seenQ[id] = true
			if _, ok := c.in.QueryResults[id]; ok {
				cs.queries = append(cs.queries, id)
			}
		}
	}
	check := func(fld, text string) {
		for _, m := range refRe.FindAllStringSubmatch(text, -1) {
			kind, id, letter, rowIdx, key, val := m[1], m[1]+"-"+m[2], m[3], m[4], m[5], m[6]
			whole := m[0]
			switch kind {
			case "E":
				r, ok := c.rows[id]
				if !ok {
					c.fail(i, f, fld, RuleUnknownID, id)
					continue
				}
				note("E", id)
				if letter != "" {
					if idx := int(letter[0] - 'a'); idx >= len(r.Samples) {
						c.fail(i, f, fld, RuleUnknownID, id+letter)
					}
				}
				if key != "" {
					v, ok := r.Metrics[key]
					if !ok {
						c.fail(i, f, fld, RuleUnknownMetric, id+"."+key)
					} else if val != "" && !numEq(v, parse(val)) {
						c.fail(i, f, fld, RuleCountMismatch, fmt.Sprintf("%s: cited %s, evidence has %s", whole, val, fmtNum(v)))
					}
				}
			case "Q":
				_, inLog := c.queries[id]
				res, hasRes := c.in.QueryResults[id]
				if !inLog || !hasRes {
					c.fail(i, f, fld, RuleUnknownID, id)
					continue
				}
				note("Q", id)
				if key != "" {
					idx, err := strconv.Atoi(rowIdx)
					if rowIdx == "" || err != nil || idx >= len(res) {
						c.fail(i, f, fld, RuleUnknownID, whole)
						continue
					}
					v, ok := numeric(res[idx][key])
					if !ok {
						c.fail(i, f, fld, RuleUnknownMetric, id+"#"+rowIdx+"."+key)
					} else if val != "" && !numEq(v, parse(val)) {
						c.fail(i, f, fld, RuleCountMismatch, fmt.Sprintf("%s: cited %s, query has %s", whole, val, fmtNum(v)))
					}
				}
			}
		}
	}
	for k, e := range f.Evidence {
		check(fmt.Sprintf("evidence[%d]", k), e)
	}
	for _, fl := range textFields(f) {
		check(fl.name, fl.value)
	}
	return cs
}

func (c *checker) checkSums(i int, f Finding, cs citedSet) {
	var allowed []float64
	for _, r := range cs.rows {
		for _, v := range r.Metrics {
			allowed = append(allowed, v)
		}
	}
	for _, q := range cs.queries {
		for _, row := range c.in.QueryResults[q] {
			for _, raw := range row {
				if v, ok := numeric(raw); ok {
					allowed = append(allowed, v)
				}
			}
		}
	}
	for k, s := range f.Sums {
		fld := fmt.Sprintf("sums[%d]", k)
		if len(s.Addends) == 0 {
			c.fail(i, f, fld, RuleSumMismatch, "no addends")
			continue
		}
		total := 0.0
		for _, a := range s.Addends {
			total += a
			found := false
			for _, v := range allowed {
				if numEq(a, v) {
					found = true
					break
				}
			}
			if !found {
				c.fail(i, f, fld, RuleSumAddend, fmtNum(a))
			}
		}
		if !numEq(total, s.Total) {
			c.fail(i, f, fld, RuleSumMismatch, fmt.Sprintf("addends sum to %s, total says %s", fmtNum(total), fmtNum(s.Total)))
		}
	}
}

func (c *checker) rawAndCopies(i int, f Finding, fl field) {
	if c.rawGrams != nil {
		gs := ngramList(words(fl.value))
		if len(gs) > 0 {
			shared := 0
			first := ""
			for _, g := range gs {
				if c.rawGrams[g] {
					if shared == 0 {
						first = g
					}
					shared++
				}
			}
			if float64(shared)/float64(len(gs)) > OverlapThreshold {
				c.fail(i, f, fl.name, RuleRawTextOverlap, first)
			}
		}
	}
	for _, u := range urlRe.FindAllString(fl.value, -1) {
		if strings.Contains(c.corpus, u) {
			c.fail(i, f, fl.name, RuleCopiedURL, u)
		}
	}
	for _, cmd := range commands(fl.value) {
		if strings.Contains(c.corpus, cmd) {
			c.fail(i, f, fl.name, RuleCopiedCommand, cmd)
		}
	}
}

// commands returns the shell-looking strings of text: backticked or fenced
// strings of at least two words whose first word looks like a command name.
func commands(text string) []string {
	var cands []string
	for _, m := range fence.FindAllStringSubmatch(text, -1) {
		cands = append(cands, strings.Split(m[1], "\n")...)
	}
	text = fence.ReplaceAllString(text, "")
	for _, m := range backtickRe.FindAllStringSubmatch(text, -1) {
		cands = append(cands, m[1])
	}
	var out []string
	for _, cnd := range cands {
		cnd = strings.TrimSpace(cnd)
		parts := strings.Fields(cnd)
		if len(parts) >= 2 && cmdRe.MatchString(parts[0]) {
			out = append(out, cnd)
		}
	}
	return out
}

var splitRe = regexp.MustCompile(`[\p{L}\p{N}_]+`)

func words(s string) []string { return splitRe.FindAllString(strings.ToLower(s), -1) }

func ngramList(w []string) []string {
	if len(w) < NgramWords {
		return nil
	}
	out := make([]string, 0, len(w)-NgramWords+1)
	for i := 0; i+NgramWords <= len(w); i++ {
		out = append(out, strings.Join(w[i:i+NgramWords], " "))
	}
	return out
}

func ngrams(w []string) map[string]bool {
	m := map[string]bool{}
	for _, g := range ngramList(w) {
		m[g] = true
	}
	return m
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func numeric(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

func parse(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

func numEq(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
