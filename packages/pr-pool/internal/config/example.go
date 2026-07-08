package config

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

const exampleHeader = `# pr-pool configuration — repo-local at <RepoRoot>/.pr-pool/config.toml
# (override the path with PR_POOL_CONFIG).
#
# When this file is present, the [[role]] array below REPLACES the built-in roles
# entirely. With NO config file, pr-pool runs exactly these built-in feedback +
# worker + review defaults. Copy this file and edit it to customize, or run
# 'pr-pool config --print-defaults' to regenerate it.
#
# Each [[role]] is one role. NOTE the DOUBLE brackets — a single [role] table is a
# typo and is rejected.
#   type           role executor: "ccpool" (dispatch a Claude session) or
#                  "command" (run an executable, completion = exit code).
#   [role.query]   the work source. Its own "type" selects the query
#                  (beads-ready | beads-list | command | github-issues | jira-issues);
#                  that type's fields live in the same-named [role.query.<type>] table.
#   [role.ccpool]  ccpool-role behavior. completion / on_failure / on_dispatch_fail
#                  are fixed enums. When authorship_guard = true, pr-pool prepends a
#                  NON-editable safety preamble (assert author is me, branch starts
#                  with phillipg., never force-push) ahead of the task prompt below.
#
# Pool-wide scalars (budgets, gates, model, worktree_dir, ...) come from PR_POOL_*
# env vars and an optional [pool] section; this file defines roles. Where both a
# PR_POOL_* env var and a [pool] key set the same scalar, [pool] (this file) wins —
# e.g. [pool].worktree_dir overrides PR_POOL_WORKTREE_DIR.

`

// ExampleTOML returns a commented, canonical config.toml equal to the built-in
// default role set. It powers 'pr-pool config --print-defaults' and is the
// copy-paste starting point for operators. It is GENERATED from
// roles.BuiltinRoleSet, so it can never drift from the real defaults.
func ExampleTOML() string {
	rs := roles.BuiltinRoleSet(roles.BuiltinParams{
		WorktreeDir: Default().WorktreeDir,
		MaxFeedback: Default().MaxFeedback,
		MaxWorker:   Default().MaxWorker,
	})
	var b strings.Builder
	b.WriteString(exampleHeader)
	for _, r := range rs {
		emitRole(&b, r)
	}
	return b.String()
}

func emitRole(b *strings.Builder, r roles.Role) {
	fmt.Fprintf(b, "[[role]]\nname = %q\ntype = %q\ncap = %d\nenabled = %t\n", r.Name, r.Type, r.Cap, r.Enabled)
	emitQuery(b, r.Query)
	if r.CCPool != nil {
		cc := r.CCPool
		b.WriteString("[role.ccpool]\n")
		fmt.Fprintf(b, "actor = %q\n", cc.Actor)
		fmt.Fprintf(b, "completion = %q\n", string(cc.Completion))
		fmt.Fprintf(b, "on_failure = %q\n", string(cc.OnFailure))
		fmt.Fprintf(b, "on_dispatch_fail = %q\n", string(cc.OnDispatchFail))
		fmt.Fprintf(b, "authorship_guard = %t\n", cc.AuthorshipGuard)
		// '''...''': a newline right after the opening delimiter is trimmed by TOML,
		// so the decoded value equals PromptBody exactly (PromptBody has no trailing
		// newline). PromptBody never contains ''' so no escaping is needed.
		fmt.Fprintf(b, "prompt = '''\n%s'''\n", cc.PromptBody)
		// Emit the budget EXPLICITLY so a print-defaults reload reproduces the in-memory
		// budget. Without it, buildCCPool seeds every role from the pool default
		// (Time=25m), silently giving the feedback role an unwanted watchdog (pg2-yt0n).
		// tokens/cost are Limit (<=0 == unlimited); time uses Duration.String()
		// ("0s" for unlimited), which time.ParseDuration round-trips.
		b.WriteString("[role.ccpool.budget]\n")
		fmt.Fprintf(b, "tokens = %d\n", int64(cc.Budget.Tokens))
		fmt.Fprintf(b, "cost = %d\n", int64(cc.Budget.Cost))
		fmt.Fprintf(b, "time = %q\n", cc.Budget.Time.String())
	}
	b.WriteString("\n")
}

func emitQuery(b *strings.Builder, q query.Query) {
	br, ok := q.(query.BeadsReady)
	if !ok {
		// The built-in roles only use beads-ready; other query types would extend here.
		return
	}
	b.WriteString("[role.query]\ntype = \"beads-ready\"\n")
	b.WriteString("[role.query.beads-ready]\n")
	fmt.Fprintf(b, "labels = %s\n", tomlStrList(br.Labels))
	fmt.Fprintf(b, "exclude_labels = %s\n", tomlStrList(br.ExcludeLabels))
	if br.TitlePrefix != "" {
		fmt.Fprintf(b, "title_prefix = %q\n", br.TitlePrefix)
	}
	if br.ItemType != "" {
		fmt.Fprintf(b, "item_type = %q\n", br.ItemType)
	}
}

func tomlStrList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}
