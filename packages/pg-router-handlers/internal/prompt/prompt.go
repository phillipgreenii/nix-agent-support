// Package prompt renders role prompts via text/template (parsed once, reused) and
// supplies the code-owned, non-editable safety preamble. missingkey=error makes a
// typo'd variable fail loudly at render rather than silently inserting "".
package prompt

import (
	"strings"
	"text/template"

	"github.com/phillipgreenii/pg-router-handlers/internal/item"
)

// Context is the interpolation surface available to every prompt template.
type Context struct {
	Item        item.Item
	WorktreeDir string
	SkillMD     string
	SelfLogin   string
	RepoRoot    string
	// Prefetch is the outcome of the role's handler-side pre-fetch (bead
	// pg2-hh32y); the zero value (OK == false, no paths) when the role has no
	// isolation.prefetch block or the pre-fetch failed.
	Prefetch PrefetchInfo
}

// PrefetchInfo describes what the handler pre-fetched into the worktree before
// the session started. OK is true only when the worktree is checked out,
// detached, at Rev. DiffFile/NumstatFile are absolute paths, empty when not
// produced (no DiffBase configured, or the base did not resolve).
type PrefetchInfo struct {
	OK          bool   `json:"ok"`
	Rev         string `json:"rev,omitempty"`
	Dir         string `json:"dir,omitempty"`
	DiffFile    string `json:"diffFile,omitempty"`
	NumstatFile string `json:"numstatFile,omitempty"`
	// Skipped is "origin-unavailable" when the fetch was skipped because
	// pg-origin-guard reported the origin unavailable (OK is then false and the
	// session does the steps itself); empty otherwise.
	Skipped string `json:"skipped,omitempty"`
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

// ProtectedPathNotice is the fixed, code-owned block prepended to a ccpool role's
// task prompt when the session runs under permissionMode=dontAsk. Claude Code
// auto-denies writes (Edit/Write) to its protected paths -- the .claude/
// directory, except .claude/worktrees -- under dontAsk, and no allowed-tools
// entry can grant them, so a worker that discovers this by trial burns turns on
// the denial before handing back. Stating it up front lets the worker hand the
// change back on its first attempt. Like AuthorshipPreamble it is NOT in any
// prompt_file, so editing config cannot weaken it. (bead pg2-a4kzx)
func ProtectedPathNotice() string {
	return "Permission note: this session runs with permissionMode=dontAsk. Writes " +
		"(Edit/Write) to Claude Code protected paths -- anything under .claude/ except " +
		".claude/worktrees -- are denied automatically, and no allowed-tools grant can " +
		"override that. If completing this task requires changing a file under .claude/, " +
		"do NOT attempt the write and do NOT retry or work around the denial: stop " +
		"immediately, make no partial changes to those paths, and hand the task back " +
		"through this role's normal hand-back path, stating that the change needs a " +
		".claude/ protected-path write and including the exact intended change (the " +
		"full unified diff, or the complete new file content for a new file) in your " +
		"hand-back so a person can apply it. Changes to all other paths proceed as normal.\n\n"
}

// ActorNotice is the fixed, code-owned block that tells a session which bd
// identity every claim of its bead MUST carry. The handler exports the same
// value as BEADS_ACTOR and judges a death by it, so a session that derives its
// own id (an empty session id gives "-worker", a bead id gives "review-<bead>")
// writes a claim the handler cannot attribute and may never release. "" for an
// empty actor.
func ActorNotice(actor string) string {
	if strings.TrimSpace(actor) == "" {
		return ""
	}
	return "Identity note: your bd actor id is " + actor + " (already set in BEADS_ACTOR). " +
		"Claim and release beads with exactly that value, never one derived from a session " +
		"id or a bead id: `bd update <id> --claim --actor " + actor + "`.\n\n"
}
