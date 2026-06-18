// Package prompt renders role prompts via text/template (parsed once, reused) and
// supplies the code-owned, non-editable safety preamble. missingkey=error makes a
// typo'd variable fail loudly at render rather than silently inserting "".
package prompt

import (
	"strings"
	"text/template"

	"github.com/phillipgreenii/pr-pool/internal/item"
)

// Context is the interpolation surface available to every prompt template.
type Context struct {
	Item        item.Item
	WorktreeDir string
	SkillMD     string
	SelfLogin   string
	RepoRoot    string
}

// BeadID is a convenience alias for {{.BeadID}} == {{.Item.ID}}.
func (c Context) BeadID() string { return c.Item.ID }

// Parse compiles a prompt template once; callers store the result and Render per
// dispatch. name is used only in error messages.
func Parse(name, body string) (*template.Template, error) {
	return template.New(name).Option("missingkey=error").Parse(body)
}

// Render executes a parsed template against ctx.
func Render(t *template.Template, ctx Context) (string, error) {
	var sb strings.Builder
	if err := t.Execute(&sb, ctx); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// AuthorshipPreamble is the fixed, code-owned safety block prepended to a ccpool
// role's task prompt when ccpool.authorship_guard is true. It is NOT in any
// prompt_file, so editing config cannot weaken it. (Spec C decision 4.)
func AuthorshipPreamble() string {
	return "Before doing anything: resolve this bead's PR + head branch from the " +
		"parent merge-request bead's metadata (repo, pr_number, branch). Assert " +
		"metadata.author is me AND the branch starts with 'phillipg.'. If you cannot " +
		"resolve the PR, it is not mine, or the branch is not phillipg.-prefixed, make " +
		"NO changes, comment why, and add the human label (bd update <bead> --add-label " +
		"human). NEVER git push --force (use --force-with-lease only if instructed).\n\n"
}
