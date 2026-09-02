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
#               "type" selects the query (command | event), whose fields live in
#               the same-named [query.<type>] table. "command" is an opaque
#               token — pr-pool just invokes argv and parses its JSON/JSONL
#               stdout; it never interprets what the command does, so this is
#               how you wire pr-pool to bd, gh, Jira, or anything else (see
#               MIGRATION.md for worked examples). An optional [query.trigger]
#               picks the firing strategy (period [default] | threshold | manual).
#   [[role]]    a consumer. "binds" is the event type(s) it responds to (ANY of
#               them). NOTE the DOUBLE brackets — a single [role]/[query] table is
#               a typo and is rejected. There is no per-role capacity/concurrency
#               key: capacity is the handler's own business (INV-CONC-1), never a
#               number the core holds.
#   [role.ccpool]  ccpool-role behavior. completion / on_failure / on_dispatch_fail
#                  are fixed enums. When authorship_guard = true, pr-pool prepends a
#                  NON-editable safety preamble (assert author is me, branch starts
#                  with phillipg., never force-push) ahead of the task prompt below.
#
# Pool-wide scalars (budgets, gates, model, worktree_dir, ...) come from PR_POOL_*
# env vars and an optional [pool] section; this file defines roles + queries. Where
# both a PR_POOL_* env var and a [pool] key set the same scalar, [pool] (this file)
# wins — e.g. [pool].worktree_dir overrides PR_POOL_WORKTREE_DIR. In general,
# [pool] wins over PR_POOL_* env, which wins over the built-in default.
#
# Gate file paths (INV-LIFE-2; "pr-pool pause"/"pr-pool resume" act on these
# directly, file-direct, without a running core) default to
# <LogDir>/gates/{quota-paused,cicd-down} — run 'pr-pool config --show' to see the
# actual resolved paths for THIS environment (LogDir varies with XDG_STATE_HOME /
# PR_POOL_LOG_DIR). Override with [pool].quota_paused_path / cicd_down_path
# (uncomment and set an absolute path), e.g.:
# quota_paused_path = "/home/example/.local/state/pr-pool/gates/quota-paused"
# cicd_down_path = "/home/example/.local/state/pr-pool/gates/cicd-down"
#
# Monitoring sinks (INTF-MON, optional; no built-in default): a [[monitor]]
# entry resolves the "id" a kind=monitor sink registers with over the common
# register verb to the metric-catalog "subset" (names) it may then read via
# mon.read. An id absent from every [[monitor]] resolves to no subset. e.g.:
# [[monitor]]
# id = "example-sink"
# subset = ["queue_depth", "unconsumed_expired"]

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
// query.BeadsReady (a Go-native type — NOT a TOML-configurable query type since
// pg2-n75tk removed `beads-ready` from the query factory); other query types
// would extend the type switch here. Each one is printed as an equivalent
// `command` block (the one surviving generic source type) that shells the same
// `bd ready` invocation the Go-native query.BeadsReady.Run would issue, piped
// through jq to translate bd's issue envelope into the command contract
// ({id,type,title,metadata}) and reproduce the title_prefix/item_type
// post-filters — see beadsReadyCommand. This keeps `config --print-defaults`
// a real, loadable, behaviorally faithful starting point (it is exercised by
// TestExampleTOML_roundTrips) and doubles as the worked beads-ready ->
// command example MIGRATION.md points to.
func emitQuery(b *strings.Builder, s query.Source) {
	br, ok := s.Query.(query.BeadsReady)
	if !ok {
		return
	}
	fmt.Fprintf(b, "[[query]]\nname = %q\n", s.Name)
	fmt.Fprintf(b, "emits = %s\n", tomlStrList(br.Emits()))
	b.WriteString("type = \"command\"\n")
	b.WriteString("[query.command]\n")
	fmt.Fprintf(b, "argv = %s\n", tomlStrList(beadsReadyCommand(br)))
	b.WriteString("format = \"json\"\n")
	b.WriteString("\n")
}

// beadsReadyCommand renders a BeadsReady query as the argv of an equivalent
// `command` source: `bd ready` with the same --label/--exclude-label filters,
// piped through jq to (1) translate bd's `{"data":[{"id","issue_type",
// "title","metadata",...}]}` envelope into the command contract's flat
// {id,type,title,metadata} array, and (2) reproduce the title_prefix /
// item_type client-side post-filters BeadsReady.Run applies in Go. `sh -c` is
// needed because `command` runs a single argv[0] with no shell — the pipeline
// itself is the payload. bd, sh and jq are all real, already-depended-on
// tools (bd is pr-pool's own first-class dependency; sh/jq are bundled onto
// the pr-pool wrapper's PATH — see default.nix — precisely so this generated
// pipeline keeps working under a minimal launchd-style PATH, the same
// motivation as INV-WORKFLOW-1 check 5).
func beadsReadyCommand(br query.BeadsReady) []string {
	var sb strings.Builder
	sb.WriteString("bd ready")
	for _, l := range br.Labels {
		fmt.Fprintf(&sb, " --label %s", l)
	}
	for _, l := range br.ExcludeLabels {
		fmt.Fprintf(&sb, " --exclude-label %s", l)
	}
	sb.WriteString(" --json --limit 0 | jq -c '[(.data // [])[] ")
	if br.TitlePrefix != "" {
		fmt.Fprintf(&sb, "| select(.title | startswith(%q)) ", br.TitlePrefix)
	}
	if br.ItemType != "" {
		fmt.Fprintf(&sb, "| select(.issue_type == %q) ", br.ItemType)
	}
	sb.WriteString("| {id, type: .issue_type, title, metadata}]'")
	return []string{"sh", "-c", sb.String()}
}

func emitRole(b *strings.Builder, r roles.Role) {
	fmt.Fprintf(b, "[[role]]\nname = %q\ntype = %q\nenabled = %t\n", r.Name, r.Type, r.Enabled)
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
		// Isolation is OMITTED when zero-valued: an absent table already decodes
		// back to the same zero value (buildIsolation), and every built-in role
		// leaves it unset — printing it unconditionally would just be noise on
		// the copy-paste starting point every operator sees.
		if cc.Isolation.Type != "" {
			b.WriteString("[role.ccpool.isolation]\n")
			fmt.Fprintf(b, "type = %q\n", cc.Isolation.Type)
			if cc.Isolation.Path != "" {
				fmt.Fprintf(b, "path = %q\n", cc.Isolation.Path)
			}
		}
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
