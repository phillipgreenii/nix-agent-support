package config

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/pg-router/internal/roles"
)

const exampleHeader = `# pg-router configuration — repo-local at <RepoRoot>/.pg-router/config.toml
# (override the path with PG_ROUTER_CONFIG).
#
# When this file is present, the [[role]] + [[query]] arrays below define pg-router's
# roles/queries. With NO config file (or a file with no [[role]]), pg-router runs
# with ZERO roles and ZERO queries and does nothing until configured — there is no
# built-in default set (docket pg2-oju6w's Task 5.8 removed it; ADR 0065's
# "Source-side boundary" section). The example [[query]]/[[role]] pair below is a
# copy-paste STARTING POINT, not a reproduction of any built-in behavior. Copy this
# file and edit it to customize, or run 'pg-router config --print-defaults' to
# regenerate it.
#
# Under the event model a query is a PRODUCER and a role is a CONSUMER, wired only
# through a shared event-type string:
#   [[query]]   a work source. "emits" is the event type(s) it publishes; its own
#               "type" selects the query (command | event), whose fields live in
#               the same-named [query.<type>] table. "command" is an opaque
#               token — pg-router just invokes argv and parses its JSON/JSONL
#               stdout; it never interprets what the command does, so this is
#               how you wire pg-router to bd, gh, Jira, or anything else (see
#               MIGRATION.md for worked examples). An optional [query.trigger]
#               picks the firing strategy (period [default] | threshold | manual).
#   [[role]]    a consumer. "binds" is the event type(s) it responds to (ANY of
#               them). NOTE the DOUBLE brackets — a single [role]/[query] table is
#               a typo and is rejected. There is no per-role capacity/concurrency
#               key: capacity is the handler's own business (INV-CONC-1), never a
#               number the core holds. A role names only its identity, enablement,
#               and bindings — how its own registered handler participant actually
#               executes (actor, skill, prompt, completion policy, ...) is that
#               participant's own private config, reached over the wire, never
#               authored here (ADR 0065's "Open question resolved" section).
#
# Pool-wide scalars (budgets, gates, model, worktree_dir, ...) come from PG_ROUTER_*
# env vars and an optional [pool] section; this file defines roles + queries. Where
# both a PG_ROUTER_* env var and a [pool] key set the same scalar, [pool] (this file)
# wins — e.g. [pool].worktree_dir overrides PG_ROUTER_WORKTREE_DIR. In general,
# [pool] wins over PG_ROUTER_* env, which wins over the built-in default.
#
# Gate file paths (INV-LIFE-2; "pg-router pause"/"pg-router resume" act on these
# directly, file-direct, without a running core) default to
# <LogDir>/gates/{operator-paused,cicd-down,disk-space-low} — run 'pg-router
# config --show' to see the actual resolved paths for THIS environment (LogDir
# varies with XDG_STATE_HOME / PG_ROUTER_LOG_DIR). Override with
# [pool].operator_paused_path / cicd_down_path / disk_space_low_path
# (uncomment and set an absolute path), e.g.:
# operator_paused_path = "/home/example/.local/state/pg-router/gates/operator-paused"
# cicd_down_path = "/home/example/.local/state/pg-router/gates/cicd-down"
# disk_space_low_path = "/home/example/.local/state/pg-router/gates/disk-space-low"
#
# Monitoring sinks (INTF-MON, optional; no built-in default): a [[monitor]]
# entry resolves the "id" a kind=monitor sink registers with over the common
# register verb to the metric-catalog "subset" (names) it may then read via
# mon.read. An id absent from every [[monitor]] resolves to no subset. e.g.:
# [[monitor]]
# id = "example-sink"
# subset = ["queue_depth", "unconsumed_expired"]

`

// ExampleTOML returns a commented, illustrative config.toml: one [[query]]/
// [[role]] pair, wired through a shared event type, showing the pattern
// exampleHeader's own prose documents. It powers 'pg-router config
// --print-defaults' — the copy-paste starting point for operators to edit.
//
// It no longer mirrors any built-in default set (docket pg2-oju6w's Task
// 5.8 deleted roles.BuiltinRoleSet/BuiltinQuerySet and query.BeadsReady
// outright — an unconfigured core now runs with zero roles/queries, so
// there is nothing left to "reproduce"). The example below uses the
// "command" query type (the one surviving generic TOML source) shelling out
// to a placeholder lister; MIGRATION.md carries real, worked recipes
// (bd/gh/Jira/pg-connector) for an operator to adapt instead.
func ExampleTOML() string {
	var b strings.Builder
	b.WriteString(exampleHeader)
	fmt.Fprintf(&b, "[[query]]\nname = %q\n", "example-source")
	fmt.Fprintf(&b, "emits = %s\n", tomlStrList([]string{"example.ready"}))
	b.WriteString("type = \"command\"\n")
	b.WriteString("[query.command]\n")
	fmt.Fprintf(&b, "argv = %s\n", tomlStrList([]string{"my-lister", "--json"}))
	b.WriteString("format = \"jsonl\"\n\n")
	emitRole(&b, roles.Role{Name: "example-role", Enabled: true, Binds: []string{"example.ready"}})
	return b.String()
}

// emitRole serializes one role. As of docket pg2-oju6w's Task 5.4
// (ADR 0065's "Open question resolved" section) a role carries only its
// identity, enablement, and bindings — the former [role.ccpool]/[role.command]
// tables (actor/skill/prompt/completion policy, or a bare argv) had no
// successor once roles.Role's Type/CCPoolConfig/CommandConfig fields were
// deleted: how a registered handler participant actually executes is now
// entirely that participant's own config, never authored here.
func emitRole(b *strings.Builder, r roles.Role) {
	fmt.Fprintf(b, "[[role]]\nname = %q\nenabled = %t\n", r.Name, r.Enabled)
	fmt.Fprintf(b, "binds = %s\n", tomlStrList(r.Binds))
	b.WriteString("\n")
}

func tomlStrList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}
