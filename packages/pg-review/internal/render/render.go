// Package render turns a stored write-up into the plain-text review item
// that `human show` prints: the thirteen required sections, in order.
package render

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

// Item is a stored review item: the write-up plus the ledger facts that go
// in its header and its "where from" section.
type Item struct {
	Number  int
	Pending int
	// State is the item's ruling state, for example Pending or Deferred.
	State   string
	Lens    string
	Scope   string
	RunDate string
	// Missing is the information the analyst could not verify (section 11).
	Missing []string
	Finding validate.Finding
}

// Sections are the headings of the thirteen sections, in order.
var Sections = []string{
	"Title", "Where from", "Category", "Situation", "History", "Problem",
	"Why it is a problem", "Impact", "Options", "Recommendation",
	"What information is missing", "Glossary", "Evidence ids",
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// Strip removes ANSI escape sequences and C0 and C1 control characters from
// stored text, keeping newline and tab.
func Strip(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return -1
		}
		return r
	}, s)
}

// Render returns the plain-text review item.
func Render(it Item) string {
	f := it.Finding
	var b strings.Builder
	fmt.Fprintf(&b, "Item %d    State: %s    Pending: %d\n\n", it.Number, Strip(it.State), it.Pending)

	sec := func(n int, body string) {
		body = strings.TrimSpace(Strip(body))
		if body == "" {
			body = "(not stated)"
		}
		fmt.Fprintf(&b, "%d. %s\n%s\n\n", n, Sections[n-1], body)
	}
	sec(1, f.Title)
	sec(2, fmt.Sprintf("Scope: %s\nLens: %s\nRun date: %s\nRaw evidence: pg-review human show %d --evidence",
		it.Scope, it.Lens, it.RunDate, it.Number))
	sec(3, fmt.Sprintf("%s (kind: %s)", f.Category, f.Kind))
	sec(4, f.Situation)
	sec(5, f.History)
	sec(6, f.Problem)
	sec(7, f.WhyProblem)
	sec(8, f.Impact)

	var opts strings.Builder
	for i, o := range f.Options {
		if i > 0 {
			opts.WriteString("\n")
		}
		fmt.Fprintf(&opts, "Option %d: %s\n  Pros: %s\n  Cons: %s\n  If ruled: %s\n", i+1, o.Name, o.Pros, o.Cons, o.IfRuled)
	}
	sec(9, opts.String())

	rec := ""
	if f.Recommendation.Option != "" || f.Recommendation.Why != "" {
		rec = fmt.Sprintf("%s\n%s", f.Recommendation.Option, f.Recommendation.Why)
	}
	sec(10, rec)

	var miss strings.Builder
	for _, m := range it.Missing {
		fmt.Fprintf(&miss, "- %s\n", m)
	}
	if len(it.Missing) == 0 {
		miss.WriteString("None.")
	}
	sec(11, miss.String())

	var gl strings.Builder
	for _, g := range f.Glossary {
		fmt.Fprintf(&gl, "- %s: %s\n", g.Term, g.Meaning)
	}
	sec(12, gl.String())
	sec(13, strings.Join(f.Evidence, ", "))

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// Renumber rewrites a stored rendering (made with item number 0) for display:
// the header line gets the real item number, state and pending count, and the
// raw-evidence hint names the real number. The rest of the text is untouched,
// so the stored write-up stays the single source of the sections.
func Renumber(rendered string, number int, state string, pending int) string {
	head, rest, ok := strings.Cut(rendered, "\n")
	if !ok || !strings.HasPrefix(head, "Item ") {
		return rendered
	}
	head = fmt.Sprintf("Item %d    State: %s    Pending: %d", number, Strip(state), pending)
	rest = strings.ReplaceAll(rest, "pg-review human show 0 --evidence", fmt.Sprintf("pg-review human show %d --evidence", number))
	return head + "\n" + rest
}
