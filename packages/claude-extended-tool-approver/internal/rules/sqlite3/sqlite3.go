package sqlite3

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

type queryKind int

const (
	queryRead    queryKind = iota // SELECT, WITH
	queryWrite                    // INSERT, UPDATE, DELETE, REPLACE
	queryDDL                      // CREATE, DROP, ALTER
	queryUnknown                  // anything else
)

// skipFlags lists sqlite3 flags that are standalone (no value argument).
var standaloneFlags = map[string]bool{
	"-json": true, "-header": true, "-noheader": true,
	"-csv": true, "-column": true, "-html": true,
	"-line": true, "-list": true, "-ascii": true,
	"-tabs": true, "-bail": true, "-batch": true,
	"-interactive": true, "-readonly": true,
}

// valueFlags lists sqlite3 flags that consume the next argument.
//
// pg2-33mai (ADR 0055 mode 4): this table used to list only 4 of sqlite3's ~14
// value-taking flags (confirmed against `sqlite3 --help`, sqlite3 3.51.0). Any
// flag missing here falls to the generic `strings.HasPrefix(a, "-")` branch
// below, which assumes NO operand — so its value was mistaken for dbPath (or
// dbPath was already set, for query), shifting both positionals and generally
// classifying the query as queryUnknown, which this rule's Approve-only design
// (see evaluateSqlite3) can only turn into a missed Approve, never a wrongly
// Approved write — but it is still the same "absence from the table lets an
// untabled flag's value fall into the wrong positional slot" shape the other
// four rules were audited for. Completed from the real flag list rather than
// left as a narrower point fix, matching pg2-ygjs5's completion of ugrep's
// file-flag table.
var valueFlags = map[string]bool{
	"-separator": true, "-newline": true, "-cmd": true, "-init": true,
	"-escape": true, "-hexkey": true, "-key": true, "-maxsize": true,
	"-nonce": true, "-nullvalue": true, "-textkey": true, "-vfs": true,
}

// twoValueFlags lists sqlite3 flags that consume the NEXT TWO arguments
// (`-lookaside SIZE N`, `-pagecache SIZE N`) — a distinct arity from valueFlags,
// so parseArgs must skip 3 tokens (the flag plus both values), not 2.
var twoValueFlags = map[string]bool{
	"-lookaside": true, "-pagecache": true,
}

type Rule struct {
	pathEval *patheval.PathEvaluator
}

func New(eval *patheval.PathEvaluator) *Rule {
	return &Rule{pathEval: eval}
}

func (r *Rule) Name() string {
	return "sqlite3"
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := cmdparse.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("sqlite3: read bash command: %w", err)
	}
	for _, pc := range parsed {
		basename := filepath.Base(pc.Executable)
		if basename != "sqlite3" {
			continue
		}
		return r.evaluateSqlite3(pc.Args)
	}
	return hookio.NotApplicable()
}

func (r *Rule) evaluateSqlite3(args []string) (hookio.RuleResult, error) {
	dbPath, query := parseArgs(args)
	if dbPath == "" || query == "" {
		return hookio.NotApplicable()
	}

	access := r.pathEval.Evaluate(dbPath)
	kind := classifyQuery(query)

	switch {
	case kind == queryRead && access.CanRead():
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "sqlite3: read query on " + access.String() + " path",
			Module:   r.Name(),
		}, nil
	case kind == queryWrite && access.CanWrite():
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "sqlite3: write query on read-write path",
			Module:   r.Name(),
		}, nil
	default:
		return hookio.NotApplicable()
	}
}

// parseArgs extracts the database path and SQL query from sqlite3 arguments,
// skipping flags.
func parseArgs(args []string) (string, string) {
	var dbPath, query string
	i := 0
	for i < len(args) {
		a := args[i]
		if standaloneFlags[a] {
			i++
			continue
		}
		if valueFlags[a] {
			i += 2
			continue
		}
		if twoValueFlags[a] {
			i += 3
			continue
		}
		if strings.HasPrefix(a, "-") {
			i++
			continue
		}
		if dbPath == "" {
			dbPath = a
		} else if query == "" {
			query = a
		}
		i++
	}
	return dbPath, query
}

// classifyQuery determines the kind of SQL query from the first keyword.
func classifyQuery(query string) queryKind {
	q := strings.TrimSpace(query)

	// sqlite3 dot-commands (e.g. .schema, .tables, .headers) are read-only introspection.
	if strings.HasPrefix(q, ".") {
		return queryRead
	}

	q = strings.ToUpper(q)

	switch {
	case strings.HasPrefix(q, "SELECT "), strings.HasPrefix(q, "SELECT\n"),
		strings.HasPrefix(q, "WITH "), strings.HasPrefix(q, "WITH\n"):
		return queryRead
	case strings.HasPrefix(q, "INSERT "), strings.HasPrefix(q, "INSERT\n"),
		strings.HasPrefix(q, "UPDATE "), strings.HasPrefix(q, "UPDATE\n"),
		strings.HasPrefix(q, "DELETE "), strings.HasPrefix(q, "DELETE\n"),
		strings.HasPrefix(q, "REPLACE "), strings.HasPrefix(q, "REPLACE\n"):
		return queryWrite
	case strings.HasPrefix(q, "CREATE "), strings.HasPrefix(q, "CREATE\n"),
		strings.HasPrefix(q, "DROP "), strings.HasPrefix(q, "DROP\n"),
		strings.HasPrefix(q, "ALTER "), strings.HasPrefix(q, "ALTER\n"):
		return queryDDL
	default:
		return queryUnknown
	}
}
