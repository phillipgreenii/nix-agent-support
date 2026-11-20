package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ReviewByLayout is the date format of an exception's review-by key.
const ReviewByLayout = "2006-01-02"

// Exception is one standing exception: a worktree path (or glob) that
// legitimately lives outside pg-gwm, with the reason, the person answerable for
// it and the date by which it is reviewed again.
type Exception struct {
	// Path is an absolute path or a glob. "*" matches within one path element,
	// "**" matches across elements, "?" matches one character.
	Path     string `toml:"path"`
	Reason   string `toml:"reason"`
	Owner    string `toml:"owner"`
	ReviewBy string `toml:"review-by"` // YYYY-MM-DD, quoted
}

// Matches reports whether p is covered by the exception's path or glob.
func (e Exception) Matches(p string) bool {
	re := globRegexp(e.Path)
	return re.MatchString(p)
}

func globRegexp(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// Now is the clock the review-by check reads; tests replace it.
var Now = time.Now

// checkExceptions returns the findings of one repository's exceptions.
func checkExceptions(key string, list []Exception) []Finding {
	var out []Finding
	for i, e := range list {
		at := fmt.Sprintf("exceptions[%d]", i)
		if e.Path != "" {
			at += fmt.Sprintf(" (%s)", e.Path)
		}
		errf := func(format string, a ...any) {
			out = append(out, Finding{LevelError, key, at + ": " + fmt.Sprintf(format, a...)})
		}
		if strings.TrimSpace(e.Path) == "" {
			errf("path is required")
		}
		if strings.TrimSpace(e.Reason) == "" {
			errf("reason is required")
		}
		if strings.TrimSpace(e.Owner) == "" {
			errf("owner is required")
		}
		if e.ReviewBy == "" {
			errf("review-by is required (YYYY-MM-DD)")
			continue
		}
		d, err := time.ParseInLocation(ReviewByLayout, e.ReviewBy, time.Local)
		if err != nil {
			errf("review-by %q: want a quoted YYYY-MM-DD date", e.ReviewBy)
			continue
		}
		if Now().After(d.AddDate(0, 0, 1)) {
			out = append(out, Finding{LevelWarning, key, fmt.Sprintf("%s: review-by %s has passed; review it and move the date", at, e.ReviewBy)})
		}
	}
	return out
}
