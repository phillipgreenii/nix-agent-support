package sqlite3

import (
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
var valueFlags = map[string]bool{
	"-separator": true, "-newline": true, "-cmd": true, "-init": true,
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

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName != "Bash" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	parsed := cmdparse.Parse(cmdStr)
	for _, pc := range parsed {
		basename := filepath.Base(pc.Executable)
		if basename != "sqlite3" {
			continue
		}
		return r.evaluateSqlite3(pc.Args)
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func (r *Rule) evaluateSqlite3(args []string) hookio.RuleResult {
	dbPath, query := parseArgs(args)
	if dbPath == "" || query == "" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}

	access := r.pathEval.Evaluate(dbPath)
	kind := classifyQuery(query)

	switch {
	case kind == queryRead && access.CanRead():
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "sqlite3: read query on " + access.String() + " path",
			Module:   r.Name(),
		}
	case kind == queryWrite && access.CanWrite():
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "sqlite3: write query on read-write path",
			Module:   r.Name(),
		}
	default:
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
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
