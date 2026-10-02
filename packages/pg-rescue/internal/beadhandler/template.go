package beadhandler

import (
	"fmt"
	"io"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/phillipgreenii/pg-rescue/internal/tmpldata"
)

// DefaultTitleTemplate is the built-in title.
const DefaultTitleTemplate = "pg-rescue: {{.Cmd}} failed in {{.Repo}}"

// DefaultBodyTemplate is the built-in body. The item outlives the run
// directory, so everything a later reader needs is inlined. Every span of
// untrusted text (command output, context, a handler's claims) is fenced;
// the wrapper's own facts are plain.
const DefaultBodyTemplate = `pg-rescue deferred this failure to a person. This item is self-contained: the run directory it came from may have expired.

- Host: {{.Host}}
- Working directory: {{.Cwd}}
- Started: {{.StartedAt}}
- Run id: {{.RunID}}
- Fingerprint: {{.Fingerprint}}
{{if .Chain}}- Chain: {{.Chain}}
{{end}}
## Command

{{if eq .Mode "stdin"}}No command ran (stdin mode): the caller supplied the failing output.
{{else}}{{.Quote .Cmd}}Exit code: {{.Exit}}
{{end}}
{{if .Context}}## Context

{{.Quote .Context}}
{{end}}## Output tail

{{.Quote .OutputTail}}{{if .Attempts}}
## Attempts

The wrapper recorded the facts; the quoted blocks are each handler's own claims.
{{range .Attempts}}
### {{.Position}}. {{.Handler}}: {{.Outcome}}

- Reason: {{.Reason}}
{{if .Summary}}
Summary (handler's claim):

{{$.Quote .Summary}}{{end}}{{if .Details}}
Details (handler's claim):

{{$.Quote .Details}}{{end}}{{end}}{{end}}
state was left as-is at {{.FiledAt}}
`

// maxBodyBytes keeps the body under bd's 64 KiB (65535 byte) text-field cap
// with headroom for appended instructions.
const maxBodyBytes = 60000

// truncatedMarker ends a body that had to be cut to fit maxBodyBytes.
const truncatedMarker = "\n\n[pg-rescue-bead: body truncated to fit the tracker's text limit]\n"

// templateData is the shared pg-rescue template data model plus one field
// that only this handler needs: the time the item is filed, which is when the
// working copy's state was last observed.
type templateData struct {
	tmpldata.Data
	// FiledAt is when this item was filed, RFC 3339 in UTC.
	FiledAt string
}

// render executes text over d. It is not tmpldata.Render because that is
// typed to the shared Data, which has no FiledAt. A reference to an unknown
// field is an error.
func render(name, text string, d templateData) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("template %s: %w", name, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, d); err != nil {
		return "", fmt.Errorf("template %s: %w", name, err)
	}
	return b.String(), nil
}

// printVars writes the shared data model plus this handler's extra field.
func printVars(w io.Writer) error {
	if err := tmpldata.PrintVars(w); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\nExtra field in this handler's templates:\n.FiledAt     string     when the item is filed (RFC 3339, UTC)\n")
	return err
}

// printDefaults writes the built-in templates for --print-default-template.
func printDefaults(w io.Writer) error {
	return tmpldata.PrintDefaultTemplates(w, []tmpldata.Template{
		{Name: "title", Text: DefaultTitleTemplate},
		{Name: "body", Text: DefaultBodyTemplate},
	})
}

// lastLines returns the last n lines of s, keeping the original line endings.
// n <= 0 returns "".
func lastLines(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	end := len(s)
	if s[end-1] == '\n' {
		end--
	}
	idx := end
	for ; n > 0; n-- {
		i := strings.LastIndexByte(s[:idx], '\n')
		if i < 0 {
			return s
		}
		idx = i
	}
	return s[idx+1:]
}

// lineCount counts the lines in s the way lastLines does.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if s[len(s)-1] != '\n' {
		n++
	}
	return n
}

// singleLine replaces every line break with a space and trims the result, so
// a title can ride in a single argv element.
func singleLine(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	return strings.TrimSpace(s)
}

// clampBytes cuts s to at most max bytes on a rune boundary and appends the
// truncation marker (within the same budget).
func clampBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max - len(truncatedMarker)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncatedMarker
}
