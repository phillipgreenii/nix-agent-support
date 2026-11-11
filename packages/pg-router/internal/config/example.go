package config

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"

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
#               number the core holds. A role names its identity, enablement,
#               and bindings, plus its own adapter (handler). How the handler
#               actually executes (actor, skill, prompt, completion policy, ...)
#               is that adapter's own business: core hands it the role's opaque
#               [role.config] table and never interprets it (ADR 0065's "Open
#               question resolved" section).
#
# A [[role]] names its own adapter with handler (REQUIRED; a role without it
# fails the load, naming the role) and MAY set the other two keys; all three
# are decoded strictly (a wrong type fails the load):
#   handler = "/abs/path/to/adapter"   the adapter command, an ABSOLUTE path.
#               Core runs "<handler> dispatch|postStartup|preShutdown|validate"
#               with the request on stdin and the role object
#               {"name": ..., "config": {...}} inside it.
#   survives_shutdown = true           leave this role's running dispatch alone
#               when the daemon shuts down (its handler supervises its own work);
#               default false.
#   [role.config]                      an opaque table (written right under the
#               role's own [[role]] entry) that core forwards to the adapter as
#               role.config on every request, e.g. [role.config.sessionEnv]. Core
#               never interprets it; the adapter's own validate checks it.
# 'pg-router config --show' prints, per role, the resolved command, its
# survives_shutdown value and the role object exactly as it is sent.
#
# Pool-wide scalars (budgets, model, worktree_dir, ...) come from PG_ROUTER_*
# env vars and an optional [pool] section; this file defines roles + queries. Where
# both a PG_ROUTER_* env var and a [pool] key set the same scalar, [pool] (this file)
# wins — e.g. [pool].worktree_dir overrides PG_ROUTER_WORKTREE_DIR. In general,
# [pool] wins over PG_ROUTER_* env, which wins over the built-in default.
#
# The router pause (INV-LIFE-2) is NOT configured here: it is the durable
# PAUSED level of the 'router' resource, set and cleared at runtime
# ('pg-router pause'/'resume'). The built-in type = "timer" query is never
# blocked by any level.
# Resources, levels and requirements (generic example; the real registry, sensor
# and tracker names live in the deployment's own config). A [[resource]] names an
# ordered scale (best first), what silence means for a role ("ignored": "ANY", or
# "<op> <LEVEL>"), the sensor that answers for it, an optional grace_factor (a
# multiple of valid_for_s, default [levels].grace_factor) and what an absent level
# reads ("unknown" = "best" | "worst"). A role declares which levels it needs with
# requires = [ "<resource> <op> <LEVEL>", ... ] (operators ==, !=, <, <=, >, >=
# by position in the scale, "greater" means better; AND only; prefer >= and <=).
# A resource a role does not mention does not matter to it. A query that answers
# for a sensor is marked level_source = "<sensor>", and a query's pull deadline is
# deadline = "20s". 'pg-router config --show' prints each clause with its
# expansion, e.g. disk >= OK (allowed: PLENTY, OK).
# [levels]
# observed_at_skew = "5m"
# grace_factor = 2
# start_wait = "10s"
# start_stagger = "30s"
# wake_threshold = "30s"
# [[resource]]
# name = "disk"
# scale = ["PLENTY", "OK", "TIGHT", "EXHAUSTED"]
# ignored = "ANY"
# sensor = "<sensor>"
# unknown = "best"
# [[resource]]
# name = "git_auth_<remote>"
# scale = ["AVAILABLE", "UNAVAILABLE"]
# sensor = "<sensor>"
# unknown = "worst"
# (then, inside a [[role]]:)
# requires = ["disk >= OK", "git_auth_<remote> >= AVAILABLE"]
# A role MAY opt in to a bounded re-run of a dispatch whose handler failed with a
# TRANSIENT error (killed / deadline / unavailable; never a deterministic
# failure), at the role's retry cadence, at most this many times per event
# (0 = off, the default; hard cap 3):
# max_dispatch_retries = 2
# The event log (queue.jsonl) is size-limited: [pool].max_log_bytes (default
# 67108864 = 64 MiB; PG_ROUTER_MAX_LOG_BYTES takes units such as 64MiB). Past 90%
# of it polled emitters are halted; at it new events are rejected with a log_full
# reason. compact_threshold_bytes must stay below 90% of it (see README, "Queue
# log size limit"):
# [pool]
# max_log_bytes = 67108864
# (an integer byte count, or a string with a unit: max_log_bytes = "64MiB")
# The retired [pool].operator_paused_path / cicd_down_path / disk_space_low_path
# keys are ignored (with a warning); see MIGRATION.md.
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
	emitRole(&b, roles.Role{Name: "example-role", Enabled: true, Binds: []string{"example.ready"}, Handler: "/path/to/adapter"})
	return b.String()
}

// emitRole serializes one role: identity, enablement and bindings (docket
// pg2-oju6w's Task 5.4, ADR 0065's "Open question resolved" section: how a
// registered handler participant executes is its own config), plus its handler
// (docket pg2-12gr4.10), survives_shutdown and opaque [role.config] table.
func emitRole(b *strings.Builder, r roles.Role) {
	fmt.Fprintf(b, "[[role]]\nname = %q\nenabled = %t\n", r.Name, r.Enabled)
	fmt.Fprintf(b, "binds = %s\n", tomlStrList(r.Binds))
	fmt.Fprintf(b, "handler = %q\n", r.Handler)
	if r.SurvivesShutdown {
		b.WriteString("survives_shutdown = true\n")
	}
	if len(r.Config) > 0 {
		var cb bytes.Buffer
		if err := toml.NewEncoder(&cb).Encode(map[string]any{"role": map[string]any{"config": r.Config}}); err == nil {
			// The encoder prefixes a "[role]" header; the table belongs to the
			// [[role]] entry just written, so drop that one line.
			b.WriteString(strings.TrimPrefix(cb.String(), "[role]\n"))
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
