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
# When this file is present, the [[role]] + [[query]] arrays below REPLACE the
# built-in roles/queries entirely. With NO config file, pr-pool runs exactly these
# built-in feedback + worker + review defaults. Copy this file and edit it to
# customize, or run 'pr-pool config --print-defaults' to regenerate it.
#
# Under the event model a query is a PRODUCER and a role is a CONSUMER, wired only
# through a shared event-type string:
#   [[query]]   a work source. "emits" is the event type(s) it publishes; its own
#               "type" selects the query (beads-ready | beads-list | command |
#               github-issues | jira-issues | event), whose fields live in the
#               same-named [query.<type>] table. An optional [query.trigger] picks
#               the firing strategy (period [default] | threshold | manual).
#   [[role]]    a consumer. "binds" is the event type(s) it responds to (ANY of
#               them). NOTE the DOUBLE brackets — a single [role]/[query] table is
#               a typo and is rejected. An optional [role.correlation] opts the
#               role into ALL-style aggregation (all-of | count-of).
#   [role.ccpool]  ccpool-role behavior. completion / on_failure / on_dispatch_fail
#                  are fixed enums. When authorship_guard = true, pr-pool prepends a
#                  NON-editable safety preamble (assert author is me, branch starts
#                  with phillipg., never force-push) ahead of the task prompt below.
#
# Pool-wide scalars (budgets, gates, model, worktree_dir, ...) come from PR_POOL_*
# env vars and an optional [pool] section; this file defines roles + queries. Where
# both a PR_POOL_* env var and a [pool] key set the same scalar, [pool] (this file)
# wins — e.g. [pool].worktree_dir overrides PR_POOL_WORKTREE_DIR.

`

// ExampleTOML returns a commented, canonical config.toml equal to the built-in
// default role + query sets. It powers 'pr-pool config --print-defaults' and is
// the copy-paste starting point for operators. It is GENERATED from
// roles.BuiltinRoleSet + roles.BuiltinQuerySet, so it can never drift from the
// real defaults.
func ExampleTOML() string {
	d := Default()
	bp := roles.BuiltinParams{
		WorktreeDir: d.WorktreeDir,
		MaxFeedback: d.MaxFeedback,
		MaxWorker:   d.MaxWorker,
	}
	rs := roles.BuiltinRoleSet(bp)
	qs := roles.BuiltinQuerySet(bp)
	var b strings.Builder
	b.WriteString(exampleHeader)
	for _, s := range qs {
		emitQuery(&b, s)
	}
	for _, r := range rs {
		emitRole(&b, r)
	}
	return b.String()
}

// emitQuery serializes one built-in producer. The built-in queries are all
// beads-ready; other query types would extend the type switch here.
func emitQuery(b *strings.Builder, s query.Source) {
	br, ok := s.Query.(query.BeadsReady)
	if !ok {
		return
	}
	fmt.Fprintf(b, "[[query]]\nname = %q\n", s.Name)
	fmt.Fprintf(b, "emits = %s\n", tomlStrList(br.Emits()))
	b.WriteString("type = \"beads-ready\"\n")
	b.WriteString("[query.beads-ready]\n")
	fmt.Fprintf(b, "labels = %s\n", tomlStrList(br.Labels))
	fmt.Fprintf(b, "exclude_labels = %s\n", tomlStrList(br.ExcludeLabels))
	if br.TitlePrefix != "" {
		fmt.Fprintf(b, "title_prefix = %q\n", br.TitlePrefix)
	}
	if br.ItemType != "" {
		fmt.Fprintf(b, "item_type = %q\n", br.ItemType)
	}
	b.WriteString("\n")
}

func emitRole(b *strings.Builder, r roles.Role) {
	fmt.Fprintf(b, "[[role]]\nname = %q\ntype = %q\ncap = %d\nenabled = %t\n", r.Name, r.Type, r.Cap, r.Enabled)
	fmt.Fprintf(b, "binds = %s\n", tomlStrList(r.Binds))
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

func tomlStrList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}
